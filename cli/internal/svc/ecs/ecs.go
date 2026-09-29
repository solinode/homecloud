// Package ecs implements container services (ECS on Fargate): versioned task
// definitions, services that keep a desired number of tasks running with
// rolling deployments and automatic load balancer registration, and one-off
// tasks. Secrets from Secrets Manager are injected as environment variables.
package ecs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	docker "github.com/fsouza/go-dockerclient"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/runtime"
	"github.com/homecloudhq/homecloud/cli/internal/store"
	"github.com/homecloudhq/homecloud/cli/internal/svc"
	"github.com/homecloudhq/homecloud/cli/internal/svc/elb"
	"github.com/homecloudhq/homecloud/cli/internal/svc/secrets"
	"github.com/homecloudhq/homecloud/cli/internal/svc/vpc"
)

const (
	cTaskDefs = "ecs_task_definitions"
	cServices = "ecs_services"
	cTasks    = "ecs_tasks"
	taskTTL   = time.Hour // stopped tasks stay visible this long
)

type SecretRef struct {
	Name      string `json:"name"`       // environment variable
	ValueFrom string `json:"value_from"` // Secrets Manager secret name (optionally name:json-key)
}

type TaskDefinition struct {
	Family        string            `json:"family"`
	Revision      int               `json:"revision"`
	ARN           string            `json:"arn"`
	Image         string            `json:"image"`
	Command       []string          `json:"command,omitempty"`
	Entrypoint    []string          `json:"entrypoint,omitempty"`
	CPU           float64           `json:"cpu"` // vCPUs
	MemoryMB      int64             `json:"memory_mb"`
	ContainerPort int               `json:"container_port,omitempty"`
	Environment   map[string]string `json:"environment,omitempty"`
	Secrets       []SecretRef       `json:"secrets,omitempty"`
	Status        string            `json:"status"` // ACTIVE | INACTIVE
	CreatedAt     time.Time         `json:"created_at"`
}

func tdKey(family string, rev int) string { return family + ":" + strconv.Itoa(rev) }

type LBBinding struct {
	TargetGroup   string `json:"target_group"`
	ContainerPort int    `json:"container_port"`
}

type Service struct {
	Name           string     `json:"name"`
	ARN            string     `json:"arn"`
	TaskDefinition string     `json:"task_definition"` // family:revision
	DesiredCount   int        `json:"desired_count"`
	SubnetID       string     `json:"subnet_id"`
	SecurityGroups []string   `json:"security_groups"`
	LoadBalancer   *LBBinding `json:"load_balancer,omitempty"`
	Status         string     `json:"status"` // ACTIVE | DRAINING
	Events         []Event    `json:"events"`
	CreatedAt      time.Time  `json:"created_at"`
	Tags           core.Tags  `json:"tags,omitempty"`
}

type Event struct {
	Time    time.Time `json:"time"`
	Message string    `json:"message"`
}

