package cfn

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

// Infrastructure resource types: load balancing (ELBv2), ECS, RDS, Route 53
// and ACM. Like the other AWS::* types they call the native API as the caller,
// so what the native model cannot express is refused rather than dropped.

const (
	// infraELBZone is the CanonicalHostedZoneId of a HomeCloud load balancer
	// (the value Route 53 reports for its alias targets).
	infraELBZone = "Z35SXDOTRQ7X7K"
	// The native API cannot create a load balancer without a listener, but a
	// template creates the load balancer first and listeners after it. A
	// placeholder listener (a fixed 503 answer on a high port) bridges the gap;
	// the first real listener replaces it and the last one's removal restores it.
	infraPlaceholder     = "homecloud-cfn-placeholder"
	infraPlaceholderPort = 65535
)

var (
	// Conditions and redirects are rendered into nginx configuration, so, like
	// the AWS API, they are checked against the same whitelists.
	infraPathRe     = regexp.MustCompile(`^/[A-Za-z0-9._~%/*-]*$`)
	infraHostRe     = regexp.MustCompile(`^(\*\.)?[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)*$`)
	infraRedirectRe = regexp.MustCompile(`^[A-Za-z0-9._~%/?&=+#{}:*,;@!-]*$`)
	infraCtypeRe    = regexp.MustCompile(`^[A-Za-z0-9.+/-]+$`)
)

// ---- helpers ----

// infraUnsupported rejects properties HomeCloud cannot honor. Empty values and
// false are what the property defaults to, so they pass.
func infraUnsupported(in map[string]any, names ...string) error {
	for _, n := range names {
		v, ok := in[n]
		if !ok || v == nil {
			continue
		}
		switch t := v.(type) {
		case []any:
			if len(t) == 0 {
				continue
			}
		case map[string]any:
			if len(t) == 0 {
				continue
			}
		case bool:
			if !t {
				continue
			}
		case string:
			if t == "" || strings.EqualFold(t, "false") {
				continue
			}
		}
		return fmt.Errorf("Property %s is not supported by HomeCloud", n)
	}
	return nil
}

func infraInt(in map[string]any, k string, def int) int {
	if n, ok := iv(in, k); ok {
		return n
	}
	return def
}

// infraELBName extracts the name from a load balancer or target group ARN (a
// bare name passes through).
func infraELBName(arn, kind string) (string, error) {
	if arn == "" {
		return "", fmt.Errorf("a %s ARN is required", kind)
	}
	if !strings.HasPrefix(arn, "arn:") {
		return arn, nil
	}
	_, rest, ok := strings.Cut(arn, ":"+kind+"/")
	if !ok {
		return "", fmt.Errorf("%q is not a %s ARN", arn, kind)
	}
	p := strings.Split(rest, "/")
	if kind == "loadbalancer" {
		if len(p) < 2 || p[1] == "" {
			return "", fmt.Errorf("%q is not a load balancer ARN", arn)
		}
		return p[1], nil
	}
	if p[0] == "" {
		return "", fmt.Errorf("%q is not a %s ARN", arn, kind)
	}
	return p[0], nil
}

// infraListenerParts returns the load balancer name and listener ID of a listener ARN.
func infraListenerParts(arn string) (lb, id string, err error) {
	_, rest, ok := strings.Cut(arn, ":listener/")
	p := strings.Split(rest, "/")
	if !ok || len(p) != 4 || p[1] == "" || p[3] == "" {
		return "", "", fmt.Errorf("%q is not a listener ARN", arn)
	}
	return p[1], p[3], nil
}

