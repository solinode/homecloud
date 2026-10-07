// Package autoscaling implements EC2 Auto Scaling groups: a launch
// configuration kept at a desired capacity across subnets, unhealthy
// instances replaced, load balancer target groups kept in sync, and
// target-tracking scaling on average CPU utilization from CloudWatch.
package autoscaling

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/store"
	"github.com/homecloudhq/homecloud/cli/internal/svc"
	"github.com/homecloudhq/homecloud/cli/internal/svc/cloudwatch"
	"github.com/homecloudhq/homecloud/cli/internal/svc/ec2"
	"github.com/homecloudhq/homecloud/cli/internal/svc/elb"
)

const (
	cGroups  = "autoscaling_groups"
	groupTag = "hc:autoscaling:groupName"
)

type LaunchConfig struct {
	ImageID          string        `json:"image_id"`
	InstanceType     string        `json:"instance_type"`
	SecurityGroupIDs []string      `json:"security_group_ids"`
	UserData         string        `json:"user_data,omitempty"`
	FileSystems      []ec2.FSMount `json:"file_systems,omitempty"`
	// IAMInstanceProfile is the launch template's instance profile; creating or
	// updating a group needs iam:PassRole for its role.
	IAMInstanceProfile string `json:"iam_instance_profile,omitempty"`
}

type Policy struct {
	Name            string  `json:"name"`
	Metric          string  `json:"metric"` // CPUUtilization | MemoryUtilization
	TargetValue     float64 `json:"target_value"`
	CooldownSeconds int     `json:"cooldown_seconds"`
	// AWS API fields (see aws.go). Policies the group cannot act on (step and
	// simple scaling, other metrics) are stored as Inert.
	Type             string           `json:"type,omitempty"`
	Inert            bool             `json:"inert,omitempty"`
	AdjustmentType   string           `json:"adjustment_type,omitempty"`
	ScalingAdjust    int              `json:"scaling_adjustment,omitempty"`
	MinAdjustment    int              `json:"min_adjustment,omitempty"`
	Cooldown         *int             `json:"cooldown,omitempty"`
	Warmup           int              `json:"warmup,omitempty"`
	PredefinedMetric string           `json:"predefined_metric,omitempty"`
	ResourceLabel    string           `json:"resource_label,omitempty"`
	DisableScaleIn   bool             `json:"disable_scale_in,omitempty"`
	Disabled         bool             `json:"disabled,omitempty"`
	MetricAggregate  string           `json:"metric_aggregate,omitempty"`
	Steps            []StepAdjustment `json:"steps,omitempty"`
}

type StepAdjustment struct {
	MetricIntervalLowerBound *float64
	MetricIntervalUpperBound *float64
	ScalingAdjustment        int
}

// TemplateRef points a group at an EC2 launch template.
type TemplateRef struct {
	ID      string `json:"id,omitempty"`
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"` // "$Latest", "$Default" or a number
}

type GroupTag struct {
	Key               string `json:"key"`
	Value             string `json:"value"`
	PropagateAtLaunch bool   `json:"propagate_at_launch"`
}

type Activity struct {
	ID          string    `json:"id,omitempty"`
	Time        time.Time `json:"time"`
	Description string    `json:"description"`
	Cause       string    `json:"cause"`
	Status      string    `json:"status"`
}

type Group struct {
	Name            string       `json:"name"`
	ARN             string       `json:"arn"`
	Launch          LaunchConfig `json:"launch"`
	MinSize         int          `json:"min_size"`
	MaxSize         int          `json:"max_size"`
	DesiredCapacity int          `json:"desired_capacity"`
	SubnetIDs       []string     `json:"subnet_ids"`
	TargetGroups    []string     `json:"target_groups"`
	HealthGraceSecs int          `json:"health_check_grace_seconds"`
	Policies        []Policy     `json:"policies"`
	Suspended       bool         `json:"suspended"`
	Deleting        bool         `json:"deleting,omitempty"`
	LastScaling     *time.Time   `json:"last_scaling,omitempty"`
	Activities      []Activity   `json:"activities"`
	CreatedAt       time.Time    `json:"created_at"`
	// AWS API fields.
	Template            *TemplateRef `json:"template,omitempty"`
	Tags                []GroupTag   `json:"tags,omitempty"`
	HealthCheckType     string       `json:"health_check_type,omitempty"`
	DefaultCooldown     int          `json:"default_cooldown,omitempty"`
	SuspendedProcesses  []string     `json:"suspended_processes,omitempty"`
	TerminationPolicies []string     `json:"termination_policies,omitempty"`
	ProtectNewInstances bool         `json:"protect_new_instances,omitempty"`
}

