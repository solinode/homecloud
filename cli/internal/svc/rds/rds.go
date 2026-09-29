// Package rds implements managed databases (RDS: PostgreSQL, MySQL, MariaDB),
// in-memory caches (ElastiCache: Redis, Valkey, Memcached) and a document
// database (MongoDB) on containers with persistent volumes, generated master
// credentials in Secrets Manager, snapshots, automated backups and a query editor.
package rds

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	docker "github.com/fsouza/go-dockerclient"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/runtime"
	"github.com/homecloudhq/homecloud/cli/internal/store"
	"github.com/homecloudhq/homecloud/cli/internal/svc"
	"github.com/homecloudhq/homecloud/cli/internal/svc/secrets"
	"github.com/homecloudhq/homecloud/cli/internal/svc/vpc"
)

const (
	cInstances = "rds_instances"
	cSnapshots = "rds_snapshots"
)

type Endpoint struct {
	Address     string `json:"address"`     // private DNS name inside the VPC
	Port        int    `json:"port"`        // engine port inside the VPC
	PublicHost  string `json:"public_host"` // host reachable from outside, when publicly accessible
	PublicPort  int    `json:"public_port"` // host port, when publicly accessible
	PrivateIP   string `json:"private_ip"`
	ConnectHint string `json:"connect_hint"` // example connection string
}

