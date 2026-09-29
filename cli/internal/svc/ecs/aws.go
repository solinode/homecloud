package ecs

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

// The Amazon ECS API (awsJson 1.1): clusters, task definitions, services and
// tasks. Tasks run one container each, as Fargate tasks in the service's VPC
// subnets ("awsvpc" networking).

const ecsTasksPrincipal = "ecs-tasks.amazonaws.com"

// RegisterAWS serves ECS over the AWS protocol.
func (e *ECS) RegisterAWS() {
	ops := map[string]awsapi.Op{
		"CreateCluster":               e.awsCreateCluster,
		"DescribeClusters":            e.awsDescribeClusters,
		"DeleteCluster":               e.awsDeleteCluster,
		"ListClusters":                e.awsListClusters,
		"UpdateCluster":               e.awsUpdateCluster,
		"UpdateClusterSettings":       e.awsUpdateCluster,
		"PutClusterCapacityProviders": e.awsPutCapacityProviders,
		"DescribeCapacityProviders":   e.awsDescribeCapacityProviders,
		"RegisterTaskDefinition":      e.awsRegisterTaskDefinition,
		"DescribeTaskDefinition":      e.awsDescribeTaskDefinition,
		"DeregisterTaskDefinition":    e.awsDeregisterTaskDefinition,
		"DeleteTaskDefinitions":       e.awsDeleteTaskDefinitions,
		"ListTaskDefinitions":         e.awsListTaskDefinitions,
		"ListTaskDefinitionFamilies":  e.awsListTaskDefinitionFamilies,
		"CreateService":               e.awsCreateService,
		"UpdateService":               e.awsUpdateService,
		"DescribeServices":            e.awsDescribeServices,
		"DeleteService":               e.awsDeleteService,
		"ListServices":                e.awsListServices,
		"RunTask":                     e.awsRunTask,
		"StopTask":                    e.awsStopTask,
		"DescribeTasks":               e.awsDescribeTasks,
		"ListTasks":                   e.awsListTasks,
		"TagResource":                 e.awsTagResource,
		"UntagResource":               e.awsUntagResource,
		"ListTagsForResource":         e.awsListTags,
		"ListAccountSettings":         func(*awsapi.Req) (any, error) { return map[string]any{"settings": []any{}}, nil },
		"ListContainerInstances":      func(*awsapi.Req) (any, error) { return map[string]any{"containerInstanceArns": []any{}}, nil },
	}
	svc := &awsapi.Service{Name: "ecs", JSONPrefix: "AmazonEC2ContainerServiceV20141113", JSONVersion: "1.1", Ops: map[string]awsapi.Op{}}
	for name, fn := range ops {
		svc.Ops[name] = func(q *awsapi.Req) (any, error) {
			out, err := fn(q)
			return out, ecsError(err)
		}
	}
	awsapi.Register(svc)
}

func apiErr(code, format string, a ...any) error {
	return awsapi.Errorf(http.StatusBadRequest, code, format, a...)
}

func ecsError(err error) error {
	var ce *core.Error
	if err == nil || !errors.As(err, &ce) {
		return err
	}
	code, msg := "InvalidParameterException", ce.Message
	switch ce.Code {
	case "ResourceNotFound":
		switch {
		case strings.HasPrefix(msg, "cluster "):
			code, msg = "ClusterNotFoundException", "Cluster not found."
		case strings.HasPrefix(msg, "service "):
			code, msg = "ServiceNotFoundException", "Service not found."
		}
	case "AccessDenied":
		return &awsapi.Error{Status: http.StatusForbidden, Code: "AccessDeniedException", Message: msg}
	case "ServiceUnavailable":
		return awsapi.Errorf(http.StatusInternalServerError, "ServerException", "%s", msg)
	}
	return apiErr(code, "%s", msg)
}

// ---- shapes ----

type kv struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