// infraWait polls a native resource until field is in ready.
func infraWait(x *xctx, path, field string, ready, failed []string, what string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		cur, err := x.Call("GET", path, nil)
		if err != nil {
			return err
		}
		m, _ := cur.(map[string]any)
		st := sv(m, field)
		if slices.Contains(ready, st) {
			return nil
		}
		if slices.Contains(failed, st) {
			return fmt.Errorf("%s became %s: %v", what, st, firstNonNil(m["state_reason"], m["status_reason"]))
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for %s", what)
		}
		select {
		case <-x.ctx.Done():
			return x.ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
}

func infraLBPath(name string) string { return "/api/v1/elb/load-balancers/" + esc(name) }

func infraWaitLB(x *xctx, name string) error {
	return infraWait(x, infraLBPath(name), "state", []string{"active"}, []string{"failed"}, "load balancer "+name, 10*time.Minute)
}

// ---- actions and conditions (shared by listeners and rules) ----

// infraActions validates CloudFormation actions the way the AWS API does and
// derives the native routing: a target group name, a redirect or a fixed
// response. Actions are stored as sent so Describe returns them.
func infraActions(v any, prop string) (acts []any, tg string, red, fx map[string]any, err error) {
	var items []map[string]any
	for _, e := range asList(v) {
		if m, ok := e.(map[string]any); ok {
			items = append(items, m)
		}
	}
	if len(items) == 0 {
		return nil, "", nil, nil, fmt.Errorf("Property validation failure: [%s cannot be empty]", prop)
	}
	sort.SliceStable(items, func(i, j int) bool {
		oi, _ := iv(items[i], "Order")
		oj, _ := iv(items[j], "Order")
		return oi != 0 && (oj == 0 || oi < oj)
	})
	routed := false
	for i, a := range items {
		typ := sv(a, "Type")
		act := map[string]any{"Type": typ, "Order": i + 1}
		switch typ {
		case "forward", "redirect", "fixed-response":
			if routed {
				return nil, "", nil, nil, fmt.Errorf("%s may contain only one forward, redirect or fixed-response action", prop)
			}
			routed = true
		}
		switch typ {
		case "forward":
			arn, w := sv(a, "TargetGroupArn"), 1
			if fc := mv(a, "ForwardConfig"); fc != nil {
				tuples := lv(fc, "TargetGroups")
				if len(tuples) > 1 {
					return nil, "", nil, nil, fmt.Errorf("Property ForwardConfig with several target groups is not supported by HomeCloud")
				}
				if len(tuples) == 1 {
					t, _ := tuples[0].(map[string]any)
					arn = sv(t, "TargetGroupArn")
					w = infraInt(t, "Weight", 1)
				}
			}
			name, err := infraELBName(arn, "targetgroup")
			if err != nil {
				return nil, "", nil, nil, fmt.Errorf("A forward action requires a target group: %v", err)
			}
			act["TargetGroupArn"] = arn
			act["ForwardConfig"] = map[string]any{
				"TargetGroups":                []any{map[string]any{"TargetGroupArn": arn, "Weight": w}},
				"TargetGroupStickinessConfig": map[string]any{"Enabled": false},
			}
			tg = name
		case "redirect":
			c := mv(a, "RedirectConfig")
			code := sv(c, "StatusCode")
			if c == nil || (code != "HTTP_301" && code != "HTTP_302") {
				return nil, "", nil, nil, fmt.Errorf("A redirect action requires RedirectConfig with StatusCode HTTP_301 or HTTP_302")
			}
			rc := map[string]any{"StatusCode": code}
			for _, f := range [][2]string{{"Protocol", "#{protocol}"}, {"Port", "#{port}"}, {"Host", "#{host}"}, {"Path", "/#{path}"}, {"Query", "#{query}"}} {
				val := sv(c, f[0])
				if !infraRedirectRe.MatchString(val) {
					return nil, "", nil, nil, fmt.Errorf("RedirectConfig contains unsupported characters")
				}
				if val == "" {
					val = f[1]
				}
				rc[f[0]] = val
			}
			if p := rc["Protocol"]; p != "HTTP" && p != "HTTPS" && p != "#{protocol}" {
				return nil, "", nil, nil, fmt.Errorf("RedirectConfig Protocol must be HTTP, HTTPS or #{protocol}")
			}
			act["RedirectConfig"], red = rc, rc
		case "fixed-response":
			c := mv(a, "FixedResponseConfig")
			code := sv(c, "StatusCode")
			if n, err := strconv.Atoi(code); c == nil || len(code) != 3 || err != nil || n < 200 || n > 599 {
				return nil, "", nil, nil, fmt.Errorf("A fixed-response action requires FixedResponseConfig with a StatusCode of 2XX, 4XX or 5XX")
			}
			ct := sv(c, "ContentType")
			if ct != "" && !infraCtypeRe.MatchString(ct) {
				return nil, "", nil, nil, fmt.Errorf("FixedResponseConfig ContentType is not valid")
			}
			fc := map[string]any{"StatusCode": code}
			if ct != "" {
				fc["ContentType"] = ct
			}
			if b := sv(c, "MessageBody"); b != "" {
				fc["MessageBody"] = b
			}
			act["FixedResponseConfig"], fx = fc, fc
		case "authenticate-oidc", "authenticate-cognito":
			return nil, "", nil, nil, fmt.Errorf("Property %s: %s actions are not supported by HomeCloud", prop, typ)
		default:
			return nil, "", nil, nil, fmt.Errorf("Action type '%s' is not valid", typ)
		}
		acts = append(acts, act)
	}
	return acts, tg, red, fx, nil
}

// infraConditions validates rule conditions (path-pattern and host-header are
// supported) and derives the native host and path lists.
func infraConditions(v any) (conds []any, hosts, paths []string, err error) {
	l := asList(v)
	if len(l) == 0 {
		return nil, nil, nil, fmt.Errorf("Property validation failure: [Conditions cannot be empty]")
	}
	for _, e := range l {
		c, _ := e.(map[string]any)
		field := sv(c, "Field")
		var cfg map[string]any
		switch field {
		case "path-pattern":
			cfg = mv(c, "PathPatternConfig")
		case "host-header":
			cfg = mv(c, "HostHeaderConfig")
		default:
			return nil, nil, nil, fmt.Errorf("Condition field '%s' is not supported by HomeCloud (path-pattern and host-header are)", field)
		}
		vals := strs(asList(c["Values"]))
		if cfg != nil {
			vals = strs(lv(cfg, "Values"))
		}
		if len(vals) == 0 {
			return nil, nil, nil, fmt.Errorf("Condition %s needs at least one value", field)
		}
		vc := map[string]any{"Values": anyStrs(vals)}
		cond := map[string]any{"Field": field, "Values": anyStrs(vals)}
		for _, val := range vals {
			if field == "path-pattern" {
				if !infraPathRe.MatchString(val) {
					return nil, nil, nil, fmt.Errorf("path pattern '%s' is not supported: it must start with / and use letters, digits and . _ ~ %% / * -", val)
				}
				paths = append(paths, val)
			} else {
				if !infraHostRe.MatchString(val) {
					return nil, nil, nil, fmt.Errorf("host pattern '%s' is not supported", val)
				}
				hosts = append(hosts, val)
			}
		}
		if field == "path-pattern" {
			cond["PathPatternConfig"] = vc
		} else {
			cond["HostHeaderConfig"] = vc
		}
		conds = append(conds, cond)
	}
	return conds, hosts, paths, nil
}

func anyStrs(s []string) []any {
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}

// ---- ECS ----

var infraVerbatim = map[string]bool{"Options": true, "DockerLabels": true, "Labels": true, "DriverOpts": true, "SecretOptions": true}

// infraCamel turns a CloudFormation ECS structure into the camelCase request
// DescribeTaskDefinition echoes; user-defined maps keep their keys.
func infraCamel(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			if infraVerbatim[k] {
				out[lowerFirst(k)] = e
			} else {
				out[lowerFirst(k)] = infraCamel(e)
			}
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = infraCamel(e)
		}
		return out
	}
	return v
}

var infraMemRe = regexp.MustCompile(`^([0-9.]+)\s*(gb|mb)?$`)

// infraCPU reads ECS CPU units ("256") or vCPUs ("0.25 vCPU") as vCPUs.
func infraCPU(s string) float64 {
	s = strings.ToLower(strings.TrimSpace(s))
	f := strings.Fields(s)
	if len(f) == 0 {
		return 0
	}
	n, err := strconv.ParseFloat(strings.TrimSuffix(f[0], "vcpu"), 64)
	if err != nil {
		return 0
	}
	if strings.Contains(s, "vcpu") {
		return n
	}
	return n / 1024
}

// infraMemory reads ECS memory in MiB ("512") or GB ("1GB", "2 GB").
func infraMemory(s string) int64 {
	m := infraMemRe.FindStringSubmatch(strings.ToLower(strings.TrimSpace(s)))
	if m == nil {
		return 0
	}
	n, _ := strconv.ParseFloat(m[1], 64)
	if m[2] == "gb" {
		n *= 1024
	}
	return int64(n)
}

