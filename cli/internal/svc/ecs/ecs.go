// Package ecs implements container services (ECS on Fargate): versioned task
// definitions, services that keep a desired number of tasks running with
// rolling deployments and automatic load balancer registration, and one-off
// tasks. Secrets from Secrets Manager are injected as environment variables.
package ecs

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
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
	cClusters = "ecs_clusters"
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

	// Set for task definitions registered through the AWS API.
	ContainerName  string         `json:"container_name,omitempty"`
	TaskRole       string         `json:"task_role,omitempty"`
	ExecutionRole  string         `json:"execution_role,omitempty"`
	LogGroup       string         `json:"log_group,omitempty"`
	LogStreamBase  string         `json:"log_stream_base,omitempty"`
	AWS            map[string]any `json:"aws,omitempty"` // the request as registered, echoed by Describe
	Tags           core.Tags      `json:"tags,omitempty"`
	RegisteredBy   string         `json:"registered_by,omitempty"`
	DeregisteredAt *time.Time     `json:"deregistered_at,omitempty"`
}

func tdKey(family string, rev int) string { return family + ":" + strconv.Itoa(rev) }

type LBBinding struct {
	TargetGroup   string `json:"target_group"`
	ContainerPort int    `json:"container_port"`
	ContainerName string `json:"container_name,omitempty"`
}

// Cluster groups services and tasks. Services and tasks created through the
// native API belong to the "default" cluster.
type Cluster struct {
	Name      string         `json:"name"`
	ARN       string         `json:"arn"`
	Status    string         `json:"status"` // ACTIVE | INACTIVE
	Tags      core.Tags      `json:"tags,omitempty"`
	AWS       map[string]any `json:"aws,omitempty"` // settings, configuration, capacity providers
	CreatedAt time.Time      `json:"created_at"`
}

