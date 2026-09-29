package autoscaling

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/store"
	"github.com/homecloudhq/homecloud/cli/internal/svc/elb"
)

// The AWS EC2 Auto Scaling API (awsQuery, 2011-01-01).

const xmlns = "http://autoscaling.amazonaws.com/doc/2011-01-01/"

type awsOp func(q *awsapi.Req) (any, error)

// RegisterAWS serves Auto Scaling over the AWS protocol.
func (s *Service) RegisterAWS() {
	ops := map[string]awsOp{
		"CreateAutoScalingGroup":              s.awsCreateGroup,
		"UpdateAutoScalingGroup":              s.awsUpdateGroup,
		"DeleteAutoScalingGroup":              s.awsDeleteGroup,
		"DescribeAutoScalingGroups":           s.awsDescribeGroups,
		"SetDesiredCapacity":                  s.awsSetDesiredCapacity,
		"DescribeAutoScalingInstances":        s.awsDescribeInstances,
		"DescribeScalingActivities":           s.awsDescribeActivities,
		"TerminateInstanceInAutoScalingGroup": s.awsTerminateInstance,
		"PutScalingPolicy":                    s.awsPutPolicy,
		"DescribePolicies":                    s.awsDescribePolicies,
		"DeletePolicy":                        s.awsDeletePolicy,
		"ExecutePolicy":                       s.awsExecutePolicy,
		"AttachLoadBalancerTargetGroups":      s.awsAttachTGs,
		"DetachLoadBalancerTargetGroups":      s.awsDetachTGs,
		"DescribeLoadBalancerTargetGroups":    s.awsDescribeTGs,
		"DescribeLoadBalancers":               s.awsDescribeClassicLBs,
		"AttachTrafficSources":                s.awsAttachTrafficSources,
		"DetachTrafficSources":                s.awsDetachTrafficSources,
		"DescribeTrafficSources":              s.awsDescribeTrafficSources,
		"CreateOrUpdateTags":                  s.awsCreateOrUpdateTags,
		"DeleteTags":                          s.awsDeleteTags,
		"DescribeTags":                        s.awsDescribeTags,
		"SuspendProcesses":                    s.awsSuspendProcesses,
		"ResumeProcesses":                     s.awsResumeProcesses,
		"EnableMetricsCollection":             s.awsNoop("EnableMetricsCollection"),
		"DisableMetricsCollection":            s.awsNoop("DisableMetricsCollection"),
		"DescribeLifecycleHooks":              s.awsEmptyList("DescribeLifecycleHooks", "LifecycleHooks"),
		"DescribeWarmPool":                    s.awsEmptyList("DescribeWarmPool", "Instances"),
		"DescribeNotificationConfigurations":  s.awsEmptyList("DescribeNotificationConfigurations", "NotificationConfigurations"),
		"DescribeInstanceRefreshes":           s.awsEmptyList("DescribeInstanceRefreshes", "InstanceRefreshes"),
		"DescribeScheduledActions":            s.awsEmptyList("DescribeScheduledActions", "ScheduledUpdateGroupActions"),
	}
	svc := &awsapi.Service{Name: "autoscaling", XMLNS: xmlns, Ops: map[string]awsapi.Op{}}
	for name, fn := range ops {
		svc.Ops[name] = func(q *awsapi.Req) (any, error) {
			out, err := fn(q)
			if err != nil {
				return nil, asgError(err)
			}
			if out == nil {
				return awsapi.NoResult{}, nil
			}
			return out, nil
		}
	}
	awsapi.Register(svc)
}

func apiErr(code, format string, a ...any) error {
	return awsapi.Errorf(http.StatusBadRequest, code, format, a...)
}

func asgError(err error) error {
	var ae *awsapi.Error
	if errors.As(err, &ae) {
		return ae
	}
	var ce *core.Error
	if !errors.As(err, &ce) {
		return err
	}
	code := ce.Code
	switch code {
	case "AlreadyExists", "Conflict", "ResourceConflict":
		code = "AlreadyExists"
	case "AccessDenied":
		return &awsapi.Error{Status: http.StatusForbidden, Code: "AccessDenied", Message: ce.Message}
	case "ResourceInUse":
	default:
		code = "ValidationError"
	}
	status := ce.Status
	if status < 500 {
		status = http.StatusBadRequest
	}
	return &awsapi.Error{Status: status, Code: code, Message: ce.Message}
}

func uuidOf(seed string) string {
	h := sha256.Sum256([]byte(seed))
	return fmt.Sprintf("%x-%x-%x-%x-%x", h[0:4], h[4:6], h[6:8], h[8:10], h[10:16])
}