// suspends reports whether an Auto Scaling process is suspended.
func (g Group) suspends(process string) bool { return slices.Contains(g.SuspendedProcesses, process) }

type Service struct {
	env *svc.Env
	ec2 *ec2.Service
	elb *elb.Service
	cw  *cloudwatch.Service
	mu  sync.Mutex
}

func New(env *svc.Env, e *ec2.Service, lb *elb.Service, cw *cloudwatch.Service) *Service {
	return &Service{env: env, ec2: e, elb: lb, cw: cw}
}

func (s *Service) activity(name, desc, cause, status string) {
	_, _ = store.Update(s.env.Store, cGroups, name, func(g *Group) error {
		g.Activities = append([]Activity{{ID: newUUID(), Time: core.Now(), Description: desc, Cause: cause, Status: status}}, g.Activities...)
		if len(g.Activities) > 100 {
			g.Activities = g.Activities[:100]
		}
		return nil
	})
}

// members returns the group's live instances (pending or running), oldest first.
func (s *Service) members(name string) (live, dead []ec2.Instance) {
	for _, i := range s.ec2.Instances() {
		if i.Tags[groupTag] != name {
			continue
		}
		switch i.State {
		case "pending", "running":
			live = append(live, i)
		case "stopped", "stopping":
			dead = append(dead, i)
		}
	}
	slices.SortFunc(live, func(a, b ec2.Instance) int { return a.LaunchTime.Compare(b.LaunchTime) })
	return live, dead
}

// Run reconciles every group every 10 seconds.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		for _, g := range store.List[Group](s.env.Store, cGroups) {
			s.reconcile(g)
		}
	}
}

func (s *Service) reconcile(stale Group) {
	defer core.Recover("autoscaling " + stale.Name)
	s.mu.Lock()
	defer s.mu.Unlock()
	// Act on the current record, not the caller's copy (it may be outdated or deleted).
	g, err := store.Get[Group](s.env.Store, cGroups, stale.Name)
	if err != nil {
		return
	}
	live, dead := s.members(g.Name)
	// Replace instances that stopped (an unhealthy instance in a group is terminated).
	for _, i := range dead {
		s.detach(g, i.ID)
		if _, err := s.ec2.Terminate(i.ID); err == nil {
			s.activity(g.Name, "Terminating unhealthy instance "+i.ID, "instance was "+i.State, "Successful")
		}
	}
	if g.Deleting {
		for _, i := range live {
			s.detach(g, i.ID)
			_, _ = s.ec2.Terminate(i.ID)
		}
		_ = store.Delete(s.env.Store, cGroups, g.Name)
		return
	}
	// Keep target groups in step with running members.
	for _, i := range live {
		if i.State == "running" {
			for _, tg := range g.TargetGroups {
				_ = s.ensureTarget(tg, i.ID)
			}
		}
	}
	if !g.Suspended && !g.suspends("AlarmNotification") {
		g = s.scale(g, live)
	}
	switch {
	case len(live) < g.DesiredCapacity && !g.suspends("Launch"):
		n := g.DesiredCapacity - len(live)
		for k := 0; k < n; k++ {
			subnet := ""
			if len(g.SubnetIDs) > 0 {
				subnet = g.SubnetIDs[(len(live)+k)%len(g.SubnetIDs)]
			}
			in, err := s.launchInput(g, subnet)
			var out []ec2.Instance
			if err == nil {
				out, err = s.ec2.Launch(in)
			}
			if err != nil {
				s.activity(g.Name, "Launching a new instance", err.Error(), "Failed")
				return
			}
			s.activity(g.Name, "Launching a new instance: "+out[0].ID, fmt.Sprintf("capacity %d below desired %d", len(live)+k, g.DesiredCapacity), "Successful")
		}
	case len(live) > g.DesiredCapacity && !g.suspends("Terminate"):
		// Terminate the newest instances first.
		for _, i := range live[g.DesiredCapacity:] {
			s.detach(g, i.ID)
			if _, err := s.ec2.Terminate(i.ID); err == nil {
				s.activity(g.Name, "Terminating instance "+i.ID, fmt.Sprintf("capacity above desired %d", g.DesiredCapacity), "Successful")
			}
		}
	}
}

