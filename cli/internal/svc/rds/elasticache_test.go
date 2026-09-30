package rds_test

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi/awstest"
	"github.com/homecloudhq/homecloud/cli/internal/runtime"
)

func list(m map[string]any, k string) []any { l, _ := m[k].([]any); return l }

// cacheNode waits for a cluster to be available and returns its description with node info.
func waitCache(t *testing.T, h *awstest.Harness, id string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Minute)
	for {
		d := h.AWSJSON(t, "elasticache", "describe-cache-clusters", "--cache-cluster-id", id, "--show-cache-node-info")["CacheClusters"].([]any)[0].(map[string]any)
		switch d["CacheClusterStatus"] {
		case "available":
			return d
		case "create-failed":
			t.Fatalf("%s failed: %v", id, d)
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: status %v", id, d["CacheClusterStatus"])
		}
		time.Sleep(time.Second)
	}
}

func waitGroup(t *testing.T, h *awstest.Harness, id string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Minute)
	for {
		d := h.AWSJSON(t, "elasticache", "describe-replication-groups", "--replication-group-id", id)["ReplicationGroups"].([]any)[0].(map[string]any)
		if d["Status"] == "available" {
			return d
		}
		if d["Status"] == "create-failed" || time.Now().After(deadline) {
			t.Fatalf("%s: status %v", id, d["Status"])
		}
		time.Sleep(time.Second)
	}
}

