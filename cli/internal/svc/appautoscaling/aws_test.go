package appautoscaling_test

import (
	"strings"
	"testing"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi/awstest"
	"github.com/homecloudhq/homecloud/cli/internal/svc/appautoscaling"
)

// The calls Terraform makes for an ECS service with autoscaling (the
// terraform-aws-modules ECS module turns it on by default).
func TestAWSScalableTargetsAndPolicies(t *testing.T) {
	h := awstest.New(t)
	appautoscaling.New(h.Env).RegisterAWS()
	const res = "service/web-cluster/web"
	target := []string{"--service-namespace", "ecs", "--resource-id", res, "--scalable-dimension", "ecs:service:DesiredCount"}

	if o, err := h.AWSErr(t, append([]string{"application-autoscaling", "put-scaling-policy", "--policy-name", "cpu",
		"--policy-type", "TargetTrackingScaling", "--target-tracking-scaling-policy-configuration", `{"TargetValue":50}`}, target...)...); err == nil || !strings.Contains(o, "ObjectNotFoundException") {
		t.Fatalf("policy without a target: %v %s", err, o)
	}
	arn := h.AWSJSON(t, append([]string{"application-autoscaling", "register-scalable-target", "--min-capacity", "1", "--max-capacity", "4",
		"--tags", "team=web"}, target...)...)["ScalableTargetARN"].(string)
	if !strings.HasPrefix(arn, "arn:aws:application-autoscaling:us-east-1:"+h.Env.AccountID+":scalable-target/") {
		t.Fatalf("arn %s", arn)
	}
	// Registering again updates the target in place.
	h.AWS(t, append([]string{"application-autoscaling", "register-scalable-target", "--max-capacity", "6"}, target...)...)
	ts := h.AWSJSON(t, "application-autoscaling", "describe-scalable-targets", "--service-namespace", "ecs", "--resource-ids", res)["ScalableTargets"].([]any)
	if len(ts) != 1 {
		t.Fatalf("targets %v", ts)
	}
	tg := ts[0].(map[string]any)
	if tg["MinCapacity"] != float64(1) || tg["MaxCapacity"] != float64(6) || tg["ScalableTargetARN"] != arn || !strings.Contains(tg["RoleARN"].(string), "AWSServiceRoleForApplicationAutoScaling_ECSService") {
		t.Fatalf("target %v", tg)
	}
	if tags := h.AWSJSON(t, "application-autoscaling", "list-tags-for-resource", "--resource-arn", arn)["Tags"].(map[string]any); tags["team"] != "web" {
		t.Fatalf("tags %v", tags)
	}

	pol := h.AWSJSON(t, append([]string{"application-autoscaling", "put-scaling-policy", "--policy-name", "cpu", "--policy-type", "TargetTrackingScaling",
		"--target-tracking-scaling-policy-configuration", `{"TargetValue":50,"PredefinedMetricSpecification":{"PredefinedMetricType":"ECSServiceAverageCPUUtilization"}}`}, target...)...)
	if !strings.Contains(pol["PolicyARN"].(string), ":scalingPolicy:") {
		t.Fatalf("policy %v", pol)
	}
	ps := h.AWSJSON(t, "application-autoscaling", "describe-scaling-policies", "--service-namespace", "ecs", "--resource-id", res)["ScalingPolicies"].([]any)
	if len(ps) != 1 || ps[0].(map[string]any)["TargetTrackingScalingPolicyConfiguration"].(map[string]any)["TargetValue"] != float64(50) {
		t.Fatalf("policies %v", ps)
	}
	h.AWS(t, append([]string{"application-autoscaling", "put-scheduled-action", "--scheduled-action-name", "night", "--schedule", "cron(0 22 * * ? *)",
		"--scalable-target-action", "MinCapacity=0,MaxCapacity=1"}, target...)...)
	if sa := h.AWSJSON(t, "application-autoscaling", "describe-scheduled-actions", "--service-namespace", "ecs")["ScheduledActions"].([]any); len(sa) != 1 {
		t.Fatalf("scheduled actions %v", sa)
	}

	h.AWS(t, append([]string{"application-autoscaling", "delete-scaling-policy", "--policy-name", "cpu"}, target...)...)
	// Deregistering removes what is left on the target.
	h.AWS(t, append([]string{"application-autoscaling", "deregister-scalable-target"}, target...)...)
	if sa := h.AWSJSON(t, "application-autoscaling", "describe-scheduled-actions", "--service-namespace", "ecs")["ScheduledActions"].([]any); len(sa) != 0 {
		t.Fatalf("scheduled actions left %v", sa)
	}
	if o, err := h.AWSErr(t, append([]string{"application-autoscaling", "deregister-scalable-target"}, target...)...); err == nil || !strings.Contains(o, "ObjectNotFoundException") {
		t.Fatalf("deregister twice: %v %s", err, o)
	}
	if o, err := h.AWSErr(t, "application-autoscaling", "register-scalable-target", "--service-namespace", "ecs", "--resource-id", "table/x",
		"--scalable-dimension", "dynamodb:table:ReadCapacityUnits", "--min-capacity", "1", "--max-capacity", "2"); err == nil || !strings.Contains(o, "ValidationException") {
		t.Fatalf("mismatched dimension: %v %s", err, o)
	}
}