func infraTaskDefProps(x *xctx, in map[string]any) (map[string]any, error) {
	if err := infraUnsupported(in, "Volumes", "PlacementConstraints", "ProxyConfiguration", "InferenceAccelerators", "IpcMode", "PidMode"); err != nil {
		return nil, err
	}
	if nm := sv(in, "NetworkMode"); nm != "" && nm != "awsvpc" {
		return nil, fmt.Errorf("Property NetworkMode=%s is not supported by HomeCloud (awsvpc is)", nm)
	}
	cds := lv(in, "ContainerDefinitions")
	if len(cds) == 0 {
		return nil, fmt.Errorf("Property validation failure: [The property {/ContainerDefinitions} is required]")
	}
	if len(cds) > 1 {
		return nil, fmt.Errorf("HomeCloud runs a single container per task; this task definition has %d container definitions", len(cds))
	}
	c, _ := cds[0].(map[string]any)
	if err := infraUnsupported(c, "MountPoints", "VolumesFrom", "Links", "User", "WorkingDirectory", "Privileged", "EnvironmentFiles", "ExtraHosts", "Hostname"); err != nil {
		return nil, fmt.Errorf("ContainerDefinitions: %v", err)
	}
	if err := req(c, "Image"); err != nil {
		return nil, err
	}
	family := sv(in, "Family")
	if family == "" {
		family = x.GenName(255, false)
	}
	out := map[string]any{"family": family, "image": sv(c, "Image"), "container_name": sv(c, "Name")}
	if l := lv(c, "Command"); len(l) > 0 {
		out["command"] = strs(l)
	}
	if l := lv(c, "EntryPoint"); len(l) > 0 {
		out["entrypoint"] = strs(l)
	}
	for _, p := range lv(c, "PortMappings") {
		pm, _ := p.(map[string]any)
		if n, ok := iv(pm, "ContainerPort"); ok {
			out["container_port"] = n
			break
		}
	}
	if envs := lv(c, "Environment"); len(envs) > 0 {
		m := map[string]string{}
		for _, e := range envs {
			em, _ := e.(map[string]any)
			m[sv(em, "Name")] = sv(em, "Value")
		}
		out["environment"] = m
	}
	var secrets []any
	for _, e := range lv(c, "Secrets") {
		em, _ := e.(map[string]any)
		secrets = append(secrets, map[string]any{"name": sv(em, "Name"), "value_from": sv(em, "ValueFrom")})
	}
	if secrets != nil {
		out["secrets"] = secrets
	}
	cpu, mem := infraCPU(sv(in, "Cpu")), infraMemory(sv(in, "Memory"))
	if cpu == 0 {
		cpu = float64(infraInt(c, "Cpu", 0)) / 1024
	}
	if mem == 0 {
		mem = int64(infraInt(c, "Memory", 0))
	}
	if mem == 0 {
		mem = int64(infraInt(c, "MemoryReservation", 0))
	}
	if cpu > 0 {
		out["cpu"] = cpu
	}
	if mem > 0 {
		out["memory_mb"] = mem
	}
	if lc := mv(c, "LogConfiguration"); lc != nil && sv(lc, "LogDriver") == "awslogs" {
		o := mv(lc, "Options")
		out["log_group"], out["log_stream_base"] = sv(o, "awslogs-group"), sv(o, "awslogs-stream-prefix")
	}
	if r := sv(in, "TaskRoleArn"); r != "" {
		out["task_role"] = r
	}
	if r := sv(in, "ExecutionRoleArn"); r != "" {
		out["execution_role"] = r
	}
	if t := tagMap(in["Tags"]); t != nil {
		out["tags"] = t
	}
	echo := infraCamel(in).(map[string]any)
	delete(echo, "tags")
	echo["family"] = family
	out["aws"] = echo
	return out, nil
}

// infraTDKey turns a task definition ARN into family:revision.
func infraTDKey(ref string) string {
	if _, rest, ok := strings.Cut(ref, "task-definition/"); ok {
		return rest
	}
	return ref
}