// client runs a shell command in a container on the VPC network, the way an
// application in the VPC reaches a cache through the endpoint it was given.
func client(t *testing.T, h *awstest.Harness, network, script string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cid, err := h.Env.Docker.Run(ctx, runtime.RunSpec{Image: "redis:7.4-alpine", Entrypoint: []string{"sh", "-c"}, Cmd: []string{script},
		Network: network, Labels: runtime.Labels("test", "cache-client", nil), Start: true})
	if err != nil {
		t.Fatalf("client container: %v", err)
	}
	defer h.Env.Docker.Remove(cid)
	if _, err := h.Env.Docker.C.WaitContainerWithContext(cid, ctx); err != nil {
		t.Fatalf("client wait: %v", err)
	}
	out, err := h.Env.Docker.Logs(cid, 50, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func endpointOf(t *testing.T, d map[string]any) (string, string) {
	t.Helper()
	nodes := list(d, "CacheNodes")
	if len(nodes) != 1 {
		t.Fatalf("cache nodes: %v", d)
	}
	ep := nodes[0].(map[string]any)["Endpoint"].(map[string]any)
	port, _ := ep["Port"].(float64)
	return str(ep, "Address"), strconv.Itoa(int(port))
}

func TestElastiCacheMetadataAndErrors(t *testing.T) {
	h, _ := noDocker(t)

	o := h.AWS(t, "elasticache", "describe-cache-engine-versions", "--engine", "redis")
	if !strings.Contains(o, `"redis7"`) || !strings.Contains(o, "redis6.x") || strings.Contains(o, "memcached") {
		t.Fatalf("redis versions: %s", o)
	}
	if o := h.AWS(t, "elasticache", "describe-cache-engine-versions"); !strings.Contains(o, "valkey8") || !strings.Contains(o, "memcached1.6") {
		t.Fatalf("all versions: %s", o)
	}
	if l := h.AWSJSON(t, "elasticache", "describe-cache-engine-versions", "--engine", "redis", "--default-only")["CacheEngineVersions"].([]any); len(l) != 1 {
		t.Fatalf("default only: %v", l)
	}
	if l := h.AWSJSON(t, "elasticache", "describe-cache-clusters")["CacheClusters"].([]any); len(l) != 0 {
		t.Fatalf("empty list: %v", l)
	}

	for _, c := range []struct {
		args []string
		code string
	}{
		{[]string{"describe-cache-clusters", "--cache-cluster-id", "nope"}, "CacheClusterNotFound"},
		{[]string{"delete-cache-cluster", "--cache-cluster-id", "nope"}, "CacheClusterNotFound"},
		{[]string{"reboot-cache-cluster", "--cache-cluster-id", "nope", "--cache-node-ids-to-reboot", "0001"}, "CacheClusterNotFound"},
		{[]string{"modify-cache-cluster", "--cache-cluster-id", "nope"}, "CacheClusterNotFound"},
		{[]string{"describe-replication-groups", "--replication-group-id", "nope"}, "ReplicationGroupNotFoundFault"},
		{[]string{"delete-replication-group", "--replication-group-id", "nope"}, "ReplicationGroupNotFoundFault"},
		{[]string{"describe-snapshots", "--snapshot-name", "nope"}, "SnapshotNotFoundFault"},
		{[]string{"delete-snapshot", "--snapshot-name", "nope"}, "SnapshotNotFoundFault"},
		{[]string{"create-snapshot", "--snapshot-name", "s", "--cache-cluster-id", "nope"}, "CacheClusterNotFound"},
		{[]string{"describe-cache-subnet-groups", "--cache-subnet-group-name", "nope"}, "CacheSubnetGroupNotFoundFault"},
		{[]string{"delete-cache-subnet-group", "--cache-subnet-group-name", "nope"}, "CacheSubnetGroupNotFoundFault"},
		{[]string{"create-cache-subnet-group", "--cache-subnet-group-name", "g", "--cache-subnet-group-description", "d", "--subnet-ids", "subnet-nope"}, "InvalidSubnet"},
		{[]string{"list-tags-for-resource", "--resource-name", "junk"}, "InvalidARN"},
		{[]string{"list-tags-for-resource", "--resource-name", "arn:aws:elasticache:us-east-1:" + h.Env.AccountID + ":cluster:nope"}, "CacheClusterNotFound"},
		{[]string{"list-tags-for-resource", "--resource-name", "arn:aws:elasticache:us-east-1:" + h.Env.AccountID + ":replicationgroup:nope"}, "ReplicationGroupNotFoundFault"},
		// Rejected before anything is created.
		{[]string{"create-cache-cluster", "--cache-cluster-id", "x", "--engine", "oracle"}, "InvalidParameterValue"},
		{[]string{"create-cache-cluster", "--cache-cluster-id", "x", "--engine", "redis", "--num-cache-nodes", "3"}, "InvalidParameterValue"},
		{[]string{"create-cache-cluster", "--cache-cluster-id", "x", "--engine", "redis", "--port", "7000"}, "InvalidParameterValue"},
		{[]string{"create-cache-cluster", "--cache-cluster-id", "x", "--engine", "redis", "--cache-node-type", "cache.nope.huge"}, "InvalidParameterValue"},
		{[]string{"create-cache-cluster", "--cache-cluster-id", "x", "--engine", "redis", "--engine-version", "3.2"}, "InvalidParameterValue"},
		{[]string{"create-cache-cluster", "--cache-cluster-id", "x", "--engine", "memcached", "--auth-token", "0123456789abcdef0123"}, "InvalidParameterCombination"},
		{[]string{"create-cache-cluster", "--cache-cluster-id", "x", "--engine", "redis", "--cache-subnet-group-name", "nope"}, "CacheSubnetGroupNotFoundFault"},
		{[]string{"create-cache-cluster", "--cache-cluster-id", "x", "--engine", "redis", "--security-group-ids", "sg-0123456789abcdef0"}, "InvalidParameterValue"},
		{[]string{"create-cache-cluster", "--cache-cluster-id", "bad--id", "--engine", "redis"}, "InvalidParameterValue"},
		{[]string{"create-replication-group", "--replication-group-id", "g", "--replication-group-description", "d", "--engine", "redis", "--num-cache-clusters", "2"}, "InvalidParameterValue"},
		{[]string{"create-replication-group", "--replication-group-id", "g", "--replication-group-description", "d", "--engine", "redis", "--automatic-failover-enabled"}, "InvalidParameterCombination"},
		{[]string{"create-replication-group", "--replication-group-id", "g", "--replication-group-description", "d", "--engine", "redis", "--transit-encryption-enabled"}, "InvalidParameterCombination"},
		{[]string{"create-replication-group", "--replication-group-id", "g", "--replication-group-description", "d", "--engine", "memcached"}, "InvalidParameterValue"},
	} {
		if o, err := h.AWSErr(t, append([]string{"elasticache"}, c.args...)...); err == nil || !strings.Contains(o, c.code) {
			t.Errorf("%v: want %s, got %v %s", c.args, c.code, err, o)
		}
	}
}

func TestElastiCacheIAM(t *testing.T) {
	h, _ := noDocker(t)
	akid, secret := h.User(t, "ro", "ReadOnlyAccess")
	if o, err := h.AWSAs(t, akid, secret, "", "elasticache", "describe-cache-clusters"); err != nil {
		t.Fatalf("read-only describe: %v %s", err, o)
	}
	acct := h.Env.AccountID
	for _, args := range [][]string{
		{"elasticache", "create-cache-cluster", "--cache-cluster-id", "x", "--engine", "redis"},
		{"elasticache", "create-cache-cluster", "--cache-cluster-id", "x", "--engine", "redis", "--tags", "Key=a,Value=b"},
		{"elasticache", "modify-cache-cluster", "--cache-cluster-id", "unknown"}, // authorized before existence
		{"elasticache", "delete-cache-cluster", "--cache-cluster-id", "unknown"},
		{"elasticache", "reboot-cache-cluster", "--cache-cluster-id", "unknown", "--cache-node-ids-to-reboot", "0001"},
		{"elasticache", "create-replication-group", "--replication-group-id", "g", "--replication-group-description", "d"},
		{"elasticache", "modify-replication-group", "--replication-group-id", "unknown"},
		{"elasticache", "delete-replication-group", "--replication-group-id", "unknown"},
		{"elasticache", "create-snapshot", "--snapshot-name", "s", "--cache-cluster-id", "unknown"},
		{"elasticache", "delete-snapshot", "--snapshot-name", "unknown"},
		{"elasticache", "create-cache-subnet-group", "--cache-subnet-group-name", "g", "--cache-subnet-group-description", "d", "--subnet-ids", "subnet-0123456789abcdef0"},
		{"elasticache", "delete-cache-subnet-group", "--cache-subnet-group-name", "unknown"},
		{"elasticache", "add-tags-to-resource", "--resource-name", "arn:aws:elasticache:us-east-1:" + acct + ":cluster:unknown", "--tags", "Key=a,Value=b"},
		{"elasticache", "remove-tags-from-resource", "--resource-name", "arn:aws:elasticache:us-east-1:" + acct + ":cluster:unknown", "--tag-keys", "a"},
	} {
		if o, err := h.AWSAs(t, akid, secret, "", args...); err == nil || !strings.Contains(o, "AccessDenied") {
			t.Fatalf("%v as read-only: %v %s", args, err, o)
		}
	}
	// A policy scoped to one cluster ARN.
	h.Native(t, "POST", "/api/v1/iam/policies", map[string]any{"name": "one-cache", "document": map[string]any{"Version": "2012-10-17",
		"Statement": []any{map[string]any{"Effect": "Allow", "Action": "elasticache:*", "Resource": "arn:aws:elasticache:us-east-1:" + acct + ":cluster:allowed"}}}})
	akid2, secret2 := h.User(t, "scoped", "one-cache")
	if o, err := h.AWSAs(t, akid2, secret2, "", "elasticache", "describe-cache-clusters", "--cache-cluster-id", "allowed"); err == nil || !strings.Contains(o, "CacheClusterNotFound") {
		t.Fatalf("scoped, allowed id: %v %s", err, o)
	}
	for _, args := range [][]string{
		{"describe-cache-clusters", "--cache-cluster-id", "other"},
		{"delete-cache-cluster", "--cache-cluster-id", "other"},
		{"describe-replication-groups", "--replication-group-id", "other"},
	} {
		if o, err := h.AWSAs(t, akid2, secret2, "", append([]string{"elasticache"}, args...)...); err == nil || !strings.Contains(o, "AccessDenied") {
			t.Fatalf("scoped, %v: %v %s", args, err, o)
		}
	}
	found := false
	for _, a := range h.AuditLog() {
		if strings.HasPrefix(a, "elasticache:DescribeCacheClusters ") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no audit entry: %v", h.AuditLog())
	}
}

// TestElastiCacheRedisLifecycle creates a Redis cluster, reaches it through the
// endpoint it reports, snapshots and restores it, and deletes it.
func TestElastiCacheRedisLifecycle(t *testing.T) {
	h, v, subs := withDocker(t)
	var network string
	for _, x := range v.List() {
		if x.Default {
			network = x.Network
		}
	}
	sg := v.DefaultSecurityGroup(v.Subnets()[0].VpcID)
	h.AWS(t, "elasticache", "create-cache-subnet-group", "--cache-subnet-group-name", "cache-net", "--cache-subnet-group-description", "d",
		"--subnet-ids", subs[0], subs[1], "--tags", "Key=a,Value=b")
	g := h.AWSJSON(t, "elasticache", "describe-cache-subnet-groups", "--cache-subnet-group-name", "cache-net")["CacheSubnetGroups"].([]any)[0].(map[string]any)
	if len(list(g, "Subnets")) != 2 || !strings.HasSuffix(str(g, "ARN"), ":subnetgroup:cache-net") {
		t.Fatalf("subnet group: %v", g)
	}
	for _, id := range []string{"sessions", "sessions-copy", "locked"} {
		t.Cleanup(func() {
			_, _ = h.AWSErr(t, "elasticache", "delete-cache-cluster", "--cache-cluster-id", id)
		})
	}

	// Creation returns at once in the creating state, with no endpoint yet.
	c := h.AWSJSON(t, "elasticache", "create-cache-cluster", "--cache-cluster-id", "Sessions", "--engine", "redis", "--engine-version", "7.2",
		"--cache-node-type", "cache.t3.micro", "--num-cache-nodes", "1", "--port", "6379", "--cache-subnet-group-name", "cache-net",
		"--security-group-ids", sg, "--tags", "Key=env,Value=qa")["CacheCluster"].(map[string]any)
	if c["CacheClusterId"] != "sessions" || c["CacheClusterStatus"] != "creating" || c["Engine"] != "redis" || c["CacheNodeType"] != "cache.t3.micro" ||
		c["CacheSubnetGroupName"] != "cache-net" || !strings.HasSuffix(str(c, "ARN"), ":cluster:sessions") || c["NumCacheNodes"] != float64(1) {
		t.Fatalf("create: %v", c)
	}
	arn := str(c, "ARN")
	if o, err := h.AWSErr(t, "elasticache", "create-cache-cluster", "--cache-cluster-id", "sessions", "--engine", "redis"); err == nil || !strings.Contains(o, "CacheClusterAlreadyExists") {
		t.Fatalf("duplicate: %v %s", err, o)
	}
	if o, err := h.AWSErr(t, "elasticache", "delete-cache-subnet-group", "--cache-subnet-group-name", "cache-net"); err == nil || !strings.Contains(o, "CacheSubnetGroupInUse") {
		t.Fatalf("subnet group in use: %v %s", err, o)
	}
	// A cluster that is still being created cannot be rebooted (unless it just became available).
	if o, err := h.AWSErr(t, "elasticache", "reboot-cache-cluster", "--cache-cluster-id", "sessions", "--cache-node-ids-to-reboot", "0001"); err != nil && !strings.Contains(o, "InvalidCacheClusterState") {
		t.Fatalf("reboot while creating: %v %s", err, o)
	}

	d := waitCache(t, h, "sessions")
	host, port := endpointOf(t, d)
	if port != "6379" || !strings.HasSuffix(host, ".elasticache.internal") || d["EngineVersion"] != "7.2" || d["AuthTokenEnabled"] != false ||
		d["SecurityGroups"].([]any)[0].(map[string]any)["SecurityGroupId"] != sg || d["ConfigurationEndpoint"] != nil {
		t.Fatalf("available: %v", d)
	}

	// The application reaches the cache through the reported endpoint (no password: no AuthToken).
	if out := client(t, h, network, "redis-cli -h "+host+" -p "+port+" ping"); !strings.Contains(out, "PONG") {
		t.Fatalf("PING %s:%s: %q", host, port, out)
	}
	if out := client(t, h, network, "redis-cli -h "+host+" -p "+port+" set greeting hello && redis-cli -h "+host+" -p "+port+" get greeting"); !strings.Contains(out, "hello") {
		t.Fatalf("set/get: %q", out)
	}

	// Filters, tags, modify.
	if l := h.AWSJSON(t, "elasticache", "describe-cache-clusters")["CacheClusters"].([]any); len(l) != 1 || l[0].(map[string]any)["CacheNodes"] != nil {
		t.Fatalf("listing (no node info without ShowCacheNodeInfo): %v", l)
	}
	h.AWS(t, "elasticache", "add-tags-to-resource", "--resource-name", arn, "--tags", "Key=team,Value=web")
	if o := h.AWS(t, "elasticache", "list-tags-for-resource", "--resource-name", arn); !strings.Contains(o, `"team"`) || !strings.Contains(o, `"env"`) {
		t.Fatalf("tags: %s", o)
	}
	h.AWS(t, "elasticache", "remove-tags-from-resource", "--resource-name", arn, "--tag-keys", "env")
	if o := h.AWS(t, "elasticache", "list-tags-for-resource", "--resource-name", arn); strings.Contains(o, `"env"`) || !strings.Contains(o, `"team"`) {
		t.Fatalf("tags after remove: %s", o)
	}
	m := h.AWSJSON(t, "elasticache", "modify-cache-cluster", "--cache-cluster-id", "sessions", "--cache-node-type", "cache.t3.small",
		"--snapshot-retention-limit", "3", "--preferred-maintenance-window", "mon:03:00-mon:04:00", "--apply-immediately")["CacheCluster"].(map[string]any)
	if m["CacheNodeType"] != "cache.t3.small" || m["SnapshotRetentionLimit"] != float64(3) || m["PreferredMaintenanceWindow"] != "mon:03:00-mon:04:00" {
		t.Fatalf("modify: %v", m)
	}
	if o, err := h.AWSErr(t, "elasticache", "modify-cache-cluster", "--cache-cluster-id", "sessions", "--num-cache-nodes", "2"); err == nil || !strings.Contains(o, "InvalidParameterValue") {
		t.Fatalf("scale out: %v %s", err, o)
	}
	if o, err := h.AWSErr(t, "elasticache", "modify-cache-cluster", "--cache-cluster-id", "sessions", "--engine-version", "7.4"); err == nil || !strings.Contains(o, "InvalidParameterCombination") {
		t.Fatalf("engine upgrade: %v %s", err, o)
	}

	// Snapshot and restore into a new cluster.
	sn := h.AWSJSON(t, "elasticache", "create-snapshot", "--cache-cluster-id", "sessions", "--snapshot-name", "sessions-snap",
		"--tags", "Key=k,Value=v")["Snapshot"].(map[string]any)
	if sn["SnapshotName"] != "sessions-snap" || sn["SnapshotStatus"] != "available" || sn["SnapshotSource"] != "manual" || sn["Engine"] != "redis" ||
		sn["CacheClusterId"] != "sessions" || !strings.HasSuffix(str(sn, "ARN"), ":snapshot:sessions-snap") {
		t.Fatalf("snapshot: %v", sn)
	}
	if o, err := h.AWSErr(t, "elasticache", "create-snapshot", "--cache-cluster-id", "sessions", "--snapshot-name", "sessions-snap"); err == nil || !strings.Contains(o, "SnapshotAlreadyExistsFault") {
		t.Fatalf("duplicate snapshot: %v %s", err, o)
	}
	if l := h.AWSJSON(t, "elasticache", "describe-snapshots", "--cache-cluster-id", "sessions", "--snapshot-source", "manual")["Snapshots"].([]any); len(l) != 1 {
		t.Fatalf("snapshots: %v", l)
	}
	if o := h.AWS(t, "elasticache", "list-tags-for-resource", "--resource-name", str(sn, "ARN")); !strings.Contains(o, `"k"`) {
		t.Fatalf("snapshot tags: %s", o)
	}
	h.AWS(t, "elasticache", "create-cache-cluster", "--cache-cluster-id", "sessions-copy", "--engine", "redis", "--snapshot-name", "sessions-snap")
	d2 := waitCache(t, h, "sessions-copy")
	host2, port2 := endpointOf(t, d2)
	if out := client(t, h, network, "redis-cli -h "+host2+" -p "+port2+" get greeting"); !strings.Contains(out, "hello") {
		t.Fatalf("restored data: %q", out)
	}
	if o, err := h.AWSErr(t, "elasticache", "create-cache-cluster", "--cache-cluster-id", "other", "--engine", "redis", "--snapshot-name", "nope-snap"); err == nil || !strings.Contains(o, "SnapshotNotFoundFault") {
		t.Fatalf("restore from unknown snapshot: %v %s", err, o)
	}
	h.AWS(t, "elasticache", "delete-cache-cluster", "--cache-cluster-id", "sessions-copy")

	// Reboot keeps the data (the cache persists to an append-only file).
	if r := h.AWSJSON(t, "elasticache", "reboot-cache-cluster", "--cache-cluster-id", "sessions", "--cache-node-ids-to-reboot", "0001")["CacheCluster"].(map[string]any); r["CacheClusterStatus"] != "rebooting cluster nodes" {
		t.Fatalf("reboot: %v", r)
	}
	waitCache(t, h, "sessions")
	if out := client(t, h, network, "redis-cli -h "+host+" -p "+port+" get greeting"); !strings.Contains(out, "hello") {
		t.Fatalf("data after reboot: %q", out)
	}

	// A cluster with an AuthToken requires it.
	h.AWS(t, "elasticache", "create-cache-cluster", "--cache-cluster-id", "locked", "--engine", "redis", "--auth-token", "SuperSecretToken123456")
	dl := waitCache(t, h, "locked")
	hostL, portL := endpointOf(t, dl)
	if dl["AuthTokenEnabled"] != true {
		t.Fatalf("locked: %v", dl)
	}
	if out := client(t, h, network, "redis-cli -h "+hostL+" -p "+portL+" ping"); !strings.Contains(out, "NOAUTH") {
		t.Fatalf("PING without the token: %q", out)
	}
	if out := client(t, h, network, "redis-cli --no-auth-warning -a SuperSecretToken123456 -h "+hostL+" -p "+portL+" ping"); !strings.Contains(out, "PONG") {
		t.Fatalf("PING with the token: %q", out)
	}
	h.AWS(t, "elasticache", "delete-cache-cluster", "--cache-cluster-id", "locked")

	// Delete with a final snapshot.
	del := h.AWSJSON(t, "elasticache", "delete-cache-cluster", "--cache-cluster-id", "sessions", "--final-snapshot-identifier", "sessions-final")["CacheCluster"].(map[string]any)
	if del["CacheClusterStatus"] != "deleting" {
		t.Fatalf("delete: %v", del)
	}
	if o, err := h.AWSErr(t, "elasticache", "describe-cache-clusters", "--cache-cluster-id", "sessions"); err == nil || !strings.Contains(o, "CacheClusterNotFound") {
		t.Fatalf("describe deleted: %v %s", err, o)
	}
	if o := h.AWS(t, "elasticache", "describe-snapshots", "--snapshot-name", "sessions-final"); !strings.Contains(o, "sessions-final") {
		t.Fatalf("final snapshot: %s", o)
	}
	h.AWS(t, "elasticache", "delete-snapshot", "--snapshot-name", "sessions-final")
	h.AWS(t, "elasticache", "delete-snapshot", "--snapshot-name", "sessions-snap")
	if o, err := h.AWSErr(t, "elasticache", "describe-snapshots", "--snapshot-name", "sessions-snap"); err == nil || !strings.Contains(o, "SnapshotNotFoundFault") {
		t.Fatalf("snapshot gone: %v %s", err, o)
	}
	h.AWS(t, "elasticache", "delete-cache-subnet-group", "--cache-subnet-group-name", "cache-net")
}

// TestElastiCacheReplicationGroupAndMemcached covers a Valkey replication
// group (one primary, reached through the group's primary endpoint) and a
// Memcached cluster (a configuration endpoint, no snapshots).
func TestElastiCacheReplicationGroupAndMemcached(t *testing.T) {
	h, v, _ := withDocker(t)
	var network string
	for _, x := range v.List() {
		if x.Default {
			network = x.Network
		}
	}
	t.Cleanup(func() {
		_, _ = h.AWSErr(t, "elasticache", "delete-replication-group", "--replication-group-id", "cart")
		_, _ = h.AWSErr(t, "elasticache", "delete-cache-cluster", "--cache-cluster-id", "mc")
	})

	g := h.AWSJSON(t, "elasticache", "create-replication-group", "--replication-group-id", "Cart", "--replication-group-description", "cart cache",
		"--engine", "valkey", "--cache-node-type", "cache.t3.micro", "--num-cache-clusters", "1", "--tags", "Key=env,Value=qa")["ReplicationGroup"].(map[string]any)
	if g["ReplicationGroupId"] != "cart" || g["Status"] != "creating" || g["Description"] != "cart cache" || g["AutomaticFailover"] != "disabled" ||
		g["ClusterEnabled"] != false || !strings.HasSuffix(str(g, "ARN"), ":replicationgroup:cart") || len(list(g, "MemberClusters")) != 1 || list(g, "MemberClusters")[0] != "cart-001" {
		t.Fatalf("create group: %v", g)
	}
	if o, err := h.AWSErr(t, "elasticache", "create-replication-group", "--replication-group-id", "cart", "--replication-group-description", "d", "--engine", "valkey"); err == nil || !strings.Contains(o, "ReplicationGroupAlreadyExists") {
		t.Fatalf("duplicate: %v %s", err, o)
	}
	g = waitGroup(t, h, "cart")
	ng := list(g, "NodeGroups")[0].(map[string]any)
	prim := ng["PrimaryEndpoint"].(map[string]any)
	if str(prim, "Address") != "cart.elasticache.internal" || prim["Port"] != float64(6379) || ng["ReaderEndpoint"] == nil ||
		ng["NodeGroupMembers"].([]any)[0].(map[string]any)["CurrentRole"] != "primary" {
		t.Fatalf("group endpoints: %v", ng)
	}
	if out := client(t, h, network, "redis-cli -h cart.elasticache.internal ping"); !strings.Contains(out, "PONG") {
		t.Fatalf("PING through the primary endpoint: %q", out)
	}
	if out := client(t, h, network, "redis-cli -h cart-ro.elasticache.internal set item 1 && redis-cli -h cart.elasticache.internal get item"); !strings.Contains(out, "1") {
		t.Fatalf("reader endpoint: %q", out)
	}

	// The primary is a cluster of the group.
	cl := h.AWSJSON(t, "elasticache", "describe-cache-clusters", "--cache-cluster-id", "cart-001", "--show-cache-node-info")["CacheClusters"].([]any)[0].(map[string]any)
	if cl["ReplicationGroupId"] != "cart" || cl["Engine"] != "valkey" {
		t.Fatalf("member: %v", cl)
	}
	if l := h.AWSJSON(t, "elasticache", "describe-cache-clusters", "--show-cache-clusters-not-in-replication-groups")["CacheClusters"].([]any); len(l) != 0 {
		t.Fatalf("clusters not in groups: %v", l)
	}
	if o, err := h.AWSErr(t, "elasticache", "delete-cache-cluster", "--cache-cluster-id", "cart-001"); err == nil || !strings.Contains(o, "InvalidCacheClusterState") {
		t.Fatalf("delete member: %v %s", err, o)
	}
	if o := h.AWS(t, "elasticache", "list-tags-for-resource", "--resource-name", str(g, "ARN")); !strings.Contains(o, `"env"`) {
		t.Fatalf("group tags: %s", o)
	}

	mg := h.AWSJSON(t, "elasticache", "modify-replication-group", "--replication-group-id", "cart", "--replication-group-description", "renamed",
		"--snapshot-retention-limit", "2", "--apply-immediately")["ReplicationGroup"].(map[string]any)
	if mg["Description"] != "renamed" || mg["SnapshotRetentionLimit"] != float64(2) {
		t.Fatalf("modify group: %v", mg)
	}

	sn := h.AWSJSON(t, "elasticache", "create-snapshot", "--replication-group-id", "cart", "--snapshot-name", "cart-snap")["Snapshot"].(map[string]any)
	if sn["ReplicationGroupId"] != "cart" || sn["SnapshotStatus"] != "available" || sn["CacheClusterId"] != "cart-001" || sn["NumNodeGroups"] != float64(1) {
		t.Fatalf("group snapshot: %v", sn)
	}
	if l := h.AWSJSON(t, "elasticache", "describe-snapshots", "--replication-group-id", "cart")["Snapshots"].([]any); len(l) != 1 {
		t.Fatalf("group snapshots: %v", l)
	}

	// Delete the group with a final snapshot: its primary goes with it.
	dg := h.AWSJSON(t, "elasticache", "delete-replication-group", "--replication-group-id", "cart", "--final-snapshot-identifier", "cart-final")["ReplicationGroup"].(map[string]any)
	if dg["Status"] != "deleting" {
		t.Fatalf("delete group: %v", dg)
	}
	if o, err := h.AWSErr(t, "elasticache", "describe-replication-groups", "--replication-group-id", "cart"); err == nil || !strings.Contains(o, "ReplicationGroupNotFoundFault") {
		t.Fatalf("describe deleted group: %v %s", err, o)
	}
	if o, err := h.AWSErr(t, "elasticache", "describe-cache-clusters", "--cache-cluster-id", "cart-001"); err == nil || !strings.Contains(o, "CacheClusterNotFound") {
		t.Fatalf("member deleted with the group: %v %s", err, o)
	}
	h.AWS(t, "elasticache", "describe-snapshots", "--snapshot-name", "cart-final")
	h.AWS(t, "elasticache", "delete-snapshot", "--snapshot-name", "cart-final")
	h.AWS(t, "elasticache", "delete-snapshot", "--snapshot-name", "cart-snap")

	// Memcached: a configuration endpoint, no snapshots.
	h.AWS(t, "elasticache", "create-cache-cluster", "--cache-cluster-id", "mc", "--engine", "memcached", "--num-cache-nodes", "1")
	d := waitCache(t, h, "mc")
	host, port := endpointOf(t, d)
	cfg := d["ConfigurationEndpoint"].(map[string]any)
	if port != "11211" || str(cfg, "Address") != host || d["Engine"] != "memcached" {
		t.Fatalf("memcached: %v", d)
	}
	if out := client(t, h, network, `printf 'version\r\nquit\r\n' | nc -w 3 `+host+" "+port); !strings.Contains(out, "VERSION") {
		t.Fatalf("memcached version: %q", out)
	}
	if o, err := h.AWSErr(t, "elasticache", "create-snapshot", "--cache-cluster-id", "mc", "--snapshot-name", "mc-snap"); err == nil || !strings.Contains(o, "SnapshotFeatureNotSupportedFault") {
		t.Fatalf("memcached snapshot: %v %s", err, o)
	}
	h.AWS(t, "elasticache", "delete-cache-cluster", "--cache-cluster-id", "mc")
}

func TestElastiCacheBoto3(t *testing.T) {
	h, _, _ := withDocker(t)
	out := h.Python(t, `
ec = boto3.client("elasticache")
c = ec.create_cache_cluster(CacheClusterId="py-cache", Engine="redis", CacheNodeType="cache.t3.micro", NumCacheNodes=1,
    Tags=[{"Key": "a", "Value": "b"}])["CacheCluster"]
print(c["CacheClusterStatus"], c["Engine"])
ec.get_waiter("cache_cluster_available").wait(CacheClusterId="py-cache", WaiterConfig={"Delay": 2, "MaxAttempts": 150})
d = ec.describe_cache_clusters(CacheClusterId="py-cache", ShowCacheNodeInfo=True)["CacheClusters"][0]
print(d["CacheClusterStatus"], d["CacheNodes"][0]["Endpoint"]["Port"])
print(ec.list_tags_for_resource(ResourceName=d["ARN"])["TagList"][0]["Key"])
try:
    ec.describe_cache_clusters(CacheClusterId="nope")
except ec.exceptions.CacheClusterNotFoundFault as e:
    print("notfound", e.response["Error"]["Code"])
ec.delete_cache_cluster(CacheClusterId="py-cache")
ec.get_waiter("cache_cluster_deleted").wait(CacheClusterId="py-cache", WaiterConfig={"Delay": 2, "MaxAttempts": 60})
print("deleted")
`)
	for _, want := range []string{"creating redis", "available 6379", "notfound CacheClusterNotFound", "deleted"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}