type Task struct {
	ID             string         `json:"id"`
	ARN            string         `json:"arn"`
	Service        string         `json:"service,omitempty"`
	TaskDefinition string         `json:"task_definition"`
	ContainerID    string         `json:"container_id,omitempty"`
	VpcID          string         `json:"vpc_id"`
	SubnetID       string         `json:"subnet_id"`
	PrivateIP      string         `json:"private_ip"`
	LastStatus     string         `json:"last_status"` // PROVISIONING | RUNNING | STOPPED
	DesiredStatus  string         `json:"desired_status"`
	StopReason     string         `json:"stop_reason,omitempty"`
	ExitCode       *int           `json:"exit_code,omitempty"`
	PublicPorts    map[string]int `json:"public_ports,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
	StartedAt      *time.Time     `json:"started_at,omitempty"`
	StoppedAt      *time.Time     `json:"stopped_at,omitempty"`
}

type ECS struct {
	// DNSFor returns resolver addresses for containers in a VPC (Route 53).
	DNSFor  func(vpcID string) []string
	env     *svc.Env
	vpc     *vpc.Service
	elb     *elb.Service
	secrets *secrets.Service
	hostCPU float64
	mu      sync.Mutex // serialises reconciliation
}

func New(env *svc.Env, v *vpc.Service, lb *elb.Service, sec *secrets.Service) *ECS {
	e := &ECS{env: env, vpc: v, elb: lb, secrets: sec, hostCPU: 1}
	if info, err := env.Docker.C.Info(); err == nil && info.NCPU > 0 {
		e.hostCPU = float64(info.NCPU)
	}
	return e
}

func (e *ECS) taskLogFile(id string) string { return e.env.Cfg.Path("ecs-logs", id+".log") }

// PrivateIP resolves a running task for load balancer targets.
func (e *ECS) PrivateIP(id string) (string, string, bool) {
	t, err := store.Get[Task](e.env.Store, cTasks, id)
	if err != nil || t.LastStatus != "RUNNING" {
		return "", "", false
	}
	return t.PrivateIP, t.VpcID, true
}

func (e *ECS) event(svcName, msg string) {
	_, _ = store.Update(e.env.Store, cServices, svcName, func(s *Service) error {
		s.Events = append([]Event{{Time: core.Now(), Message: msg}}, s.Events...)
		if len(s.Events) > 50 {
			s.Events = s.Events[:50]
		}
		return nil
	})
}

// ---- tasks ----

func (e *ECS) resolveSecrets(td TaskDefinition) (map[string]string, error) {
	env := map[string]string{}
	for k, v := range td.Environment {
		env[k] = v
	}
	for _, s := range td.Secrets {
		name, key, _ := strings.Cut(s.ValueFrom, ":")
		val, _, err := e.secrets.Value(name, "", "")
		if err != nil {
			return nil, fmt.Errorf("secret %s: %w", s.ValueFrom, err)
		}
		if key != "" {
			var m map[string]any
			if json.Unmarshal([]byte(val), &m) != nil {
				return nil, fmt.Errorf("secret %s is not JSON", name)
			}
			val = fmt.Sprint(m[key])
		}
		env[s.Name] = val
	}
	return env, nil
}

// launch starts a task container; it returns once the container is running.
func (e *ECS) launch(ctx context.Context, td TaskDefinition, service, subnet string, sgs []string) (Task, error) {
	id := core.RandHex(32)
	pl, err := e.vpc.Place(subnet, "task:"+id)
	if err != nil {
		return Task{}, err
	}
	t := Task{ID: id, ARN: e.env.ARN("ecs", "task/"+id), Service: service, TaskDefinition: tdKey(td.Family, td.Revision),
		VpcID: pl.VPC.ID, SubnetID: pl.Subnet.ID, PrivateIP: pl.IP, LastStatus: "PROVISIONING", DesiredStatus: "RUNNING", CreatedAt: core.Now()}
	if err := store.Put(e.env.Store, cTasks, id, t); err != nil {
		return t, err
	}
	stop := func(reason string) (Task, error) {
		e.vpc.Release("task:" + id)
		t, _ = store.Update(e.env.Store, cTasks, id, func(x *Task) error {
			n := core.Now()
			x.LastStatus, x.DesiredStatus, x.StopReason, x.StoppedAt = "STOPPED", "STOPPED", reason, &n
			return nil
		})
		return t, fmt.Errorf("%s", reason)
	}
	env, err := e.resolveSecrets(td)
	if err != nil {
		return stop("ResourceInitializationError: " + err.Error())
	}
	env["HC_TASK_ID"], env["HC_TASK_ARN"] = id, t.ARN
	var ports []runtime.Port
	if service == "" && td.ContainerPort > 0 {
		ports = e.vpc.PublishedPorts(sgs)
	}
	aliases := []string{id}
	if service != "" {
		aliases = append(aliases, service+".ecs.internal")
	}
	var dns []string
	if e.DNSFor != nil {
		dns = e.DNSFor(pl.VPC.ID)
	}
	cid, err := e.env.Docker.Run(ctx, runtime.RunSpec{
		DNS:  dns,
		Name: svc.ContainerName("ecs", id[:12]), Image: td.Image, Cmd: td.Command, Entrypoint: td.Entrypoint, Env: env,
		Labels:   runtime.Labels("ecs", id, map[string]string{"homecloud.ecs.service": service, "homecloud.ecs.taskdef": t.TaskDefinition}),
		NanoCPUs: int64(min(td.CPU, e.hostCPU) * 1e9), MemoryMB: td.MemoryMB,
		Network: pl.Network, IP: pl.IP, Aliases: aliases, Ports: ports, Start: true,
	})
	if err != nil {
		return stop("CannotStartContainerError: " + err.Error())
	}
	t, _ = store.Update(e.env.Store, cTasks, id, func(x *Task) error {
		n := core.Now()
		x.ContainerID, x.LastStatus, x.StartedAt, x.PublicPorts = cid, "RUNNING", &n, e.env.Docker.PublishedPorts(cid)
		return nil
	})
	return t, nil
}

func (e *ECS) stopTask(t Task, reason string) {
	if t.LastStatus == "STOPPED" {
		return
	}
	if t.Service != "" {
		if s, err := store.Get[Service](e.env.Store, cServices, t.Service); err == nil && s.LoadBalancer != nil {
			_ = e.elb.SetTarget(s.LoadBalancer.TargetGroup, t.ID, 0, false)
		}
	}
	var code *int
	if t.ContainerID != "" {
		_ = e.env.Docker.Stop(t.ContainerID, 10)
		if c, err := e.env.Docker.Inspect(t.ContainerID); err == nil {
			ec := c.State.ExitCode
			code = &ec
		}
		// Keep the output, then remove the container: a stopped container still
		// holds its static IP, which the VPC is about to hand out again.
		if out, err := e.env.Docker.Logs(t.ContainerID, 5000, time.Time{}); err == nil && out != "" {
			_ = os.MkdirAll(e.env.Cfg.Path("ecs-logs"), 0o700)
			_ = os.WriteFile(e.taskLogFile(t.ID), []byte(out), 0o600)
		}
		_ = e.env.Docker.Remove(t.ContainerID)
	}
	e.vpc.Release("task:" + t.ID)
	_, _ = store.Update(e.env.Store, cTasks, t.ID, func(x *Task) error {
		n := core.Now()
		x.LastStatus, x.DesiredStatus, x.StopReason, x.StoppedAt, x.ExitCode = "STOPPED", "STOPPED", reason, &n, code
		return nil
	})
}

// ---- reconciliation ----

// Run keeps services at their desired count and cleans up stopped tasks.
func (e *ECS) Run(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		e.reconcile(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (e *ECS) reconcile(ctx context.Context) {
	defer core.Recover("ecs reconcile")
	e.mu.Lock()
	defer e.mu.Unlock()
	tasks := store.List[Task](e.env.Store, cTasks)
	// Detect tasks whose containers exited.
	for i, t := range tasks {
		if t.LastStatus == "PROVISIONING" && time.Since(t.CreatedAt) > 15*time.Minute {
			e.stopTask(t, "Task did not start (interrupted launch)")
			tasks[i].LastStatus = "STOPPED"
			continue
		}
		if t.LastStatus != "RUNNING" {
			if t.LastStatus == "STOPPED" && t.StoppedAt != nil && time.Since(*t.StoppedAt) > taskTTL {
				if t.ContainerID != "" {
					_ = e.env.Docker.Remove(t.ContainerID)
				}
				_ = os.Remove(e.taskLogFile(t.ID))
				_ = store.Delete(e.env.Store, cTasks, t.ID)
			}
			continue
		}
		if st := e.env.Docker.State(t.ContainerID); st != "running" {
			reason := "Essential container in task exited"
			if st == "missing" {
				reason = "Task container disappeared"
			}
			e.stopTask(t, reason)
			tasks[i].LastStatus = "STOPPED"
			if t.Service != "" {
				e.event(t.Service, fmt.Sprintf("task %s stopped: %s", t.ID[:12], reason))
			}
		}
	}
	for _, s := range store.List[Service](e.env.Store, cServices) {
		td, err := store.Get[TaskDefinition](e.env.Store, cTaskDefs, s.TaskDefinition)
		if err != nil {
			continue
		}
		var current, old []Task
		for _, t := range tasks {
			if t.Service != s.Name || t.LastStatus == "STOPPED" {
				continue
			}
			if t.TaskDefinition == s.TaskDefinition {
				current = append(current, t)
			} else {
				old = append(old, t)
			}
		}
		desired := s.DesiredCount
		if s.Status == "DRAINING" {
			desired = 0
		}
		// Start missing tasks (one per cycle keeps deployments rolling and gentle).
		if len(current) < desired {
			t, err := e.launch(ctx, td, s.Name, s.SubnetID, s.SecurityGroups)
			if err != nil {
				e.event(s.Name, "failed to start a task: "+err.Error())
				continue
			}
			if s.LoadBalancer != nil {
				_ = e.elb.SetTarget(s.LoadBalancer.TargetGroup, t.ID, s.LoadBalancer.ContainerPort, true)
			}
			e.event(s.Name, fmt.Sprintf("started task %s (%s)", t.ID[:12], t.TaskDefinition))
			continue
		}
		// Once the new revision is fully up, retire tasks of older revisions.
		if len(current) >= desired && len(old) > 0 {
			e.stopTask(old[0], "Deployment: replaced by "+s.TaskDefinition)
			e.event(s.Name, fmt.Sprintf("stopped task %s from the previous deployment", old[0].ID[:12]))
			continue
		}
		if len(current) > desired {
			e.stopTask(current[len(current)-1], "Scaling activity: desired count lowered")
			e.event(s.Name, "stopped a task to reach the desired count")
			continue
		}
		if s.Status == "DRAINING" && len(current) == 0 && len(old) == 0 {
			_ = store.Delete(e.env.Store, cServices, s.Name)
		}
	}
}

// ---- routes ----

func (e *ECS) Routes(r *httpx.Router) {
	r.Handle("GET /api/v1/ecs/task-definitions", "ecs:ListTaskDefinitions", e.listTDs)
	r.Handle("POST /api/v1/ecs/task-definitions", "ecs:RegisterTaskDefinition", e.registerTD)
	tdRes := httpx.Res("arn:hc:ecs:local-1:{account}:task-definition/{key}")
	svcRes := httpx.Res("arn:hc:ecs:local-1:{account}:service/{name}")
	taskRes := httpx.Res("arn:hc:ecs:local-1:{account}:task/{id}")
	r.Handle("GET /api/v1/ecs/task-definitions/{key}", "ecs:DescribeTaskDefinition", e.getTD, tdRes)
	r.Handle("DELETE /api/v1/ecs/task-definitions/{key}", "ecs:DeregisterTaskDefinition", e.deregisterTD, tdRes)
	r.Handle("GET /api/v1/ecs/services", "ecs:ListServices", e.listServices)
	r.Handle("POST /api/v1/ecs/services", "ecs:CreateService", e.createService)
	r.Handle("GET /api/v1/ecs/services/{name}", "ecs:DescribeServices", e.getService, svcRes)
	r.Handle("PATCH /api/v1/ecs/services/{name}", "ecs:UpdateService", e.updateService, svcRes)
	r.Handle("DELETE /api/v1/ecs/services/{name}", "ecs:DeleteService", e.deleteService, svcRes)
	r.Handle("GET /api/v1/ecs/tasks", "ecs:ListTasks", e.listTasks)
	r.Handle("POST /api/v1/ecs/tasks", "ecs:RunTask", e.runTask)
	r.Handle("GET /api/v1/ecs/tasks/{id}", "ecs:DescribeTasks", e.getTask, taskRes)
	r.Handle("POST /api/v1/ecs/tasks/{id}/stop", "ecs:StopTask", e.stopTaskRoute, taskRes)
	r.Handle("GET /api/v1/ecs/tasks/{id}/logs", "logs:GetLogEvents", e.taskLogs, taskRes)
}

var familyRe = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,255}$`)