func newUUID() string {
	h := core.RandHex(32)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

func (s *Service) groupARN(name string) string {
	return s.env.ARN("autoscaling", "autoScalingGroup:"+uuidOf("asg/"+name)+":autoScalingGroupName/"+name)
}

func (s *Service) policyARN(group, name string) string {
	return s.env.ARN("autoscaling", "scalingPolicy:"+uuidOf("pol/"+group+"/"+name)+":autoScalingGroupName/"+group+":policyName/"+name)
}

func (s *Service) group(name string) (Group, error) {
	g, err := store.Get[Group](s.env.Store, cGroups, name)
	if err != nil {
		return g, apiErr("ValidationError", "AutoScalingGroup name not found - no such group: %s", name)
	}
	return g, nil
}

// TemplateInUse names a group that launches from the launch template, or "".
func (s *Service) TemplateInUse(id string) string {
	for _, g := range store.List[Group](s.env.Store, cGroups) {
		if g.Template != nil && g.Template.ID == id {
			return g.Name
		}
	}
	return ""
}

// ---- views ----

type awsLTSpec struct {
	LaunchTemplateId   string
	LaunchTemplateName string
	Version            string
}

type awsInstance struct {
	InstanceId           string
	InstanceType         string `json:",omitempty"`
	AvailabilityZone     string
	LifecycleState       string
	HealthStatus         string
	LaunchTemplate       *awsLTSpec `json:",omitempty"`
	ProtectedFromScaleIn bool
	AutoScalingGroupName string `json:",omitempty"` // DescribeAutoScalingInstances
}

type awsGTag struct {
	ResourceId        string
	ResourceType      string
	Key               string
	Value             string
	PropagateAtLaunch bool
}

type awsSuspended struct {
	ProcessName      string
	SuspensionReason string
}

type awsGroup struct {
	AutoScalingGroupName             string
	AutoScalingGroupARN              string
	LaunchTemplate                   *awsLTSpec `json:",omitempty"`
	MinSize                          int
	MaxSize                          int
	DesiredCapacity                  int
	DefaultCooldown                  int
	AvailabilityZones                []string
	LoadBalancerNames                []string
	TargetGroupARNs                  []string
	HealthCheckType                  string
	HealthCheckGracePeriod           int
	Instances                        []awsInstance
	CreatedTime                      time.Time
	SuspendedProcesses               []awsSuspended
	VPCZoneIdentifier                string
	EnabledMetrics                   []struct{}
	Status                           string `json:",omitempty"`
	Tags                             []awsGTag
	TerminationPolicies              []string
	NewInstancesProtectedFromScaleIn bool
}

func (s *Service) ltSpec(g Group) *awsLTSpec {
	if g.Template == nil {
		return nil
	}
	return &awsLTSpec{LaunchTemplateId: g.Template.ID, LaunchTemplateName: g.Template.Name, Version: g.Template.Version}
}

func (s *Service) instancesOf(g Group) []awsInstance {
	live, _ := s.members(g.Name)
	out := []awsInstance{}
	for _, i := range live {
		state := "InService"
		if i.State == "pending" {
			state = "Pending"
		}
		out = append(out, awsInstance{InstanceId: i.ID, InstanceType: i.InstanceType, AvailabilityZone: i.AvailabilityZone, LifecycleState: state,
			HealthStatus: "Healthy", LaunchTemplate: s.ltSpec(g), ProtectedFromScaleIn: g.ProtectNewInstances})
	}
	return out
}

func (s *Service) tagsOf(g Group) []awsGTag {
	out := []awsGTag{}
	for _, t := range g.Tags {
		out = append(out, awsGTag{ResourceId: g.Name, ResourceType: "auto-scaling-group", Key: t.Key, Value: t.Value, PropagateAtLaunch: t.PropagateAtLaunch})
	}
	return out
}

func (s *Service) groupXML(g Group) awsGroup {
	azs := []string{}
	for _, sn := range g.SubnetIDs {
		if az := s.ec2.SubnetAZ(sn); az != "" && !slices.Contains(azs, az) {
			azs = append(azs, az)
		}
	}
	tgs := []string{}
	for _, n := range g.TargetGroups {
		tgs = append(tgs, s.elb.TargetGroupARN(n))
	}
	susp := []awsSuspended{}
	for _, p := range g.SuspendedProcesses {
		susp = append(susp, awsSuspended{ProcessName: p, SuspensionReason: "User suspended"})
	}
	x := awsGroup{AutoScalingGroupName: g.Name, AutoScalingGroupARN: s.groupARN(g.Name), LaunchTemplate: s.ltSpec(g), MinSize: g.MinSize, MaxSize: g.MaxSize,
		DesiredCapacity: g.DesiredCapacity, DefaultCooldown: g.DefaultCooldown, AvailabilityZones: azs, LoadBalancerNames: []string{}, TargetGroupARNs: tgs,
		HealthCheckType: g.HealthCheckType, HealthCheckGracePeriod: g.HealthGraceSecs, Instances: s.instancesOf(g), CreatedTime: g.CreatedAt,
		SuspendedProcesses: susp, VPCZoneIdentifier: strings.Join(g.SubnetIDs, ","), EnabledMetrics: []struct{}{}, Tags: s.tagsOf(g),
		TerminationPolicies: g.TerminationPolicies, NewInstancesProtectedFromScaleIn: g.ProtectNewInstances}
	if x.DefaultCooldown == 0 {
		x.DefaultCooldown = 300
	}
	if x.HealthCheckType == "" {
		x.HealthCheckType = "EC2"
	}
	if len(x.TerminationPolicies) == 0 {
		x.TerminationPolicies = []string{"Default"}
	}
	if g.Deleting {
		x.Status = "Delete in progress"
	}
	return x
}

// ---- groups ----

type awsLTIn struct {
	LaunchTemplateId   string
	LaunchTemplateName string
	Version            string
}

func (l *awsLTIn) ref() *TemplateRef {
	if l == nil {
		return nil
	}
	return &TemplateRef{ID: l.LaunchTemplateId, Name: l.LaunchTemplateName, Version: l.Version}
}

func tagsIn(ts []awsGTag) []GroupTag {
	var out []GroupTag
	for _, t := range ts {
		out = append(out, GroupTag{Key: t.Key, Value: t.Value, PropagateAtLaunch: t.PropagateAtLaunch})
	}
	return out
}

func splitZones(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (s *Service) tgNames(arns []string) ([]string, error) {
	out := []string{}
	for _, a := range arns {
		n, ok := elb.TargetGroupName(a)
		if !ok {
			return nil, apiErr("ValidationError", "'%s' is not a valid target group ARN", a)
		}
		if _, found := s.elb.TargetGroupVPC(n); !found {
			return nil, apiErr("ValidationError", "Provided Target Group ARN '%s' does not exist", a)
		}
		out = append(out, n)
	}
	return out, nil
}

func (s *Service) awsCreateGroup(q *awsapi.Req) (any, error) {
	var in struct {
		AutoScalingGroupName             string
		LaunchTemplate                   *awsLTIn
		MinSize, MaxSize                 int
		DesiredCapacity                  *int
		DefaultCooldown                  int
		AvailabilityZones                []string
		TargetGroupARNs                  []string
		HealthCheckType                  string
		HealthCheckGracePeriod           int
		VPCZoneIdentifier                string
		TerminationPolicies              []string
		NewInstancesProtectedFromScaleIn bool
		Tags                             []awsGTag
	}
	if err := q.Decode(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("autoscaling:CreateAutoScalingGroup", s.groupARN(in.AutoScalingGroupName)); err != nil {
		return nil, err
	}
	for k := range q.Form {
		if strings.HasPrefix(k, "MixedInstancesPolicy.") || k == "LaunchConfigurationName" || k == "InstanceId" {
			return nil, apiErr("ValidationError", "HomeCloud groups launch from a launch template; %s is not supported", strings.Split(k, ".")[0])
		}
	}
	if in.LaunchTemplate == nil {
		return nil, apiErr("ValidationError", "Valid requests must contain either LaunchTemplate, LaunchConfigurationName, InstanceId or MixedInstancesPolicy")
	}
	if q.Param("MinSize") == "" || q.Param("MaxSize") == "" {
		return nil, apiErr("ValidationError", "Values for MinSize and MaxSize are required")
	}
	subnets := splitZones(in.VPCZoneIdentifier)
	if len(subnets) == 0 && len(in.AvailabilityZones) > 0 {
		var err error
		if subnets, err = s.ec2.DefaultSubnetIDs(in.AvailabilityZones); err != nil {
			return nil, err
		}
	}
	tgs, err := s.tgNames(in.TargetGroupARNs)
	if err != nil {
		return nil, err
	}
	desired := in.MinSize
	if in.DesiredCapacity != nil {
		desired = *in.DesiredCapacity
	}
	if in.HealthCheckType == "" {
		in.HealthCheckType = "EC2"
	}
	if in.HealthCheckType != "EC2" && in.HealthCheckType != "ELB" {
		return nil, apiErr("ValidationError", "HealthCheckType must be EC2 or ELB")
	}
	g := Group{Name: in.AutoScalingGroupName, Template: in.LaunchTemplate.ref(), MinSize: in.MinSize, MaxSize: in.MaxSize, DesiredCapacity: desired,
		SubnetIDs: subnets, TargetGroups: tgs, HealthGraceSecs: in.HealthCheckGracePeriod, HealthCheckType: in.HealthCheckType,
		DefaultCooldown: in.DefaultCooldown, TerminationPolicies: in.TerminationPolicies, ProtectNewInstances: in.NewInstancesProtectedFromScaleIn,
		Tags: tagsIn(in.Tags)}
	if _, err := s.createIn(q, g); err != nil {
		return nil, err
	}
	return nil, nil
}

func (s *Service) awsUpdateGroup(q *awsapi.Req) (any, error) {
	var in struct {
		AutoScalingGroupName                                                       string
		LaunchTemplate                                                             *awsLTIn
		MinSize, MaxSize, DesiredCapacity, DefaultCooldown, HealthCheckGracePeriod *int
		AvailabilityZones                                                          []string
		HealthCheckType                                                            *string
		VPCZoneIdentifier                                                          *string
		TerminationPolicies                                                        []string
		NewInstancesProtectedFromScaleIn                                           *bool
	}
	if err := q.Decode(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("autoscaling:UpdateAutoScalingGroup", s.groupARN(in.AutoScalingGroupName)); err != nil {
		return nil, err
	}
	for k := range q.Form {
		if strings.HasPrefix(k, "MixedInstancesPolicy.") || k == "LaunchConfigurationName" {
			return nil, apiErr("ValidationError", "HomeCloud groups launch from a launch template; %s is not supported", strings.Split(k, ".")[0])
		}
	}
	if _, err := s.group(in.AutoScalingGroupName); err != nil {
		return nil, err
	}
	u := updateInput{MinSize: in.MinSize, MaxSize: in.MaxSize, DesiredCapacity: in.DesiredCapacity, Template: in.LaunchTemplate.ref(),
		HealthCheckType: in.HealthCheckType, HealthGraceSecs: in.HealthCheckGracePeriod, DefaultCooldown: in.DefaultCooldown,
		ProtectNewInstances: in.NewInstancesProtectedFromScaleIn}
	if len(in.TerminationPolicies) > 0 {
		u.TerminationPolicies = &in.TerminationPolicies
	}
	switch {
	case in.VPCZoneIdentifier != nil:
		sn := splitZones(*in.VPCZoneIdentifier)
		u.SubnetIDs = &sn
	case len(in.AvailabilityZones) > 0:
		sn, err := s.ec2.DefaultSubnetIDs(in.AvailabilityZones)
		if err != nil {
			return nil, err
		}
		u.SubnetIDs = &sn
	}
	if _, err := s.updateIn(q, in.AutoScalingGroupName, u); err != nil {
		return nil, err
	}
	return nil, nil
}

func (s *Service) awsDeleteGroup(q *awsapi.Req) (any, error) {
	name := q.Param("AutoScalingGroupName")
	if err := q.Authorize("autoscaling:DeleteAutoScalingGroup", s.groupARN(name)); err != nil {
		return nil, err
	}
	if _, err := s.group(name); err != nil {
		return nil, err
	}
	live, _ := s.members(name)
	if len(live) > 0 && !q.ParamBool("ForceDelete", false) {
		return nil, apiErr("ScalingActivityInProgress", "You cannot delete an AutoScalingGroup while there are instances or pending Spot instance request(s) still in the group.")
	}
	_, err := s.deleteIn(name)
	return nil, err
}

func (s *Service) awsDescribeGroups(q *awsapi.Req) (any, error) {
	names := q.List("AutoScalingGroupNames")
	if err := q.Authorize("autoscaling:DescribeAutoScalingGroups", "*"); err != nil {
		return nil, err
	}
	all := store.List[Group](s.env.Store, cGroups)
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	out := []awsGroup{}
	for _, g := range all {
		if len(names) == 0 || slices.Contains(names, g.Name) {
			out = append(out, s.groupXML(g))
		}
	}
	return map[string]any{"AutoScalingGroups": out}, nil
}

func (s *Service) awsSetDesiredCapacity(q *awsapi.Req) (any, error) {
	name := q.Param("AutoScalingGroupName")
	if err := q.Authorize("autoscaling:SetDesiredCapacity", s.groupARN(name)); err != nil {
		return nil, err
	}
	g, err := s.group(name)
	if err != nil {
		return nil, err
	}
	d := q.ParamInt("DesiredCapacity", -1)
	if d < 0 {
		return nil, apiErr("ValidationError", "DesiredCapacity is required")
	}
	if d < g.MinSize {
		return nil, apiErr("ValidationError", "New SetDesiredCapacity value %d is below min value %d for the AutoScalingGroup.", d, g.MinSize)
	}
	if d > g.MaxSize {
		return nil, apiErr("ValidationError", "New SetDesiredCapacity value %d is above max value %d for the AutoScalingGroup.", d, g.MaxSize)
	}
	return nil, s.setDesired(g, d, "a user request explicitly set group desired capacity")
}

func (s *Service) setDesired(g Group, d int, cause string) error {
	old := g.DesiredCapacity
	g, err := store.Update(s.env.Store, cGroups, g.Name, func(x *Group) error { x.DesiredCapacity = d; return nil })
	if err != nil {
		return err
	}
	if old != d {
		s.activity(g.Name, fmt.Sprintf("Changing desired capacity from %d to %d", old, d), cause, "Successful")
	}
	go s.reconcile(g)
	return nil
}

func (s *Service) awsDescribeInstances(q *awsapi.Req) (any, error) {
	ids := q.List("InstanceIds")
	if err := q.Authorize("autoscaling:DescribeAutoScalingInstances", "*"); err != nil {
		return nil, err
	}
	out := []awsInstance{}
	for _, g := range store.List[Group](s.env.Store, cGroups) {
		for _, i := range s.instancesOf(g) {
			if len(ids) == 0 || slices.Contains(ids, i.InstanceId) {
				i.AutoScalingGroupName = g.Name
				out = append(out, i)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].InstanceId < out[j].InstanceId })
	return map[string]any{"AutoScalingInstances": out}, nil
}

type awsActivity struct {
	ActivityId           string
	AutoScalingGroupName string
	Description          string
	Cause                string
	StartTime            time.Time
	EndTime              time.Time `json:",omitempty"`
	StatusCode           string
	Progress             int
}

func (s *Service) activityXML(g Group, a Activity) awsActivity {
	id := a.ID
	if id == "" {
		id = uuidOf("act/" + g.Name + a.Time.String() + a.Description)
	}
	code := a.Status
	if code == "" {
		code = "Successful"
	}
	return awsActivity{ActivityId: id, AutoScalingGroupName: g.Name, Description: a.Description, Cause: a.Cause, StartTime: a.Time, EndTime: a.Time,
		StatusCode: code, Progress: 100}
}

func (s *Service) awsDescribeActivities(q *awsapi.Req) (any, error) {
	name, ids := q.Param("AutoScalingGroupName"), q.List("ActivityIds")
	res := "*"
	if name != "" {
		res = s.groupARN(name)
	}
	if err := q.Authorize("autoscaling:DescribeScalingActivities", res); err != nil {
		return nil, err
	}
	var groups []Group
	if name != "" {
		g, err := s.group(name)
		if err != nil {
			return nil, err
		}
		groups = []Group{g}
	} else {
		groups = store.List[Group](s.env.Store, cGroups)
	}
	out := []awsActivity{}
	for _, g := range groups {
		for _, a := range g.Activities {
			x := s.activityXML(g, a)
			if len(ids) == 0 || slices.Contains(ids, x.ActivityId) {
				out = append(out, x)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartTime.After(out[j].StartTime) })
	return map[string]any{"Activities": out}, nil
}

func (s *Service) awsTerminateInstance(q *awsapi.Req) (any, error) {
	id := q.Param("InstanceId")
	var g Group
	for _, x := range store.List[Group](s.env.Store, cGroups) {
		for _, i := range s.instancesOf(x) {
			if i.InstanceId == id {
				g = x
			}
		}
	}
	if g.Name == "" {
		return nil, apiErr("ValidationError", "Instance Id not found - No managed instance found for instance ID: %s", id)
	}
	if err := q.Authorize("autoscaling:TerminateInstanceInAutoScalingGroup", s.groupARN(g.Name)); err != nil {
		return nil, err
	}
	cause := "an instance was taken out of service in response to a user request"
	if q.ParamBool("ShouldDecrementDesiredCapacity", false) {
		if g.DesiredCapacity <= g.MinSize {
			return nil, apiErr("ValidationError", "Cannot decrement the desired capacity below the group's minimum size")
		}
		if err := s.setDesired(g, g.DesiredCapacity-1, cause); err != nil {
			return nil, err
		}
	}
	s.detach(g, id)
	if _, err := s.ec2.Terminate(id); err != nil {
		return nil, err
	}
	s.activity(g.Name, "Terminating EC2 instance: "+id, cause, "Successful")
	g, _ = s.group(g.Name)
	return map[string]any{"Activity": s.activityXML(g, g.Activities[0])}, nil
}

// ---- policies ----

type awsAlarm struct{ AlarmName, AlarmARN string }

type awsPolicy struct {
	AutoScalingGroupName        string
	PolicyName                  string
	PolicyARN                   string
	PolicyType                  string
	AdjustmentType              string           `json:",omitempty"`
	MinAdjustmentStep           int              `json:",omitempty"`
	MinAdjustmentMagnitude      int              `json:",omitempty"`
	ScalingAdjustment           *int             `json:",omitempty"`
	Cooldown                    *int             `json:",omitempty"`
	StepAdjustments             []StepAdjustment `json:",omitempty"`
	MetricAggregationType       string           `json:",omitempty"`
	EstimatedInstanceWarmup     int              `json:",omitempty"`
	Alarms                      []awsAlarm
	TargetTrackingConfiguration *awsTT `json:",omitempty"`
	Enabled                     bool
}

type awsTT struct {
	PredefinedMetricSpecification *struct {
		PredefinedMetricType string
		ResourceLabel        string `json:",omitempty"`
	} `json:",omitempty"`
	TargetValue    float64
	DisableScaleIn bool
}

func (s *Service) policyXML(g Group, p Policy) awsPolicy {
	x := awsPolicy{AutoScalingGroupName: g.Name, PolicyName: p.Name, PolicyARN: s.policyARN(g.Name, p.Name), PolicyType: p.Type, Alarms: []awsAlarm{},
		Enabled: !p.Disabled}
	if p.Type == "" { // created through the native API
		x.PolicyType = "TargetTrackingScaling"
		p.PredefinedMetric = map[string]string{"CPUUtilization": "ASGAverageCPUUtilization"}[p.Metric]
	}
	switch x.PolicyType {
	case "TargetTrackingScaling":
		tt := &awsTT{TargetValue: p.TargetValue, DisableScaleIn: p.DisableScaleIn}
		tt.PredefinedMetricSpecification = &struct {
			PredefinedMetricType string
			ResourceLabel        string `json:",omitempty"`
		}{p.PredefinedMetric, p.ResourceLabel}
		x.TargetTrackingConfiguration, x.EstimatedInstanceWarmup = tt, p.Warmup
	default:
		x.AdjustmentType, x.MinAdjustmentMagnitude, x.MinAdjustmentStep = p.AdjustmentType, p.MinAdjustment, p.MinAdjustment
		if x.PolicyType == "SimpleScaling" {
			sa := p.ScalingAdjust
			x.ScalingAdjustment, x.Cooldown = &sa, p.Cooldown
		} else {
			x.StepAdjustments, x.MetricAggregationType, x.EstimatedInstanceWarmup = p.Steps, p.MetricAggregate, p.Warmup
		}
	}
	return x
}

func (s *Service) awsPutPolicy(q *awsapi.Req) (any, error) {
	var in struct {
		AutoScalingGroupName        string
		PolicyName                  string
		PolicyType                  string
		AdjustmentType              string
		MinAdjustmentStep           int
		MinAdjustmentMagnitude      int
		ScalingAdjustment           *int
		Cooldown                    *int
		MetricAggregationType       string
		StepAdjustments             []StepAdjustment
		EstimatedInstanceWarmup     int
		Enabled                     *bool
		TargetTrackingConfiguration *struct {
			PredefinedMetricSpecification *struct{ PredefinedMetricType, ResourceLabel string }
			TargetValue                   float64
			DisableScaleIn                bool
		}
	}
	if err := q.Decode(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("autoscaling:PutScalingPolicy", s.groupARN(in.AutoScalingGroupName)); err != nil {
		return nil, err
	}
	g, err := s.group(in.AutoScalingGroupName)
	if err != nil {
		return nil, err
	}
	if in.PolicyName == "" {
		return nil, apiErr("ValidationError", "PolicyName is required")
	}
	if in.PolicyType == "" {
		in.PolicyType = "SimpleScaling"
	}
	p := Policy{Name: in.PolicyName, Type: in.PolicyType, Warmup: in.EstimatedInstanceWarmup, Disabled: in.Enabled != nil && !*in.Enabled}
	switch in.PolicyType {
	case "TargetTrackingScaling":
		tt := in.TargetTrackingConfiguration
		if tt == nil || tt.TargetValue <= 0 {
			return nil, apiErr("ValidationError", "TargetTrackingConfiguration with a TargetValue is required")
		}
		p.TargetValue, p.DisableScaleIn = tt.TargetValue, tt.DisableScaleIn
		p.CooldownSeconds = in.EstimatedInstanceWarmup
		if m := tt.PredefinedMetricSpecification; m != nil {
			p.PredefinedMetric, p.ResourceLabel = m.PredefinedMetricType, m.ResourceLabel
		}
		switch p.PredefinedMetric {
		case "ASGAverageCPUUtilization":
			p.Metric = "CPUUtilization"
		default:
			p.Inert = true // stored and reported, but the group does not act on it
		}
		if p.TargetValue > 100 {
			p.Inert = true
		}
	case "SimpleScaling", "StepScaling":
		if in.AdjustmentType == "" {
			return nil, apiErr("ValidationError", "AdjustmentType is required")
		}
		p.Inert, p.AdjustmentType, p.MinAdjustment = true, in.AdjustmentType, max(in.MinAdjustmentMagnitude, in.MinAdjustmentStep)
		if in.PolicyType == "SimpleScaling" {
			if in.ScalingAdjustment == nil {
				return nil, apiErr("ValidationError", "ScalingAdjustment is required for SimpleScaling policies")
			}
			p.ScalingAdjust, p.Cooldown = *in.ScalingAdjustment, in.Cooldown
		} else {
			p.Steps, p.MetricAggregate = in.StepAdjustments, in.MetricAggregationType
		}
	default:
		return nil, apiErr("ValidationError", "PolicyType %s is not supported", in.PolicyType)
	}
	pols := slices.Clone(g.Policies)
	if i := slices.IndexFunc(pols, func(x Policy) bool { return x.Name == p.Name }); i >= 0 {
		pols[i] = p
	} else {
		pols = append(pols, p)
	}
	if _, err := s.updateIn(q, g.Name, updateInput{Policies: &pols}); err != nil {
		return nil, err
	}
	return map[string]any{"PolicyARN": s.policyARN(g.Name, p.Name), "Alarms": []awsAlarm{}}, nil
}

func (s *Service) awsDescribePolicies(q *awsapi.Req) (any, error) {
	name, names, types := q.Param("AutoScalingGroupName"), q.List("PolicyNames"), q.List("PolicyTypes")
	res := "*"
	if name != "" {
		res = s.groupARN(name)
	}
	if err := q.Authorize("autoscaling:DescribePolicies", res); err != nil {
		return nil, err
	}
	var groups []Group
	if name != "" {
		g, err := s.group(name)
		if err != nil {
			return nil, err
		}
		groups = []Group{g}
	} else {
		groups = store.List[Group](s.env.Store, cGroups)
	}
	out := []awsPolicy{}
	for _, g := range groups {
		for _, p := range g.Policies {
			x := s.policyXML(g, p)
			if (len(names) == 0 || slices.Contains(names, p.Name) || slices.Contains(names, x.PolicyARN)) && (len(types) == 0 || slices.Contains(types, x.PolicyType)) {
				out = append(out, x)
			}
		}
	}
	return map[string]any{"ScalingPolicies": out}, nil
}

func (s *Service) awsDeletePolicy(q *awsapi.Req) (any, error) {
	name, pol := q.Param("AutoScalingGroupName"), q.Param("PolicyName")
	if strings.HasPrefix(pol, "arn:") { // ...:autoScalingGroupName/<group>:policyName/<name>
		if _, r, ok := strings.Cut(pol, ":autoScalingGroupName/"); ok {
			name, pol, _ = strings.Cut(r, ":policyName/")
		}
	}
	if err := q.Authorize("autoscaling:DeletePolicy", s.groupARN(name)); err != nil {
		return nil, err
	}
	g, err := s.group(name)
	if err != nil {
		return nil, err
	}
	pols := slices.DeleteFunc(slices.Clone(g.Policies), func(p Policy) bool { return p.Name == pol })
	if len(pols) == len(g.Policies) {
		return nil, apiErr("ValidationError", "No policy name found for %s in group %s", pol, name)
	}
	if pols == nil {
		pols = []Policy{}
	}
	_, err = s.updateIn(q, name, updateInput{Policies: &pols})
	return nil, err
}

func (s *Service) awsExecutePolicy(q *awsapi.Req) (any, error) {
	name, pol := q.Param("AutoScalingGroupName"), q.Param("PolicyName")
	if err := q.Authorize("autoscaling:ExecutePolicy", s.groupARN(name)); err != nil {
		return nil, err
	}
	g, err := s.group(name)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(g.Policies, func(p Policy) bool { return p.Name == pol })
	if i < 0 {
		return nil, apiErr("ValidationError", "No policy name found for %s in group %s", pol, name)
	}
	p := g.Policies[i]
	if p.Type != "SimpleScaling" {
		return nil, apiErr("ValidationError", "Only SimpleScaling policies can be executed")
	}
	d := g.DesiredCapacity
	switch p.AdjustmentType {
	case "ChangeInCapacity":
		d += p.ScalingAdjust
	case "ExactCapacity":
		d = p.ScalingAdjust
	case "PercentChangeInCapacity":
		delta := d * p.ScalingAdjust / 100
		if delta == 0 && p.ScalingAdjust != 0 {
			delta = max(-1, min(1, p.ScalingAdjust))
		}
		if a := p.MinAdjustment; a > 0 && delta > -a && delta < a {
			delta = a
			if p.ScalingAdjust < 0 {
				delta = -a
			}
		}
		d += delta
	}
	return nil, s.setDesired(g, min(max(d, g.MinSize), g.MaxSize), "a scaling policy was executed: "+p.Name)
}

// ---- load balancer target groups ----

func (s *Service) awsAttachTGs(q *awsapi.Req) (any, error) {
	return map[string]any{}, s.changeTGs(q, "autoscaling:AttachLoadBalancerTargetGroups", q.List("TargetGroupARNs"), true)
}

func (s *Service) awsDetachTGs(q *awsapi.Req) (any, error) {
	return map[string]any{}, s.changeTGs(q, "autoscaling:DetachLoadBalancerTargetGroups", q.List("TargetGroupARNs"), false)
}

func (s *Service) changeTGs(q *awsapi.Req, action string, arns []string, attach bool) error {
	name := q.Param("AutoScalingGroupName")
	if err := q.Authorize(action, s.groupARN(name)); err != nil {
		return err
	}
	g, err := s.group(name)
	if err != nil {
		return err
	}
	names, err := s.tgNames(arns)
	if err != nil {
		return err
	}
	cur := slices.Clone(g.TargetGroups)
	for _, n := range names {
		if attach && !slices.Contains(cur, n) {
			cur = append(cur, n)
		}
		if !attach {
			cur = slices.DeleteFunc(cur, func(x string) bool { return x == n })
		}
	}
	_, err = s.updateIn(q, name, updateInput{TargetGroups: &cur})
	return err
}

func (s *Service) awsDescribeTGs(q *awsapi.Req) (any, error) {
	name := q.Param("AutoScalingGroupName")
	if err := q.Authorize("autoscaling:DescribeLoadBalancerTargetGroups", s.groupARN(name)); err != nil {
		return nil, err
	}
	g, err := s.group(name)
	if err != nil {
		return nil, err
	}
	type tg struct{ LoadBalancerTargetGroupARN, State string }
	out := []tg{}
	for _, n := range g.TargetGroups {
		out = append(out, tg{s.elb.TargetGroupARN(n), "InService"})
	}
	return map[string]any{"LoadBalancerTargetGroups": out}, nil
}

func (s *Service) awsDescribeClassicLBs(q *awsapi.Req) (any, error) {
	name := q.Param("AutoScalingGroupName")
	if err := q.Authorize("autoscaling:DescribeLoadBalancers", s.groupARN(name)); err != nil {
		return nil, err
	}
	if _, err := s.group(name); err != nil {
		return nil, err
	}
	return map[string]any{"LoadBalancers": []struct{}{}}, nil
}

type awsTrafficSource struct{ Identifier, Type string }

func (s *Service) trafficArns(q *awsapi.Req) ([]string, error) {
	var arns []string
	for _, t := range q.Structs("TrafficSources") {
		if t["Type"] != "" && t["Type"] != "elbv2" {
			return nil, apiErr("ValidationError", "Traffic source type %s is not supported (elbv2)", t["Type"])
		}
		arns = append(arns, t["Identifier"])
	}
	return arns, nil
}

func (s *Service) awsAttachTrafficSources(q *awsapi.Req) (any, error) {
	arns, err := s.trafficArns(q)
	if err != nil {
		return nil, err
	}
	return map[string]any{}, s.changeTGs(q, "autoscaling:AttachTrafficSources", arns, true)
}

func (s *Service) awsDetachTrafficSources(q *awsapi.Req) (any, error) {
	arns, err := s.trafficArns(q)
	if err != nil {
		return nil, err
	}
	return map[string]any{}, s.changeTGs(q, "autoscaling:DetachTrafficSources", arns, false)
}

func (s *Service) awsDescribeTrafficSources(q *awsapi.Req) (any, error) {
	name := q.Param("AutoScalingGroupName")
	if err := q.Authorize("autoscaling:DescribeTrafficSources", s.groupARN(name)); err != nil {
		return nil, err
	}
	g, err := s.group(name)
	if err != nil {
		return nil, err
	}
	type ts struct{ TrafficSource, Identifier, Type, State string }
	out := []ts{}
	for _, n := range g.TargetGroups {
		a := s.elb.TargetGroupARN(n)
		out = append(out, ts{a, a, "elbv2", "InService"})
	}
	return map[string]any{"TrafficSources": out}, nil
}

// ---- tags ----

func (s *Service) awsCreateOrUpdateTags(q *awsapi.Req) (any, error) {
	var in struct{ Tags []awsGTag }
	if err := q.Decode(&in); err != nil {
		return nil, err
	}
	byGroup := map[string][]awsGTag{}
	for _, t := range in.Tags {
		if t.ResourceType != "" && t.ResourceType != "auto-scaling-group" {
			return nil, apiErr("ValidationError", "ResourceType must be auto-scaling-group")
		}
		byGroup[t.ResourceId] = append(byGroup[t.ResourceId], t)
	}
	for name, ts := range byGroup {
		if err := q.Authorize("autoscaling:CreateOrUpdateTags", s.groupARN(name)); err != nil {
			return nil, err
		}
		g, err := s.group(name)
		if err != nil {
			return nil, err
		}
		cur := slices.Clone(g.Tags)
		for _, t := range tagsIn(ts) {
			if i := slices.IndexFunc(cur, func(x GroupTag) bool { return x.Key == t.Key }); i >= 0 {
				cur[i] = t
			} else {
				cur = append(cur, t)
			}
		}
		if _, err := s.updateIn(q, name, updateInput{Tags: &cur}); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

func (s *Service) awsDeleteTags(q *awsapi.Req) (any, error) {
	var in struct{ Tags []awsGTag }
	if err := q.Decode(&in); err != nil {
		return nil, err
	}
	for _, t := range in.Tags {
		if err := q.Authorize("autoscaling:DeleteTags", s.groupARN(t.ResourceId)); err != nil {
			return nil, err
		}
		g, err := s.group(t.ResourceId)
		if err != nil {
			return nil, err
		}
		cur := slices.DeleteFunc(slices.Clone(g.Tags), func(x GroupTag) bool { return x.Key == t.Key })
		if cur == nil {
			cur = []GroupTag{}
		}
		if _, err := s.updateIn(q, t.ResourceId, updateInput{Tags: &cur}); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

func (s *Service) awsDescribeTags(q *awsapi.Req) (any, error) {
	if err := q.Authorize("autoscaling:DescribeTags", "*"); err != nil {
		return nil, err
	}
	filters := map[string][]string{}
	for _, f := range q.Structs("Filters") {
		var vals []string
		for i := 1; ; i++ {
			v, ok := f[fmt.Sprintf("Values.member.%d", i)]
			if !ok {
				break
			}
			vals = append(vals, v)
		}
		filters[f["Name"]] = vals
	}
	all := store.List[Group](s.env.Store, cGroups)
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	out := []awsGTag{}
	for _, g := range all {
		for _, t := range s.tagsOf(g) {
			ok := true
			for name, vals := range filters {
				var v string
				switch name {
				case "auto-scaling-group":
					v = g.Name
				case "key":
					v = t.Key
				case "value":
					v = t.Value
				case "propagate-at-launch":
					v = fmt.Sprint(t.PropagateAtLaunch)
				default:
					continue
				}
				ok = ok && slices.Contains(vals, v)
			}
			if ok {
				out = append(out, t)
			}
		}
	}
	return map[string]any{"Tags": out}, nil
}

// ---- processes and empty listings ----

var processes = []string{"Launch", "Terminate", "HealthCheck", "ReplaceUnhealthy", "AZRebalance", "AlarmNotification", "ScheduledActions", "AddToLoadBalancer", "InstanceRefresh"}

func (s *Service) changeProcesses(q *awsapi.Req, action string, suspend bool) error {
	name := q.Param("AutoScalingGroupName")
	if err := q.Authorize(action, s.groupARN(name)); err != nil {
		return err
	}
	g, err := s.group(name)
	if err != nil {
		return err
	}
	req := q.List("ScalingProcesses")
	if len(req) == 0 {
		req = processes
	}
	cur := slices.Clone(g.SuspendedProcesses)
	for _, p := range req {
		if !slices.Contains(processes, p) {
			return apiErr("ValidationError", "'%s' is not a valid scaling process", p)
		}
		if suspend && !slices.Contains(cur, p) {
			cur = append(cur, p)
		}
		if !suspend {
			cur = slices.DeleteFunc(cur, func(x string) bool { return x == p })
		}
	}
	if cur == nil {
		cur = []string{}
	}
	_, err = s.updateIn(q, name, updateInput{SuspendedProcesses: &cur})
	return err
}

func (s *Service) awsSuspendProcesses(q *awsapi.Req) (any, error) {
	return nil, s.changeProcesses(q, "autoscaling:SuspendProcesses", true)
}

func (s *Service) awsResumeProcesses(q *awsapi.Req) (any, error) {
	return nil, s.changeProcesses(q, "autoscaling:ResumeProcesses", false)
}

func (s *Service) awsNoop(action string) awsOp {
	return func(q *awsapi.Req) (any, error) {
		name := q.Param("AutoScalingGroupName")
		if err := q.Authorize("autoscaling:"+action, s.groupARN(name)); err != nil {
			return nil, err
		}
		_, err := s.group(name)
		return nil, err
	}
}

// awsEmptyList serves listings of features HomeCloud does not have (lifecycle
// hooks, warm pools, notifications, refreshes, scheduled actions).
func (s *Service) awsEmptyList(action, field string) awsOp {
	return func(q *awsapi.Req) (any, error) {
		name := q.Param("AutoScalingGroupName")
		res := "*"
		if name != "" {
			res = s.groupARN(name)
		}
		if err := q.Authorize("autoscaling:"+action, res); err != nil {
			return nil, err
		}
		if name != "" {
			if _, err := s.group(name); err != nil {
				return nil, err
			}
		}
		return map[string]any{field: []struct{}{}}, nil
	}
}
