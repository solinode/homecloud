package s3

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/httpx"
)

// Bucket policies are stored in MinIO (so the MinIO endpoint applies them to
// its own anonymous access too) and evaluated by HomeCloud's policy engine
// (see iam.evalResourcePolicy) for requests to the AWS and native APIs: an
// Allow grants access to principals the identity policies do not (including
// anonymous callers for Principal "*"), an explicit Deny overrides identity
// policies (except for the account root), and Conditions are evaluated against
// the request (aws:SourceIp, aws:SecureTransport, s3:prefix, ...).

// policyCache holds bucket policies for a few seconds; writes through
// HomeCloud invalidate it.
type policyCache struct {
	mu sync.Mutex
	m  map[string]cachedPolicy
}

type cachedPolicy struct {
	text string // "": no policy
	at   time.Time
}

const policyTTL = 5 * time.Second

// bucketPolicy returns the bucket's policy document ("" for none).
func (s *Service) bucketPolicy(ctx context.Context, bucket string) (string, error) {
	s.policies.mu.Lock()
	if c, ok := s.policies.m[bucket]; ok && time.Since(c.at) < policyTTL {
		s.policies.mu.Unlock()
		return c.text, nil
	}
	s.policies.mu.Unlock()
	cl, err := s.cl()
	if err != nil {
		return "", err
	}
	raw, err := cl.GetBucketPolicy(ctx, bucket)
	if err != nil {
		return "", s3err(err)
	}
	raw = strings.TrimSpace(raw)
	s.policies.mu.Lock()
	if s.policies.m == nil {
		s.policies.m = map[string]cachedPolicy{}
	}
	s.policies.m[bucket] = cachedPolicy{text: raw, at: time.Now()}
	s.policies.mu.Unlock()
	return raw, nil
}

func (s *Service) forgetPolicy(bucket string) {
	s.policies.mu.Lock()
	delete(s.policies.m, bucket)
	s.policies.mu.Unlock()
}

// policyProvider makes bucket policies part of authorization on the native API.
func (s *Service) policyProvider(arn string) (httpx.Access, bool) {
	rest, ok := strings.CutPrefix(arn, "arn:aws:s3:::")
	if !ok {
		return httpx.Access{}, false
	}
	bucket, _, _ := strings.Cut(rest, "/")
	if bucket == "" || bucket == "*" || !s.knownBucket(bucket) {
		return httpx.Access{}, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	text, err := s.bucketPolicy(ctx, bucket)
	return httpx.Access{Policy: text}, err == nil && text != ""
}

// publicPolicy reports whether a policy grants everyone access unconditionally
// (GetBucketPolicyStatus IsPublic).
func publicPolicy(text string) bool {
	var d struct {
		Statement json.RawMessage
	}
	if json.Unmarshal([]byte(text), &d) != nil {
		return false
	}
	var stmts []struct {
		Effect    string
		Principal json.RawMessage
		Condition json.RawMessage
	}
	if raw := strings.TrimSpace(string(d.Statement)); strings.HasPrefix(raw, "{") {
		var one struct {
			Effect    string
			Principal json.RawMessage
			Condition json.RawMessage
		}
		if json.Unmarshal(d.Statement, &one) == nil {
			stmts = append(stmts, one)
		}
	} else if json.Unmarshal(d.Statement, &stmts) != nil {
		return false
	}
	for _, st := range stmts {
		if st.Effect != "Allow" || len(st.Condition) != 0 && string(st.Condition) != "null" {
			continue
		}
		var star string
		var m map[string]json.RawMessage
		switch {
		case json.Unmarshal(st.Principal, &star) == nil && star == "*":
			return true
		case json.Unmarshal(st.Principal, &m) == nil:
			var one string
			var many []string
			if json.Unmarshal(m["AWS"], &one) == nil && one == "*" {
				return true
			}
			if json.Unmarshal(m["AWS"], &many) == nil {
				for _, a := range many {
					if a == "*" {
						return true
					}
				}
			}
		}
	}
	return false
}