func (e *ECS) listTDs(c *httpx.Ctx) (any, error) {
	out := []TaskDefinition{}
	for _, td := range store.List[TaskDefinition](e.env.Store, cTaskDefs) {
		if f := c.Query("family"); f != "" && td.Family != f {
			continue
		}
		if c.Query("status") != "all" && td.Status != "ACTIVE" {
			continue
		}
		out = append(out, td)
	}
	slices.SortFunc(out, func(a, b TaskDefinition) int {
		if a.Family != b.Family {
			return strings.Compare(a.Family, b.Family)
		}
		return b.Revision - a.Revision
	})
	return out, nil
}

// authorizeSecrets requires the caller to be able to read every secret a task
// definition injects: running the task discloses them to its code and logs.
func (e *ECS) authorizeSecrets(c *httpx.Ctx, td TaskDefinition) error {
	for _, s := range td.Secrets {
		name, _, _ := strings.Cut(s.ValueFrom, ":")
		if err := c.Authorize("secretsmanager:GetSecretValue", e.env.ARN("secretsmanager", "secret:"+name)); err != nil {
			return err
		}
		if _, _, err := e.secrets.Value(name, "", ""); err != nil {
			return core.BadRequest("secret %q for %s: %v", s.ValueFrom, s.Name, err)
		}
	}
	return nil
}