func infraServiceCreate(x *xctx, in map[string]any) (string, map[string]any, error) {
	if err := req(in, "TaskDefinition"); err != nil {
		return "", nil, err
	}
	if err := infraUnsupported(in, "ServiceRegistries", "ServiceConnectConfiguration", "VolumeConfigurations"); err != nil {
		return "", nil, err
	}
	if lt := sv(in, "LaunchType"); lt != "" && lt != "FARGATE" {
		return "", nil, fmt.Errorf("Property LaunchType=%s is not supported by HomeCloud (FARGATE is)", lt)
	}
	if t := sv(mv(in, "DeploymentController"), "Type"); t != "" && t != "ECS" {
		return "", nil, fmt.Errorf("Property DeploymentController Type=%s is not supported by HomeCloud (ECS is)", t)
	}
	name := sv(in, "ServiceName")
	if name == "" {
		name = x.GenName(255, false)
	}
	body := map[string]any{"name": name, "task_definition": infraTDKey(sv(in, "TaskDefinition")), "desired_count": infraInt(in, "DesiredCount", 1)}
	if c := sv(in, "Cluster"); c != "" {
		body["cluster"] = lastSeg(c)
	}
	if vc := mv(mv(in, "NetworkConfiguration"), "AwsvpcConfiguration"); vc != nil {
		if s := strs(lv(vc, "Subnets")); len(s) > 0 {
			body["subnets"] = s
		}
		if s := strs(lv(vc, "SecurityGroups")); len(s) > 0 {
			body["security_groups"] = s
		}
	}
	switch lbs := lv(in, "LoadBalancers"); {
	case len(lbs) > 1:
		return "", nil, fmt.Errorf("HomeCloud registers a service with one load balancer target group; this service has %d", len(lbs))
	case len(lbs) == 1:
		m, _ := lbs[0].(map[string]any)
		if sv(m, "TargetGroupArn") == "" {
			return "", nil, fmt.Errorf("Property LoadBalancers requires a TargetGroupArn")
		}
		b := map[string]any{"target_group": sv(m, "TargetGroupArn")}
		if n, ok := iv(m, "ContainerPort"); ok {
			b["container_port"] = n
		}
		if n := sv(m, "ContainerName"); n != "" {
			b["container_name"] = n
		}
		body["load_balancer"] = b
	}
	if t := tagMap(in["Tags"]); t != nil {
		body["tags"] = t
	}
	// The generic create would refuse a name that an earlier, deleted service
	// still holds as INACTIVE, so the request is made here.
	resp, err := x.Call("POST", "/api/v1/ecs/services", body)
	if err != nil {
		return "", nil, err
	}
	attrs, _ := resp.(map[string]any)
	if attrs == nil {
		attrs = map[string]any{}
	}
	// Like CloudFormation, wait for the service to reach its desired count.
	deadline := time.Now().Add(5 * time.Minute)
	for {
		cur, err := x.Call("GET", "/api/v1/ecs/services/"+esc(name), nil)
		if err != nil {
			return name, attrs, err
		}
		m, _ := cur.(map[string]any)
		attrs = merge(attrs, m)
		want, _ := iv(m, "desired_count")
		got, _ := iv(m, "running_count")
		if got >= want {
			return name, attrs, nil
		}
		if time.Now().After(deadline) {
			return name, attrs, fmt.Errorf("service %s did not reach a steady state (%d of %d tasks running)", name, got, want)
		}
		select {
		case <-x.ctx.Done():
			return name, attrs, x.ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// infraServiceDelete drains the service and waits until its name is free again.
func infraServiceDelete(x *xctx, r *Resource) error {
	name := r.native()
	if _, err := x.Call("DELETE", "/api/v1/ecs/services/"+esc(name), nil); err != nil {
		return err
	}
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		cur, err := x.Call("GET", "/api/v1/ecs/services/"+esc(name), nil)
		if err != nil {
			if gone(err) {
				return nil
			}
			return err
		}
		if m, _ := cur.(map[string]any); sv(m, "status") == "INACTIVE" {
			return nil
		}
		select {
		case <-x.ctx.Done():
			return x.ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
	return fmt.Errorf("timed out waiting for service %s to drain", name)
}

// ---- RDS ----

// infraSubnetGroup finds the subnets of a DB subnet group created by a stack.
func infraSubnetGroup(x *xctx, name string) ([]string, bool) {
	for _, st := range store.List[Stack](x.s.env.Store, cStacks) {
		if accountOf(st.ARN) != x.Account {
			continue
		}
		for _, r := range st.Resources {
			if r.Type == "AWS::RDS::DBSubnetGroup" && r.PhysicalID == name {
				if ids := strs(asList(r.Attributes["subnet_ids"])); len(ids) > 0 {
					return ids, true
				}
			}
		}
	}
	return nil, false
}

func infraDBProps(x *xctx, in map[string]any) (map[string]any, error) {
	if err := req(in, "Engine", "DBInstanceClass"); err != nil {
		return nil, err
	}
	if err := infraUnsupported(in, "SourceDBInstanceIdentifier", "DBSnapshotIdentifier", "DBClusterIdentifier", "ManageMasterUserPassword",
		"MultiAZ", "RestoreTime", "SourceDBClusterIdentifier", "ReplicaMode", "UseLatestRestorableTime"); err != nil {
		return nil, err
	}
	id := strings.ToLower(sv(in, "DBInstanceIdentifier"))
	if id == "" {
		id = x.GenName(63, true)
	}
	out := map[string]any{"id": id, "engine": strings.ToLower(sv(in, "Engine")), "class": sv(in, "DBInstanceClass")}
	setInt(out, "_port", in, "Port") // for Post, which sees this request; the native API ignores it
	for k, n := range map[string]string{"EngineVersion": "engine_version", "MasterUsername": "master_username", "MasterUserPassword": "master_password", "DBName": "db_name"} {
		if has(in, k) {
			out[n] = sv(in, k)
		}
	}
	setInt(out, "storage_gb", in, "AllocatedStorage")
	setInt(out, "backup_retention_days", in, "BackupRetentionPeriod")
	if bv(in, "PubliclyAccessible") {
		out["publicly_accessible"] = true
	}
	if bv(in, "DeletionProtection") {
		out["deletion_protection"] = true
	}
	if g := sv(in, "DBSubnetGroupName"); g != "" {
		ids, ok := infraSubnetGroup(x, g)
		if !ok {
			return nil, fmt.Errorf("DBSubnetGroup '%s' was not found: HomeCloud resolves DB subnet groups created by AWS::RDS::DBSubnetGroup resources in a stack", g)
		}
		out["subnet_id"] = ids[0]
	}
	if t := tagMap(in["Tags"]); t != nil {
		out["tags"] = t
	}
	return out, nil
}

// ---- Route 53 ----

// infraZoneID resolves HostedZoneId or HostedZoneName.
func infraZoneID(x *xctx, in map[string]any) (string, error) {
	if id := sv(in, "HostedZoneId"); id != "" {
		return strings.TrimPrefix(id, "/hostedzone/"), nil
	}
	name := strings.ToLower(sv(in, "HostedZoneName"))
	if name == "" {
		return "", fmt.Errorf("Property validation failure: [HostedZoneId or HostedZoneName is required]")
	}
	if !strings.HasSuffix(name, ".") {
		name += "."
	}
	out, err := x.Call("GET", "/api/v1/route53/zones", nil)
	if err != nil {
		return "", err
	}
	found := ""
	for _, z := range asList(out) {
		m, _ := z.(map[string]any)
		if sv(m, "name") == name && (found == "" || !bv(m, "private")) {
			found = sv(m, "id")
		}
	}
	if found == "" {
		return "", fmt.Errorf("Hosted zone %s not found", name)
	}
	return found, nil
}

var infraRecordTypes = map[string]bool{"A": true, "AAAA": true, "CNAME": true, "TXT": true, "MX": true, "SRV": true, "NS": true, "CAA": true, "PTR": true}

// infraUnquoteTXT joins the quoted character strings of a TXT value.
func infraUnquoteTXT(v string) string {
	v = strings.TrimSpace(v)
	if !strings.HasPrefix(v, `"`) {
		return v
	}
	var b strings.Builder
	for len(v) > 0 {
		v = strings.TrimLeft(v, " ")
		if v == "" || v[0] != '"' {
			break
		}
		i := 1
		for ; i < len(v); i++ {
			if v[i] == '\\' {
				i++
				continue
			}
			if v[i] == '"' {
				break
			}
		}
		if i >= len(v) {
			return v
		}
		if u, err := strconv.Unquote(v[:i+1]); err == nil {
			b.WriteString(u)
		} else {
			b.WriteString(strings.ReplaceAll(v[1:i], `\"`, `"`))
		}
		v = v[i+1:]
	}
	return b.String()
}

// infraRecord converts a CloudFormation record set into the native record.
func infraRecord(in map[string]any) (map[string]any, error) {
	if err := req(in, "Name", "Type"); err != nil {
		return nil, err
	}
	if err := infraUnsupported(in, "SetIdentifier", "Weight", "Region", "Failover", "MultiValueAnswer", "HealthCheckId", "GeoLocation",
		"CidrRoutingConfig", "GeoProximityLocation"); err != nil {
		return nil, err
	}
	name := strings.ToLower(strings.TrimSpace(sv(in, "Name")))
	name = strings.Replace(name, `\052`, "*", 1)
	if !strings.HasSuffix(name, ".") {
		name += "."
	}
	typ := strings.ToUpper(sv(in, "Type"))
	if !infraRecordTypes[typ] {
		return nil, fmt.Errorf("Record type %q is not supported by HomeCloud", sv(in, "Type"))
	}
	rec := map[string]any{"name": name, "type": typ, "ttl": infraInt(in, "TTL", 300)}
	if at := mv(in, "AliasTarget"); at != nil {
		if typ != "A" {
			return nil, fmt.Errorf("alias records must be type A")
		}
		dns := strings.TrimPrefix(strings.ToLower(sv(at, "DNSName")), "dualstack.")
		if !strings.HasSuffix(dns, ".") {
			dns += "."
		}
		lb, ok := strings.CutSuffix(dns, ".elb.internal.")
		if !ok || lb == "" || strings.Contains(lb, ".") {
			return nil, fmt.Errorf("AliasTarget %q is not a HomeCloud load balancer DNS name", sv(at, "DNSName"))
		}
		rec["alias"] = lb
		return rec, nil
	}
	var vals []string
	for _, v := range lv(in, "ResourceRecords") {
		s := toStr(v)
		if typ == "TXT" {
			s = infraUnquoteTXT(s)
		}
		vals = append(vals, s)
	}
	if len(vals) == 0 {
		return nil, fmt.Errorf("Property validation failure: [ResourceRecords or AliasTarget is required]")
	}
	rec["values"] = vals
	return rec, nil
}

func infraChange(x *xctx, zone, action string, recs ...any) error {
	var changes []any
	for _, r := range recs {
		changes = append(changes, map[string]any{"action": action, "record": r})
	}
	_, err := x.Call("POST", "/api/v1/route53/zones/"+esc(zone)+"/changes", map[string]any{"changes": changes})
	return err
}

// ---- registration ----

func init() {
	// ---- Elastic Load Balancing v2 ----

	awsTypes["AWS::ElasticLoadBalancingV2::TargetGroup"] = awsType{
		HC: "HC::ELB::TargetGroup",
		Props: func(x *xctx, in map[string]any) (map[string]any, error) {
			if tt := sv(in, "TargetType"); tt != "" && tt != "instance" && tt != "ip" {
				return nil, fmt.Errorf("Property TargetType=%s is not supported by HomeCloud (instance and ip are)", tt)
			}
			if ip := sv(in, "IpAddressType"); ip != "" && ip != "ipv4" {
				return nil, fmt.Errorf("Property IpAddressType=%s is not supported by HomeCloud", ip)
			}
			if err := req(in, "Port", "VpcId"); err != nil {
				return nil, err
			}
			proto := strings.ToUpper(sv(in, "Protocol"))
			if proto == "" {
				proto = "HTTP"
			}
			if proto != "HTTP" && proto != "HTTPS" {
				return nil, fmt.Errorf("Property Protocol=%s is not supported by HomeCloud (HTTP and HTTPS are)", sv(in, "Protocol"))
			}
			name := sv(in, "Name")
			if name == "" {
				name = x.GenName(32, false)
			}
			hc := map[string]any{
				"path":                infraFirst(sv(in, "HealthCheckPath"), "/"),
				"interval_seconds":    infraInt(in, "HealthCheckIntervalSeconds", 30),
				"timeout_seconds":     infraInt(in, "HealthCheckTimeoutSeconds", 5),
				"healthy_threshold":   infraInt(in, "HealthyThresholdCount", 5),
				"unhealthy_threshold": infraInt(in, "UnhealthyThresholdCount", 2),
				"matcher":             "200",
			}
			if has(in, "HealthCheckProtocol") {
				hc["protocol"] = strings.ToUpper(sv(in, "HealthCheckProtocol"))
			}
			if has(in, "HealthCheckPort") {
				hc["port"] = sv(in, "HealthCheckPort")
			}
			if has(in, "HealthCheckEnabled") && !bv(in, "HealthCheckEnabled") {
				hc["disabled"] = true
			}
			if m := mv(in, "Matcher"); m != nil {
				if has(m, "GrpcCode") {
					return nil, fmt.Errorf("Property Matcher.GrpcCode is not supported by HomeCloud")
				}
				if has(m, "HttpCode") {
					hc["matcher"] = sv(m, "HttpCode")
				}
			}
			port, _ := iv(in, "Port")
			out := map[string]any{"name": name, "protocol": proto, "port": port, "vpc_id": sv(in, "VpcId"), "health_check": hc}
			if has(in, "TargetType") {
				out["target_type"] = sv(in, "TargetType")
			}
			if has(in, "ProtocolVersion") {
				out["protocol_version"] = sv(in, "ProtocolVersion")
			}
			if l := lv(in, "TargetGroupAttributes"); len(l) > 0 {
				a := map[string]string{}
				for _, e := range l {
					em, _ := e.(map[string]any)
					a[sv(em, "Key")] = sv(em, "Value")
				}
				out["attributes"] = a
			}
			var targets []any
			for _, t := range lv(in, "Targets") {
				tm, _ := t.(map[string]any)
				targets = append(targets, map[string]any{"id": sv(tm, "Id"), "port": infraInt(tm, "Port", port)})
			}
			if targets != nil {
				out["_targets"] = targets
			}
			if t := tagMap(in["Tags"]); t != nil {
				out["tags"] = t
			}
			return out, nil
		},
		Post: func(x *xctx, id string, in, attrs map[string]any) error {
			// Post sees the translated request; the targets travel in it (the
			// native API ignores unknown fields).
			targets := lv(in, "_targets")
			if targets == nil {
				return nil
			}
			_, err := x.Call("POST", "/api/v1/elb/target-groups/"+esc(id)+"/targets", map[string]any{"targets": targets})
			return err
		},
		Ref: func(v *attrView) string { return infraFirst(attrStr(v, "arn"), v.ID) },
		Att: func(v *attrView, n string) (any, bool) {
			arn := infraFirst(attrStr(v, "arn"), v.ID)
			switch n {
			case "TargetGroupArn":
				return arn, true
			case "TargetGroupName":
				return v.ID, true
			case "TargetGroupFullName":
				_, rest, _ := strings.Cut(arn, ":")
				_, rest, _ = strings.Cut(rest, ":targetgroup/")
				return "targetgroup/" + rest, true
			}
			return nil, false
		},
	}

	awsTypes["AWS::ElasticLoadBalancingV2::LoadBalancer"] = awsType{
		HC: "HC::ELB::LoadBalancer",
		Props: func(x *xctx, in map[string]any) (map[string]any, error) {
			if t := sv(in, "Type"); t != "" && t != "application" {
				return nil, fmt.Errorf("Property Type=%s is not supported by HomeCloud (application load balancers only)", t)
			}
			if ip := sv(in, "IpAddressType"); ip != "" && ip != "ipv4" {
				return nil, fmt.Errorf("Property IpAddressType=%s is not supported by HomeCloud", ip)
			}
			name := sv(in, "Name")
			if name == "" {
				name = x.GenName(32, false)
			}
			out := map[string]any{"name": name, "scheme": infraFirst(sv(in, "Scheme"), "internet-facing")}
			if subs := strs(lv(in, "Subnets")); len(subs) > 0 {
				out["subnet_id"] = subs[0] // the load balancer runs in one subnet
			} else if sm := lv(in, "SubnetMappings"); len(sm) > 0 {
				m, _ := sm[0].(map[string]any)
				out["subnet_id"] = sv(m, "SubnetId")
			}
			fixed := map[string]any{"StatusCode": "503", "ContentType": "text/plain", "MessageBody": infraPlaceholder}
			out["listeners"] = []any{map[string]any{"port": infraPlaceholderPort, "protocol": "HTTP", "fixed": fixed,
				"actions": []any{map[string]any{"Type": "fixed-response", "Order": 1, "FixedResponseConfig": fixed}}}}
			if t := tagMap(in["Tags"]); t != nil {
				out["tags"] = t
			}
			return out, nil
		},
		Ref: func(v *attrView) string { return infraFirst(attrStr(v, "arn"), v.ID) },
		Att: func(v *attrView, n string) (any, bool) {
			arn := infraFirst(attrStr(v, "arn"), v.ID)
			switch n {
			case "DNSName":
				return infraFirst(attrStr(v, "dns_name"), v.ID+".elb.internal"), true
			case "LoadBalancerArn":
				return arn, true
			case "LoadBalancerName":
				return v.ID, true
			case "LoadBalancerFullName":
				_, rest, _ := strings.Cut(arn, ":loadbalancer/")
				return rest, true
			case "CanonicalHostedZoneID":
				return infraELBZone, true
			case "SecurityGroups":
				return v.Props["SecurityGroups"], true
			}
			return nil, false
		},
	}

	awsTypes["AWS::ElasticLoadBalancingV2::Listener"] = awsType{
		HC: "",
		Create: func(x *xctx, in map[string]any) (string, map[string]any, error) {
			if err := req(in, "LoadBalancerArn", "Port", "DefaultActions"); err != nil {
				return "", nil, err
			}
			if err := infraUnsupported(in, "MutualAuthentication"); err != nil {
				return "", nil, err
			}
			lb, err := infraELBName(sv(in, "LoadBalancerArn"), "loadbalancer")
			if err != nil {
				return "", nil, err
			}
			proto := infraFirst(strings.ToUpper(sv(in, "Protocol")), "HTTP")
			if proto != "HTTP" && proto != "HTTPS" {
				return "", nil, fmt.Errorf("Property Protocol=%s is not supported by HomeCloud (HTTP and HTTPS are)", sv(in, "Protocol"))
			}
			port, _ := iv(in, "Port")
			acts, tg, red, fx, err := infraActions(in["DefaultActions"], "DefaultActions")
			if err != nil {
				return "", nil, err
			}
			body := map[string]any{"port": port, "protocol": proto, "actions": acts}
			if tg != "" {
				body["default_target_group"] = tg
			}
			if red != nil {
				body["redirect"] = red
			}
			if fx != nil {
				body["fixed"] = fx
			}
			if has(in, "SslPolicy") {
				body["ssl_policy"] = sv(in, "SslPolicy")
			}
			var certs []string
			for _, c := range lv(in, "Certificates") {
				cm, _ := c.(map[string]any)
				certs = append(certs, sv(cm, "CertificateArn"))
			}
			if len(certs) > 0 {
				body["certificate_arn"] = certs[0]
				if len(certs) > 1 {
					body["extra_certs"] = certs[1:]
				}
			}
			resp, err := x.Call("POST", infraLBPath(lb)+"/listeners", body)
			if err != nil {
				return "", nil, err
			}
			m, _ := resp.(map[string]any)
			id := ""
			for _, l := range lv(m, "listeners") {
				lm, _ := l.(map[string]any)
				if p, _ := iv(lm, "port"); p == port {
					id = sv(lm, "id")
				}
			}
			if id == "" {
				return "", nil, fmt.Errorf("the load balancer did not report the new listener")
			}
			attrs := map[string]any{"lb": lb, "listener_id": id, "port": port,
				"arn": strings.Replace(sv(m, "arn"), ":loadbalancer/", ":listener/", 1) + "/" + id}
			if err := infraWaitLB(x, lb); err != nil {
				return id, attrs, err
			}
			// The first real listener replaces the placeholder.
			if cur, err := x.Call("GET", infraLBPath(lb), nil); err == nil {
				cm, _ := cur.(map[string]any)
				for _, l := range lv(cm, "listeners") {
					lm, _ := l.(map[string]any)
					if sv(mv(lm, "fixed"), "MessageBody") == infraPlaceholder {
						if _, err := x.Call("DELETE", infraLBPath(lb)+"/listeners/"+esc(sv(lm, "id")), nil); err != nil {
							return id, attrs, err
						}
						if err := infraWaitLB(x, lb); err != nil {
							return id, attrs, err
						}
					}
				}
			}
			return id, attrs, nil
		},
		Delete: func(x *xctx, r *Resource) error {
			lb, id := sv(r.Attributes, "lb"), r.native()
			cur, err := x.Call("GET", infraLBPath(lb), nil)
			if err != nil {
				return err
			}
			cm, _ := cur.(map[string]any)
			ls := lv(cm, "listeners")
			mine := slices.ContainsFunc(ls, func(l any) bool { lm, _ := l.(map[string]any); return sv(lm, "id") == id })
			if !mine {
				return nil
			}
			if len(ls) == 1 { // a load balancer keeps at least one listener
				fixed := map[string]any{"StatusCode": "503", "ContentType": "text/plain", "MessageBody": infraPlaceholder}
				if _, err := x.Call("POST", infraLBPath(lb)+"/listeners", map[string]any{"port": infraPlaceholderPort, "protocol": "HTTP", "fixed": fixed,
					"actions": []any{map[string]any{"Type": "fixed-response", "Order": 1, "FixedResponseConfig": fixed}}}); err != nil {
					return err
				}
				if err := infraWaitLB(x, lb); err != nil {
					return err
				}
			}
			if _, err := x.Call("DELETE", infraLBPath(lb)+"/listeners/"+esc(id), nil); err != nil {
				return err
			}
			_ = infraWaitLB(x, lb)
			return nil
		},
		Ref: func(v *attrView) string { return infraFirst(attrStr(v, "arn"), v.ID) },
		Att: func(v *attrView, n string) (any, bool) {
			if n == "ListenerArn" {
				return infraFirst(attrStr(v, "arn"), v.ID), true
			}
			return nil, false
		},
	}

	awsTypes["AWS::ElasticLoadBalancingV2::ListenerRule"] = awsType{
		Create: func(x *xctx, in map[string]any) (string, map[string]any, error) {
			if err := req(in, "ListenerArn", "Priority", "Conditions", "Actions"); err != nil {
				return "", nil, err
			}
			lb, lid, err := infraListenerParts(sv(in, "ListenerArn"))
			if err != nil {
				return "", nil, err
			}
			prio, ok := iv(in, "Priority")
			if !ok || prio < 1 || prio > 50000 {
				return "", nil, fmt.Errorf("Priority must be between 1 and 50000")
			}
			conds, hosts, paths, err := infraConditions(in["Conditions"])
			if err != nil {
				return "", nil, err
			}
			acts, tg, red, fx, err := infraActions(in["Actions"], "Actions")
			if err != nil {
				return "", nil, err
			}
			id := core.RandHex(16)
			rule := map[string]any{"id": id, "priority": prio, "conditions": conds, "actions": acts}
			if len(paths) > 0 {
				rule["paths"], rule["path_prefix"] = paths, paths[0]
			}
			if len(hosts) > 0 {
				rule["hosts"], rule["host_header"] = hosts, hosts[0]
			}
			if tg != "" {
				rule["target_group"] = tg
			}
			if red != nil {
				rule["redirect"] = red
			}
			if fx != nil {
				rule["fixed"] = fx
			}
			if _, err := x.Call("POST", infraLBPath(lb)+"/listeners/"+esc(lid)+"/rules", rule); err != nil {
				return "", nil, err
			}
			arn := strings.Replace(sv(in, "ListenerArn"), ":listener/", ":listener-rule/", 1) + "/" + id
			return id, map[string]any{"lb": lb, "listener_id": lid, "arn": arn}, nil
		},
		Delete: func(x *xctx, r *Resource) error {
			_, err := x.Call("DELETE", infraLBPath(sv(r.Attributes, "lb"))+"/listeners/"+esc(sv(r.Attributes, "listener_id"))+"/rules/"+esc(r.native()), nil)
			return err
		},
		Ref: func(v *attrView) string { return infraFirst(attrStr(v, "arn"), v.ID) },
		Att: func(v *attrView, n string) (any, bool) {
			switch n {
			case "RuleArn":
				return infraFirst(attrStr(v, "arn"), v.ID), true
			case "IsDefault":
				return false, true
			}
			return nil, false
		},
	}

	// ---- ECS ----

	awsTypes["AWS::ECS::TaskDefinition"] = awsType{
		HC:    "HC::ECS::TaskDefinition",
		Props: infraTaskDefProps,
		Ref:   func(v *attrView) string { return infraFirst(attrStr(v, "arn"), v.ID) },
		Att: func(v *attrView, n string) (any, bool) {
			if n == "TaskDefinitionArn" {
				return infraFirst(attrStr(v, "arn"), v.ID), true
			}
			return nil, false
		},
	}

	awsTypes["AWS::ECS::Service"] = awsType{
		HC:     "HC::ECS::Service",
		Create: infraServiceCreate,
		Delete: infraServiceDelete,
		Ref:    func(v *attrView) string { return infraFirst(attrStr(v, "arn"), v.ID) },
		Att: func(v *attrView, n string) (any, bool) {
			switch n {
			case "Name":
				return v.ID, true
			case "ServiceArn":
				return infraFirst(attrStr(v, "arn"), v.ID), true
			}
			return nil, false
		},
	}

	// ---- RDS ----

	awsTypes["AWS::RDS::DBInstance"] = awsType{
		HC:    "HC::RDS::DBInstance",
		Props: infraDBProps,
		Post: func(x *xctx, id string, in, attrs map[string]any) error {
			// The engine listens on its own port; a different Port cannot be honored.
			if want, ok := iv(in, "_port"); ok {
				if got, _ := iv(mv(attrs, "endpoint"), "port"); got != 0 && got != want {
					return fmt.Errorf("Property Port=%d is not supported by HomeCloud: %s listens on port %d", want, sv(in, "engine"), got)
				}
			}
			return nil
		},
		Att: func(v *attrView, n string) (any, bool) {
			ep := mv(v.Attrs, "endpoint")
			switch n {
			case "Endpoint.Address":
				return sv(ep, "address"), true
			case "Endpoint.Port":
				return sv(ep, "port"), true
			case "DBInstanceArn":
				return attrStr(v, "arn"), true
			}
			return nil, false
		},
	}

	// The native RDS API has no subnet or parameter groups (only the AWS
	// protocol does), so these are recorded by the stack: a DB subnet group
	// decides where its DBInstance runs; a parameter group holds no settings.
	awsTypes["AWS::RDS::DBSubnetGroup"] = awsType{
		Create: func(x *xctx, in map[string]any) (string, map[string]any, error) {
			if err := req(in, "DBSubnetGroupDescription", "SubnetIds"); err != nil {
				return "", nil, err
			}
			ids := strs(lv(in, "SubnetIds"))
			if len(ids) == 0 {
				return "", nil, fmt.Errorf("Please provide at least one subnet")
			}
			name := strings.ToLower(sv(in, "DBSubnetGroupName"))
			if name == "" {
				name = x.GenName(255, true)
			}
			return name, map[string]any{"subnet_ids": anyStrs(ids), "description": sv(in, "DBSubnetGroupDescription")}, nil
		},
		Delete: func(x *xctx, r *Resource) error { return nil },
	}

	awsTypes["AWS::RDS::DBParameterGroup"] = awsType{
		Create: func(x *xctx, in map[string]any) (string, map[string]any, error) {
			if err := req(in, "Description", "Family"); err != nil {
				return "", nil, err
			}
			if err := infraUnsupported(in, "Parameters"); err != nil {
				return "", nil, err
			}
			return x.GenName(255, true), map[string]any{"family": sv(in, "Family")}, nil
		},
		Delete: func(x *xctx, r *Resource) error { return nil },
	}

	// ---- Route 53 ----

	awsTypes["AWS::Route53::HostedZone"] = awsType{
		HC: "HC::Route53::HostedZone",
		Props: func(x *xctx, in map[string]any) (map[string]any, error) {
			if err := req(in, "Name"); err != nil {
				return nil, err
			}
			if err := infraUnsupported(in, "QueryLoggingConfig"); err != nil {
				return nil, err
			}
			out := map[string]any{"name": strings.ToLower(sv(in, "Name"))}
			if vpcs := lv(in, "VPCs"); len(vpcs) > 0 {
				var ids []string
				for _, v := range vpcs {
					vm, _ := v.(map[string]any)
					ids = append(ids, sv(vm, "VPCId"))
				}
				out["private"], out["vpc_ids"] = true, ids
			}
			if c := sv(mv(in, "HostedZoneConfig"), "Comment"); c != "" {
				out["comment"] = c
			}
			if t := tagMap(in["HostedZoneTags"]); t != nil {
				out["tags"] = t
			}
			return out, nil
		},
		Att: func(v *attrView, n string) (any, bool) {
			switch n {
			case "Id":
				return v.ID, true
			case "NameServers":
				var out []any
				for _, s := range lv(v.Attrs, "name_servers") {
					if f := strings.Fields(toStr(s)); len(f) > 0 {
						out = append(out, f[0])
					}
				}
				return out, true
			}
			return nil, false
		},
	}

	awsTypes["AWS::Route53::RecordSet"] = awsType{
		Create: func(x *xctx, in map[string]any) (string, map[string]any, error) {
			rec, err := infraRecord(in)
			if err != nil {
				return "", nil, err
			}
			zone, err := infraZoneID(x, in)
			if err != nil {
				return "", nil, err
			}
			if err := infraChange(x, zone, "CREATE", rec); err != nil {
				return "", nil, err
			}
			return sv(in, "Name"), map[string]any{"zone": zone, "record": rec}, nil
		},
		Delete: func(x *xctx, r *Resource) error {
			return infraChange(x, sv(r.Attributes, "zone"), "DELETE", r.Attributes["record"])
		},
	}

	awsTypes["AWS::Route53::RecordSetGroup"] = awsType{
		Create: func(x *xctx, in map[string]any) (string, map[string]any, error) {
			sets := lv(in, "RecordSets")
			if len(sets) == 0 {
				return "", nil, fmt.Errorf("Property validation failure: [The property {/RecordSets} is required]")
			}
			var recs []any
			for _, s := range sets {
				sm, _ := s.(map[string]any)
				rec, err := infraRecord(sm)
				if err != nil {
					return "", nil, err
				}
				recs = append(recs, rec)
			}
			zone, err := infraZoneID(x, in)
			if err != nil {
				return "", nil, err
			}
			if err := infraChange(x, zone, "CREATE", recs...); err != nil { // one atomic batch
				return "", nil, err
			}
			return x.Stack + "-" + x.Logical + "-" + randID(8), map[string]any{"zone": zone, "records": recs}, nil
		},
		Delete: func(x *xctx, r *Resource) error {
			for _, rec := range lv(r.Attributes, "records") {
				if err := infraChange(x, sv(r.Attributes, "zone"), "DELETE", rec); err != nil && !gone(err) {
					return err
				}
			}
			return nil
		},
	}

	// ---- ACM ----

	awsTypes["AWS::ACM::Certificate"] = awsType{
		HC: "HC::ACM::Certificate",
		Props: func(x *xctx, in map[string]any) (map[string]any, error) {
			if err := req(in, "DomainName"); err != nil {
				return nil, err
			}
			if err := infraUnsupported(in, "CertificateAuthorityArn"); err != nil {
				return nil, err
			}
			out := map[string]any{"domain_name": sv(in, "DomainName"), "validation_method": infraFirst(sv(in, "ValidationMethod"), "EMAIL")}
			if sans := strs(lv(in, "SubjectAlternativeNames")); len(sans) > 0 {
				out["subject_alternative_names"] = sans
			}
			if t := tagMap(in["Tags"]); t != nil {
				out["tags"] = t
			}
			return out, nil
		},
		Ref: func(v *attrView) string { return infraFirst(attrStr(v, "arn"), v.ID) },
		Att: func(v *attrView, n string) (any, bool) {
			if n == "Id" {
				return v.ID, true
			}
			return nil, false
		},
	}
}

func infraFirst(vs ...string) string { return firstNonEmpty(vs...) }