const defaultCluster = "default"

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

	Cluster      string         `json:"cluster,omitempty"`
	Subnets      []string       `json:"subnets,omitempty"` // tasks are spread over these (SubnetID is the first)
	Extra        map[string]any `json:"extra,omitempty"`   // AWS request fields echoed by DescribeServices
	DeploymentID string         `json:"deployment_id,omitempty"`
	DeployedAt   time.Time      `json:"deployed_at,omitempty"`
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
	Cluster        string         `json:"cluster,omitempty"`
	ContainerName  string         `json:"container_name,omitempty"`
	Image          string         `json:"image,omitempty"`
	StartedBy      string         `json:"started_by,omitempty"`
	Tags           core.Tags      `json:"tags,omitempty"`
	LogGroup       string         `json:"log_group,omitempty"`
	LogStream      string         `json:"log_stream,omitempty"`
	LogCursor      time.Time      `json:"log_cursor,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
	StartedAt      *time.Time     `json:"started_at,omitempty"`
	StoppedAt      *time.Time     `json:"stopped_at,omitempty"`
}

type ECS struct {
	// DNSFor returns resolver addresses for containers in a VPC (Route 53).
	DNSFor func(vpcID string) []string
	// Roles issues credentials to tasks with a task role (IAM).
	Roles Roles
	// Logs receives the output of containers configured with the awslogs driver.
	Logs func(group, stream string, ts []time.Time, msgs []string) error
	// RegistryHost is the ECR registry address ("localhost:5500"); images from
	// <account>.dkr.ecr.<region>.amazonaws.com are pulled from it.
	RegistryHost string
	env          *svc.Env
	vpc          *vpc.Service
	elb          *elb.Service
	secrets      *secrets.Service
	// Params resolves SSM parameter secrets.
	Params  ParamStore
	hostCPU float64
	mu      sync.Mutex // serialises reconciliation
	tdMu    sync.Mutex // serialises revision numbering
}

// Credentials are temporary credentials for a task role.
type Credentials struct {
	AccessKeyID, SecretAccessKey, SessionToken string
	Expiration                                 time.Time
}

// Roles resolves task roles: TaskRole validates that the role trusts
// ecs-tasks.amazonaws.com and returns its ARN.
type Roles interface {
	TaskRole(ref string) (string, error)
	TaskCredentials(ref, session string, ttl time.Duration) (Credentials, error)
}

// SSM reads parameters for task secrets.
type ParamStore interface {
	Value(ref string) (string, error)
}

func New(env *svc.Env, v *vpc.Service, lb *elb.Service, sec *secrets.Service) *ECS {
	e := &ECS{env: env, vpc: v, elb: lb, secrets: sec, hostCPU: 1}
	if env.Docker != nil {
		if info, err := env.Docker.C.Info(); err == nil && info.NCPU > 0 {
			e.hostCPU = float64(info.NCPU)
		}
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
		if strings.HasPrefix(s.ValueFrom, "arn:") && strings.Contains(s.ValueFrom, ":ssm:") || strings.HasPrefix(s.ValueFrom, "/") {
			if e.Params == nil {
				return nil, fmt.Errorf("parameter %s: SSM is not available", s.ValueFrom)
			}
			val, err := e.Params.Value(ssmName(s.ValueFrom))
			if err != nil {
				return nil, fmt.Errorf("parameter %s: %w", s.ValueFrom, err)
			}
			env[s.Name] = val
			continue
		}
		name, key := splitSecretRef(s.ValueFrom)
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

// ssmName turns a parameter ARN into the parameter name.
func ssmName(ref string) string {
	if !strings.HasPrefix(ref, "arn:") {
		return ref
	}
	_, n, _ := strings.Cut(ref, ":parameter")
	if strings.Contains(n[min(1, len(n)):], "/") {
		return n
	}
	return strings.TrimPrefix(n, "/")
}

// splitSecretRef splits a Secrets Manager reference into the secret (name or
// ARN) and the optional JSON key: "name:key", or an ARN with
// ":json-key:version-stage:version-id" appended.
func splitSecretRef(ref string) (name, key string) {
	if strings.HasPrefix(ref, "arn:") {
		parts := strings.Split(ref, ":")
		if len(parts) > 7 {
			return strings.Join(parts[:7], ":"), parts[7]
		}
		return ref, ""
	}
	name, key, _ = strings.Cut(ref, ":")
	return name, key
}

var ecrImage = regexp.MustCompile(`^[0-9]{12}\.dkr\.ecr\.[a-z0-9-]+\.amazonaws\.com/`)

// launchSpec describes one task to start.
type launchSpec struct {
	TD        TaskDefinition
	Cluster   string
	Service   string
	Subnet    string
	SGs       []string
	StartedBy string
	Tags      core.Tags
}

// launch starts a task container; it returns once the container is running.
func (e *ECS) launch(ctx context.Context, ls launchSpec) (Task, error) {
	td, service, subnet, sgs := ls.TD, ls.Service, ls.Subnet, ls.SGs
	cluster := ls.Cluster
	if cluster == "" {
		cluster = defaultCluster
	}
	id := core.RandHex(32)
	pl, err := e.vpc.Place(subnet, "task:"+id)
	if err != nil {
		return Task{}, err
	}
	t := Task{ID: id, ARN: e.env.ARN("ecs", "task/"+cluster+"/"+id), Cluster: cluster, ContainerName: td.ContainerName, Image: td.Image,
		StartedBy: ls.StartedBy, Tags: ls.Tags, Service: service, TaskDefinition: tdKey(td.Family, td.Revision),
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
	env["AWS_REGION"], env["AWS_DEFAULT_REGION"] = core.Region, core.Region
	if e.env.ContainerAPI != "" {
		env["AWS_ENDPOINT_URL"] = e.env.ContainerAPI
	}
	if td.TaskRole != "" && e.Roles != nil {
		c, err := e.Roles.TaskCredentials(td.TaskRole, "ecs-task-"+id[:12], 12*time.Hour)
		if err != nil {
			return stop("ResourceInitializationError: the task role could not be assumed: " + err.Error())
		}
		env["AWS_ACCESS_KEY_ID"], env["AWS_SECRET_ACCESS_KEY"], env["AWS_SESSION_TOKEN"] = c.AccessKeyID, c.SecretAccessKey, c.SessionToken
	}
	if td.LogGroup != "" && e.Logs != nil {
		name := td.ContainerName
		if name == "" {
			name = "app"
		}
		stream := name + "/" + id
		if td.LogStreamBase != "" {
			stream = td.LogStreamBase + "/" + stream
		}
		t.LogGroup, t.LogStream, t.LogCursor = td.LogGroup, stream, core.Now().Add(-2*time.Second)
		_ = store.Put(e.env.Store, cTasks, id, t)
	}
	image := td.Image
	if ecrImage.MatchString(image) && e.RegistryHost != "" {
		image = e.RegistryHost + image[strings.Index(image, "/"):]
	}
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
		Name: svc.ContainerName("ecs", id[:12]), Image: image, Cmd: td.Command, Entrypoint: td.Entrypoint, Env: env,
		Labels:   runtime.Labels("ecs", id, map[string]string{"homecloud.ecs.service": service, "homecloud.ecs.taskdef": t.TaskDefinition, "homecloud.ecs.cluster": cluster}),
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
		e.shipLogs(t)
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

// shipLogs copies new container output to CloudWatch Logs (awslogs driver).
func (e *ECS) shipLogs(t Task) {
	if t.LogGroup == "" || e.Logs == nil || t.ContainerID == "" {
		return
	}
	cur, err := store.Get[Task](e.env.Store, cTasks, t.ID)
	if err != nil {
		return
	}
	out, err := e.env.Docker.Logs(t.ContainerID, 0, cur.LogCursor.Add(-time.Second))
	if err != nil || out == "" {
		return
	}
	var ts []time.Time
	var msgs []string
	last := cur.LogCursor
	for _, line := range strings.Split(out, "\n") {
		stamp, msg, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, stamp)
		if err != nil || !at.After(cur.LogCursor) {
			continue
		}
		ts, msgs = append(ts, at), append(msgs, msg)
		if at.After(last) {
			last = at
		}
	}
	if len(msgs) == 0 {
		return
	}
	if err := e.Logs(t.LogGroup, t.LogStream, ts, msgs); err != nil {
		log.Printf("ecs: ship logs of %s: %v", t.ID[:12], err)
		return
	}
	_, _ = store.Update(e.env.Store, cTasks, t.ID, func(x *Task) error { x.LogCursor = last; return nil })
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
		e.shipLogs(t)
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
		if s.Status == "INACTIVE" {
			if len(s.Events) > 0 && time.Since(s.Events[0].Time) > taskTTL {
				_ = store.Delete(e.env.Store, cServices, s.Name)
			}
			continue
		}
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
			subnet := s.SubnetID
			if len(s.Subnets) > 1 {
				subnet = s.Subnets[len(current)%len(s.Subnets)]
			}
			t, err := e.launch(ctx, launchSpec{TD: td, Cluster: s.Cluster, Service: s.Name, Subnet: subnet, SGs: s.SecurityGroups, StartedBy: "ecs-svc/" + s.DeploymentID})
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
			// Like ECS, keep the service visible as INACTIVE for a while.
			_, _ = store.Update(e.env.Store, cServices, s.Name, func(x *Service) error {
				x.Status = "INACTIVE"
				x.Events = append([]Event{{Time: core.Now(), Message: "service deleted"}}, x.Events...)
				return nil
			})
		}
	}
}

// ---- routes ----

func (e *ECS) Routes(r *httpx.Router) {
	r.Handle("GET /api/v1/ecs/task-definitions", "ecs:ListTaskDefinitions", e.listTDs)
	r.Handle("POST /api/v1/ecs/task-definitions", "ecs:RegisterTaskDefinition", e.registerTD)
	tdRes := httpx.Res("arn:aws:ecs:{region}:{account}:task-definition/{key}")
	svcRes := httpx.Res("arn:aws:ecs:{region}:{account}:service/{name}")
	taskRes := httpx.Res("arn:aws:ecs:{region}:{account}:task/{id}")
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

// authz checks the caller may perform an action on a resource.
type authz func(action, resource string) error

// authorizeSecrets requires the caller to be able to read every secret a task
// definition injects: running the task discloses them to its code and logs.
func (e *ECS) authorizeSecrets(az authz, td TaskDefinition) error {
	for _, s := range td.Secrets {
		if strings.HasPrefix(s.ValueFrom, "arn:") && strings.Contains(s.ValueFrom, ":ssm:") || strings.HasPrefix(s.ValueFrom, "/") {
			n := ssmName(s.ValueFrom)
			if err := az("ssm:GetParameter", e.env.ARN("ssm", "parameter/"+strings.TrimPrefix(n, "/"))); err != nil {
				return err
			}
			if e.Params == nil {
				return core.BadRequest("SSM parameters are not available")
			}
			if _, err := e.Params.Value(n); err != nil {
				return core.BadRequest("parameter %q for %s: %v", s.ValueFrom, s.Name, err)
			}
			continue
		}
		ref, _ := splitSecretRef(s.ValueFrom)
		name := ref
		if strings.HasPrefix(ref, "arn:") {
			_, name, _ = strings.Cut(ref, ":secret:")
			if n := len(name); n > 7 && name[n-7] == '-' {
				name = name[:n-7]
			}
		}
		if err := az("secretsmanager:GetSecretValue", e.env.ARN("secretsmanager", "secret:"+name)); err != nil {
			return err
		}
		if _, _, err := e.secrets.Value(ref, "", ""); err != nil {
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
	return e.registerTaskDef(c.Authorize, in)
}

// registerTaskDef validates in and stores it as the next revision of its family.
func (e *ECS) registerTaskDef(az authz, in TaskDefinition) (TaskDefinition, error) {
	if !familyRe.MatchString(in.Family) {
		return in, core.BadRequest("family must be 1-255 letters, digits, hyphens or underscores")
	}
	if strings.TrimSpace(in.Image) == "" {
		return in, core.BadRequest("image is required")
	}
	if in.CPU == 0 {
		in.CPU = 0.25
	}
	if in.MemoryMB == 0 {
		in.MemoryMB = 512
	}
	if in.CPU < 0.125 || in.CPU > 16 || in.MemoryMB < 64 || in.MemoryMB > 122880 {
		return in, core.BadRequest("cpu must be 0.125-16 vCPU and memory_mb 64-122880")
	}
	if err := e.authorizeSecrets(az, in); err != nil {
		return in, err
	}
	e.tdMu.Lock()
	defer e.tdMu.Unlock()
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
		if s.Status == "INACTIVE" {
			continue
		}
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
	s, err := e.createSvc(c.Authorize, in)
	if err != nil {
		return nil, err
	}
	return e.serviceView(s), nil
}

// ensureCluster returns the cluster, creating "default" on first use.
func (e *ECS) ensureCluster(name string) (Cluster, error) {
	c, err := store.Get[Cluster](e.env.Store, cClusters, name)
	if err == nil && c.Status == "ACTIVE" {
		return c, nil
	}
	if name != defaultCluster {
		return c, core.NotFound("cluster", name)
	}
	c = Cluster{Name: name, ARN: e.env.ARN("ecs", "cluster/"+name), Status: "ACTIVE", CreatedAt: core.Now()}
	return c, store.Put(e.env.Store, cClusters, name, c)
}

func newDeploymentID() string { return "ecs-svc/" + core.RandHex(9) }

// createSvc validates and starts a service. in.Cluster defaults to "default".
func (e *ECS) createSvc(az authz, in Service) (Service, error) {
	if in.Cluster == "" {
		in.Cluster = defaultCluster
	}
	if !familyRe.MatchString(in.Name) {
		return in, core.BadRequest("service names are 1-255 letters, digits, hyphens or underscores")
	}
	if _, err := e.ensureCluster(in.Cluster); err != nil {
		return in, err
	}
	if old, err := store.Get[Service](e.env.Store, cServices, in.Name); err == nil && old.Status != "INACTIVE" {
		return in, core.Conflict("service %q already exists", in.Name)
	}
	td, err := e.lookupTD(in.TaskDefinition)
	if err != nil {
		return in, err
	}
	if td.Status != "ACTIVE" {
		return in, core.BadRequest("task definition %s is inactive", in.TaskDefinition)
	}
	if err := e.authorizeSecrets(az, td); err != nil {
		return in, err
	}
	if in.DesiredCount < 0 || in.DesiredCount > 50 {
		return in, core.BadRequest("desired_count must be 0-50")
	}
	if len(in.Subnets) == 0 && in.SubnetID != "" {
		in.Subnets = []string{in.SubnetID}
	}
	if len(in.Subnets) > 0 {
		in.SubnetID = in.Subnets[0]
	}
	if in.LoadBalancer != nil {
		name, ok := elb.TargetGroupName(in.LoadBalancer.TargetGroup)
		if !ok {
			return in, core.NotFound("target group", in.LoadBalancer.TargetGroup)
		}
		in.LoadBalancer.TargetGroup = name
		if err := az("elasticloadbalancing:RegisterTargets", e.elb.TargetGroupARN(name)); err != nil {
			return in, err
		}
		vpcID, ok := e.elb.TargetGroupVPC(name)
		if !ok {
			return in, core.NotFound("target group", name)
		}
		if in.LoadBalancer.ContainerPort == 0 {
			in.LoadBalancer.ContainerPort = td.ContainerPort
		}
		if in.LoadBalancer.ContainerPort == 0 {
			return in, core.BadRequest("load_balancer.container_port is required")
		}
		pl, err := e.vpc.Place(in.SubnetID, "probe:"+in.Name)
		if err != nil {
			return in, err
		}
		e.vpc.Release("probe:" + in.Name)
		if pl.VPC.ID != vpcID {
			return in, core.BadRequest("the service subnet and target group must be in the same VPC")
		}
	}
	s := Service{Name: in.Name, ARN: e.env.ARN("ecs", "service/"+in.Cluster+"/"+in.Name), TaskDefinition: tdKey(td.Family, td.Revision),
		DesiredCount: in.DesiredCount, SubnetID: in.SubnetID, Subnets: in.Subnets, SecurityGroups: in.SecurityGroups, LoadBalancer: in.LoadBalancer,
		Status: "ACTIVE", Events: []Event{{Time: core.Now(), Message: "service created"}}, CreatedAt: core.Now(), Tags: in.Tags,
		Cluster: in.Cluster, Extra: in.Extra, DeploymentID: newDeploymentID(), DeployedAt: core.Now()}
	if s.SecurityGroups == nil {
		s.SecurityGroups = []string{}
	}
	if err := store.Put(e.env.Store, cServices, s.Name, s); err != nil {
		return s, err
	}
	go e.reconcile(context.Background())
	return s, nil
}

func (e *ECS) getService(c *httpx.Ctx) (any, error) {
	s, err := store.Get[Service](e.env.Store, cServices, c.Param("name"))
	if err != nil {
		return nil, core.NotFound("service", c.Param("name"))
	}
	return e.serviceView(s), nil
}

type serviceUpdate struct {
	DesiredCount   *int           `json:"desired_count"`
	TaskDefinition string         `json:"task_definition"`
	ForceDeploy    bool           `json:"force_new_deployment"`
	Subnets        []string       `json:"-"`
	SecurityGroups *[]string      `json:"-"`
	Extra          map[string]any `json:"-"`
	Tags           core.Tags      `json:"-"`
}

func (e *ECS) updateService(c *httpx.Ctx) (any, error) {
	var in serviceUpdate
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	s, err := e.updateSvc(c.Authorize, c.Param("name"), in)
	if err != nil {
		return nil, err
	}
	return e.serviceView(s), nil
}

func (e *ECS) updateSvc(az authz, name string, in serviceUpdate) (Service, error) {
	var td TaskDefinition
	if in.TaskDefinition != "" {
		var err error
		if td, err = e.lookupTD(in.TaskDefinition); err != nil {
			return Service{}, err
		}
		if td.Status != "ACTIVE" {
			return Service{}, core.BadRequest("task definition %s is inactive", in.TaskDefinition)
		}
		if err := e.authorizeSecrets(az, td); err != nil {
			return Service{}, err
		}
	}
	s, err := store.Update(e.env.Store, cServices, name, func(s *Service) error {
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
			s.DeploymentID, s.DeployedAt = newDeploymentID(), core.Now()
			s.Events = append([]Event{{Time: core.Now(), Message: "deployment started for " + s.TaskDefinition}}, s.Events...)
		}
		if len(in.Subnets) > 0 {
			s.Subnets, s.SubnetID = in.Subnets, in.Subnets[0]
		}
		if in.SecurityGroups != nil {
			s.SecurityGroups = *in.SecurityGroups
		}
		if len(in.Extra) > 0 {
			if s.Extra == nil {
				s.Extra = map[string]any{}
			}
			for k, v := range in.Extra {
				s.Extra[k] = v
			}
		}
		return nil
	})
	if err == store.ErrNotFound {
		return s, core.NotFound("service", name)
	}
	if err != nil {
		return s, err
	}
	if in.ForceDeploy {
		// Replace every task, one at a time, by marking them as belonging to an old deployment.
		for _, t := range store.List[Task](e.env.Store, cTasks) {
			if t.Service == s.Name && t.LastStatus == "RUNNING" {
				_, _ = store.Update(e.env.Store, cTasks, t.ID, func(x *Task) error { x.TaskDefinition += "(redeploy)"; return nil })
			}
		}
		s, _ = store.Update(e.env.Store, cServices, s.Name, func(x *Service) error {
			x.DeploymentID, x.DeployedAt = newDeploymentID(), core.Now()
			return nil
		})
		e.event(s.Name, "forced a new deployment")
	}
	go e.reconcile(context.Background())
	return s, nil
}

func (e *ECS) deleteService(c *httpx.Ctx) (any, error) {
	s, err := e.deleteSvc(c.Param("name"))
	if err != nil {
		return nil, err
	}
	return e.serviceView(s), nil
}

func (e *ECS) deleteSvc(name string) (Service, error) {
	s, err := store.Update(e.env.Store, cServices, name, func(s *Service) error {
		if s.Status == "INACTIVE" {
			return core.NotFound("service", name)
		}
		s.Status, s.DesiredCount = "DRAINING", 0
		return nil
	})
	if err == store.ErrNotFound {
		return s, core.NotFound("service", name)
	}
	if err != nil {
		return s, err
	}
	for _, t := range store.List[Task](e.env.Store, cTasks) {
		if t.Service == s.Name {
			e.stopTask(t, "Service deleted")
		}
	}
	go e.reconcile(context.Background())
	return s, nil
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
	var in runSpec
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	return e.runTaskSpec(c.R.Context(), c.Authorize, in)
}

type runSpec struct {
	TaskDefinition string            `json:"task_definition"`
	Cluster        string            `json:"cluster"`
	SubnetID       string            `json:"subnet_id"`
	SecurityGroups []string          `json:"security_groups"`
	Environment    map[string]string `json:"environment"`
	Command        []string          `json:"command"`
	StartedBy      string            `json:"-"`
	Tags           core.Tags         `json:"-"`
}

func (e *ECS) runTaskSpec(ctx context.Context, az authz, in runSpec) (Task, error) {
	if in.Cluster == "" {
		in.Cluster = defaultCluster
	}
	if _, err := e.ensureCluster(in.Cluster); err != nil {
		return Task{}, err
	}
	td, err := e.lookupTD(in.TaskDefinition)
	if err != nil {
		return Task{}, err
	}
	if td.Status != "ACTIVE" {
		return Task{}, core.BadRequest("task definition %s is inactive", in.TaskDefinition)
	}
	if err := e.authorizeSecrets(az, td); err != nil {
		return Task{}, err
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
	t, err := e.launch(ctx, launchSpec{TD: td, Cluster: in.Cluster, Subnet: in.SubnetID, SGs: in.SecurityGroups, StartedBy: in.StartedBy, Tags: in.Tags})
	if err != nil {
		return t, core.Errf(http.StatusBadRequest, "TaskFailedToStart", "%v", err)
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