// launchInput builds the launch of one instance: from the group's launch
// template (resolved now, so new template versions apply) or launch config.
func (s *Service) launchInput(g Group, subnet string) (ec2.RunInput, error) {
	in := ec2.RunInput{ImageID: g.Launch.ImageID, InstanceType: g.Launch.InstanceType, SecurityGroupIDs: g.Launch.SecurityGroupIDs,
		UserData: g.Launch.UserData, FileSystems: g.Launch.FileSystems}
	tags := core.Tags{}
	if g.Template != nil {
		rt, err := s.ec2.ResolveTemplate(g.Template.ID, g.Template.Name, g.Template.Version)
		if err != nil {
			return in, err
		}
		in = rt.Input
		for k, v := range in.Tags {
			tags[k] = v
		}
	}
	for _, t := range g.Tags {
		if t.PropagateAtLaunch {
			tags[t.Key] = t.Value
		}
	}
	tags[groupTag], tags["aws:autoscaling:groupName"] = g.Name, g.Name
	in.Name, in.SubnetID, in.Count = g.Name, subnet, 1
	if n, ok := tags["Name"]; ok {
		in.Name, in.ExactName = n, true
		delete(tags, "Name")
	}
	in.Tags = tags
	return in, nil
}

func (s *Service) ensureTarget(tg, id string) error {
	return s.elb.EnsureTarget(tg, id)
}

func (s *Service) detach(g Group, id string) {
	for _, tg := range g.TargetGroups {
		_ = s.elb.SetTarget(tg, id, 0, false)
	}
}

// scale applies target-tracking policies and returns the (possibly updated) group.
func (s *Service) scale(g Group, live []ec2.Instance) Group {
	if len(g.Policies) == 0 || len(live) == 0 {
		return g
	}
	desired := g.DesiredCapacity
	var cause string
	for _, p := range g.Policies {
		if p.Inert || p.Disabled {
			continue
		}
		cooldown := time.Duration(max(p.CooldownSeconds, 60)) * time.Second
		if g.LastScaling != nil && time.Since(*g.LastScaling) < cooldown {
			return g
		}
		var sum float64
		n := 0
		for _, i := range live {
			if i.State != "running" || time.Since(i.LaunchTime) < time.Duration(g.HealthGraceSecs)*time.Second {
				continue
			}
			dps, _ := s.cw.Statistics("HC/EC2", p.Metric, map[string]string{"InstanceId": i.ID}, time.Now().Add(-3*time.Minute), time.Now(), time.Minute)
			for _, d := range dps {
				sum += d.Average
				n++
			}
		}
		if n == 0 {
			continue
		}
		avg := sum / float64(n)
		want := int(math.Ceil(float64(len(live)) * avg / p.TargetValue))
		if want > desired {
			desired = want
			cause = fmt.Sprintf("policy %s: average %s %.1f%% above target %.0f%%", p.Name, p.Metric, avg, p.TargetValue)
		} else if want < desired && avg < p.TargetValue*0.8 && cause == "" {
			desired = max(want, desired-1) // scale in gently
			cause = fmt.Sprintf("policy %s: average %s %.1f%% below target %.0f%%", p.Name, p.Metric, avg, p.TargetValue)
		}
	}
	desired = min(max(desired, g.MinSize), g.MaxSize)
	if desired == g.DesiredCapacity {
		return g
	}
	old := g.DesiredCapacity
	g, _ = store.Update(s.env.Store, cGroups, g.Name, func(x *Group) error {
		n := core.Now()
		x.DesiredCapacity, x.LastScaling = desired, &n
		return nil
	})
	s.activity(g.Name, fmt.Sprintf("Changing desired capacity from %d to %d", old, desired), cause, "Successful")
	return g
}

// ---- routes ----

func (s *Service) Routes(r *httpx.Router) {
	res := httpx.Res("arn:aws:autoscaling:{region}:{account}:autoScalingGroup:{name}")
	r.Handle("GET /api/v1/autoscaling/groups", "autoscaling:DescribeAutoScalingGroups", s.list)
	r.Handle("POST /api/v1/autoscaling/groups", "autoscaling:CreateAutoScalingGroup", s.create)
	r.Handle("GET /api/v1/autoscaling/groups/{name}", "autoscaling:DescribeAutoScalingGroups", s.get, res)
	r.Handle("PATCH /api/v1/autoscaling/groups/{name}", "autoscaling:UpdateAutoScalingGroup", s.update, res)
	r.Handle("DELETE /api/v1/autoscaling/groups/{name}", "autoscaling:DeleteAutoScalingGroup", s.delete, res)
}

