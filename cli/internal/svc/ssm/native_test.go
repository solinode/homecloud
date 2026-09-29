package ssm_test

import (
	"strings"
	"testing"
)

func TestSSMNativeUnlabel(t *testing.T) {
	h, _ := setup(t)
	h.Native(t, "PUT", "/api/v1/ssm/parameter", map[string]any{"name": "/ul/a", "value": "1", "type": "String"})
	h.Native(t, "POST", "/api/v1/ssm/parameter/labels", map[string]any{"name": "/ul/a", "version": 1, "labels": []string{"prod"}})
	b := string(h.Native(t, "POST", "/api/v1/ssm/parameter/unlabel", map[string]any{"name": "/ul/a", "version": 1, "labels": []string{"prod", "nope"}}))
	if !strings.Contains(b, `"prod"`) || !strings.Contains(b, "nope") {
		t.Fatalf("unlabel: %s", b)
	}
	if hist := string(h.Native(t, "GET", "/api/v1/ssm/parameter/history?name=/ul/a", nil)); strings.Contains(hist, "prod") {
		t.Fatalf("history still labeled: %s", hist)
	}
}