func (e *ECS) registerTD(c *httpx.Ctx) (any, error) {
	var in TaskDefinition
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if !familyRe.MatchString(in.Family) {
		return nil, core.BadRequest("family must be 1-255 letters, digits, hyphens or underscores")
	}
	if strings.TrimSpace(in.Image) == "" {
		return nil, core.BadRequest("image is required")
	}
	if in.CPU == 0 {
		in.CPU = 0.25
	}
	if in.MemoryMB == 0 {
		in.MemoryMB = 512
	}
	if in.CPU < 0.125 || in.CPU > 16 || in.MemoryMB < 64 || in.MemoryMB > 122880 {
		return nil, core.BadRequest("cpu must be 0.125-16 vCPU and memory_mb 64-122880")
	}
	if err := e.authorizeSecrets(c, in); err != nil {
		return nil, err
	}
	rev := 1
	for _, td := range store.List[TaskDefinition](e.env.Store, cTaskDefs) {
		if td.Family == in.Family && td.Revision >= rev {
			rev = td.Revision + 1
		}
	}
	in.Revision, in.Status, in.CreatedAt = rev, "ACTIVE", core.Now()
	in.ARN = e.env.ARN("ecs", "task-definition/"+tdKey(in.Family, rev))
	return in, store.Put(e.env.Store, cTaskDefs, tdKey(in.Family, rev), in)
}

