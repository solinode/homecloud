package iam_test

import (
	"bytes"
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