type Instance struct {
	ID                  string     `json:"id"`
	ARN                 string     `json:"arn"`
	Kind                string     `json:"kind"`
	Engine              string     `json:"engine"`
	EngineVersion       string     `json:"engine_version"`
	Class               string     `json:"class"`
	VCPUs               float64    `json:"vcpus"`
	MemoryMB            int64      `json:"memory_mb"`
	StorageGB           int        `json:"storage_gb"`
	Status              string     `json:"status"`
	StatusReason        string     `json:"status_reason,omitempty"`
	MasterUsername      string     `json:"master_username,omitempty"`
	DBName              string     `json:"db_name,omitempty"`
	SecretName          string     `json:"secret_name,omitempty"`
	Endpoint            Endpoint   `json:"endpoint"`
	VpcID               string     `json:"vpc_id"`
	SubnetID            string     `json:"subnet_id"`
	AvailabilityZone    string     `json:"availability_zone"`
	PubliclyAccessible  bool       `json:"publicly_accessible"`
	BackupRetentionDays int        `json:"backup_retention_days"`
	LatestBackup        *time.Time `json:"latest_backup,omitempty"`
	DeletionProtection  bool       `json:"deletion_protection"`
	ContainerID         string     `json:"container_id,omitempty"`
	RestoredFrom        string     `json:"restored_from,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
	Tags                core.Tags  `json:"tags,omitempty"`
}

type Snapshot struct {
	ID             string    `json:"id"`
	ARN            string    `json:"arn"`
	SourceInstance string    `json:"source_instance"`
	Kind           string    `json:"kind"`
	Engine         string    `json:"engine"`
	EngineVersion  string    `json:"engine_version"`
	Type           string    `json:"type"` // manual | automated
	Status         string    `json:"status"`
	StatusReason   string    `json:"status_reason,omitempty"`
	SizeBytes      int64     `json:"size_bytes"`
	MasterUsername string    `json:"master_username,omitempty"`
	DBName         string    `json:"db_name,omitempty"`
	PasswordCT     string    `json:"password_ct,omitempty"` // encrypted master password
	StorageGB      int       `json:"storage_gb"`
	CreatedAt      time.Time `json:"created_at"`
}

func (s Snapshot) view() Snapshot { s.PasswordCT = ""; return s }

type Service struct {
	env     *svc.Env
	vpc     *vpc.Service
	secrets *secrets.Service
	hostCPU float64
	busy    sync.Map // instance id -> operation in flight
	snapMu  sync.Mutex
}

// Recover settles instances whose operation was interrupted by a restart.
func (s *Service) Recover() {
	for _, i := range store.List[Instance](s.env.Store, cInstances) {
		switch i.Status {
		case "creating", "restoring", "starting", "rebooting", "backing-up", "modifying", "stopping", "deleting":
		default:
			continue
		}
		status, reason := "failed", "HomeCloud restarted during "+i.Status
		if i.ContainerID != "" {
			switch s.env.Docker.State(i.ContainerID) {
			case "running":
				if i.Status != "creating" && i.Status != "restoring" {
					status, reason = "available", ""
				}
			case "exited", "created":
				if i.Status == "stopping" {
					status, reason = "stopped", ""
				}
			}
		}
		s.setStatus(i.ID, status, reason)
	}
	for _, sn := range store.List[Snapshot](s.env.Store, cSnapshots) {
		if sn.Status == "creating" {
			s.removeSnapshot(sn.ID)
		}
	}
}

func New(env *svc.Env, v *vpc.Service, sec *secrets.Service) *Service {
	s := &Service{env: env, vpc: v, secrets: sec, hostCPU: 1}
	if info, err := env.Docker.C.Info(); err == nil && info.NCPU > 0 {
		s.hostCPU = float64(info.NCPU)
	}
	return s
}

func (s *Service) snapshotFile(id string) string { return s.env.Cfg.Path("rds-snapshots", id+".bak") }
func volumeName(id string) string                { return "hc-rds-" + id }
func serviceLabel(kind string) string {
	if kind == "cache" {
		return "elasticache"
	}
	return "rds"
}

func (s *Service) password(i Instance) (string, error) {
	if i.SecretName == "" {
		return "", nil
	}
	v, _, err := s.secrets.Value(i.SecretName, "", "")
	if err != nil {
		return "", err
	}
	var c struct {
		Password string `json:"password"`
	}
	if err := json.Unmarshal([]byte(v), &c); err != nil {
		return "", err
	}
	return c.Password, nil
}

func (s *Service) setStatus(id, status, reason string) {
	_, _ = store.Update(s.env.Store, cInstances, id, func(x *Instance) error {
		x.Status, x.StatusReason = status, reason
		return nil
	})
}

// ---- reconciliation ----

func (s *Service) sync(i Instance) Instance {
	if i.ContainerID == "" || slices.Contains([]string{"creating", "deleting", "failed", "stopping", "starting", "rebooting", "restoring", "backing-up", "modifying"}, i.Status) {
		return i
	}
	st := s.env.Docker.State(i.ContainerID)
	want := i.Status
	switch st {
	case "running":
		want = "available"
	case "exited", "created", "dead":
		want = "stopped"
	case "missing":
		want = "failed"
	}
	if want != i.Status {
		reason := ""
		if want == "failed" {
			reason = "the database container disappeared"
		}
		s.setStatus(i.ID, want, reason)
		i.Status, i.StatusReason = want, reason
	}
	if i.PubliclyAccessible && want == "available" && i.Endpoint.PublicPort == 0 {
		i.Endpoint.PublicPort = s.env.Docker.HostPort(i.ContainerID, i.Endpoint.Port)
	}
	return i
}

func (s *Service) get(id string) (Instance, error) {
	i, err := store.Get[Instance](s.env.Store, cInstances, id)
	if err != nil {
		return i, core.NotFound("database instance", id)
	}
	return s.sync(i), nil
}

// ---- routes ----

func (s *Service) Routes(r *httpx.Router) {
	res := httpx.Res("arn:hc:rds:local-1:{account}:db:{id}")
	r.Handle("GET /api/v1/rds/engines", "rds:DescribeDBEngineVersions", s.listEngines)
	r.Handle("GET /api/v1/rds/instances", "rds:DescribeDBInstances", s.list)
	r.Handle("POST /api/v1/rds/instances", "rds:CreateDBInstance", s.create)
	r.Handle("GET /api/v1/rds/instances/{id}", "rds:DescribeDBInstances", s.describe, res)
	r.Handle("PATCH /api/v1/rds/instances/{id}", "rds:ModifyDBInstance", s.modify, res)
	r.Handle("DELETE /api/v1/rds/instances/{id}", "rds:DeleteDBInstance", s.delete, res)
	r.Handle("POST /api/v1/rds/instances/{id}/start", "rds:StartDBInstance", s.start, res)
	r.Handle("POST /api/v1/rds/instances/{id}/stop", "rds:StopDBInstance", s.stop, res)
	r.Handle("POST /api/v1/rds/instances/{id}/reboot", "rds:RebootDBInstance", s.reboot, res)
	r.Handle("POST /api/v1/rds/instances/{id}/password", "rds:ModifyDBInstance", s.resetPassword, res)
	r.Handle("POST /api/v1/rds/instances/{id}/query", "rds-data:ExecuteStatement", s.query, res)
	r.Handle("GET /api/v1/rds/instances/{id}/logs", "rds:DownloadDBLogFilePortion", s.logs, res)
	r.Handle("POST /api/v1/rds/instances/{id}/snapshots", "rds:CreateDBSnapshot", s.createSnapshot, res)
	r.Handle("GET /api/v1/rds/snapshots", "rds:DescribeDBSnapshots", s.listSnapshots)
	snapRes := httpx.Res("arn:hc:rds:local-1:{account}:snapshot:{snap}")
	r.Handle("DELETE /api/v1/rds/snapshots/{snap}", "rds:DeleteDBSnapshot", s.deleteSnapshot, snapRes)
	r.Handle("POST /api/v1/rds/snapshots/{snap}/restore", "rds:RestoreDBInstanceFromDBSnapshot", s.restore, snapRes)
}

func (s *Service) listEngines(c *httpx.Ctx) (any, error) {
	return map[string]any{"engines": engines, "classes": classes}, nil
}

func (s *Service) list(c *httpx.Ctx) (any, error) {
	out := []Instance{}
	for _, i := range store.List[Instance](s.env.Store, cInstances) {
		i = s.sync(i)
		if k := c.Query("kind"); k != "" && i.Kind != k {
			continue
		}
		out = append(out, i)
	}
	return out, nil
}

func (s *Service) describe(c *httpx.Ctx) (any, error) { return s.get(c.Param("id")) }

var idRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
var dbNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$-]{0,62}$`)
var userRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]{0,31}$`)

type createInput struct {
	ID                  string    `json:"id"`
	Engine              string    `json:"engine"`
	EngineVersion       string    `json:"engine_version"`
	Class               string    `json:"class"`
	StorageGB           int       `json:"storage_gb"`
	MasterUsername      string    `json:"master_username"`
	MasterPassword      string    `json:"master_password"`
	DBName              string    `json:"db_name"`
	SubnetID            string    `json:"subnet_id"`
	PubliclyAccessible  bool      `json:"publicly_accessible"`
	Port                int       `json:"port"` // fixed host port when publicly accessible (0 = any)
	BackupRetentionDays *int      `json:"backup_retention_days"`
	DeletionProtection  bool      `json:"deletion_protection"`
	Tags                core.Tags `json:"tags"`
}

func (s *Service) create(c *httpx.Ctx) (any, error) {
	var in createInput
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	return s.provision(in, nil)
}

// provision validates input, records the instance and boots it in the background.
// When snap is set the new instance is restored from it.
func (s *Service) provision(in createInput, snap *Snapshot) (Instance, error) {
	if !idRe.MatchString(in.ID) {
		return Instance{}, core.BadRequest("id must start with a letter and contain only lowercase letters, digits and hyphens (max 63)")
	}
	if store.Has(s.env.Store, cInstances, in.ID) {
		return Instance{}, core.Conflict("database instance %q already exists", in.ID)
	}
	e, ok := findEngine(in.Engine)
	if !ok {
		return Instance{}, core.BadRequest("unknown engine %q", in.Engine)
	}
	if in.EngineVersion == "" {
		in.EngineVersion = e.Versions[0]
	}
	if err := e.validVersion(in.EngineVersion); err != nil {
		return Instance{}, core.BadRequest("%v", err)
	}
	if in.Class == "" {
		in.Class = e.defaultClass()
	}
	cl, ok := findClass(in.Class)
	if !ok {
		return Instance{}, core.BadRequest("unknown instance class %q", in.Class)
	}
	if in.StorageGB == 0 {
		in.StorageGB = 20
	}
	if e.HasUsers {
		if in.MasterUsername == "" {
			in.MasterUsername = map[string]string{"postgres": "postgres", "mongodb": "admin"}[e.Name]
			if in.MasterUsername == "" {
				in.MasterUsername = "admin"
			}
		}
		if !userRe.MatchString(in.MasterUsername) || (in.MasterUsername == "root" && e.Kind == "relational") {
			return Instance{}, core.BadRequest("invalid master username %q", in.MasterUsername)
		}
	}
	if e.HasDatabase && in.DBName == "" {
		in.DBName = map[string]string{"postgres": "postgres", "mongodb": "admin"}[e.Name]
		if in.DBName == "" {
			in.DBName = "app"
		}
	}
	if e.HasPassword && in.MasterPassword == "" {
		in.MasterPassword = core.NewSecret(24)
	}
	if in.MasterPassword != "" && (len(in.MasterPassword) < 8 || strings.ContainsAny(in.MasterPassword, `"'/@ \`)) {
		return Instance{}, core.BadRequest("master password must be at least 8 characters and may not contain quotes, slashes, @ or spaces")
	}
	retention := 1
	if in.BackupRetentionDays != nil {
		retention = *in.BackupRetentionDays
	}
	if e.dump == nil {
		retention = 0
	}

	pl, err := s.vpc.Place(in.SubnetID, "db:"+in.ID)
	if err != nil {
		return Instance{}, err
	}
	i := Instance{
		ID: in.ID, ARN: s.env.ARN("rds", "db:"+in.ID), Kind: e.Kind, Engine: e.Name, EngineVersion: in.EngineVersion,
		Class: cl.Name, VCPUs: cl.VCPUs, MemoryMB: cl.MemoryMB, StorageGB: in.StorageGB, Status: "creating",
		MasterUsername: in.MasterUsername, DBName: in.DBName, VpcID: pl.VPC.ID, SubnetID: pl.Subnet.ID,
		AvailabilityZone: pl.Subnet.AvailabilityZone, PubliclyAccessible: in.PubliclyAccessible,
		BackupRetentionDays: retention, DeletionProtection: in.DeletionProtection, CreatedAt: core.Now(), Tags: in.Tags,
		Endpoint: Endpoint{Address: in.ID + "." + serviceLabel(e.Kind) + ".internal", Port: e.DefaultPort, PrivateIP: pl.IP},
	}
	if snap != nil {
		i.RestoredFrom = snap.ID
	}
	if in.PubliclyAccessible {
		i.Endpoint.PublicHost = s.env.Cfg.PublicHost
	}
	i.Endpoint.ConnectHint = connectHint(i)
	if e.HasPassword {
		i.SecretName = "rds!" + in.ID
		sv, _ := json.Marshal(map[string]any{"username": in.MasterUsername, "password": in.MasterPassword, "engine": e.Name,
			"host": i.Endpoint.Address, "port": e.DefaultPort, "dbname": in.DBName, "dbInstanceIdentifier": in.ID})
		if _, err := s.secrets.Put(i.SecretName, string(sv), "Master credentials for database "+in.ID, "rds"); err != nil {
			s.vpc.Release("db:" + in.ID)
			return Instance{}, err
		}
	}
	if err := store.Put(s.env.Store, cInstances, i.ID, i); err != nil {
		return Instance{}, err
	}
	go s.boot(i, e, in, pl.Network, snap)
	return i, nil
}