func (s *Service) view(g Group) map[string]any {
	live, _ := s.members(g.Name)
	ids := []map[string]any{}
	for _, i := range live {
		ids = append(ids, map[string]any{"id": i.ID, "state": i.State, "private_ip": i.PrivateIP, "subnet_id": i.SubnetID, "launch_time": i.LaunchTime})
	}
	return map[string]any{"name": g.Name, "arn": g.ARN, "launch": g.Launch, "min_size": g.MinSize, "max_size": g.MaxSize,
		"desired_capacity": g.DesiredCapacity, "subnet_ids": g.SubnetIDs, "target_groups": g.TargetGroups, "policies": g.Policies,
		"health_check_grace_seconds": g.HealthGraceSecs, "suspended": g.Suspended, "status": map[bool]string{true: "Delete in progress", false: "Active"}[g.Deleting],
		"instances": ids, "activities": g.Activities, "created_at": g.CreatedAt, "last_scaling": g.LastScaling}
}

func (s *Service) list(c *httpx.Ctx) (any, error) {
	out := []map[string]any{}
	for _, g := range store.List[Group](s.env.Store, cGroups) {
		v := s.view(g)
		delete(v, "activities")
		out = append(out, v)
	}
	return out, nil
}

func (s *Service) get(c *httpx.Ctx) (any, error) {
	g, err := store.Get[Group](s.env.Store, cGroups, c.Param("name"))
	if err != nil {
		return nil, core.NotFound("auto scaling group", c.Param("name"))
	}
	return s.view(g), nil
}

var nameRe = regexp.MustCompile(`^[\w.-]{1,255}$`)

func validate(g *Group) error {
	if g.MinSize < 0 || g.MaxSize < g.MinSize || g.MaxSize > 50 {
		return core.BadRequest("sizes must satisfy 0 <= min_size <= max_size <= 50")
	}
	if g.DesiredCapacity < g.MinSize || g.DesiredCapacity > g.MaxSize {
		return core.BadRequest("desired_capacity must be between min_size and max_size")
	}
	for i := range g.Policies {
		p := &g.Policies[i]
		if p.Type != "" { // created through the AWS API
			for _, q := range g.Policies[:i] {
				if q.Name == p.Name {
					return core.BadRequest("policy name %q is used twice", p.Name)
				}
			}
			if p.Inert {
				continue
			}
		}
		if p.Metric == "" {
			p.Metric = "CPUUtilization"
		}
		if p.Metric != "CPUUtilization" && p.Metric != "MemoryUtilization" {
			return core.BadRequest("policy metric must be CPUUtilization or MemoryUtilization")
		}
		if p.TargetValue <= 0 || p.TargetValue > 100 {
			return core.BadRequest("policy target_value must be 1-100 (percent)")
		}
		if p.CooldownSeconds == 0 {
			p.CooldownSeconds = 180
		}
		// Generated names track the policy's target; custom names are kept.
		if p.Type == "" && (p.Name == "" || strings.HasPrefix(p.Name, "target-")) {
			p.Name = fmt.Sprintf("target-%s-%.0f", p.Metric, p.TargetValue)
		}
		for _, q := range g.Policies[:i] {
			if q.Name == p.Name {
				return core.BadRequest("policy name %q is used twice", p.Name)
			}
		}
	}
	return nil
}

// authorizeLaunch checks that the caller could launch the group's instances
// themselves; the group acts on their behalf.
type authz interface {
	Authorize(action, resource string) error
}

func (s *Service) authorizeLaunch(c authz, l LaunchConfig, targetGroups []string) error {
	if err := c.Authorize("ec2:RunInstances", "*"); err != nil {
		return err
	}
	if err := s.ec2.PassProfile(c.Authorize, l.IAMInstanceProfile); err != nil {
		return err
	}
	for _, m := range l.FileSystems {
		if err := c.Authorize("elasticfilesystem:ClientMount", s.env.ARN("elasticfilesystem", "file-system/"+m.FileSystemID)); err != nil {
			return err
		}
	}
	for _, tg := range targetGroups {
		if err := c.Authorize("elasticloadbalancing:RegisterTargets", s.env.ARN("elasticloadbalancing", "targetgroup/"+tg)); err != nil {
			return err
		}
	}
	return nil
}

