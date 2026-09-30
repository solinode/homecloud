package iam_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi/awstest"
)

func loginStatus(t *testing.T, h *awstest.Harness, user, pw string) int {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"username": user, "password": pw})
	resp, err := http.Post(h.URL+"/api/v1/auth/login", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestSetRootPassword(t *testing.T) {
	h := awstest.New(t)
	if err := h.IAM.SetRootPassword("short"); err == nil {
		t.Fatal("a too-short password was accepted")
	}
	if err := h.IAM.SetRootPassword("a-chosen-passphrase"); err != nil {
		t.Fatal(err)
	}
	if st := loginStatus(t, h, "root", "a-chosen-passphrase"); st != 200 {
		t.Fatalf("sign-in with the chosen password: %d", st)
	}
	if st := loginStatus(t, h, "root", "another-passphrase"); st != 401 {
		t.Fatalf("wrong password: %d", st)
	}
}
