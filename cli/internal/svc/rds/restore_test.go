package rds_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi/awstest"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/dockertest"
	"github.com/homecloudhq/homecloud/cli/internal/runtime"
	"github.com/homecloudhq/homecloud/cli/internal/store"
	"github.com/homecloudhq/homecloud/cli/internal/svc"
	"github.com/homecloudhq/homecloud/cli/internal/svc/rds"
	"github.com/homecloudhq/homecloud/cli/internal/svc/secrets"
	"github.com/homecloudhq/homecloud/cli/internal/svc/vpc"
	"github.com/homecloudhq/homecloud/cli/internal/system"
)

// waitInstance polls the stored instance until it has the wanted status.
func waitInstance(t *testing.T, st *store.Store, id, want string) rds.Instance {
	t.Helper()
	deadline := time.Now().Add(6 * time.Minute)
	for {
		i, err := store.Get[rds.Instance](st, "rds_instances", id)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if i.Status == want {
			return i
		}
		if (i.Status == "failed" && want != "failed") || time.Now().After(deadline) {
			t.Fatalf("%s: status %s (%s), want %s", id, i.Status, i.StatusReason, want)
		}
		time.Sleep(time.Second)
	}
}

// runClient runs a shell script in a throwaway container on a VPC network and returns its output.
func runClient(t *testing.T, d *runtime.Docker, image, network, script string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cid, err := d.Run(ctx, runtime.RunSpec{Image: image, Entrypoint: []string{"sh", "-c"}, Cmd: []string{script},
		Network: network, Labels: runtime.Labels("test", "restore-client", nil), Start: true})
	if err != nil {
		t.Fatalf("client container: %v", err)
	}
	defer d.Remove(cid)
	if _, err := d.C.WaitContainerWithContext(cid, ctx); err != nil {
		t.Fatalf("client wait: %v", err)
	}
	out, _ := d.Logs(cid, 50, time.Time{})
	return out
}

func defaultNetwork(t *testing.T, v *vpc.Service) string {
	t.Helper()
	for _, x := range v.List() {
		if x.Default {
			return x.Network
		}
	}
	t.Fatal("no default VPC")
	return ""
}