// lookupTD accepts family:revision or just family (latest active revision).
func (e *ECS) lookupTD(key string) (TaskDefinition, error) {
	if strings.Contains(key, ":") {
		td, err := store.Get[TaskDefinition](e.env.Store, cTaskDefs, key)
		if err != nil {
			return td, core.NotFound("task definition", key)
		}
		return td, nil
	}
	var best TaskDefinition
	for _, td := range store.List[TaskDefinition](e.env.Store, cTaskDefs) {
		if td.Family == key && td.Status == "ACTIVE" && td.Revision > best.Revision {
			best = td
		}
	}
	if best.Revision == 0 {
		return best, core.NotFound("task definition", key)
	}
	return best, nil
}

func (e *ECS) getTD(c *httpx.Ctx) (any, error) { return e.lookupTD(c.Param("key")) }

func (e *ECS) deregisterTD(c *httpx.Ctx) (any, error) {
	td, err := store.Update(e.env.Store, cTaskDefs, c.Param("key"), func(td *TaskDefinition) error { td.Status = "INACTIVE"; return nil })
	if err == store.ErrNotFound {
		return nil, core.NotFound("task definition", c.Param("key"))
	}
	return td, err
}

func (e *ECS) serviceView(s Service) map[string]any {
	running, pending := 0, 0
	var tasks []Task
	for _, t := range store.List[Task](e.env.Store, cTasks) {
		if t.Service != s.Name {
			continue
		}
		tasks = append(tasks, t)
		switch t.LastStatus {
		case "RUNNING":
			running++
		case "PROVISIONING":
			pending++
		}
	}
	b, _ := json.Marshal(s)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	m["running_count"], m["pending_count"] = running, pending
	m["endpoint"] = s.Name + ".ecs.internal"
	if tasks == nil {
		tasks = []Task{}
	}
	m["tasks"] = tasks
	return m
}

func (e *ECS) listServices(c *httpx.Ctx) (any, error) {
	out := []map[string]any{}
	for _, s := range store.List[Service](e.env.Store, cServices) {
		v := e.serviceView(s)
		delete(v, "tasks")
		delete(v, "events")
		out = append(out, v)
	}
	return out, nil
}

