package ecr_test

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi/awstest"
	"github.com/homecloudhq/homecloud/cli/internal/svc/ecr"
)

func TestAWSRepositories(t *testing.T) {
	h := awstest.New(t)
	ecr.New(h.Env).RegisterAWS()

	out := h.AWSJSON(t, "ecr", "create-repository", "--repository-name", "web/app", "--tags", "Key=env,Value=qa", "--image-scanning-configuration", "scanOnPush=true")
	repo := out["repository"].(map[string]any)
	if repo["repositoryUri"] != "localhost:5500/web/app" || repo["imageScanningConfiguration"].(map[string]any)["scanOnPush"] != true {
		t.Fatalf("repository %v", repo)
	}
	if o, err := h.AWSErr(t, "ecr", "create-repository", "--repository-name", "web/app"); err == nil || !strings.Contains(o, "RepositoryAlreadyExistsException") {
		t.Fatalf("duplicate: %v %s", err, o)
	}
	if o, err := h.AWSErr(t, "ecr", "describe-repositories", "--repository-names", "nope"); err == nil || !strings.Contains(o, "RepositoryNotFoundException") {
		t.Fatalf("missing: %v %s", err, o)
	}

	arn := repo["repositoryArn"].(string)
	h.AWS(t, "ecr", "tag-resource", "--resource-arn", arn, "--tags", "Key=team,Value=x")
	h.AWS(t, "ecr", "untag-resource", "--resource-arn", arn, "--tag-keys", "env")
	if o := h.AWS(t, "ecr", "list-tags-for-resource", "--resource-arn", arn); !strings.Contains(o, "team") || strings.Contains(o, "env") {
		t.Fatalf("tags %s", o)
	}

	h.AWS(t, "ecr", "put-image-tag-mutability", "--repository-name", "web/app", "--image-tag-mutability", "IMMUTABLE")
	got := h.AWSJSON(t, "ecr", "describe-repositories", "--repository-names", "web/app")["repositories"].([]any)[0].(map[string]any)
	if got["imageTagMutability"] != "IMMUTABLE" {
		t.Fatalf("mutability %v", got)
	}

	pol := `{"rules":[{"rulePriority":1,"selection":{"tagStatus":"any","countType":"imageCountMoreThan","countNumber":3},"action":{"type":"expire"}}]}`
	h.AWS(t, "ecr", "put-lifecycle-policy", "--repository-name", "web/app", "--lifecycle-policy-text", pol)
	if o := h.AWSJSON(t, "ecr", "get-lifecycle-policy", "--repository-name", "web/app"); o["lifecyclePolicyText"] != pol {
		t.Fatalf("lifecycle %v", o)
	}
	h.AWS(t, "ecr", "delete-lifecycle-policy", "--repository-name", "web/app")
	if o, err := h.AWSErr(t, "ecr", "get-lifecycle-policy", "--repository-name", "web/app"); err == nil || !strings.Contains(o, "LifecyclePolicyNotFoundException") {
		t.Fatalf("deleted lifecycle: %v %s", err, o)
	}
	rp := `{"Version":"2012-10-17","Statement":[]}`
	h.AWS(t, "ecr", "set-repository-policy", "--repository-name", "web/app", "--policy-text", rp)
	if o := h.AWSJSON(t, "ecr", "get-repository-policy", "--repository-name", "web/app"); o["policyText"] != rp {
		t.Fatalf("policy %v", o)
	}

	tok := h.AWSJSON(t, "ecr", "get-authorization-token")["authorizationData"].([]any)[0].(map[string]any)
	dec, err := base64.StdEncoding.DecodeString(tok["authorizationToken"].(string))
	if err != nil || !strings.HasPrefix(string(dec), "AWS:") || tok["proxyEndpoint"] != "http://localhost:5500" {
		t.Fatalf("token %v %v", err, tok)
	}
	if o := h.AWSJSON(t, "ecr", "describe-registry"); o["registryId"] != h.Env.AccountID {
		t.Fatalf("registry %v", o)
	}
}
