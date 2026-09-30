package cognito_test

import (
	"encoding/json"
	"fmt"
	"testing"
)

// Self sign-up is open to anyone and costs a bcrypt hash and a store write each:
// a pool must stop accepting a flood of them.
func TestSelfSignUpIsRateLimited(t *testing.T) {
	h, _ := newCognito(t)
	var pool, cl map[string]any
	_ = json.Unmarshal(h.Native(t, "POST", "/api/v1/cognito/user-pools", map[string]any{"name": "open"}), &pool)
	pid := pool["id"].(string)
	_ = json.Unmarshal(h.Native(t, "POST", "/api/v1/cognito/user-pools/"+pid+"/clients", map[string]any{"name": "c"}), &cl)
	limited := 0
	for i := 0; i < 130; i++ {
		st, _ := post(t, h.URL+"/cognito/"+pid+"/sign-up", map[string]any{"client_id": cl["id"], "username": fmt.Sprintf("u%d", i), "password": "pass1234"}, "")
		if st == 429 {
			limited++
		}
	}
	if limited < 5 {
		t.Fatalf("only %d of 130 sign-ups were refused", limited)
	}
}