// TestRestoreRecreatesContainers backs up a PostgreSQL instance and Redis
// caches with data, removes every container and volume, restores the backup
// into a fresh data directory and starts the service again: the containers
// come back from the restored volumes with the same endpoints, passwords and
// data, and a stopped instance stays stopped (issue #106).
func TestRestoreRecreatesContainers(t *testing.T) {
	h := awstest.New(t)
	d := dockertest.Start(t)
	h.Env.Docker = d
	v := vpc.New(h.Env)
	dockertest.DefaultVPC(t, v.EnsureDefault)
	r := rds.New(h.Env, v, h.Secrets)
	r.RegisterAWS()
	r.Routes(h.Router)
	h.Secrets.RegisterAWS()

	sfx := strings.ToLower(core.RandHex(4))
	db, cache, idle := "pg-"+sfx, "kv-"+sfx, "idle-"+sfx
	const dbPass, token = "Restore-pass-123", "Restore-token-123456"
	// A group open to the VPC, so the test's client containers (not members of
	// any group) get through once the restored service enforces groups.
	open, err := v.CreateSecurityGroup(v.DefaultVPCID(), "open-"+sfx, "test clients", nil,
		[]vpc.Rule{{Protocol: "tcp", FromPort: 0, ToPort: 65535, CIDR: "0.0.0.0/0"}})
	if err != nil {
		t.Fatal(err)
	}
	h.Python(t, fmt.Sprintf(`
rds = boto3.client("rds")
rds.create_db_instance(DBInstanceIdentifier=%[1]q, Engine="postgres", DBInstanceClass="db.t3.micro",
    MasterUsername="app", MasterUserPassword=%[2]q, DBName="shop", AllocatedStorage=20, VpcSecurityGroupIds=[%[6]q])
ec = boto3.client("elasticache")
ec.create_cache_cluster(CacheClusterId=%[3]q, Engine="redis", CacheNodeType="cache.t3.micro", NumCacheNodes=1,
    AuthToken=%[4]q, SecurityGroupIds=[%[6]q])
ec.create_cache_cluster(CacheClusterId=%[5]q, Engine="redis", CacheNodeType="cache.t3.micro", NumCacheNodes=1)
`, db, dbPass, cache, token, idle, open.ID))
	pg := waitInstance(t, h.Env.Store, db, "available")
	kv := waitInstance(t, h.Env.Store, cache, "available")
	waitInstance(t, h.Env.Store, idle, "available")

	network := defaultNetwork(t, v)
	psql := func(network, sql string) string {
		return runClient(t, d, "postgres:17-alpine", network, fmt.Sprintf("PGPASSWORD=%s psql -h %s -U app -d shop -tA -v ON_ERROR_STOP=1 -c %q 2>&1",
			dbPass, pg.Endpoint.Address, sql))
	}
	redis := func(network, cmd string) string {
		return runClient(t, d, "redis:7.4-alpine", network, fmt.Sprintf("redis-cli --no-auth-warning -a %s -h %s %s 2>&1", token, kv.Endpoint.Address, cmd))
	}
	if out := psql(network, "CREATE TABLE items(name text); INSERT INTO items VALUES ('kept-row')"); !strings.Contains(out, "INSERT 0 1") {
		t.Fatalf("write row: %q", out)
	}
	if out := redis(network, "SET greeting kept-value"); !strings.Contains(out, "OK") {
		t.Fatalf("write key: %q", out)
	}
	h.Native(t, "POST", "/api/v1/rds/instances/"+idle+"/stop", nil)
	waitInstance(t, h.Env.Store, idle, "stopped")
	time.Sleep(2 * time.Second) // the cache's append-only file is flushed every second

	// Back up, then lose every container, network and volume.
	ctx := context.Background()
	archive := filepath.Join(t.TempDir(), "backup.tar.gz")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	b := &system.Backup{Cfg: h.Env.Cfg, Docker: d, AccountID: runtime.Account, Version: "test", OwnVolumesOnly: true}
	if err := b.Write(ctx, f, true, t.Logf); err != nil {
		t.Fatalf("backup: %v", err)
	}
	f.Close()
	dockertest.Remove(d, runtime.Account, t.Logf)
	for _, name := range []string{"hc-rds-" + db, "hc-elasticache-" + cache} {
		if st := d.State(name); st != "missing" {
			t.Fatalf("container %s still there (%s)", name, st)
		}
	}
	if _, err := d.C.InspectVolume("hc-rds-" + db); err == nil {
		t.Fatal("volume still there after removal")
	}

	// Restore into a fresh data directory and fresh volumes, and start again.
	f, err = os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cfg := h.Env.Cfg
	cfg.DataDir = filepath.Join(t.TempDir(), "restored")
	if _, err := system.Restore(ctx, d, f, system.RestoreOptions{DataDir: cfg.DataDir, Logf: t.Logf}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	st, err := store.Open(filepath.Join(cfg.DataDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	env := &svc.Env{Cfg: cfg, Store: st, Docker: d, AccountID: h.Env.AccountID}
	sec, err := secrets.New(env)
	if err != nil {
		t.Fatal(err)
	}
	v2 := vpc.New(env)
	dockertest.DefaultVPC(t, v2.EnsureDefault) // recreates the VPC networks
	rds.New(env, v2, sec).Recover()

	pg2 := waitInstance(t, st, db, "available")
	kv2 := waitInstance(t, st, cache, "available")
	if pg2.Endpoint != pg.Endpoint || kv2.Endpoint != kv.Endpoint || pg2.EngineVersion != pg.EngineVersion || pg2.Class != pg.Class ||
		fmt.Sprint(pg2.SecurityGroups) != fmt.Sprint(pg.SecurityGroups) || pg2.SecretName != pg.SecretName {
		t.Fatalf("settings changed:\n%+v\n%+v\n%+v\n%+v", pg, pg2, kv, kv2)
	}
	if c, err := d.Inspect(pg2.ContainerID); err != nil || !strings.Contains(c.Config.Image, "postgres:"+pg.EngineVersion) {
		t.Fatalf("recreated container: %v %v", c, err)
	}
	stopped := waitInstance(t, st, idle, "stopped")
	if stopped.ContainerID == "" || d.State(stopped.ContainerID) == "running" || d.State(stopped.ContainerID) == "missing" {
		t.Fatalf("stopped instance: container %q is %s", stopped.ContainerID, d.State(stopped.ContainerID))
	}

	network2 := defaultNetwork(t, v2)
	if out := psql(network2, "SELECT name FROM items"); !strings.Contains(out, "kept-row") {
		t.Fatalf("row after restore: %q", out)
	}
	if out := redis(network2, "GET greeting"); !strings.Contains(out, "kept-value") {
		t.Fatalf("key after restore: %q", out)
	}
}