func tagList(t core.Tags) []kv {
	out := make([]kv, 0, len(t))
	for k, v := range t {
		out = append(out, kv{k, v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func tagMap(l []kv) core.Tags {
	if len(l) == 0 {
		return nil
	}
	t := core.Tags{}
	for _, x := range l {
		t[x.Key] = x.Value
	}
	return t
}

// last returns the segment after the final "/" ("arn:...:cluster/x" -> "x").
func last(ref string) string {
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		return ref[i+1:]
	}
	return ref
}

func clusterName(ref string) string {
	if ref == "" {
		return defaultCluster
	}
	return last(ref)
}

func clusterOf(x string) string {
	if x == "" {
		return defaultCluster
	}
	return x
}

func (e *ECS) clusterARN(name string) string { return e.env.ARN("ecs", "cluster/"+name) }

func ts(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return awsapi.T(t)
}

func put(m map[string]any, k string, v any) {
	if v != nil {
		m[k] = v
	}
}

// ---- clusters ----

func (e *ECS) getCluster(name string) (Cluster, error) {
	c, err := store.Get[Cluster](e.env.Store, cClusters, name)
	if err != nil {
		if name == defaultCluster {
			return e.ensureCluster(name)
		}
		return c, apiErr("ClusterNotFoundException", "Cluster not found.")
	}
	return c, nil
}

func (e *ECS) clusterView(c Cluster) map[string]any {
	active, running, pending := 0, 0, 0
	for _, s := range store.List[Service](e.env.Store, cServices) {
		if clusterOf(s.Cluster) == c.Name && s.Status == "ACTIVE" {
			active++
		}
	}
	for _, t := range store.List[Task](e.env.Store, cTasks) {
		if clusterOf(t.Cluster) != c.Name {
			continue
		}
		switch t.LastStatus {
		case "RUNNING":
			running++
		case "PROVISIONING":
			pending++
		}
	}
	m := map[string]any{"clusterArn": c.ARN, "clusterName": c.Name, "status": c.Status, "registeredContainerInstancesCount": 0,
		"runningTasksCount": running, "pendingTasksCount": pending, "activeServicesCount": active, "statistics": []any{},
		"tags": tagList(c.Tags), "settings": []any{}, "capacityProviders": []any{}, "defaultCapacityProviderStrategy": []any{}, "attachments": []any{}}
	for k, v := range c.AWS {
		m[k] = v
	}
	return m
}

func (e *ECS) awsCreateCluster(q *awsapi.Req) (any, error) {
	var in struct {
		ClusterName string
		Tags        []kv
		AWS         map[string]any
	}
	var raw map[string]any
	if err := q.Bind(&raw); err != nil {
		return nil, err
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if in.ClusterName == "" {
		in.ClusterName = defaultCluster
	}
	if !familyRe.MatchString(in.ClusterName) {
		return nil, apiErr("InvalidParameterException", "Cluster names are 1-255 letters, digits, hyphens or underscores.")
	}
	if err := q.Authorize("ecs:CreateCluster", e.clusterARN(in.ClusterName)); err != nil {
		return nil, err
	}
	extra := map[string]any{}
	for _, k := range []string{"settings", "configuration", "capacityProviders", "defaultCapacityProviderStrategy", "serviceConnectDefaults"} {
		if v, ok := raw[k]; ok {
			extra[k] = v
		}
	}
	c, err := store.Get[Cluster](e.env.Store, cClusters, in.ClusterName)
	if err == nil && c.Status == "ACTIVE" {
		return map[string]any{"cluster": e.clusterView(c)}, nil // CreateCluster is idempotent
	}
	c = Cluster{Name: in.ClusterName, ARN: e.clusterARN(in.ClusterName), Status: "ACTIVE", Tags: tagMap(in.Tags), AWS: extra, CreatedAt: core.Now()}
	if err := store.Put(e.env.Store, cClusters, c.Name, c); err != nil {
		return nil, err
	}
	return map[string]any{"cluster": e.clusterView(c)}, nil
}

func (e *ECS) awsDescribeClusters(q *awsapi.Req) (any, error) {
	var in struct{ Clusters []string }
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if len(in.Clusters) == 0 {
		in.Clusters = []string{defaultCluster}
	}
	out, failures := []map[string]any{}, []map[string]any{}
	for _, ref := range in.Clusters {
		name := clusterName(ref)
		if err := q.Authorize("ecs:DescribeClusters", e.clusterARN(name)); err != nil {
			return nil, err
		}
		c, err := store.Get[Cluster](e.env.Store, cClusters, name)
		if err != nil {
			failures = append(failures, map[string]any{"arn": e.clusterARN(name), "reason": "MISSING"})
			continue
		}
		out = append(out, e.clusterView(c))
	}
	return map[string]any{"clusters": out, "failures": failures}, nil
}

func (e *ECS) awsListClusters(q *awsapi.Req) (any, error) {
	if err := q.Authorize("ecs:ListClusters", "*"); err != nil {
		return nil, err
	}
	arns := []string{}
	for _, c := range store.List[Cluster](e.env.Store, cClusters) {
		if c.Status == "ACTIVE" {
			arns = append(arns, c.ARN)
		}
	}
	slices.Sort(arns)
	return map[string]any{"clusterArns": arns}, nil
}

func (e *ECS) awsDeleteCluster(q *awsapi.Req) (any, error) {
	var in struct{ Cluster string }
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	name := clusterName(in.Cluster)
	if err := q.Authorize("ecs:DeleteCluster", e.clusterARN(name)); err != nil {
		return nil, err
	}
	c, err := e.getCluster(name)
	if err != nil {
		return nil, err
	}
	for _, s := range store.List[Service](e.env.Store, cServices) {
		if clusterOf(s.Cluster) == name && s.Status != "INACTIVE" {
			return nil, apiErr("ClusterContainsServicesException", "The Cluster cannot be deleted while Services are active.")
		}
	}
	for _, t := range store.List[Task](e.env.Store, cTasks) {
		if clusterOf(t.Cluster) == name && t.LastStatus != "STOPPED" {
			return nil, apiErr("ClusterContainsTasksException", "The Cluster cannot be deleted while Tasks are active.")
		}
	}
	c, err = store.Update(e.env.Store, cClusters, name, func(x *Cluster) error { x.Status = "INACTIVE"; return nil })
	if err != nil {
		return nil, err
	}
	return map[string]any{"cluster": e.clusterView(c)}, nil
}

func (e *ECS) awsUpdateCluster(q *awsapi.Req) (any, error) {
	var in struct{ Cluster string }
	var raw map[string]any
	if err := q.Bind(&raw); err != nil {
		return nil, err
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	name := clusterName(in.Cluster)
	if err := q.Authorize("ecs:"+q.Op, e.clusterARN(name)); err != nil {
		return nil, err
	}
	if _, err := e.getCluster(name); err != nil {
		return nil, err
	}
	c, err := store.Update(e.env.Store, cClusters, name, func(x *Cluster) error {
		if x.AWS == nil {
			x.AWS = map[string]any{}
		}
		for _, k := range []string{"settings", "configuration", "serviceConnectDefaults"} {
			if v, ok := raw[k]; ok {
				x.AWS[k] = v
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"cluster": e.clusterView(c)}, nil
}

func (e *ECS) awsPutCapacityProviders(q *awsapi.Req) (any, error) {
	var in struct {
		Cluster                         string
		CapacityProviders               []string
		DefaultCapacityProviderStrategy []map[string]any
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	name := clusterName(in.Cluster)
	if err := q.Authorize("ecs:PutClusterCapacityProviders", e.clusterARN(name)); err != nil {
		return nil, err
	}
	for _, p := range in.CapacityProviders {
		if p != "FARGATE" && p != "FARGATE_SPOT" {
			return nil, apiErr("InvalidParameterException", "The specified capacity provider '%s' was not found. Only FARGATE and FARGATE_SPOT are available.", p)
		}
	}
	if _, err := e.getCluster(name); err != nil {
		return nil, err
	}
	c, err := store.Update(e.env.Store, cClusters, name, func(x *Cluster) error {
		if x.AWS == nil {
			x.AWS = map[string]any{}
		}
		x.AWS["capacityProviders"] = nonNil(in.CapacityProviders)
		x.AWS["defaultCapacityProviderStrategy"] = nonNil(in.DefaultCapacityProviderStrategy)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"cluster": e.clusterView(c)}, nil
}

func nonNil[T any](l []T) []T {
	if l == nil {
		return []T{}
	}
	return l
}

func (e *ECS) awsDescribeCapacityProviders(q *awsapi.Req) (any, error) {
	var in struct{ CapacityProviders []string }
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("ecs:DescribeCapacityProviders", "*"); err != nil {
		return nil, err
	}
	out, failures := []map[string]any{}, []map[string]any{}
	for _, n := range []string{"FARGATE", "FARGATE_SPOT"} {
		if len(in.CapacityProviders) > 0 && !slices.ContainsFunc(in.CapacityProviders, func(r string) bool { return last(r) == n }) {
			continue
		}
		out = append(out, map[string]any{"capacityProviderArn": e.env.ARN("ecs", "capacity-provider/"+n), "name": n, "status": "ACTIVE", "tags": []any{}})
	}
	for _, r := range in.CapacityProviders {
		if n := last(r); n != "FARGATE" && n != "FARGATE_SPOT" {
			failures = append(failures, map[string]any{"arn": r, "reason": "MISSING"})
		}
	}
	return map[string]any{"capacityProviders": out, "failures": failures}, nil
}

// ---- task definitions ----

type strMap = map[string]any

func str(m strMap, k string) string { s, _ := m[k].(string); return s }

func strList(v any) []string {
	l, _ := v.([]any)
	out := make([]string, 0, len(l))
	for _, x := range l {
		out = append(out, fmt.Sprint(x))
	}
	return out
}

func num(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case string:
		f, _ := strconv.ParseFloat(x, 64)
		return f
	}
	return 0
}

func kvPairs(v any, key, val string) []strMap {
	l, _ := v.([]any)
	var out []strMap
	for _, x := range l {
		if m, ok := x.(strMap); ok {
			out = append(out, strMap{"k": m[key], "v": m[val]})
		}
	}
	return out
}

func (e *ECS) awsRegisterTaskDefinition(q *awsapi.Req) (any, error) {
	var raw strMap
	if err := q.Bind(&raw); err != nil {
		return nil, err
	}
	family := str(raw, "family")
	if err := q.Authorize("ecs:RegisterTaskDefinition", "*"); err != nil {
		return nil, err
	}
	var tags []kv
	if b, err := json.Marshal(raw["tags"]); err == nil {
		_ = json.Unmarshal(b, &tags)
	}
	delete(raw, "tags")
	cds, _ := raw["containerDefinitions"].([]any)
	if len(cds) == 0 {
		return nil, apiErr("ClientException", "Task definition must contain at least one container definition.")
	}
	if len(cds) > 1 {
		return nil, apiErr("ClientException", "HomeCloud runs a single container per task; this task definition has %d container definitions.", len(cds))
	}
	c, _ := cds[0].(strMap)
	td := TaskDefinition{Family: family, ContainerName: str(c, "name"), Image: str(c, "image"), AWS: raw, Tags: tagMap(tags),
		Command: strList(c["command"]), Entrypoint: strList(c["entryPoint"]), RegisteredBy: q.P.ARN}
	if len(td.Command) == 0 {
		td.Command = nil
	}
	if len(td.Entrypoint) == 0 {
		td.Entrypoint = nil
	}
	if td.Image == "" {
		return nil, apiErr("ClientException", "Container.image should not be null or empty.")
	}
	if pm, ok := c["portMappings"].([]any); ok && len(pm) > 0 {
		if m, ok := pm[0].(strMap); ok {
			td.ContainerPort = int(num(m["containerPort"]))
		}
	}
	for _, p := range kvPairs(c["environment"], "name", "value") {
		if td.Environment == nil {
			td.Environment = map[string]string{}
		}
		td.Environment[fmt.Sprint(p["k"])] = fmt.Sprint(p["v"])
	}
	for _, p := range kvPairs(c["secrets"], "name", "valueFrom") {
		td.Secrets = append(td.Secrets, SecretRef{Name: fmt.Sprint(p["k"]), ValueFrom: fmt.Sprint(p["v"])})
	}
	cpu, mem := num(raw["cpu"]), num(raw["memory"])
	if cpu == 0 {
		cpu = num(c["cpu"])
	}
	if mem == 0 {
		mem = num(c["memory"])
	}
	if mem == 0 {
		mem = num(c["memoryReservation"])
	}
	td.CPU, td.MemoryMB = cpu/1024, int64(mem)
	if lc, ok := c["logConfiguration"].(strMap); ok && str(lc, "logDriver") == "awslogs" {
		if o, ok := lc["options"].(strMap); ok {
			td.LogGroup, td.LogStreamBase = str(o, "awslogs-group"), str(o, "awslogs-stream-prefix")
		}
	}
	if role := str(raw, "taskRoleArn"); role != "" {
		if err := q.Authorize("iam:PassRole", role); err != nil {
			return nil, err
		}
		if e.Roles != nil {
			arn, err := e.Roles.TaskRole(role)
			if err != nil {
				return nil, apiErr("ClientException", "%v", err)
			}
			role = arn
		}
		td.TaskRole = role
	}
	if role := str(raw, "executionRoleArn"); role != "" {
		if err := q.Authorize("iam:PassRole", role); err != nil {
			return nil, err
		}
		td.ExecutionRole = role
	}
	td, err := e.registerTaskDef(q.Authorize, td)
	if err != nil {
		return nil, err
	}
	// The audit trail records the operation, not the secrets checks.
	_ = q.Authorize("ecs:RegisterTaskDefinition", "*")
	return map[string]any{"taskDefinition": e.tdView(td), "tags": tagList(td.Tags)}, nil
}

func (e *ECS) tdView(td TaskDefinition) strMap {
	m := strMap{}
	for k, v := range td.AWS {
		m[k] = v
	}
	if td.AWS == nil { // registered through the native API
		c := strMap{"name": "app", "image": td.Image, "essential": true, "cpu": 0, "memory": td.MemoryMB}
		if td.ContainerName != "" {
			c["name"] = td.ContainerName
		}
		if td.ContainerPort > 0 {
			c["portMappings"] = []strMap{{"containerPort": td.ContainerPort, "hostPort": td.ContainerPort, "protocol": "tcp"}}
		}
		if len(td.Command) > 0 {
			c["command"] = td.Command
		}
		if len(td.Entrypoint) > 0 {
			c["entryPoint"] = td.Entrypoint
		}
		var envs []strMap
		for k, v := range td.Environment {
			envs = append(envs, strMap{"name": k, "value": v})
		}
		c["environment"] = nonNil(envs)
		var secs []strMap
		for _, s := range td.Secrets {
			secs = append(secs, strMap{"name": s.Name, "valueFrom": s.ValueFrom})
		}
		c["secrets"] = nonNil(secs)
		m["containerDefinitions"] = []strMap{c}
		m["cpu"] = strconv.Itoa(int(td.CPU * 1024))
		m["memory"] = strconv.FormatInt(td.MemoryMB, 10)
		m["networkMode"] = "awsvpc"
		m["requiresCompatibilities"] = []string{"FARGATE"}
	}
	m["family"] = td.Family
	m["taskDefinitionArn"] = td.ARN
	m["revision"] = td.Revision
	m["status"] = td.Status
	m["registeredAt"] = ts(td.CreatedAt)
	if td.RegisteredBy != "" {
		m["registeredBy"] = td.RegisteredBy
	}
	if td.DeregisteredAt != nil {
		m["deregisteredAt"] = ts(*td.DeregisteredAt)
	}
	if _, ok := m["volumes"]; !ok {
		m["volumes"] = []any{}
	}
	if _, ok := m["placementConstraints"]; !ok {
		m["placementConstraints"] = []any{}
	}
	m["compatibilities"] = []string{"EC2", "FARGATE"}
	m["requiresAttributes"] = []any{}
	return m
}

// findTD resolves a task definition reference: family, family:revision or an ARN.
func (e *ECS) findTD(ref string) (TaskDefinition, error) {
	if i := strings.Index(ref, "task-definition/"); i >= 0 {
		ref = ref[i+len("task-definition/"):]
	}
	td, err := e.lookupTD(ref)
	if err != nil {
		return td, apiErr("ClientException", "Unable to describe task definition.")
	}
	return td, nil
}

func (e *ECS) awsDescribeTaskDefinition(q *awsapi.Req) (any, error) {
	var in struct{ TaskDefinition string }
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("ecs:DescribeTaskDefinition", "*"); err != nil {
		return nil, err
	}
	td, err := e.findTD(in.TaskDefinition)
	if err != nil {
		return nil, err
	}
	return map[string]any{"taskDefinition": e.tdView(td), "tags": tagList(td.Tags)}, nil
}

func (e *ECS) awsDeregisterTaskDefinition(q *awsapi.Req) (any, error) {
	var in struct{ TaskDefinition string }
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("ecs:DeregisterTaskDefinition", "*"); err != nil {
		return nil, err
	}
	if !strings.Contains(last(in.TaskDefinition), ":") {
		return nil, apiErr("ClientException", "You must specify a revision, e.g. family:revision.")
	}
	td, err := e.findTD(in.TaskDefinition)
	if err != nil {
		return nil, err
	}
	td, err = store.Update(e.env.Store, cTaskDefs, tdKey(td.Family, td.Revision), func(x *TaskDefinition) error {
		n := core.Now()
		x.Status, x.DeregisteredAt = "INACTIVE", &n
		return nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"taskDefinition": e.tdView(td)}, nil
}

func (e *ECS) awsDeleteTaskDefinitions(q *awsapi.Req) (any, error) {
	var in struct{ TaskDefinitions []string }
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("ecs:DeleteTaskDefinitions", "*"); err != nil {
		return nil, err
	}
	out, failures := []strMap{}, []strMap{}
	for _, ref := range in.TaskDefinitions {
		td, err := e.findTD(ref)
		switch {
		case err != nil:
			failures = append(failures, strMap{"arn": ref, "reason": "MISSING"})
		case td.Status == "ACTIVE":
			failures = append(failures, strMap{"arn": td.ARN, "reason": "The task definition must be deregistered first."})
		default:
			_ = store.Delete(e.env.Store, cTaskDefs, tdKey(td.Family, td.Revision))
			v := e.tdView(td)
			v["status"] = "DELETE_IN_PROGRESS"
			out = append(out, v)
		}
	}
	return map[string]any{"taskDefinitions": out, "failures": failures}, nil
}

func (e *ECS) awsListTaskDefinitions(q *awsapi.Req) (any, error) {
	var in struct {
		FamilyPrefix, Status, Sort string
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("ecs:ListTaskDefinitions", "*"); err != nil {
		return nil, err
	}
	var tds []TaskDefinition
	for _, td := range store.List[TaskDefinition](e.env.Store, cTaskDefs) {
		want := in.Status
		if want == "" {
			want = "ACTIVE"
		}
		if td.Status == want && strings.HasPrefix(td.Family, in.FamilyPrefix) {
			tds = append(tds, td)
		}
	}
	slices.SortFunc(tds, func(a, b TaskDefinition) int {
		if a.Family != b.Family {
			return strings.Compare(a.Family, b.Family)
		}
		if in.Sort == "DESC" {
			return b.Revision - a.Revision
		}
		return a.Revision - b.Revision
	})
	arns := []string{}
	for _, td := range tds {
		arns = append(arns, td.ARN)
	}
	return map[string]any{"taskDefinitionArns": arns}, nil
}

func (e *ECS) awsListTaskDefinitionFamilies(q *awsapi.Req) (any, error) {
	var in struct{ FamilyPrefix, Status string }
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("ecs:ListTaskDefinitionFamilies", "*"); err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, td := range store.List[TaskDefinition](e.env.Store, cTaskDefs) {
		if (in.Status == "ALL" || td.Status == firstNonEmpty(in.Status, "ACTIVE")) && strings.HasPrefix(td.Family, in.FamilyPrefix) {
			set[td.Family] = true
		}
	}
	fams := []string{}
	for f := range set {
		fams = append(fams, f)
	}
	slices.Sort(fams)
	return map[string]any{"families": fams}, nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// ---- services ----

func (e *ECS) svcARN(cluster, name string) string {
	return e.env.ARN("ecs", "service/"+clusterOf(cluster)+"/"+name)
}

func (e *ECS) tdARN(key string) string { return e.env.ARN("ecs", "task-definition/"+key) }

func netConfig(m strMap) (subnets, sgs []string) {
	nc, _ := m["networkConfiguration"].(strMap)
	vc, _ := nc["awsvpcConfiguration"].(strMap)
	return strList(vc["subnets"]), strList(vc["securityGroups"])
}

// serviceExtra picks the request fields DescribeServices echoes back.
func serviceExtra(raw strMap) strMap {
	skip := map[string]bool{"cluster": true, "serviceName": true, "service": true, "taskDefinition": true, "loadBalancers": true,
		"desiredCount": true, "clientToken": true, "tags": true, "forceNewDeployment": true, "force": true}
	m := strMap{}
	for k, v := range raw {
		if !skip[k] {
			m[k] = v
		}
	}
	return m
}

func (e *ECS) awsCreateService(q *awsapi.Req) (any, error) {
	var raw strMap
	if err := q.Bind(&raw); err != nil {
		return nil, err
	}
	name := str(raw, "serviceName")
	cluster := clusterName(str(raw, "cluster"))
	if err := q.Authorize("ecs:CreateService", e.svcARN(cluster, name)); err != nil {
		return nil, err
	}
	if _, err := e.getCluster(cluster); err != nil {
		return nil, err
	}
	if t := str(raw, "schedulingStrategy"); t != "" && t != "REPLICA" {
		return nil, apiErr("InvalidParameterException", "Only the REPLICA scheduling strategy is supported.")
	}
	if dc, ok := raw["deploymentController"].(strMap); ok && str(dc, "type") != "" && str(dc, "type") != "ECS" {
		return nil, apiErr("InvalidParameterException", "Only the ECS deployment controller is supported.")
	}
	td, err := e.findTD(str(raw, "taskDefinition"))
	if err != nil {
		return nil, apiErr("InvalidParameterException", "Unable to describe task definition.")
	}
	subnets, sgs := netConfig(raw)
	if len(subnets) == 0 {
		return nil, apiErr("InvalidParameterException", "networkConfiguration.awsvpcConfiguration.subnets is required.")
	}
	var tags []kv
	if b, err := json.Marshal(raw["tags"]); err == nil {
		_ = json.Unmarshal(b, &tags)
	}
	in := Service{Name: name, Cluster: cluster, TaskDefinition: tdKey(td.Family, td.Revision), DesiredCount: int(num(raw["desiredCount"])),
		Subnets: subnets, SecurityGroups: sgs, Tags: tagMap(tags), Extra: serviceExtra(raw)}
	lbs, _ := raw["loadBalancers"].([]any)
	if len(lbs) > 1 {
		return nil, apiErr("InvalidParameterException", "A service can register with one load balancer target group.")
	}
	if len(lbs) == 1 {
		lb, _ := lbs[0].(strMap)
		if str(lb, "targetGroupArn") == "" {
			return nil, apiErr("InvalidParameterException", "targetGroupArn is required.")
		}
		in.LoadBalancer = &LBBinding{TargetGroup: str(lb, "targetGroupArn"), ContainerPort: int(num(lb["containerPort"])), ContainerName: str(lb, "containerName")}
		if in.LoadBalancer.ContainerName != "" && td.ContainerName != "" && in.LoadBalancer.ContainerName != td.ContainerName {
			return nil, apiErr("InvalidParameterException", "The container %s does not exist in the task definition.", in.LoadBalancer.ContainerName)
		}
	}
	s, err := e.createSvc(q.Authorize, in)
	if err != nil {
		var ce *core.Error
		if errors.As(err, &ce) && ce.Code == "ResourceConflict" {
			return nil, apiErr("InvalidParameterException", "Creation of service was not idempotent.")
		}
		return nil, err
	}
	_ = q.Authorize("ecs:CreateService", e.svcARN(cluster, name))
	return map[string]any{"service": e.svcView(s)}, nil
}

func (e *ECS) svcView(s Service) strMap {
	cluster := clusterOf(s.Cluster)
	var running, pending int
	type dep struct{ running, pending int }
	old := map[string]*dep{}
	prim := &dep{}
	for _, t := range store.List[Task](e.env.Store, cTasks) {
		if t.Service != s.Name || t.LastStatus == "STOPPED" {
			continue
		}
		d := prim
		if t.TaskDefinition != s.TaskDefinition {
			key := strings.TrimSuffix(t.TaskDefinition, "(redeploy)")
			if old[key] == nil {
				old[key] = &dep{}
			}
			d = old[key]
		}
		switch t.LastStatus {
		case "RUNNING":
			d.running++
			running++
		case "PROVISIONING":
			d.pending++
			pending++
		}
	}
	desired := s.DesiredCount
	m := strMap{"serviceArn": s.ARN, "serviceName": s.Name, "clusterArn": e.clusterARN(cluster), "status": s.Status,
		"desiredCount": desired, "runningCount": running, "pendingCount": pending, "launchType": "FARGATE", "platformVersion": "LATEST",
		"taskDefinition": e.tdARN(s.TaskDefinition), "schedulingStrategy": "REPLICA", "propagateTags": "NONE",
		"enableECSManagedTags": false, "enableExecuteCommand": false, "placementConstraints": []any{}, "placementStrategy": []any{},
		"serviceRegistries": []any{}, "deploymentController": strMap{"type": "ECS"}, "createdBy": e.env.ARN("iam", "root"),
		"deploymentConfiguration": strMap{"maximumPercent": 200, "minimumHealthyPercent": 100, "deploymentCircuitBreaker": strMap{"enable": false, "rollback": false}},
		"createdAt":               ts(s.CreatedAt), "tags": tagList(s.Tags)}
	for k, v := range s.Extra {
		m[k] = v
	}
	m["loadBalancers"] = []strMap{}
	if s.LoadBalancer != nil {
		lb := strMap{"targetGroupArn": e.elb.TargetGroupARN(s.LoadBalancer.TargetGroup), "containerPort": s.LoadBalancer.ContainerPort}
		name := s.LoadBalancer.ContainerName
		if name == "" {
			if td, err := store.Get[TaskDefinition](e.env.Store, cTaskDefs, s.TaskDefinition); err == nil {
				name = td.ContainerName
			}
		}
		lb["containerName"] = name
		m["loadBalancers"] = []strMap{lb}
	}
	nc, _ := s.Extra["networkConfiguration"]
	primary := strMap{"id": s.DeploymentID, "status": "PRIMARY", "taskDefinition": e.tdARN(s.TaskDefinition), "desiredCount": desired,
		"pendingCount": prim.pending, "runningCount": prim.running, "failedTasks": 0, "createdAt": ts(s.DeployedAt), "updatedAt": ts(s.DeployedAt),
		"launchType": "FARGATE", "platformVersion": "1.4.0", "rolloutState": "IN_PROGRESS", "rolloutStateReason": "ECS deployment in progress."}
	if prim.running >= desired && prim.pending == 0 && len(old) == 0 {
		primary["rolloutState"], primary["rolloutStateReason"] = "COMPLETED", "ECS deployment completed."
	}
	if nc != nil {
		primary["networkConfiguration"] = nc
	}
	deps := []strMap{primary}
	keys := make([]string, 0, len(old))
	for k := range old {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		deps = append(deps, strMap{"id": "ecs-svc/old-" + core.RandHex(4), "status": "ACTIVE", "taskDefinition": e.tdARN(k), "desiredCount": 0,
			"pendingCount": old[k].pending, "runningCount": old[k].running, "failedTasks": 0, "createdAt": ts(s.DeployedAt), "updatedAt": ts(core.Now()),
			"launchType": "FARGATE", "platformVersion": "1.4.0", "rolloutState": "COMPLETED"})
	}
	m["deployments"] = deps
	evs := []strMap{}
	for i, ev := range s.Events {
		evs = append(evs, strMap{"id": fmt.Sprintf("%s-%d", core.RandHex(8), i), "createdAt": ts(ev.Time), "message": "(service " + s.Name + ") " + ev.Message})
	}
	m["events"] = evs
	return m
}

// findService returns the service if it belongs to cluster.
func (e *ECS) findService(cluster, ref string) (Service, bool) {
	s, err := store.Get[Service](e.env.Store, cServices, last(ref))
	if err != nil || clusterOf(s.Cluster) != cluster {
		return s, false
	}
	return s, true
}

func (e *ECS) awsDescribeServices(q *awsapi.Req) (any, error) {
	var in struct {
		Cluster  string
		Services []string
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	cluster := clusterName(in.Cluster)
	if _, err := e.getCluster(cluster); err != nil {
		return nil, err
	}
	out, failures := []strMap{}, []strMap{}
	for _, ref := range in.Services {
		if err := q.Authorize("ecs:DescribeServices", e.svcARN(cluster, last(ref))); err != nil {
			return nil, err
		}
		s, ok := e.findService(cluster, ref)
		if !ok {
			failures = append(failures, strMap{"arn": e.svcARN(cluster, last(ref)), "reason": "MISSING"})
			continue
		}
		out = append(out, e.svcView(s))
	}
	return map[string]any{"services": out, "failures": failures}, nil
}

func (e *ECS) awsListServices(q *awsapi.Req) (any, error) {
	var in struct{ Cluster string }
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	cluster := clusterName(in.Cluster)
	if err := q.Authorize("ecs:ListServices", e.clusterARN(cluster)); err != nil {
		return nil, err
	}
	if _, err := e.getCluster(cluster); err != nil {
		return nil, err
	}
	arns := []string{}
	for _, s := range store.List[Service](e.env.Store, cServices) {
		if clusterOf(s.Cluster) == cluster && s.Status != "INACTIVE" {
			arns = append(arns, s.ARN)
		}
	}
	slices.Sort(arns)
	return map[string]any{"serviceArns": arns}, nil
}

func (e *ECS) awsUpdateService(q *awsapi.Req) (any, error) {
	var raw strMap
	if err := q.Bind(&raw); err != nil {
		return nil, err
	}
	cluster := clusterName(str(raw, "cluster"))
	name := last(str(raw, "service"))
	if err := q.Authorize("ecs:UpdateService", e.svcARN(cluster, name)); err != nil {
		return nil, err
	}
	if _, err := e.getCluster(cluster); err != nil {
		return nil, err
	}
	if _, ok := e.findService(cluster, name); !ok {
		return nil, apiErr("ServiceNotFoundException", "Service not found.")
	}
	up := serviceUpdate{TaskDefinition: str(raw, "taskDefinition"), Extra: serviceExtra(raw)}
	if v, ok := raw["desiredCount"]; ok {
		n := int(num(v))
		up.DesiredCount = &n
	}
	up.ForceDeploy, _ = raw["forceNewDeployment"].(bool)
	if up.TaskDefinition != "" {
		td, err := e.findTD(up.TaskDefinition)
		if err != nil {
			return nil, apiErr("InvalidParameterException", "Unable to describe task definition.")
		}
		up.TaskDefinition = tdKey(td.Family, td.Revision)
	}
	if subnets, sgs := netConfig(raw); len(subnets) > 0 {
		up.Subnets, up.SecurityGroups = subnets, &sgs
	}
	delete(up.Extra, "service")
	s, err := e.updateSvc(q.Authorize, name, up)
	if err != nil {
		return nil, err
	}
	_ = q.Authorize("ecs:UpdateService", e.svcARN(cluster, name))
	return map[string]any{"service": e.svcView(s)}, nil
}

func (e *ECS) awsDeleteService(q *awsapi.Req) (any, error) {
	var in struct {
		Cluster, Service string
		Force            bool
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	cluster := clusterName(in.Cluster)
	name := last(in.Service)
	if err := q.Authorize("ecs:DeleteService", e.svcARN(cluster, name)); err != nil {
		return nil, err
	}
	if _, err := e.getCluster(cluster); err != nil {
		return nil, err
	}
	s, ok := e.findService(cluster, name)
	if !ok || s.Status == "INACTIVE" {
		return nil, apiErr("ServiceNotFoundException", "Service not found.")
	}
	if s.Status == "ACTIVE" && s.DesiredCount > 0 && !in.Force {
		return nil, apiErr("InvalidParameterException", "The service cannot be stopped while it is scaled above 0.")
	}
	s, err := e.deleteSvc(name)
	if err != nil {
		return nil, err
	}
	return map[string]any{"service": e.svcView(s)}, nil
}

// ---- tasks ----

func (e *ECS) taskView(t Task) strMap {
	cluster := clusterOf(t.Cluster)
	group := "family:" + strings.SplitN(t.TaskDefinition, ":", 2)[0]
	if t.Service != "" {
		group = "service:" + t.Service
	}
	tdKeyStr := strings.TrimSuffix(t.TaskDefinition, "(redeploy)")
	m := strMap{"taskArn": t.ARN, "clusterArn": e.clusterARN(cluster), "taskDefinitionArn": e.tdARN(tdKeyStr), "lastStatus": t.LastStatus,
		"desiredStatus": t.DesiredStatus, "group": group, "launchType": "FARGATE", "platformVersion": "1.4.0", "version": 1,
		"connectivity": "CONNECTED", "healthStatus": "UNKNOWN", "enableExecuteCommand": false, "createdAt": ts(t.CreatedAt),
		"overrides": strMap{"containerOverrides": []any{}}, "tags": tagList(t.Tags)}
	if td, err := store.Get[TaskDefinition](e.env.Store, cTaskDefs, tdKeyStr); err == nil {
		m["cpu"] = strconv.Itoa(int(td.CPU * 1024))
		m["memory"] = strconv.FormatInt(td.MemoryMB, 10)
	}
	if t.StartedBy != "" {
		m["startedBy"] = t.StartedBy
	}
	if t.StartedAt != nil {
		m["startedAt"], m["connectivityAt"] = ts(*t.StartedAt), ts(*t.StartedAt)
	}
	if t.StoppedAt != nil {
		m["stoppedAt"], m["stoppingAt"] = ts(*t.StoppedAt), ts(*t.StoppedAt)
		m["stoppedReason"] = t.StopReason
		m["stopCode"] = "UserInitiated"
	}
	name := t.ContainerName
	if name == "" {
		name = "app"
	}
	ct := strMap{"containerArn": e.env.ARN("ecs", "container/"+cluster+"/"+t.ID+"/"+t.ID[:8]), "taskArn": t.ARN, "name": name, "image": t.Image,
		"lastStatus": t.LastStatus, "networkBindings": []any{}, "networkInterfaces": []strMap{{"attachmentId": t.ID[:16], "privateIpv4Address": t.PrivateIP}}, "healthStatus": "UNKNOWN"}
	if t.ExitCode != nil {
		ct["exitCode"] = *t.ExitCode
	}
	m["containers"] = []strMap{ct}
	m["attachments"] = []strMap{{"id": t.ID[:16], "type": "ElasticNetworkInterface", "status": "ATTACHED",
		"details": []kv{{"subnetId", t.SubnetID}, {"privateIPv4Address", t.PrivateIP}}}}
	return m
}

func (e *ECS) awsRunTask(q *awsapi.Req) (any, error) {
	var raw strMap
	if err := q.Bind(&raw); err != nil {
		return nil, err
	}
	cluster := clusterName(str(raw, "cluster"))
	td, err := e.findTD(str(raw, "taskDefinition"))
	if err != nil {
		return nil, apiErr("InvalidParameterException", "Unable to describe task definition.")
	}
	if err := q.Authorize("ecs:RunTask", td.ARN); err != nil {
		return nil, err
	}
	if _, err := e.getCluster(cluster); err != nil {
		return nil, err
	}
	rs := runSpec{TaskDefinition: tdKey(td.Family, td.Revision), Cluster: cluster, StartedBy: str(raw, "startedBy")}
	subnets, sgs := netConfig(raw)
	if len(subnets) > 0 {
		rs.SubnetID = subnets[0]
	}
	rs.SecurityGroups = sgs
	var tags []kv
	if b, err := json.Marshal(raw["tags"]); err == nil {
		_ = json.Unmarshal(b, &tags)
	}
	rs.Tags = tagMap(tags)
	if ov, ok := raw["overrides"].(strMap); ok {
		cos, _ := ov["containerOverrides"].([]any)
		for _, x := range cos {
			co, _ := x.(strMap)
			if c := strList(co["command"]); len(c) > 0 {
				rs.Command = c
			}
			for _, p := range kvPairs(co["environment"], "name", "value") {
				if rs.Environment == nil {
					rs.Environment = map[string]string{}
				}
				rs.Environment[fmt.Sprint(p["k"])] = fmt.Sprint(p["v"])
			}
		}
	}
	count := int(num(raw["count"]))
	if count == 0 {
		count = 1
	}
	if count > 10 {
		return nil, apiErr("InvalidParameterException", "count must be between 1 and 10.")
	}
	tasks, failures := []strMap{}, []strMap{}
	for range count {
		t, err := e.runTaskSpec(q.R.Context(), q.Authorize, rs)
		if err != nil {
			var ce *core.Error
			if errors.As(err, &ce) && ce.Code == "TaskFailedToStart" {
				failures = append(failures, strMap{"arn": e.clusterARN(cluster), "reason": "TASK_FAILED_TO_START", "detail": ce.Message})
				continue
			}
			if len(tasks) == 0 {
				return nil, err
			}
			failures = append(failures, strMap{"arn": e.clusterARN(cluster), "reason": "ERROR", "detail": err.Error()})
			continue
		}
		tasks = append(tasks, e.taskView(t))
	}
	_ = q.Authorize("ecs:RunTask", td.ARN)
	return map[string]any{"tasks": tasks, "failures": failures}, nil
}

func (e *ECS) findTask(cluster, ref string) (Task, bool) {
	t, err := store.Get[Task](e.env.Store, cTasks, last(ref))
	if err != nil || clusterOf(t.Cluster) != cluster {
		return t, false
	}
	return t, true
}

func (e *ECS) awsStopTask(q *awsapi.Req) (any, error) {
	var in struct{ Cluster, Task, Reason string }
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	cluster := clusterName(in.Cluster)
	if err := q.Authorize("ecs:StopTask", e.env.ARN("ecs", "task/"+cluster+"/"+last(in.Task))); err != nil {
		return nil, err
	}
	if _, err := e.getCluster(cluster); err != nil {
		return nil, err
	}
	t, ok := e.findTask(cluster, in.Task)
	if !ok {
		return nil, apiErr("InvalidParameterException", "The referenced task was not found.")
	}
	e.stopTask(t, firstNonEmpty(in.Reason, "Task stopped by user"))
	t, _ = store.Get[Task](e.env.Store, cTasks, t.ID)
	return map[string]any{"task": e.taskView(t)}, nil
}

func (e *ECS) awsDescribeTasks(q *awsapi.Req) (any, error) {
	var in struct {
		Cluster string
		Tasks   []string
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	cluster := clusterName(in.Cluster)
	if _, err := e.getCluster(cluster); err != nil {
		return nil, err
	}
	out, failures := []strMap{}, []strMap{}
	for _, ref := range in.Tasks {
		arn := e.env.ARN("ecs", "task/"+cluster+"/"+last(ref))
		if err := q.Authorize("ecs:DescribeTasks", arn); err != nil {
			return nil, err
		}
		t, ok := e.findTask(cluster, ref)
		if !ok {
			failures = append(failures, strMap{"arn": arn, "reason": "MISSING"})
			continue
		}
		out = append(out, e.taskView(t))
	}
	return map[string]any{"tasks": out, "failures": failures}, nil
}

func (e *ECS) awsListTasks(q *awsapi.Req) (any, error) {
	var in struct{ Cluster, ServiceName, Family, DesiredStatus, StartedBy string }
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	cluster := clusterName(in.Cluster)
	if err := q.Authorize("ecs:ListTasks", "*"); err != nil {
		return nil, err
	}
	if _, err := e.getCluster(cluster); err != nil {
		return nil, err
	}
	want := firstNonEmpty(in.DesiredStatus, "RUNNING")
	var ts []Task
	for _, t := range store.List[Task](e.env.Store, cTasks) {
		switch {
		case clusterOf(t.Cluster) != cluster, t.DesiredStatus != want:
		case in.ServiceName != "" && t.Service != last(in.ServiceName):
		case in.Family != "" && strings.SplitN(t.TaskDefinition, ":", 2)[0] != in.Family:
		case in.StartedBy != "" && t.StartedBy != in.StartedBy:
		default:
			ts = append(ts, t)
		}
	}
	slices.SortFunc(ts, func(a, b Task) int { return b.CreatedAt.Compare(a.CreatedAt) })
	arns := []string{}
	for _, t := range ts {
		arns = append(arns, t.ARN)
	}
	return map[string]any{"taskArns": arns}, nil
}

// ---- tags ----

// mutateTags applies f to the tags of the resource an ARN names.
func (e *ECS) mutateTags(arn string, read bool, f func(core.Tags) core.Tags) (core.Tags, error) {
	parts := strings.SplitN(arn, ":", 6)
	ok := len(parts) == 6 && parts[0] == "arn" && parts[2] == "ecs"
	res := ""
	if ok {
		res = parts[5]
	}
	kind, rest, _ := strings.Cut(res, "/")
	notFound := apiErr("InvalidParameterException", "The specified resource could not be found: %s", arn)
	if !ok {
		return nil, apiErr("InvalidParameterException", "Invalid resource ARN: %s", arn)
	}
	var out core.Tags
	upd := func(err error) (core.Tags, error) {
		if err == store.ErrNotFound {
			return nil, notFound
		}
		return out, err
	}
	switch kind {
	case "cluster":
		_, err := store.Update(e.env.Store, cClusters, rest, func(x *Cluster) error { x.Tags = f(x.Tags); out = x.Tags; return nil })
		return upd(err)
	case "service":
		_, err := store.Update(e.env.Store, cServices, last(rest), func(x *Service) error { x.Tags = f(x.Tags); out = x.Tags; return nil })
		return upd(err)
	case "task-definition":
		_, err := store.Update(e.env.Store, cTaskDefs, rest, func(x *TaskDefinition) error { x.Tags = f(x.Tags); out = x.Tags; return nil })
		return upd(err)
	case "task":
		_, err := store.Update(e.env.Store, cTasks, last(rest), func(x *Task) error { x.Tags = f(x.Tags); out = x.Tags; return nil })
		return upd(err)
	}
	return nil, notFound
}

func (e *ECS) awsTagResource(q *awsapi.Req) (any, error) {
	var in struct {
		ResourceArn string
		Tags        []kv
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("ecs:TagResource", in.ResourceArn); err != nil {
		return nil, err
	}
	_, err := e.mutateTags(in.ResourceArn, false, func(t core.Tags) core.Tags {
		if t == nil {
			t = core.Tags{}
		}
		for _, x := range in.Tags {
			t[x.Key] = x.Value
		}
		return t
	})
	return map[string]any{}, err
}

func (e *ECS) awsUntagResource(q *awsapi.Req) (any, error) {
	var in struct {
		ResourceArn string
		TagKeys     []string
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("ecs:UntagResource", in.ResourceArn); err != nil {
		return nil, err
	}
	_, err := e.mutateTags(in.ResourceArn, false, func(t core.Tags) core.Tags {
		for _, k := range in.TagKeys {
			delete(t, k)
		}
		return t
	})
	return map[string]any{}, err
}

func (e *ECS) awsListTags(q *awsapi.Req) (any, error) {
	var in struct{ ResourceArn string }
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("ecs:ListTagsForResource", in.ResourceArn); err != nil {
		return nil, err
	}
	t, err := e.mutateTags(in.ResourceArn, true, func(t core.Tags) core.Tags { return t })
	if err != nil {
		return nil, err
	}
	return map[string]any{"tags": tagList(t)}, nil
}
