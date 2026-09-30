package sqs

import (
	"encoding/json"
	"testing"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi/awstest"
)

// Generated links follow public_url when it is set and public_host:port otherwise.
func TestNativeQueueURLUsesPublicURL(t *testing.T) {
	for _, c := range []struct{ publicURL, want string }{
		{"", "http://localhost:8080/api/v1/sqs/queues/jobs"},
		{"https://cloud.example.com", "https://cloud.example.com/api/v1/sqs/queues/jobs"},
	} {
		h := awstest.New(t)
		h.Env.Cfg.APIAddr, h.Env.Cfg.PublicHost, h.Env.Cfg.PublicURL = "0.0.0.0:8080", "localhost", c.publicURL
		s := New(h.Env)
		s.Routes(h.Router)
		var q struct {
			URL string `json:"url"`
		}
		out := h.Native(t, "POST", "/api/v1/sqs/queues", map[string]any{"name": "jobs"})
		if err := json.Unmarshal(out, &q); err != nil {
			t.Fatal(err)
		}
		if q.URL == "" {
			var w struct {
				Queue struct {
					URL string `json:"url"`
				} `json:"queue"`
			}
			_ = json.Unmarshal(out, &w)
			q.URL = w.Queue.URL
		}
		if q.URL != c.want {
			t.Errorf("public_url=%q: queue url %q, want %q (%s)", c.publicURL, q.URL, c.want, out)
		}
	}
}