func (e *ECS) createService(c *httpx.Ctx) (any, error) {
	var in Service
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if !familyRe.MatchString(in.Name) {
		return nil, core.BadRequest("service names are 1-255 letters, digits, hyphens or underscores")
	}
	if store.Has(e.env.Store, cServices, in.Name) {
		return nil, core.Conflict("service %q already exists", in.Name)
	}
	td, err := e.lookupTD(in.TaskDefinition)
	if err != nil {
		return nil, err
	}
	if td.Status != "ACTIVE" {
		return nil, core.BadRequest("task definition %s is inactive", in.TaskDefinition)
	}
	if err := e.authorizeSecrets(c, td); err != nil {
		return nil, err
	}
	if in.LoadBalancer != nil {
		if err := c.Authorize("elasticloadbalancing:RegisterTargets", e.env.ARN("elasticloadbalancing", "targetgroup/"+in.LoadBalancer.TargetGroup)); err != nil {
			return nil, err
		}
	}
	if in.DesiredCount < 0 || in.DesiredCount > 50 {
		return nil, core.BadRequest("desired_count must be 0-50")
	}
	if in.LoadBalancer != nil {
		vpcID, ok := e.elb.TargetGroupVPC(in.LoadBalancer.TargetGroup)
		if !ok {
			return nil, core.NotFound("target group", in.LoadBalancer.TargetGroup)
		}
		if in.LoadBalancer.ContainerPort == 0 {
			in.LoadBalancer.ContainerPort = td.ContainerPort
		}
		if in.LoadBalancer.ContainerPort == 0 {
			return nil, core.BadRequest("load_balancer.container_port is required")
		}
		pl, err := e.vpc.Place(in.SubnetID, "probe:"+in.Name)
		if err != nil {
			return nil, err
		}
		e.vpc.Release("probe:" + in.Name)
		if pl.VPC.ID != vpcID {
			return nil, core.BadRequest("the service subnet and target group must be in the same VPC")
		}
	}
	s := Service{Name: in.Name, ARN: e.env.ARN("ecs", "service/"+in.Name), TaskDefinition: tdKey(td.Family, td.Revision),
		DesiredCount: in.DesiredCount, SubnetID: in.SubnetID, SecurityGroups: in.SecurityGroups, LoadBalancer: in.LoadBalancer,
		Status: "ACTIVE", Events: []Event{{Time: core.Now(), Message: "service created"}}, CreatedAt: core.Now(), Tags: in.Tags}
	if s.SecurityGroups == nil {
		s.SecurityGroups = []string{}
	}
	if err := store.Put(e.env.Store, cServices, s.Name, s); err != nil {
		return nil, err
	}
	go e.reconcile(context.Background())
	return e.serviceView(s), nil
}

func (e *ECS) getService(c *httpx.Ctx) (any, error) {
	s, err := store.Get[Service](e.env.Store, cServices, c.Param("name"))
	if err != nil {
		return nil, core.NotFound("service", c.Param("name"))
	}
	return e.serviceView(s), nil
}

