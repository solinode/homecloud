package s3

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
)

// Bucket policies are stored in MinIO (so the MinIO endpoint applies them to
// its own anonymous access too) and evaluated by HomeCloud for requests to the
// AWS endpoint: an Allow grants access to principals the identity policies do
// not (including anonymous callers for Principal "*"), an explicit Deny
// overrides identity policies (except for the account root).
//
// Conditions are not evaluated: an Allow with a Condition is ignored and a
// Deny with a Condition applies unconditionally, erring on the side of denial.

type policyDecision int

const (
	noDecision policyDecision = iota
	policyAllow
	policyDeny
)

type strList []string

func (s *strList) UnmarshalJSON(b []byte) error {
	var one string
	if json.Unmarshal(b, &one) == nil {
		*s = strList{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*s = many
	return nil
}

type bpPrincipal struct {
	any bool
	aws []string
}

func (p *bpPrincipal) UnmarshalJSON(b []byte) error {
	var star string
	if json.Unmarshal(b, &star) == nil {
		p.any = star == "*"
		return nil
	}
	var m map[string]strList
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	for _, a := range m["AWS"] {
		if a == "*" {
			p.any = true
		}
		p.aws = append(p.aws, a)
	}
	return nil
}

type bpStatement struct {
	Effect       string          `json:"Effect"`
	Principal    *bpPrincipal    `json:"Principal"`
	NotPrincipal *bpPrincipal    `json:"NotPrincipal"`
	Action       strList         `json:"Action"`
	NotAction    strList         `json:"NotAction"`
	Resource     strList         `json:"Resource"`
	NotResource  strList         `json:"NotResource"`
	Condition    json.RawMessage `json:"Condition"`
}

type bucketPolicy struct {
	Statement []bpStatement `json:"Statement"`
}

// principalMatches reports whether the policy principal names p (nil = anonymous).
// Naming the account (ID or root ARN) matches every principal in it, but only
// counts for Deny: as in AWS, it delegates the grant to identity policies.
func (bp *bpPrincipal) matches(p *httpx.Principal, allowAccount bool) bool {
	if bp == nil {
		return false
	}
	if bp.any {
		return true
	}
	if p == nil {
		return false
	}
	ids := []string{p.ARN}
	if p.RoleName != "" {
		ids = append(ids, core.ARN(p.AccountID, "iam", "role/"+p.RoleName))
	}
	for _, a := range bp.aws {
		a = core.CanonicalARN(a)
		if allowAccount && (a == p.AccountID || a == core.ARN(p.AccountID, "iam", "root")) {
			return true
		}
		for _, id := range ids {
			if a == id {
				return true
			}
		}
	}
	return false
}

func anyGlob(patterns []string, v string, fold bool) bool {
	for _, p := range patterns {
		if fold {
			if glob(strings.ToLower(p), strings.ToLower(v)) {
				return true
			}
		} else if glob(p, v) {
			return true
		}
	}
	return false
}

// glob matches * and ? wildcards.
func glob(p, s string) bool {
	px, sx, starP, starS := 0, 0, -1, 0
	for sx < len(s) {
		switch {
		case px < len(p) && (p[px] == '?' || p[px] == s[sx]):
			px++
			sx++
		case px < len(p) && p[px] == '*':
			starP, starS = px, sx
			px++
		case starP >= 0:
			px = starP + 1
			starS++
			sx = starS
		default:
			return false
		}
	}
	for px < len(p) && p[px] == '*' {
		px++
	}
	return px == len(p)
}

func (d *bucketPolicy) evaluate(p *httpx.Principal, action, resource string) policyDecision {
	result := noDecision
	for _, st := range d.Statement {
		deny := st.Effect == "Deny"
		if !deny && (len(st.Condition) > 0 && string(st.Condition) != "null" || st.NotPrincipal != nil) {
			continue
		}
		switch {
		case st.Principal != nil:
			if !st.Principal.matches(p, deny) {
				continue
			}
		case st.NotPrincipal != nil:
			if st.NotPrincipal.matches(p, true) {
				continue
			}
		default:
			continue
		}
		if len(st.Action) > 0 && !anyGlob(st.Action, action, true) || len(st.NotAction) > 0 && anyGlob(st.NotAction, action, true) {
			continue
		}
		if len(st.Action) == 0 && len(st.NotAction) == 0 {
			continue
		}
		if len(st.Resource) > 0 && !anyGlob(st.Resource, resource, false) || len(st.NotResource) > 0 && anyGlob(st.NotResource, resource, false) {
			continue
		}
		if len(st.Resource) == 0 && len(st.NotResource) == 0 {
			continue
		}
		if deny {
			return policyDeny
		}
		result = policyAllow
	}
	return result
}

// policyCache holds parsed bucket policies for a few seconds; writes through
// HomeCloud invalidate it.
type policyCache struct {
	mu sync.Mutex
	m  map[string]cachedPolicy
}

type cachedPolicy struct {
	doc *bucketPolicy // nil: no policy
	at  time.Time
}

const policyTTL = 5 * time.Second

func (s *Service) bucketPolicy(ctx context.Context, bucket string) (*bucketPolicy, error) {
	s.policies.mu.Lock()
	if c, ok := s.policies.m[bucket]; ok && time.Since(c.at) < policyTTL {
		s.policies.mu.Unlock()
		return c.doc, nil
	}
	s.policies.mu.Unlock()
	cl, err := s.cl()
	if err != nil {
		return nil, err
	}
	raw, err := cl.GetBucketPolicy(ctx, bucket)
	if err != nil {
		return nil, s3err(err)
	}
	var doc *bucketPolicy
	if strings.TrimSpace(raw) != "" {
		doc = &bucketPolicy{}
		if err := json.Unmarshal([]byte(raw), doc); err != nil {
			// Unparseable: grant nothing, deny nothing.
			doc = &bucketPolicy{}
		}
	}
	s.policies.mu.Lock()
	if s.policies.m == nil {
		s.policies.m = map[string]cachedPolicy{}
	}
	s.policies.m[bucket] = cachedPolicy{doc: doc, at: time.Now()}
	s.policies.mu.Unlock()
	return doc, nil
}

func (s *Service) forgetPolicy(bucket string) {
	s.policies.mu.Lock()
	delete(s.policies.m, bucket)
	s.policies.mu.Unlock()
}
