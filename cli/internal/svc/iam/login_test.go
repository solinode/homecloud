package iam_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi/awstest"
)

// A burst of parallel guesses must not get past the throttle just because none
// of them had failed yet when the others were checked.
func TestLoginThrottleHoldsUnderParallelBurst(t *testing.T) {
	h := awstest.New(t)
	var mu sync.Mutex
	codes := map[int]int{}
	var wg sync.WaitGroup
	for i := 0; i < 60; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st := loginStatus(t, h, "root", "wrong-password")
			mu.Lock()
			codes[st]++
			mu.Unlock()
		}()
	}
	wg.Wait()
	if codes[401] > 10 || codes[429] < 50 {
		t.Fatalf("status counts %v: more than 10 guesses were evaluated", codes)
	}
}

// Query-string credentials end up in access logs and browser history: only a
// console session may be sent that way, never an access key secret.
func TestQueryTokenAcceptsSessionsOnly(t *testing.T) {
	h := awstest.New(t)
	get := func(tok string) int {
		resp, err := http.Get(h.URL + "/api/v1/auth/whoami?access_token=" + tok)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if st := get(h.AccessKeyID + ":" + h.SecretKey); st != http.StatusUnauthorized {
		t.Fatalf("an access key secret in the query string was accepted: %d", st)
	}
	if err := h.IAM.SetRootPassword("a-chosen-passphrase"); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(h.URL+"/api/v1/auth/login", "application/json", strings.NewReader(`{"username":"root","password":"a-chosen-passphrase"}`))
	if err != nil {
		t.Fatal(err)
	}
	var out struct{ Token string }
	_ = json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	if st := get(out.Token); st != http.StatusOK {
		t.Fatalf("a session token in the query string: %d", st)
	}
}

// Temporary credentials cannot mint new ones: a stolen session token would
// otherwise be renewable forever, surviving the revocation of the user's keys.
func TestSessionTokensCannotBeRenewed(t *testing.T) {
	h := awstest.New(t)
	c := h.AWSJSON(t, "sts", "get-session-token")["Credentials"].(map[string]any)
	out, err := h.AWSAs(t, c["AccessKeyId"].(string), c["SecretAccessKey"].(string), c["SessionToken"].(string), "sts", "get-session-token")
	if err == nil || !strings.Contains(out, "AccessDenied") {
		t.Fatalf("renewing a session token: %v %s", err, out)
	}
}

func TestLoginRejectsHugeBodies(t *testing.T) {
	h := awstest.New(t)
	body := `{"username":"root","password":"` + strings.Repeat("a", 200<<10) + `"}`
	resp, err := http.Post(h.URL+"/api/v1/auth/login", "application/json", bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d", resp.StatusCode)
	}
}