func (e *ECS) updateService(c *httpx.Ctx) (any, error) {
	var in struct {
		DesiredCount   *int   `json:"desired_count"`
		TaskDefinition string `json:"task_definition"`
		ForceDeploy    bool   `json:"force_new_deployment"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	var td TaskDefinition
	if in.TaskDefinition != "" {
		var err error
		if td, err = e.lookupTD(in.TaskDefinition); err != nil {
			return nil, err
		}
		if td.Status != "ACTIVE" {
			return nil, core.BadRequest("task definition %s is inactive", in.TaskDefinition)
		}
		if err := e.authorizeSecrets(c, td); err != nil {
			return nil, err
		}
	}
	s, err := store.Update(e.env.Store, cServices, c.Param("name"), func(s *Service) error {
		if s.Status != "ACTIVE" {
			return core.Conflict("service %s is %s", s.Name, s.Status)
		}
		if in.DesiredCount != nil {
			if *in.DesiredCount < 0 || *in.DesiredCount > 50 {
				return core.BadRequest("desired_count must be 0-50")
			}
			s.DesiredCount = *in.DesiredCount
		}
		if td.Revision > 0 && tdKey(td.Family, td.Revision) != s.TaskDefinition {
			s.TaskDefinition = tdKey(td.Family, td.Revision)
			s.Events = append([]Event{{Time: core.Now(), Message: "deployment started for " + s.TaskDefinition}}, s.Events...)
		}
		return nil
	})
	if err == store.ErrNotFound {
		return nil, core.NotFound("service", c.Param("name"))
	}
	if err != nil {
		return nil, err
	}
	if in.ForceDeploy {
		// Replace every task, one at a time, by marking them as belonging to an old deployment.
		for _, t := range store.List[Task](e.env.Store, cTasks) {
			if t.Service == s.Name && t.LastStatus == "RUNNING" {
				_, _ = store.Update(e.env.Store, cTasks, t.ID, func(x *Task) error { x.TaskDefinition += "(redeploy)"; return nil })
			}
		}
		e.event(s.Name, "forced a new deployment")
	}
	go e.reconcile(context.Background())
	return e.serviceView(s), nil
}

func (e *ECS) deleteService(c *httpx.Ctx) (any, error) {
	s, err := store.Update(e.env.Store, cServices, c.Param("name"), func(s *Service) error { s.Status, s.DesiredCount = "DRAINING", 0; return nil })
	if err == store.ErrNotFound {
		return nil, core.NotFound("service", c.Param("name"))
	}
	if err != nil {
		return nil, err
	}
	for _, t := range store.List[Task](e.env.Store, cTasks) {
		if t.Service == s.Name {
			e.stopTask(t, "Service deleted")
		}
	}
	go e.reconcile(context.Background())
	return e.serviceView(s), nil
}

func (e *ECS) listTasks(c *httpx.Ctx) (any, error) {
	out := []Task{}
	for _, t := range store.List[Task](e.env.Store, cTasks) {
		if sv := c.Query("service"); sv != "" && t.Service != sv {
			continue
		}
		out = append(out, t)
	}
	slices.SortFunc(out, func(a, b Task) int { return b.CreatedAt.Compare(a.CreatedAt) })
	return out, nil
}

func (e *ECS) runTask(c *httpx.Ctx) (any, error) {
	var in struct {
		TaskDefinition string            `json:"task_definition"`
		SubnetID       string            `json:"subnet_id"`
		SecurityGroups []string          `json:"security_groups"`
		Environment    map[string]string `json:"environment"`
		Command        []string          `json:"command"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	td, err := e.lookupTD(in.TaskDefinition)
	if err != nil {
		return nil, err
	}
	if td.Status != "ACTIVE" {
		return nil, core.BadRequest("task definition %s is inactive", in.TaskDefinition)
	}
	if err := e.authorizeSecrets(c, td); err != nil {
		return nil, err
	}
	if len(in.Command) > 0 {
		td.Command = in.Command
	}
	if len(in.Environment) > 0 {
		env := map[string]string{}
		for k, v := range td.Environment {
			env[k] = v
		}
		for k, v := range in.Environment {
			env[k] = v
		}
		td.Environment = env
	}
	t, err := e.launch(c.R.Context(), td, "", in.SubnetID, in.SecurityGroups)
	if err != nil {
		return nil, core.Errf(http.StatusBadRequest, "TaskFailedToStart", "%v", err)
	}
	return t, nil
}

func (e *ECS) getTask(c *httpx.Ctx) (any, error) {
	t, err := store.Get[Task](e.env.Store, cTasks, c.Param("id"))
	if err != nil {
		return nil, core.NotFound("task", c.Param("id"))
	}
	return t, nil
}

func (e *ECS) stopTaskRoute(c *httpx.Ctx) (any, error) {
	t, err := store.Get[Task](e.env.Store, cTasks, c.Param("id"))
	if err != nil {
		return nil, core.NotFound("task", c.Param("id"))
	}
	e.stopTask(t, "Task stopped by user")
	return store.Get[Task](e.env.Store, cTasks, t.ID)
}

func (e *ECS) taskLogs(c *httpx.Ctx) (any, error) {
	t, err := store.Get[Task](e.env.Store, cTasks, c.Param("id"))
	if err != nil {
		return nil, core.NotFound("task", c.Param("id"))
	}
	if t.ContainerID == "" {
		return map[string]string{"output": ""}, nil
	}
	if t.LastStatus == "STOPPED" {
		b, _ := os.ReadFile(e.taskLogFile(t.ID))
		return map[string]string{"output": string(b)}, nil
	}
	var buf strings.Builder
	err = e.env.Docker.C.Logs(docker.LogsOptions{Container: t.ContainerID, OutputStream: &buf, ErrorStream: &buf, Stdout: true, Stderr: true, Tail: strconv.Itoa(c.QueryInt("tail", 500)), Timestamps: true})
	if err != nil && !strings.Contains(err.Error(), "No such container") {
		return nil, err
	}
	return map[string]string{"output": buf.String()}, nil
}
