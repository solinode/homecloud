// Package appautoscaling implements the Application Auto Scaling API
// (application-autoscaling, awsJson 1.1, target prefix AnyScaleFrontendService)
// as records: scalable targets, scaling policies and scheduled actions are
// stored, described, tagged and deleted as in AWS, so tools that attach
// autoscaling to an ECS service or a DynamoDB table (Terraform's
// aws_appautoscaling_* resources, the terraform-aws-modules ECS module) work.
// HomeCloud does not act on them: capacity stays what the service is set to.
package appautoscaling

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/store"
	"github.com/homecloudhq/homecloud/cli/internal/svc"
)

const (
	cTargets   = "appautoscaling_targets"
	cPolicies  = "appautoscaling_policies"
	cScheduled = "appautoscaling_scheduled_actions"
)

type Target struct {
	ARN               string          `json:"arn"`
	ServiceNamespace  string          `json:"service_namespace"`
	ResourceID        string          `json:"resource_id"`
	ScalableDimension string          `json:"scalable_dimension"`
	MinCapacity       int64           `json:"min_capacity"`
	MaxCapacity       int64           `json:"max_capacity"`
	RoleARN           string          `json:"role_arn,omitempty"`
	SuspendedState    json.RawMessage `json:"suspended_state,omitempty"`
	CreatedAt         time.Time       `json:"created_at"`
	Tags              core.Tags       `json:"tags,omitempty"`
}

type Policy struct {
	ARN               string          `json:"arn"`
	Name              string          `json:"name"`
	ServiceNamespace  string          `json:"service_namespace"`
	ResourceID        string          `json:"resource_id"`
	ScalableDimension string          `json:"scalable_dimension"`
	PolicyType        string          `json:"policy_type"`
	StepScaling       json.RawMessage `json:"step_scaling,omitempty"`
	TargetTracking    json.RawMessage `json:"target_tracking,omitempty"`
	PredictiveScaling json.RawMessage `json:"predictive_scaling,omitempty"`
	CreatedAt         time.Time       `json:"created_at"`
}

type ScheduledAction struct {
	ARN               string          `json:"arn"`
	Name              string          `json:"name"`
	ServiceNamespace  string          `json:"service_namespace"`
	ResourceID        string          `json:"resource_id"`
	ScalableDimension string          `json:"scalable_dimension"`
	Schedule          string          `json:"schedule"`
	Timezone          string          `json:"timezone,omitempty"`
	StartTime         *awsapi.Time    `json:"start_time,omitempty"`
	EndTime           *awsapi.Time    `json:"end_time,omitempty"`
	ScalableTarget    json.RawMessage `json:"scalable_target_action,omitempty"`
	CreatedAt         time.Time       `json:"created_at"`
}

type Service struct {
	env *svc.Env
	mu  sync.Mutex
}

func New(env *svc.Env) *Service { return &Service{env: env} }

func key(ns, resource, dim string) string { return ns + "|" + resource + "|" + dim }

var namespaces = []string{"ecs", "elasticmapreduce", "ec2", "appstream", "dynamodb", "rds", "sagemaker",
	"custom-resource", "comprehend", "lambda", "cassandra", "kafka", "elasticache", "neptune", "workspaces"}

func validation(format string, a ...any) error {
	return awsapi.Errorf(http.StatusBadRequest, "ValidationException", format, a...)
}

func notFound(format string, a ...any) error {
	return awsapi.Errorf(http.StatusBadRequest, "ObjectNotFoundException", format, a...)
}

func checkTarget(ns, resource, dim string) error {
	if !slices.Contains(namespaces, ns) {
		return validation("1 validation error detected: Value '%s' at 'serviceNamespace' failed to satisfy constraint: Member must satisfy enum value set: [%s]", ns, strings.Join(namespaces, ", "))
	}
	if resource == "" {
		return validation("ResourceId is required")
	}
	if dim == "" || !strings.HasPrefix(dim, ns+":") && !(ns == "custom-resource" && strings.HasPrefix(dim, "custom-resource:")) {
		return validation("Unsupported scalable dimension '%s' for service namespace %s", dim, ns)
	}
	if ns == "ecs" && (!strings.HasPrefix(resource, "service/") || dim != "ecs:service:DesiredCount") {
		return validation("Unsupported resource '%s' with scalable dimension '%s' for service namespace ecs: use service/<cluster>/<service> and ecs:service:DesiredCount", resource, dim)
	}
	return nil
}