// checkPlacement verifies the launch configuration, subnets, security groups and
// target groups up front, so mistakes fail the request instead of every launch.
func (s *Service) checkPlacement(l LaunchConfig, subnets, targetGroups []string) error {
	vpcID, err := s.ec2.CheckLaunch(ec2.RunInput{ImageID: l.ImageID, InstanceType: l.InstanceType, SecurityGroupIDs: l.SecurityGroupIDs}, subnets)
	if err != nil {
		return err
	}
	for _, tg := range targetGroups {
		v, ok := s.elb.TargetGroupVPC(tg)
		if !ok {
			return core.NotFound("target group", tg)
		}
		if v != vpcID {
			return core.BadRequest("target group %s is in %s, but the group's subnets are in %s", tg, v, vpcID)
		}
	}
	return nil
}

func (s *Service) create(c *httpx.Ctx) (any, error) {
	var g Group
	if err := c.Bind(&g); err != nil {
		return nil, err
	}
	g, err := s.createIn(c, g)
	if err != nil {
		return nil, err
	}
	return s.view(g), nil
}

// resolveTemplate fills the group's launch settings from its launch template.
func (s *Service) resolveTemplate(g *Group) error {
	if g.Template == nil {
		return nil
	}
	if g.Template.Version == "" {
		g.Template.Version = "$Default"
	}
	rt, err := s.ec2.ResolveTemplate(g.Template.ID, g.Template.Name, g.Template.Version)
	if err != nil {
		return err
	}
	g.Template.ID, g.Template.Name = rt.ID, rt.Name
	g.Launch.ImageID, g.Launch.InstanceType, g.Launch.SecurityGroupIDs, g.Launch.UserData = rt.Input.ImageID, rt.Input.InstanceType, rt.Input.SecurityGroupIDs, rt.Input.UserData
	g.Launch.IAMInstanceProfile = rt.Input.IAMInstanceProfile
	return nil
}

func (s *Service) createIn(c authz, g Group) (Group, error) {
	if err := s.resolveTemplate(&g); err != nil {
		return g, err
	}
	if err := s.authorizeLaunch(c, g.Launch, g.TargetGroups); err != nil {
		return g, err
	}
	if !nameRe.MatchString(g.Name) {
		return g, core.BadRequest("group names are 1-255 letters, digits, dots, hyphens or underscores")
	}
	if store.Has(s.env.Store, cGroups, g.Name) {
		return g, core.Errf(http.StatusConflict, "AlreadyExists", "auto scaling group %q already exists", g.Name)
	}
	if g.Launch.ImageID == "" {
		return g, core.BadRequest("launch.image_id is required")
	}
	if g.MaxSize == 0 {
		g.MaxSize = max(g.DesiredCapacity, 1)
	}
	if err := validate(&g); err != nil {
		return g, err
	}
	if err := s.checkPlacement(g.Launch, g.SubnetIDs, g.TargetGroups); err != nil {
		return g, err
	}
	if g.HealthGraceSecs == 0 && g.HealthCheckType == "" { // the AWS API always sets the type and keeps an explicit 0
		g.HealthGraceSecs = 120
	}
	g.ARN = s.env.ARN("autoscaling", "autoScalingGroup:"+g.Name)
	g.CreatedAt, g.Activities, g.Deleting, g.LastScaling = core.Now(), []Activity{}, false, nil
	if g.SubnetIDs == nil {
		g.SubnetIDs = []string{}
	}
	if g.TargetGroups == nil {
		g.TargetGroups = []string{}
	}
	if g.Policies == nil {
		g.Policies = []Policy{}
	}
	if err := store.Put(s.env.Store, cGroups, g.Name, g); err != nil {
		return g, err
	}
	go s.reconcile(g)
	return g, nil
}

type updateInput struct {
	MinSize         *int          `json:"min_size"`
	MaxSize         *int          `json:"max_size"`
	DesiredCapacity *int          `json:"desired_capacity"`
	Launch          *LaunchConfig `json:"launch"`
	Policies        *[]Policy     `json:"policies"`
	Suspended       *bool         `json:"suspended"`
	SubnetIDs       *[]string     `json:"subnet_ids"`
	TargetGroups    *[]string     `json:"target_groups"`
	// AWS API fields.
	Template            *TemplateRef `json:"-"`
	HealthCheckType     *string      `json:"-"`
	HealthGraceSecs     *int         `json:"-"`
	DefaultCooldown     *int         `json:"-"`
	TerminationPolicies *[]string    `json:"-"`
	ProtectNewInstances *bool        `json:"-"`
	Tags                *[]GroupTag  `json:"-"`
	SuspendedProcesses  *[]string    `json:"-"`
}