func connectHint(i Instance) string {
	host, port := i.Endpoint.Address, i.Endpoint.Port
	if i.PubliclyAccessible {
		host = i.Endpoint.PublicHost
		if i.Endpoint.PublicPort != 0 {
			port = i.Endpoint.PublicPort
		}
	}
	switch i.Engine {
	case "postgres":
		return fmt.Sprintf("postgresql://%s:<password>@%s:%d/%s", i.MasterUsername, host, port, i.DBName)
	case "mysql", "mariadb":
		return fmt.Sprintf("mysql -h %s -P %d -u %s -p %s", host, port, i.MasterUsername, i.DBName)
	case "redis", "valkey":
		return fmt.Sprintf("redis://:<password>@%s:%d", host, port)
	case "memcached":
		return fmt.Sprintf("%s:%d", host, port)
	case "mongodb":
		return fmt.Sprintf("mongodb://%s:<password>@%s:%d/?authSource=admin", i.MasterUsername, host, port)
	}
	return ""
}

func (s *Service) boot(i Instance, e Engine, in createInput, network string, snap *Snapshot) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	fail := func(err error) {
		log.Printf("rds: %s: %v", i.ID, err)
		s.setStatus(i.ID, "failed", err.Error())
	}
	vol := volumeName(i.ID)
	if err := s.env.Docker.CreateVolume(vol, runtime.Labels(serviceLabel(e.Kind), i.ID, nil)); err != nil {
		fail(err)
		return
	}
	spec := runtime.RunSpec{
		Name:     svc.ContainerName(serviceLabel(e.Kind), i.ID),
		Image:    e.image(i.EngineVersion),
		Labels:   runtime.Labels(serviceLabel(e.Kind), i.ID, map[string]string{"homecloud.engine": e.Name}),
		NanoCPUs: int64(min(i.VCPUs, s.hostCPU) * 1e9),
		MemoryMB: i.MemoryMB,
		Network:  network,
		IP:       i.Endpoint.PrivateIP,
		Aliases:  []string{i.Endpoint.Address, i.ID},
		Restart:  "unless-stopped",
	}
	if e.DataDir != "" {
		spec.Mounts = []runtime.Mount{{Volume: vol, Target: e.DataDir}}
	}
	if e.env != nil {
		spec.Env = e.env(in.MasterUsername, in.MasterPassword, in.DBName)
	}
	if e.cmd != nil {
		spec.Cmd = e.cmd(in.MasterPassword)
	}
	if e.Name == "memcached" {
		spec.Cmd = []string{"memcached", "-m", fmt.Sprint(i.MemoryMB * 3 / 4)}
	}
	if i.PubliclyAccessible {
		spec.Ports = []runtime.Port{{ContainerPort: e.DefaultPort, HostPort: in.Port}}
	}
	restoreCache := snap != nil && e.Kind == "cache"
	final := spec
	if restoreCache {
		// Redis ignores an RDB file when AOF is on and no AOF exists yet, so boot
		// once with AOF off to load the snapshot, then switch AOF on (below).
		spec.Cmd = append(append([]string(nil), spec.Cmd...), "--appendonly", "no")
	}
	cid, err := s.env.Docker.Run(ctx, spec)
	if err != nil {
		fail(err)
		return
	}
	// If the instance was deleted while we were creating it, clean up and stop.
	if !store.Has(s.env.Store, cInstances, i.ID) {
		_ = s.env.Docker.Remove(cid)
		_ = s.env.Docker.RemoveVolume(vol)
		return
	}
	_, _ = store.Update(s.env.Store, cInstances, i.ID, func(x *Instance) error { x.ContainerID = cid; return nil })
	// Redis-style snapshots are restored by placing the RDB file before first start.
	if restoreCache {
		b, err := os.ReadFile(s.snapshotFile(snap.ID))
		if err == nil {
			err = s.env.Docker.CopyIn(ctx, cid, "/data", map[string][]byte{"dump.rdb": b}, 0o644)
		}
		if err != nil {
			fail(fmt.Errorf("restore snapshot: %w", err))
			return
		}
	}
	_, _ = store.Update(s.env.Store, cInstances, i.ID, func(x *Instance) error { x.ContainerID = cid; return nil })
	if err := s.env.Docker.Start(cid); err != nil {
		fail(err)
		return
	}
	if err := s.waitReady(ctx, cid, e, in.MasterUsername, in.MasterPassword, in.DBName); err != nil {
		fail(err)
		return
	}
	if restoreCache {
		if cid, err = s.enableAOF(ctx, cid, e, final, in.MasterPassword); err != nil {
			fail(fmt.Errorf("restore snapshot: %w", err))
			return
		}
		_, _ = store.Update(s.env.Store, cInstances, i.ID, func(x *Instance) error { x.ContainerID = cid; return nil })
	}
	if snap != nil && e.restore != nil {
		s.setStatus(i.ID, "restoring", "")
		f, err := os.Open(s.snapshotFile(snap.ID))
		if err != nil {
			fail(fmt.Errorf("open snapshot: %w", err))
			return
		}
		res, err := s.env.Docker.Exec(ctx, cid, e.restore(in.MasterUsername, in.MasterPassword), f)
		f.Close()
		if err != nil {
			fail(fmt.Errorf("restore snapshot: %w", err))
			return
		}
		if res.ExitCode != 0 {
			msg := fmt.Sprintf("snapshot restored with errors (exit %d): %s", res.ExitCode, tail(res.Stdout+res.Stderr, 300))
			log.Printf("rds: %s: %s", i.ID, msg)
			defer func() {
				_, _ = store.Update(s.env.Store, cInstances, i.ID, func(x *Instance) error { x.StatusReason = msg; return nil })
			}()
		}
	}
	_, _ = store.Update(s.env.Store, cInstances, i.ID, func(x *Instance) error {
		x.Status, x.StatusReason = "available", ""
		if x.PubliclyAccessible {
			x.Endpoint.PublicPort = s.env.Docker.HostPort(cid, x.Endpoint.Port)
		}
		x.Endpoint.ConnectHint = connectHint(*x)
		return nil
	})
}

