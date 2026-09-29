package secrets_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/homecloudhq/homecloud/cli/internal/svc/secrets"
)

func TestSecretsNativeRotationPolicyStages(t *testing.T) {
	h, sec, _, _ := setup(t)
	sec.Routes(h.Router)
	sec.Lambda = secrets.InvokerFunc(func(context.Context, string, []byte) ([]byte, string, error) { return []byte("null"), "", nil })
	get := func(method, path string, body any) map[string]any {
		var m map[string]any
		if err := json.Unmarshal(h.Native(t, method, path, body), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	h.Native(t, "POST", "/api/v1/secrets", map[string]any{"name": "nr", "value": "v1"})

	r := get("POST", "/api/v1/secrets/nr/rotate", map[string]any{"rotation_lambda_arn": "rotator", "schedule_expression": "rate(10 days)", "rotate_immediately": false})
	s := r["secret"].(map[string]any)
	if s["rotation_enabled"] != true || s["next_rotation"] == nil {
		t.Fatalf("rotate: %v", r)
	}
	if s := get("POST", "/api/v1/secrets/nr/cancel-rotation", nil); s["rotation_enabled"] == true {
		t.Fatalf("cancel: %v", s)
	}

	pol := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::` + h.Env.AccountID + `:root"},"Action":"secretsmanager:GetSecretValue","Resource":"*"}]}`
	h.Native(t, "PUT", "/api/v1/secrets/nr/policy", map[string]any{"policy": pol})
	if p := get("GET", "/api/v1/secrets/nr/policy", nil); p["policy"] != pol {
		t.Fatalf("policy: %v", p)
	}
	h.Native(t, "DELETE", "/api/v1/secrets/nr/policy", nil)
	if p := get("GET", "/api/v1/secrets/nr/policy", nil); p["policy"] != "" {
		t.Fatalf("deleted policy: %v", p)
	}

	// Move AWSCURRENT back to the first version.
	h.Native(t, "PUT", "/api/v1/secrets/nr/value", map[string]any{"value": "v2"})
	v1 := get("GET", "/api/v1/secrets/nr/value?version_stage=AWSPREVIOUS", nil)["version_id"].(string)
	v2 := get("GET", "/api/v1/secrets/nr/value", nil)["version_id"].(string)
	h.Native(t, "PUT", "/api/v1/secrets/nr/stages", map[string]any{"stage": "AWSCURRENT", "remove_from_version_id": v2, "move_to_version_id": v1})
	if v := get("GET", "/api/v1/secrets/nr/value", nil); v["value"] != "v1" {
		t.Fatalf("stage move: %v", v)
	}
}