func (s *Service) update(c *httpx.Ctx) (any, error) {
	var in updateInput
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	g, err := s.updateIn(c, c.Param("name"), in)
	if err != nil {
		return nil, err
	}
	return s.view(g), nil
}

func (s *Service) updateIn(c authz, name string, in updateInput) (Group, error) {
	cur, err := store.Get[Group](s.env.Store, cGroups, name)
	if err != nil {
		return cur, core.NotFound("auto scaling group", name)
	}
	if in.Template != nil {
		probe := cur
		probe.Template = in.Template
		if err := s.resolveTemplate(&probe); err != nil {
			return cur, err
		}
		l := probe.Launch
		in.Template, in.Launch = probe.Template, &l
	}
	launch, subnets, tgs := cur.Launch, cur.SubnetIDs, cur.TargetGroups
	var newTGs []string
	if in.Launch != nil {
		launch = *in.Launch
	}
	if in.SubnetIDs != nil {
		subnets = *in.SubnetIDs
	}
	if in.TargetGroups != nil {
		tgs = *in.TargetGroups
		for _, tg := range tgs {
			if !slices.Contains(cur.TargetGroups, tg) {
				newTGs = append(newTGs, tg)
			}
		}
	}
	if in.Launch != nil || newTGs != nil {
		if err := s.authorizeLaunch(c, launch, newTGs); err != nil {
			return cur, err
		}
	}
	if in.Launch != nil || in.SubnetIDs != nil || in.TargetGroups != nil {
		if err := s.checkPlacement(launch, subnets, tgs); err != nil {
			return cur, err
		}
	}
	var dropped []string
	g, err := store.Update(s.env.Store, cGroups, name, func(g *Group) error {
		if in.MinSize != nil {
			g.MinSize = *in.MinSize
		}
		if in.MaxSize != nil {
			g.MaxSize = *in.MaxSize
		}
		if in.DesiredCapacity != nil {
			g.DesiredCapacity = *in.DesiredCapacity
		} else {
			g.DesiredCapacity = min(max(g.DesiredCapacity, g.MinSize), g.MaxSize)
		}
		if in.Launch != nil {
			g.Launch = *in.Launch // applies to instances launched from now on
		}
		if in.Policies != nil {
			g.Policies = *in.Policies
		}
		if in.Suspended != nil {
			g.Suspended = *in.Suspended
		}
		if in.SubnetIDs != nil {
			g.SubnetIDs = *in.SubnetIDs // existing instances stay where they are
		}
		if in.TargetGroups != nil {
			dropped = nil
			for _, tg := range g.TargetGroups {
				if !slices.Contains(*in.TargetGroups, tg) {
					dropped = append(dropped, tg)
				}
			}
			g.TargetGroups = *in.TargetGroups
		}
		if in.Template != nil {
			g.Template = in.Template
		}
		if in.HealthCheckType != nil {
			g.HealthCheckType = *in.HealthCheckType
		}
		if in.HealthGraceSecs != nil {
			g.HealthGraceSecs = *in.HealthGraceSecs
		}
		if in.DefaultCooldown != nil {
			g.DefaultCooldown = *in.DefaultCooldown
		}
		if in.TerminationPolicies != nil {
			g.TerminationPolicies = *in.TerminationPolicies
		}
		if in.ProtectNewInstances != nil {
			g.ProtectNewInstances = *in.ProtectNewInstances
		}
		if in.Tags != nil {
			g.Tags = *in.Tags
		}
		if in.SuspendedProcesses != nil {
			g.SuspendedProcesses = *in.SuspendedProcesses
		}
		return validate(g)
	})
	if err == store.ErrNotFound {
		return cur, core.NotFound("auto scaling group", name)
	}
	if err != nil {
		return cur, err
	}
	if len(dropped) > 0 {
		live, _ := s.members(g.Name)
		for _, i := range live {
			s.detach(Group{TargetGroups: dropped}, i.ID)
		}
	}
	go s.reconcile(g)
	return g, nil
}

func (s *Service) delete(c *httpx.Ctx) (any, error) {
	g, err := s.deleteIn(c.Param("name"))
	if err != nil {
		return nil, err
	}
	return s.view(g), nil
}

func (s *Service) deleteIn(name string) (Group, error) {
	g, err := store.Update(s.env.Store, cGroups, name, func(g *Group) error { g.Deleting = true; return nil })
	if err == store.ErrNotFound {
		return g, core.NotFound("auto scaling group", name)
	}
	if err != nil {
		return g, err
	}
	go s.reconcile(g)
	return g, nil
}