// enableAOF turns on append-only persistence in a cache that was booted from an
// RDB snapshot, waits for the AOF to be written, then recreates the container
// with its normal command line so later restarts load the AOF.
func (s *Service) enableAOF(ctx context.Context, cid string, e Engine, spec runtime.RunSpec, pass string) (string, error) {
	cli := e.Name + "-cli --no-auth-warning -a " + q(pass)
	res, err := s.env.Docker.Exec(ctx, cid, sh(cli+" CONFIG SET appendonly yes"), nil)
	if err != nil {
		return "", fmt.Errorf("enable AOF: %w", err)
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("enable AOF: %s", tail(res.Stdout+res.Stderr, 300))
	}
	for i := 0; i < 60; i++ {
		res, err = s.env.Docker.Exec(ctx, cid, sh(cli+" INFO persistence"), nil)
		if err == nil && strings.Contains(res.Stdout, "aof_rewrite_in_progress:0") && strings.Contains(res.Stdout, "aof_rewrite_scheduled:0") {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err := s.env.Docker.Remove(cid); err != nil {
		return "", err
	}
	spec.Start = true
	cid, err = s.env.Docker.Run(ctx, spec)
	if err != nil {
		return "", err
	}
	return cid, s.waitReady(ctx, cid, e, "", pass, "")
}

func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

func (s *Service) waitReady(ctx context.Context, cid string, e Engine, user, pass, db string) error {
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		if s.env.Docker.State(cid) != "running" {
			logs, _ := s.env.Docker.Logs(cid, 20, time.Time{})
			return fmt.Errorf("database process exited during startup: %s", tail(logs, 600))
		}
		pctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		res, err := s.env.Docker.Exec(pctx, cid, e.probe(user, pass, db), nil)
		cancel()
		if err == nil && res.ExitCode == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return fmt.Errorf("database did not become ready within 5 minutes")
}

func (s *Service) lock(id string) (func(), error) {
	if _, loaded := s.busy.LoadOrStore(id, true); loaded {
		return nil, core.Errf(http.StatusConflict, "InvalidDBInstanceState", "another operation is in progress on %s", id)
	}
	return func() { s.busy.Delete(id) }, nil
}

func (s *Service) requireStatus(i Instance, allowed ...string) error {
	if !slices.Contains(allowed, i.Status) {
		return core.Errf(http.StatusConflict, "InvalidDBInstanceState", "database %s is %s", i.ID, i.Status)
	}
	return nil
}

func (s *Service) start(c *httpx.Ctx) (any, error) {
	i, err := s.get(c.Param("id"))
	if err != nil {
		return nil, err
	}
	if err := s.requireStatus(i, "stopped"); err != nil {
		return nil, err
	}
	unlock, err := s.lock(i.ID)
	if err != nil {
		return nil, err
	}
	s.setStatus(i.ID, "starting", "")
	go func() {
		defer unlock()
		e, _ := findEngine(i.Engine)
		if err := s.env.Docker.Start(i.ContainerID); err != nil {
			s.setStatus(i.ID, "stopped", err.Error())
			return
		}
		pass, _ := s.password(i)
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
		defer cancel()
		if err := s.waitReady(ctx, i.ContainerID, e, i.MasterUsername, pass, i.DBName); err != nil {
			s.setStatus(i.ID, "failed", err.Error())
			return
		}
		s.setStatus(i.ID, "available", "")
	}()
	return s.get(i.ID)
}

func (s *Service) stop(c *httpx.Ctx) (any, error) {
	i, err := s.get(c.Param("id"))
	if err != nil {
		return nil, err
	}
	if err := s.requireStatus(i, "available"); err != nil {
		return nil, err
	}
	unlock, err := s.lock(i.ID)
	if err != nil {
		return nil, err
	}
	defer unlock()
	s.setStatus(i.ID, "stopping", "")
	if err := s.env.Docker.Stop(i.ContainerID, 30); err != nil {
		s.setStatus(i.ID, "available", "")
		return nil, err
	}
	s.setStatus(i.ID, "stopped", "")
	return s.get(i.ID)
}

func (s *Service) reboot(c *httpx.Ctx) (any, error) {
	i, err := s.get(c.Param("id"))
	if err != nil {
		return nil, err
	}
	if err := s.requireStatus(i, "available"); err != nil {
		return nil, err
	}
	unlock, err := s.lock(i.ID)
	if err != nil {
		return nil, err
	}
	s.setStatus(i.ID, "rebooting", "")
	go func() {
		defer unlock()
		e, _ := findEngine(i.Engine)
		if err := s.env.Docker.Restart(i.ContainerID); err != nil {
			s.setStatus(i.ID, "failed", err.Error())
			return
		}
		pass, _ := s.password(i)
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
		defer cancel()
		if err := s.waitReady(ctx, i.ContainerID, e, i.MasterUsername, pass, i.DBName); err != nil {
			s.setStatus(i.ID, "failed", err.Error())
			return
		}
		s.setStatus(i.ID, "available", "")
	}()
	return s.get(i.ID)
}

func (s *Service) modify(c *httpx.Ctx) (any, error) {
	var in struct {
		Class               string    `json:"class"`
		StorageGB           int       `json:"storage_gb"`
		BackupRetentionDays *int      `json:"backup_retention_days"`
		DeletionProtection  *bool     `json:"deletion_protection"`
		Tags                core.Tags `json:"tags"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	i, err := s.get(c.Param("id"))
	if err != nil {
		return nil, err
	}
	var cl InstanceClass
	if in.Class != "" && in.Class != i.Class {
		var ok bool
		if cl, ok = findClass(in.Class); !ok {
			return nil, core.BadRequest("unknown instance class %q", in.Class)
		}
		if err := s.requireStatus(i, "available", "stopped"); err != nil {
			return nil, err
		}
		mem := cl.MemoryMB * 1024 * 1024
		if err := s.env.Docker.C.UpdateContainer(i.ContainerID, docker.UpdateContainerOptions{
			Memory: int(mem), MemorySwap: int(mem * 2), CPUPeriod: 100000, CPUQuota: int(min(cl.VCPUs, s.hostCPU) * 100000),
		}); err != nil {
			return nil, fmt.Errorf("resize: %w", err)
		}
	}
	e, _ := findEngine(i.Engine)
	if in.BackupRetentionDays != nil && (*in.BackupRetentionDays < 0 || *in.BackupRetentionDays > 35 || (e.dump == nil && *in.BackupRetentionDays > 0)) {
		return nil, core.BadRequest("backup_retention_days must be 0-35 (and 0 for engines without backup support)")
	}
	return store.Update(s.env.Store, cInstances, i.ID, func(x *Instance) error {
		if cl.Name != "" {
			x.Class, x.VCPUs, x.MemoryMB = cl.Name, cl.VCPUs, cl.MemoryMB
		}
		if in.StorageGB > x.StorageGB {
			x.StorageGB = in.StorageGB
		}
		if in.BackupRetentionDays != nil {
			x.BackupRetentionDays = *in.BackupRetentionDays
		}
		if in.DeletionProtection != nil {
			x.DeletionProtection = *in.DeletionProtection
		}
		if in.Tags != nil {
			x.Tags = in.Tags
		}
		return nil
	})
}

func (s *Service) delete(c *httpx.Ctx) (any, error) {
	i, err := s.get(c.Param("id"))
	if err != nil {
		return nil, err
	}
	if i.DeletionProtection {
		return nil, core.Errf(http.StatusConflict, "InvalidParameterCombination", "deletion protection is enabled for %s", i.ID)
	}
	unlock, err := s.lock(i.ID)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if c.Query("final_snapshot") == "true" && i.Status == "available" {
		if _, err := s.snapshot(c.R.Context(), i, "manual", i.ID+"-final-"+time.Now().UTC().Format("20060102150405")); err != nil {
			return nil, fmt.Errorf("final snapshot failed (instance kept): %w", err)
		}
	}
	s.setStatus(i.ID, "deleting", "")
	if i.ContainerID != "" {
		if err := s.env.Docker.Remove(i.ContainerID); err != nil {
			return nil, err
		}
	}
	_ = s.env.Docker.RemoveVolume(volumeName(i.ID))
	if i.SecretName != "" {
		s.secrets.Remove(i.SecretName)
	}
	s.vpc.Release("db:" + i.ID)
	if c.Query("delete_automated_backups") != "false" {
		for _, sn := range store.List[Snapshot](s.env.Store, cSnapshots) {
			if sn.SourceInstance == i.ID && sn.Type == "automated" {
				s.removeSnapshot(sn.ID)
			}
		}
	}
	return nil, store.Delete(s.env.Store, cInstances, i.ID)
}

func (s *Service) resetPassword(c *httpx.Ctx) (any, error) {
	var in struct {
		Password string `json:"password"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	i, err := s.get(c.Param("id"))
	if err != nil {
		return nil, err
	}
	if err := s.requireStatus(i, "available"); err != nil {
		return nil, err
	}
	if in.Password == "" {
		in.Password = core.NewSecret(24)
	}
	if len(in.Password) < 8 || strings.ContainsAny(in.Password, `"'/@ \`) {
		return nil, core.BadRequest("password must be at least 8 characters and may not contain quotes, slashes, @ or spaces")
	}
	old, err := s.password(i)
	if err != nil {
		return nil, err
	}
	var cmd []string
	switch i.Engine {
	case "postgres":
		cmd = sh("psql -U " + q(i.MasterUsername) + " -d postgres -c " + q(fmt.Sprintf(`ALTER USER "%s" WITH PASSWORD '%s'`, i.MasterUsername, in.Password)))
	case "mysql":
		cmd = sh("MYSQL_PWD=" + q(old) + " mysql -h127.0.0.1 -uroot -e " + q(fmt.Sprintf("ALTER USER 'root'@'%%' IDENTIFIED BY '%s'; ALTER USER 'root'@'localhost' IDENTIFIED BY '%s'; ALTER USER '%s'@'%%' IDENTIFIED BY '%s';", in.Password, in.Password, i.MasterUsername, in.Password)))
	case "mariadb":
		cmd = sh("MYSQL_PWD=" + q(old) + " mariadb -h127.0.0.1 -uroot -e " + q(fmt.Sprintf("ALTER USER 'root'@'%%' IDENTIFIED BY '%s'; ALTER USER 'root'@'localhost' IDENTIFIED BY '%s'; ALTER USER '%s'@'%%' IDENTIFIED BY '%s';", in.Password, in.Password, i.MasterUsername, in.Password)))
	case "mongodb":
		cmd = sh("mongosh --quiet -u " + q(i.MasterUsername) + " -p " + q(old) + " --authenticationDatabase admin admin --eval " + q(fmt.Sprintf(`db.changeUserPassword("%s", "%s")`, i.MasterUsername, in.Password)))
	default:
		return nil, core.BadRequest("password rotation is not supported for engine %s; create a new cluster from a snapshot instead", i.Engine)
	}
	res, err := s.env.Docker.Exec(c.R.Context(), i.ContainerID, cmd, nil)
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("password change failed: %s", tail(res.Stdout+res.Stderr, 400))
	}
	v, _, _ := s.secrets.Value(i.SecretName, "", "")
	var m map[string]any
	_ = json.Unmarshal([]byte(v), &m)
	if m == nil {
		m = map[string]any{}
	}
	m["password"] = in.Password
	b, _ := json.Marshal(m)
	if _, err := s.secrets.Put(i.SecretName, string(b), "", "rds"); err != nil {
		return nil, err
	}
	return map[string]string{"secret_name": i.SecretName, "status": "password updated"}, nil
}

func (s *Service) query(c *httpx.Ctx) (any, error) {
	var in struct {
		SQL      string `json:"sql"`
		Database string `json:"database"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	i, err := s.get(c.Param("id"))
	if err != nil {
		return nil, err
	}
	if err := s.requireStatus(i, "available"); err != nil {
		return nil, err
	}
	e, _ := findEngine(i.Engine)
	if e.query == nil {
		return nil, core.BadRequest("engine %s does not support queries", e.Name)
	}
	if strings.TrimSpace(in.SQL) == "" {
		return nil, core.BadRequest("sql is required")
	}
	if in.Database == "" {
		in.Database = i.DBName
	}
	// A plain identifier: psql and mongosh would treat "host=..." or a URI as a
	// connection target and send the master password there.
	if !dbNameRe.MatchString(in.Database) {
		return nil, core.BadRequest("database must be a plain name (letters, digits, _ $ -)")
	}
	pass, err := s.password(i)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(c.R.Context(), 60*time.Second)
	defer cancel()
	start := time.Now()
	res, err := s.env.Docker.Exec(ctx, i.ContainerID, e.query(i.MasterUsername, pass, in.Database, in.SQL), nil)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"exit_code": res.ExitCode, "output": res.Stdout, "error": strings.TrimSpace(res.Stderr), "duration_ms": time.Since(start).Milliseconds()}
	if res.ExitCode == 0 {
		switch i.Engine {
		case "postgres":
			out["columns"], out["rows"] = parseTable(res.Stdout, ',')
		case "mysql", "mariadb":
			out["columns"], out["rows"] = parseTable(res.Stdout, '\t')
		}
	}
	return out, nil
}

func parseTable(s string, sep rune) ([]string, [][]string) {
	r := csv.NewReader(strings.NewReader(s))
	r.Comma = sep
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	recs, err := r.ReadAll()
	if err != nil || len(recs) == 0 {
		return nil, nil
	}
	return recs[0], recs[1:]
}

func (s *Service) logs(c *httpx.Ctx) (any, error) {
	i, err := s.get(c.Param("id"))
	if err != nil {
		return nil, err
	}
	if i.ContainerID == "" {
		return map[string]string{"output": ""}, nil
	}
	out, err := s.env.Docker.Logs(i.ContainerID, c.QueryInt("tail", 500), time.Time{})
	return map[string]string{"output": out}, err
}

// ---- snapshots ----

func (s *Service) snapshot(ctx context.Context, i Instance, typ, id string) (Snapshot, error) {
	e, _ := findEngine(i.Engine)
	if e.dump == nil {
		return Snapshot{}, core.BadRequest("engine %s does not support snapshots", e.Name)
	}
	s.snapMu.Lock()
	if store.Has(s.env.Store, cSnapshots, id) {
		s.snapMu.Unlock()
		return Snapshot{}, core.Conflict("snapshot %q already exists", id)
	}
	pass, err := s.password(i)
	if err != nil {
		return Snapshot{}, err
	}
	sn := Snapshot{ID: id, ARN: s.env.ARN("rds", "snapshot:"+id), SourceInstance: i.ID, Kind: i.Kind, Engine: i.Engine,
		EngineVersion: i.EngineVersion, Type: typ, Status: "creating", MasterUsername: i.MasterUsername, DBName: i.DBName,
		PasswordCT: s.secrets.Encrypt([]byte(pass)), StorageGB: i.StorageGB, CreatedAt: core.Now()}
	err = store.Put(s.env.Store, cSnapshots, id, sn)
	s.snapMu.Unlock()
	if err != nil {
		return sn, err
	}
	if err := os.MkdirAll(s.env.Cfg.Path("rds-snapshots"), 0o700); err != nil {
		return sn, err
	}
	f, err := os.Create(s.snapshotFile(id))
	if err != nil {
		return sn, err
	}
	var stderr bytes.Buffer
	ex, err := s.env.Docker.C.CreateExec(docker.CreateExecOptions{Container: i.ContainerID, Cmd: e.dump(i.MasterUsername, pass), AttachStdout: true, AttachStderr: true, Context: ctx})
	if err == nil {
		err = s.env.Docker.C.StartExec(ex.ID, docker.StartExecOptions{OutputStream: f, ErrorStream: &stderr, Context: ctx})
	}
	f.Close()
	if err == nil {
		if ins, ierr := s.env.Docker.C.InspectExec(ex.ID); ierr == nil && ins.ExitCode != 0 {
			err = fmt.Errorf("backup exited with code %d: %s", ins.ExitCode, tail(stderr.String(), 400))
		}
	}
	if err != nil {
		os.Remove(s.snapshotFile(id))
		sn, _ = store.Update(s.env.Store, cSnapshots, id, func(x *Snapshot) error { x.Status, x.StatusReason = "failed", err.Error(); return nil })
		return sn, err
	}
	st, _ := os.Stat(s.snapshotFile(id))
	sn, err = store.Update(s.env.Store, cSnapshots, id, func(x *Snapshot) error { x.Status, x.SizeBytes = "available", st.Size(); return nil })
	if typ == "automated" {
		_, _ = store.Update(s.env.Store, cInstances, i.ID, func(x *Instance) error { n := core.Now(); x.LatestBackup = &n; return nil })
	}
	return sn, err
}

func (s *Service) createSnapshot(c *httpx.Ctx) (any, error) {
	var in struct {
		ID string `json:"id"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	i, err := s.get(c.Param("id"))
	if err != nil {
		return nil, err
	}
	if err := s.requireStatus(i, "available"); err != nil {
		return nil, err
	}
	if in.ID == "" {
		in.ID = i.ID + "-" + time.Now().UTC().Format("20060102-150405")
	}
	if !idRe.MatchString(in.ID) {
		return nil, core.BadRequest("snapshot id must start with a letter and contain only lowercase letters, digits and hyphens")
	}
	unlock, err := s.lock(i.ID)
	if err != nil {
		return nil, err
	}
	defer unlock()
	s.setStatus(i.ID, "backing-up", "")
	defer s.setStatus(i.ID, "available", "")
	sn, err := s.snapshot(c.R.Context(), i, "manual", in.ID)
	if err != nil {
		return nil, err
	}
	return sn.view(), nil
}

func (s *Service) listSnapshots(c *httpx.Ctx) (any, error) {
	out := []Snapshot{}
	for _, sn := range store.List[Snapshot](s.env.Store, cSnapshots) {
		if src := c.Query("instance"); src != "" && sn.SourceInstance != src {
			continue
		}
		out = append(out, sn.view())
	}
	slices.SortFunc(out, func(a, b Snapshot) int { return b.CreatedAt.Compare(a.CreatedAt) })
	return out, nil
}

func (s *Service) removeSnapshot(id string) {
	_ = os.Remove(s.snapshotFile(id))
	_ = store.Delete(s.env.Store, cSnapshots, id)
}

func (s *Service) deleteSnapshot(c *httpx.Ctx) (any, error) {
	if !store.Has(s.env.Store, cSnapshots, c.Param("snap")) {
		return nil, core.NotFound("snapshot", c.Param("snap"))
	}
	s.removeSnapshot(c.Param("snap"))
	return nil, nil
}

func (s *Service) restore(c *httpx.Ctx) (any, error) {
	var in createInput
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	sn, err := store.Get[Snapshot](s.env.Store, cSnapshots, c.Param("snap"))
	if err != nil {
		return nil, core.NotFound("snapshot", c.Param("snap"))
	}
	if sn.Status != "available" {
		return nil, core.Errf(http.StatusConflict, "InvalidDBSnapshotState", "snapshot %s is %s", sn.ID, sn.Status)
	}
	pass, err := s.secrets.Decrypt(sn.PasswordCT)
	if err != nil {
		return nil, err
	}
	// A restored database keeps the snapshot's engine and master credentials, as in AWS.
	in.Engine, in.EngineVersion, in.MasterUsername, in.MasterPassword, in.DBName = sn.Engine, sn.EngineVersion, sn.MasterUsername, string(pass), sn.DBName
	if in.StorageGB < sn.StorageGB {
		in.StorageGB = sn.StorageGB
	}
	return s.provision(in, &sn)
}

// Run takes daily automated backups and prunes them after the retention period.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		// Automated backups outlive neither their instance nor a retention of 0 (after a grace week).
		for _, sn := range store.List[Snapshot](s.env.Store, cSnapshots) {
			if sn.Type != "automated" {
				continue
			}
			inst, err := store.Get[Instance](s.env.Store, cInstances, sn.SourceInstance)
			if (err != nil || inst.BackupRetentionDays == 0) && time.Since(sn.CreatedAt) > 7*24*time.Hour {
				s.removeSnapshot(sn.ID)
			}
		}
		for _, i := range store.List[Instance](s.env.Store, cInstances) {
			if i.BackupRetentionDays <= 0 {
				continue
			}
			for _, sn := range store.List[Snapshot](s.env.Store, cSnapshots) {
				if sn.SourceInstance == i.ID && sn.Type == "automated" && time.Since(sn.CreatedAt) > time.Duration(i.BackupRetentionDays)*24*time.Hour {
					s.removeSnapshot(sn.ID)
				}
			}
			if i.Status != "available" || (i.LatestBackup != nil && time.Since(*i.LatestBackup) < 24*time.Hour) || time.Since(i.CreatedAt) < time.Hour {
				continue
			}
			unlock, err := s.lock(i.ID)
			if err != nil {
				continue
			}
			id := "rds-" + i.ID + "-" + time.Now().UTC().Format("2006-01-02-15-04")
			if _, err := s.snapshot(ctx, i, "automated", id); err != nil {
				log.Printf("rds: automated backup of %s failed: %v", i.ID, err)
			}
			unlock()
		}
	}
}

// PrivateIP resolves a database instance to its private IP (for DNS aliases).
func (s *Service) PrivateIP(id string) (string, bool) {
	i, err := store.Get[Instance](s.env.Store, cInstances, id)
	if err != nil || i.Status == "failed" || i.Status == "deleting" {
		return "", false
	}
	return i.Endpoint.PrivateIP, true
}