// RegisterAWS serves the API over awsJson 1.1.
func (s *Service) RegisterAWS() {
	awsapi.Register(&awsapi.Service{
		Name: "application-autoscaling", JSONPrefix: "AnyScaleFrontendService", JSONVersion: "1.1",
		Ops: map[string]awsapi.Op{
			"RegisterScalableTarget":    s.awsRegister,
			"DescribeScalableTargets":   s.awsDescribeTargets,
			"DeregisterScalableTarget":  s.awsDeregister,
			"PutScalingPolicy":          s.awsPutPolicy,
			"DescribeScalingPolicies":   s.awsDescribePolicies,
			"DeleteScalingPolicy":       s.awsDeletePolicy,
			"PutScheduledAction":        s.awsPutScheduled,
			"DescribeScheduledActions":  s.awsDescribeScheduled,
			"DeleteScheduledAction":     s.awsDeleteScheduled,
			"DescribeScalingActivities": s.awsDescribeActivities,
			"TagResource":               s.awsTag,
			"UntagResource":             s.awsUntag,
			"ListTagsForResource":       s.awsListTags,
		},
	})
}

type targetRef struct {
	ServiceNamespace  string
	ResourceId        string
	ScalableDimension string
}

func (s *Service) awsRegister(q *awsapi.Req) (any, error) {
	var in struct {
		targetRef
		MinCapacity    *int64
		MaxCapacity    *int64
		RoleARN        string
		SuspendedState json.RawMessage
		Tags           map[string]string
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := checkTarget(in.ServiceNamespace, in.ResourceId, in.ScalableDimension); err != nil {
		return nil, err
	}
	k := key(in.ServiceNamespace, in.ResourceId, in.ScalableDimension)
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := store.Get[Target](s.env.Store, cTargets, k)
	exists := err == nil
	arn := t.ARN
	if !exists {
		arn = s.env.ARN("application-autoscaling", "scalable-target/"+strings.ToLower(core.RandHex(16)))
	}
	if err := q.Authorize("application-autoscaling:RegisterScalableTarget", arn); err != nil {
		return nil, err
	}
	if !exists {
		if in.MinCapacity == nil || in.MaxCapacity == nil {
			return nil, validation("MinCapacity and MaxCapacity are required for a new scalable target")
		}
		t = Target{ARN: arn, ServiceNamespace: in.ServiceNamespace, ResourceID: in.ResourceId, ScalableDimension: in.ScalableDimension,
			CreatedAt: core.Now(), Tags: in.Tags}
	}
	if in.MinCapacity != nil {
		t.MinCapacity = *in.MinCapacity
	}
	if in.MaxCapacity != nil {
		t.MaxCapacity = *in.MaxCapacity
	}
	if t.MinCapacity < 0 || t.MaxCapacity < t.MinCapacity {
		return nil, validation("Maximum capacity cannot be less than minimum capacity")
	}
	if in.RoleARN != "" {
		t.RoleARN = in.RoleARN
	}
	if t.RoleARN == "" {
		t.RoleARN = s.env.ARN("iam", "role/aws-service-role/"+in.ServiceNamespace+".application-autoscaling.amazonaws.com/AWSServiceRoleForApplicationAutoScaling_"+serviceRoleSuffix(in.ServiceNamespace))
	}
	if len(in.SuspendedState) > 0 && string(in.SuspendedState) != "null" {
		t.SuspendedState = in.SuspendedState
	}
	if err := store.Put(s.env.Store, cTargets, k, t); err != nil {
		return nil, err
	}
	return map[string]any{"ScalableTargetARN": t.ARN}, nil
}

func serviceRoleSuffix(ns string) string {
	switch ns {
	case "ecs":
		return "ECSService"
	case "dynamodb":
		return "DynamoDBTable"
	case "rds":
		return "RDSCluster"
	case "lambda":
		return "LambdaConcurrency"
	}
	return "CustomResource"
}

func (s *Service) targetOut(t Target) map[string]any {
	m := map[string]any{"ServiceNamespace": t.ServiceNamespace, "ResourceId": t.ResourceID, "ScalableDimension": t.ScalableDimension,
		"MinCapacity": t.MinCapacity, "MaxCapacity": t.MaxCapacity, "RoleARN": t.RoleARN, "CreationTime": awsapi.Epoch(t.CreatedAt),
		"ScalableTargetARN": t.ARN}
	ss := json.RawMessage(`{"DynamicScalingInSuspended":false,"DynamicScalingOutSuspended":false,"ScheduledScalingSuspended":false}`)
	if len(t.SuspendedState) > 0 {
		ss = t.SuspendedState
	}
	m["SuspendedState"] = ss
	return m
}

// page applies MaxResults/NextToken (an index) to a sorted list.
func page[T any](items []T, max int, next string) ([]T, string) {
	start := 0
	if next != "" {
		for i := range items {
			if pageKey(items[i]) == next {
				start = i + 1
			}
		}
	}
	items = items[start:]
	if max > 0 && len(items) > max {
		return items[:max], pageKey(items[max-1])
	}
	return items, ""
}

func pageKey(v any) string {
	switch x := v.(type) {
	case Target:
		return x.ARN
	case Policy:
		return x.ARN
	case ScheduledAction:
		return x.ARN
	}
	return ""
}

func (s *Service) awsDescribeTargets(q *awsapi.Req) (any, error) {
	var in struct {
		ServiceNamespace  string
		ResourceIds       []string
		ScalableDimension string
		MaxResults        int
		NextToken         string
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("application-autoscaling:DescribeScalableTargets", "*"); err != nil {
		return nil, err
	}
	if !slices.Contains(namespaces, in.ServiceNamespace) {
		return nil, validation("ServiceNamespace '%s' is not valid", in.ServiceNamespace)
	}
	var all []Target
	for _, t := range store.List[Target](s.env.Store, cTargets) {
		if t.ServiceNamespace != in.ServiceNamespace || (len(in.ResourceIds) > 0 && !slices.Contains(in.ResourceIds, t.ResourceID)) ||
			(in.ScalableDimension != "" && in.ScalableDimension != t.ScalableDimension) {
			continue
		}
		all = append(all, t)
	}
	slices.SortFunc(all, func(a, b Target) int { return strings.Compare(a.ARN, b.ARN) })
	items, next := page(all, in.MaxResults, in.NextToken)
	out := []map[string]any{}
	for _, t := range items {
		out = append(out, s.targetOut(t))
	}
	res := map[string]any{"ScalableTargets": out}
	if next != "" {
		res["NextToken"] = next
	}
	return res, nil
}

func (s *Service) awsDeregister(q *awsapi.Req) (any, error) {
	var in targetRef
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	k := key(in.ServiceNamespace, in.ResourceId, in.ScalableDimension)
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := store.Get[Target](s.env.Store, cTargets, k)
	if err != nil {
		if err := q.Authorize("application-autoscaling:DeregisterScalableTarget", "*"); err != nil {
			return nil, err
		}
		return nil, notFound("No scalable target registered for service namespace: %s, resource ID: %s, scalable dimension: %s", in.ServiceNamespace, in.ResourceId, in.ScalableDimension)
	}
	if err := q.Authorize("application-autoscaling:DeregisterScalableTarget", t.ARN); err != nil {
		return nil, err
	}
	// Deregistering a target deletes its policies and scheduled actions, as in AWS.
	for _, p := range store.List[Policy](s.env.Store, cPolicies) {
		if key(p.ServiceNamespace, p.ResourceID, p.ScalableDimension) == k {
			_ = store.Delete(s.env.Store, cPolicies, p.ARN)
		}
	}
	for _, a := range store.List[ScheduledAction](s.env.Store, cScheduled) {
		if key(a.ServiceNamespace, a.ResourceID, a.ScalableDimension) == k {
			_ = store.Delete(s.env.Store, cScheduled, a.ARN)
		}
	}
	return nil, store.Delete(s.env.Store, cTargets, k)
}

func (s *Service) requireTarget(ref targetRef) error {
	if !store.Has(s.env.Store, cTargets, key(ref.ServiceNamespace, ref.ResourceId, ref.ScalableDimension)) {
		return notFound("No scalable target registered for service namespace: %s, resource ID: %s, scalable dimension: %s", ref.ServiceNamespace, ref.ResourceId, ref.ScalableDimension)
	}
	return nil
}

func (s *Service) findPolicy(ref targetRef, name string) (Policy, bool) {
	for _, p := range store.List[Policy](s.env.Store, cPolicies) {
		if p.Name == name && key(p.ServiceNamespace, p.ResourceID, p.ScalableDimension) == key(ref.ServiceNamespace, ref.ResourceId, ref.ScalableDimension) {
			return p, true
		}
	}
	return Policy{}, false
}

func raw(m json.RawMessage) json.RawMessage {
	if len(m) == 0 || string(m) == "null" {
		return nil
	}
	return m
}

func (s *Service) awsPutPolicy(q *awsapi.Req) (any, error) {
	var in struct {
		targetRef
		PolicyName                               string
		PolicyType                               string
		StepScalingPolicyConfiguration           json.RawMessage
		TargetTrackingScalingPolicyConfiguration json.RawMessage
		PredictiveScalingPolicyConfiguration     json.RawMessage
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if in.PolicyName == "" {
		return nil, validation("PolicyName is required")
	}
	if err := checkTarget(in.ServiceNamespace, in.ResourceId, in.ScalableDimension); err != nil {
		return nil, err
	}
	if in.PolicyType == "" {
		in.PolicyType = "StepScaling"
	}
	switch {
	case in.PolicyType == "StepScaling" && raw(in.StepScalingPolicyConfiguration) == nil,
		in.PolicyType == "TargetTrackingScaling" && raw(in.TargetTrackingScalingPolicyConfiguration) == nil,
		in.PolicyType == "PredictiveScaling" && raw(in.PredictiveScalingPolicyConfiguration) == nil:
		return nil, validation("A %s policy needs its configuration", in.PolicyType)
	case !slices.Contains([]string{"StepScaling", "TargetTrackingScaling", "PredictiveScaling"}, in.PolicyType):
		return nil, validation("Unsupported policy type '%s'", in.PolicyType)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireTarget(in.targetRef); err != nil {
		return nil, err
	}
	p, ok := s.findPolicy(in.targetRef, in.PolicyName)
	if !ok {
		p = Policy{ARN: s.env.ARN("autoscaling", "scalingPolicy:"+newUUID()+":resource/"+in.ServiceNamespace+"/"+in.ResourceId+":policyName/"+in.PolicyName),
			Name: in.PolicyName, ServiceNamespace: in.ServiceNamespace, ResourceID: in.ResourceId, ScalableDimension: in.ScalableDimension, CreatedAt: core.Now()}
	}
	if err := q.Authorize("application-autoscaling:PutScalingPolicy", p.ARN); err != nil {
		return nil, err
	}
	p.PolicyType, p.StepScaling, p.TargetTracking, p.PredictiveScaling = in.PolicyType, raw(in.StepScalingPolicyConfiguration),
		raw(in.TargetTrackingScalingPolicyConfiguration), raw(in.PredictiveScalingPolicyConfiguration)
	if err := store.Put(s.env.Store, cPolicies, p.ARN, p); err != nil {
		return nil, err
	}
	return map[string]any{"PolicyARN": p.ARN, "Alarms": []any{}}, nil
}

func newUUID() string {
	h := core.RandHex(32)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

func (s *Service) awsDescribePolicies(q *awsapi.Req) (any, error) {
	var in struct {
		ServiceNamespace  string
		PolicyNames       []string
		ResourceId        string
		ScalableDimension string
		MaxResults        int
		NextToken         string
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("application-autoscaling:DescribeScalingPolicies", "*"); err != nil {
		return nil, err
	}
	var all []Policy
	for _, p := range store.List[Policy](s.env.Store, cPolicies) {
		if p.ServiceNamespace != in.ServiceNamespace || (len(in.PolicyNames) > 0 && !slices.Contains(in.PolicyNames, p.Name)) ||
			(in.ResourceId != "" && in.ResourceId != p.ResourceID) || (in.ScalableDimension != "" && in.ScalableDimension != p.ScalableDimension) {
			continue
		}
		all = append(all, p)
	}
	slices.SortFunc(all, func(a, b Policy) int { return strings.Compare(a.ARN, b.ARN) })
	items, next := page(all, in.MaxResults, in.NextToken)
	out := []map[string]any{}
	for _, p := range items {
		m := map[string]any{"PolicyARN": p.ARN, "PolicyName": p.Name, "ServiceNamespace": p.ServiceNamespace, "ResourceId": p.ResourceID,
			"ScalableDimension": p.ScalableDimension, "PolicyType": p.PolicyType, "CreationTime": awsapi.Epoch(p.CreatedAt), "Alarms": []any{}}
		if p.StepScaling != nil {
			m["StepScalingPolicyConfiguration"] = p.StepScaling
		}
		if p.TargetTracking != nil {
			m["TargetTrackingScalingPolicyConfiguration"] = p.TargetTracking
		}
		if p.PredictiveScaling != nil {
			m["PredictiveScalingPolicyConfiguration"] = p.PredictiveScaling
		}
		out = append(out, m)
	}
	res := map[string]any{"ScalingPolicies": out}
	if next != "" {
		res["NextToken"] = next
	}
	return res, nil
}

func (s *Service) awsDeletePolicy(q *awsapi.Req) (any, error) {
	var in struct {
		targetRef
		PolicyName string
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.findPolicy(in.targetRef, in.PolicyName)
	arn := "*"
	if ok {
		arn = p.ARN
	}
	if err := q.Authorize("application-autoscaling:DeleteScalingPolicy", arn); err != nil {
		return nil, err
	}
	if !ok {
		return nil, notFound("No scaling policy found for service namespace: %s, resource ID: %s, scalable dimension: %s, policy name: %s", in.ServiceNamespace, in.ResourceId, in.ScalableDimension, in.PolicyName)
	}
	return nil, store.Delete(s.env.Store, cPolicies, p.ARN)
}

func (s *Service) findScheduled(ref targetRef, name string) (ScheduledAction, bool) {
	for _, a := range store.List[ScheduledAction](s.env.Store, cScheduled) {
		if a.Name == name && key(a.ServiceNamespace, a.ResourceID, a.ScalableDimension) == key(ref.ServiceNamespace, ref.ResourceId, ref.ScalableDimension) {
			return a, true
		}
	}
	return ScheduledAction{}, false
}

func (s *Service) awsPutScheduled(q *awsapi.Req) (any, error) {
	var in struct {
		targetRef
		ScheduledActionName  string
		Schedule             string
		Timezone             string
		StartTime            *awsapi.Time
		EndTime              *awsapi.Time
		ScalableTargetAction json.RawMessage
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if in.ScheduledActionName == "" {
		return nil, validation("ScheduledActionName is required")
	}
	if err := checkTarget(in.ServiceNamespace, in.ResourceId, in.ScalableDimension); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireTarget(in.targetRef); err != nil {
		return nil, err
	}
	a, ok := s.findScheduled(in.targetRef, in.ScheduledActionName)
	if !ok {
		if in.Schedule == "" {
			return nil, validation("Schedule is required")
		}
		a = ScheduledAction{ARN: s.env.ARN("autoscaling", "scheduledAction:"+newUUID()+":resource/"+in.ServiceNamespace+"/"+in.ResourceId+":scheduledActionName/"+in.ScheduledActionName),
			Name: in.ScheduledActionName, ServiceNamespace: in.ServiceNamespace, ResourceID: in.ResourceId, ScalableDimension: in.ScalableDimension, CreatedAt: core.Now()}
	}
	if err := q.Authorize("application-autoscaling:PutScheduledAction", a.ARN); err != nil {
		return nil, err
	}
	if in.Schedule != "" {
		a.Schedule = in.Schedule
	}
	if in.Timezone != "" {
		a.Timezone = in.Timezone
	}
	if in.StartTime != nil {
		a.StartTime = in.StartTime
	}
	if in.EndTime != nil {
		a.EndTime = in.EndTime
	}
	if r := raw(in.ScalableTargetAction); r != nil {
		a.ScalableTarget = r
	}
	return nil, store.Put(s.env.Store, cScheduled, a.ARN, a)
}

func (s *Service) awsDescribeScheduled(q *awsapi.Req) (any, error) {
	var in struct {
		ServiceNamespace     string
		ScheduledActionNames []string
		ResourceId           string
		ScalableDimension    string
		MaxResults           int
		NextToken            string
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("application-autoscaling:DescribeScheduledActions", "*"); err != nil {
		return nil, err
	}
	var all []ScheduledAction
	for _, a := range store.List[ScheduledAction](s.env.Store, cScheduled) {
		if a.ServiceNamespace != in.ServiceNamespace || (len(in.ScheduledActionNames) > 0 && !slices.Contains(in.ScheduledActionNames, a.Name)) ||
			(in.ResourceId != "" && in.ResourceId != a.ResourceID) || (in.ScalableDimension != "" && in.ScalableDimension != a.ScalableDimension) {
			continue
		}
		all = append(all, a)
	}
	slices.SortFunc(all, func(a, b ScheduledAction) int { return strings.Compare(a.ARN, b.ARN) })
	items, next := page(all, in.MaxResults, in.NextToken)
	out := []map[string]any{}
	for _, a := range items {
		m := map[string]any{"ScheduledActionARN": a.ARN, "ScheduledActionName": a.Name, "ServiceNamespace": a.ServiceNamespace,
			"ResourceId": a.ResourceID, "ScalableDimension": a.ScalableDimension, "Schedule": a.Schedule, "CreationTime": awsapi.Epoch(a.CreatedAt)}
		if a.Timezone != "" {
			m["Timezone"] = a.Timezone
		}
		if a.StartTime != nil {
			m["StartTime"] = a.StartTime
		}
		if a.EndTime != nil {
			m["EndTime"] = a.EndTime
		}
		if a.ScalableTarget != nil {
			m["ScalableTargetAction"] = a.ScalableTarget
		}
		out = append(out, m)
	}
	res := map[string]any{"ScheduledActions": out}
	if next != "" {
		res["NextToken"] = next
	}
	return res, nil
}

func (s *Service) awsDeleteScheduled(q *awsapi.Req) (any, error) {
	var in struct {
		targetRef
		ScheduledActionName string
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.findScheduled(in.targetRef, in.ScheduledActionName)
	arn := "*"
	if ok {
		arn = a.ARN
	}
	if err := q.Authorize("application-autoscaling:DeleteScheduledAction", arn); err != nil {
		return nil, err
	}
	if !ok {
		return nil, notFound("No scheduled action found for service namespace: %s, resource ID: %s, scalable dimension: %s, scheduled action name: %s", in.ServiceNamespace, in.ResourceId, in.ScalableDimension, in.ScheduledActionName)
	}
	return nil, store.Delete(s.env.Store, cScheduled, a.ARN)
}

// awsDescribeScalingActivities reports none: HomeCloud never scales.
func (s *Service) awsDescribeActivities(q *awsapi.Req) (any, error) {
	if err := q.Authorize("application-autoscaling:DescribeScalingActivities", "*"); err != nil {
		return nil, err
	}
	return map[string]any{"ScalingActivities": []any{}}, nil
}

func (s *Service) targetByARN(arn string) (string, Target, bool) {
	for _, t := range store.List[Target](s.env.Store, cTargets) {
		if t.ARN == arn {
			return key(t.ServiceNamespace, t.ResourceID, t.ScalableDimension), t, true
		}
	}
	return "", Target{}, false
}

func (s *Service) awsTag(q *awsapi.Req) (any, error) {
	var in struct {
		ResourceARN string
		Tags        map[string]string
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("application-autoscaling:TagResource", in.ResourceARN); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k, t, ok := s.targetByARN(in.ResourceARN)
	if !ok {
		return nil, awsapi.Errorf(http.StatusBadRequest, "ResourceNotFoundException", "Resource %s not found", in.ResourceARN)
	}
	if t.Tags == nil {
		t.Tags = core.Tags{}
	}
	for k, v := range in.Tags {
		t.Tags[k] = v
	}
	return nil, store.Put(s.env.Store, cTargets, k, t)
}

func (s *Service) awsUntag(q *awsapi.Req) (any, error) {
	var in struct {
		ResourceARN string
		TagKeys     []string
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("application-autoscaling:UntagResource", in.ResourceARN); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k, t, ok := s.targetByARN(in.ResourceARN)
	if !ok {
		return nil, awsapi.Errorf(http.StatusBadRequest, "ResourceNotFoundException", "Resource %s not found", in.ResourceARN)
	}
	for _, tk := range in.TagKeys {
		delete(t.Tags, tk)
	}
	return nil, store.Put(s.env.Store, cTargets, k, t)
}

func (s *Service) awsListTags(q *awsapi.Req) (any, error) {
	var in struct{ ResourceARN string }
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("application-autoscaling:ListTagsForResource", in.ResourceARN); err != nil {
		return nil, err
	}
	_, t, ok := s.targetByARN(in.ResourceARN)
	if !ok {
		return nil, awsapi.Errorf(http.StatusBadRequest, "ResourceNotFoundException", "Resource %s not found", in.ResourceARN)
	}
	tags := t.Tags
	if tags == nil {
		tags = core.Tags{}
	}
	return map[string]any{"Tags": tags}, nil
}
