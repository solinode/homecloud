package ecs_test

import (
	"strings"
	"testing"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi/awstest"
	"github.com/homecloudhq/homecloud/cli/internal/svc/ecs"
)

const td = `{"family":"web","cpu":"256","memory":"512","networkMode":"awsvpc","requiresCompatibilities":["FARGATE"],
 "containerDefinitions":[{"name":"web","image":"nginx:alpine","essential":true,"portMappings":[{"containerPort":80}],
 "environment":[{"name":"A","value":"1"}],"secrets":[{"name":"S","valueFrom":"arn:aws:secretsmanager:us-east-1:123456789012:secret:nope-AbCdEf:key::"}]}]}`

func TestAWSClustersAndTaskDefinitions(t *testing.T) {
	h := awstest.New(t)
	ecs.New(h.Env, nil, nil, h.Secrets).RegisterAWS()

	c := h.AWSJSON(t, "ecs", "create-cluster", "--cluster-name", "qa", "--tags", "key=env,value=qa")["cluster"].(map[string]any)
	if c["status"] != "ACTIVE" || !strings.HasSuffix(c["clusterArn"].(string), ":cluster/qa") {
		t.Fatalf("cluster %v", c)
	}
	h.AWS(t, "ecs", "create-cluster", "--cluster-name", "qa") // idempotent
	if o := h.AWS(t, "ecs", "list-clusters"); strings.Count(o, "cluster/qa") != 1 {
		t.Fatalf("clusters %s", o)
	}
	d := h.AWSJSON(t, "ecs", "describe-clusters", "--clusters", "qa", "missing", "--include", "TAGS")
	if len(d["clusters"].([]any)) != 1 || d["failures"].([]any)[0].(map[string]any)["reason"] != "MISSING" {
		t.Fatalf("describe %v", d)
	}
	h.AWS(t, "ecs", "put-cluster-capacity-providers", "--cluster", "qa", "--capacity-providers", "FARGATE", "--default-capacity-provider-strategy", "capacityProvider=FARGATE,weight=1")
	if o := h.AWS(t, "ecs", "describe-capacity-providers"); !strings.Contains(o, "FARGATE_SPOT") {
		t.Fatalf("capacity providers %s", o)
	}

	// A secret that does not exist fails registration.
	if o, err := h.AWSErr(t, "ecs", "register-task-definition", "--cli-input-json", td); err == nil || !strings.Contains(o, "nope") {
		t.Fatalf("missing secret: %v %s", err, o)
	}
	plain := strings.Replace(td, `,"secrets":[{"name":"S","valueFrom":"arn:aws:secretsmanager:us-east-1:123456789012:secret:nope-AbCdEf:key::"}]`, "", 1)
	r := h.AWSJSON(t, "ecs", "register-task-definition", "--cli-input-json", plain)["taskDefinition"].(map[string]any)
	if r["revision"].(float64) != 1 || r["status"] != "ACTIVE" {
		t.Fatalf("task definition %v", r)
	}
	h.AWS(t, "ecs", "register-task-definition", "--cli-input-json", plain)
	if o, err := h.AWSErr(t, "ecs", "register-task-definition", "--cli-input-json",
		`{"family":"two","containerDefinitions":[{"name":"a","image":"x"},{"name":"b","image":"y"}]}`); err == nil || !strings.Contains(o, "single container") {
		t.Fatalf("two containers: %v %s", err, o)
	}
	got := h.AWSJSON(t, "ecs", "describe-task-definition", "--task-definition", "web")["taskDefinition"].(map[string]any)
	if got["revision"].(float64) != 2 || got["cpu"] != "256" {
		t.Fatalf("latest %v", got)
	}
	h.AWS(t, "ecs", "tag-resource", "--resource-arn", got["taskDefinitionArn"].(string), "--tags", "key=k,value=v")
	if o := h.AWS(t, "ecs", "list-tags-for-resource", "--resource-arn", got["taskDefinitionArn"].(string)); !strings.Contains(o, `"k"`) {
		t.Fatalf("tags %s", o)
	}
	h.AWS(t, "ecs", "deregister-task-definition", "--task-definition", "web:1")
	if o := h.AWS(t, "ecs", "list-task-definitions", "--family-prefix", "we"); strings.Contains(o, "web:1") || !strings.Contains(o, "web:2") {
		t.Fatalf("list %s", o)
	}
	if o := h.AWS(t, "ecs", "list-task-definition-families"); !strings.Contains(o, `"web"`) {
		t.Fatalf("families %s", o)
	}

	if o, err := h.AWSErr(t, "ecs", "describe-services", "--cluster", "nope", "--services", "x"); err == nil || !strings.Contains(o, "ClusterNotFoundException") {
		t.Fatalf("cluster missing: %v %s", err, o)
	}
	h.AWS(t, "ecs", "delete-cluster", "--cluster", "qa")
	if o := h.AWS(t, "ecs", "list-clusters"); strings.Contains(o, "cluster/qa") {
		t.Fatalf("deleted cluster listed: %s", o)
	}
}
