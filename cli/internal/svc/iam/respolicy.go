package iam

import (
	"encoding/json"
	"strings"
	"sync"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
)

// Resource-based policies (S3 bucket policies, SQS queue policies, SNS topic
// policies, KMS key policies) are evaluated here with the same engine and
// condition operators as identity policies; httpx.Principal.Permits combines
// the two. See httpx/authz.go for the combination rules.

func init() { httpx.EvalResourcePolicy = evalResourcePolicy }

// parsedPolicies caches parsed resource policies by their text.
var parsedPolicies struct {
	mu sync.Mutex
	m  map[string]*PolicyDocument
}

func parseResourcePolicy(text string) *PolicyDocument {
	c := &parsedPolicies
	c.mu.Lock()
	defer c.mu.Unlock()
	if d, ok := c.m[text]; ok {
		return d
	}
	var d *PolicyDocument
	var doc PolicyDocument
	if json.Unmarshal([]byte(text), &doc) == nil {
		d = &doc
	} // unparseable: grants nothing, denies nothing
	if c.m == nil || len(c.m) > 512 {
		c.m = map[string]*PolicyDocument{}
	}
	c.m[text] = d
	return d
}

// ValidateResourcePolicy checks a resource-based policy document as
// PutBucketPolicy, SetQueueAttributes(Policy), SetTopicAttributes(Policy) and
// PutKeyPolicy accept it: known elements, Effect, Action, Resource and a
// Principal on every statement.
func ValidateResourcePolicy(text string) error {
	d, err := parseForValidation(text)
	if err != nil {
		return err
	}
	if len(d.Statement) == 0 {
		return malformed("policy document needs at least one Statement")
	}
	for i, st := range d.Statement {
		if err := st.validateCommon(i); err != nil {
			return err
		}
		if st.Principal == nil && st.NotPrincipal == nil {
			return malformed("Statement[%d] needs a Principal", i)
		}
	}
	return nil
}

func parseForValidation(text string) (PolicyDocument, error) {
	// ParsePolicy rejects NotPrincipal (identity policies); resource policies allow it.
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &top); err != nil {
		return PolicyDocument{}, malformed("Syntax errors in policy: %v", err)
	}
	var d PolicyDocument
	if err := json.Unmarshal([]byte(text), &d); err != nil {
		return d, malformed("Syntax errors in policy: %v", err)
	}
	return d, nil
}

// principalIDs are the ARNs a policy's Principal element can name the caller by.
func principalIDs(p *httpx.Principal) []string {
	ids := []string{p.ARN}
	for _, a := range p.Context["aws:principalarn"] {
		ids = append(ids, a)
	}
	return ids
}

// principalMatch reports whether pr names the caller. delegated is set when it
// does so only through the caller's account (the account or its root), which
// in an Allow statement delegates the decision to identity policies.
func principalMatch(pr *Principals, who httpx.Who) (matched, delegated bool) {
	if pr == nil {
		return false, false
	}
	if pr.Any {
		return true, false
	}
	if who.Service != "" {
		return slicesContains(pr.Service, who.Service) || slicesContains(pr.AWS, "*"), false
	}
	if who.Principal == nil {
		return slicesContains(pr.AWS, "*"), false
	}
	p := who.Principal
	root := core.ARN(p.AccountID, "iam", "root")
	ids := principalIDs(p)
	for _, a := range pr.AWS {
		if a == "*" {
			return true, false
		}
		a = core.CanonicalARN(a)
		for _, id := range ids {
			if a == id {
				return true, false
			}
		}
		if a == p.AccountID || a == root {
			delegated = true
		}
	}
	return false, delegated
}

func slicesContains(l StringList, v string) bool {
	for _, x := range l {
		if x == v {
			return true
		}
	}
	return false
}

// evalResourcePolicy evaluates a resource policy for a caller and request.
func evalResourcePolicy(text string, who httpx.Who, action, resource string, keys map[string][]string) httpx.Verdict {
	var v httpx.Verdict
	doc := parseResourcePolicy(text)
	if doc == nil {
		return v
	}
	ctx := CondContext{}
	for k, vals := range keys {
		ctx[strings.ToLower(k)] = vals
	}
	if who.Service != "" {
		ctx["aws:principalservicename"] = []string{who.Service}
	}
	resource = core.CanonicalARN(resource)
	for _, st := range doc.Statement {
		var matched, delegated bool
		switch {
		case st.Principal != nil:
			matched, delegated = principalMatch(st.Principal, who)
		case st.NotPrincipal != nil:
			m, d := principalMatch(st.NotPrincipal, who)
			matched = !m && !d
		default:
			continue
		}
		deny := st.Effect == "Deny"
		if !matched && !(deny && delegated) {
			continue // naming the account in a Deny covers everyone in it
		}
		if !st.applies(doc.Version, action, resource, ctx) {
			continue
		}
		switch {
		case deny:
			v.Deny = true
		case matched:
			v.Allow = true
		default:
			v.Delegated = true
		}
	}
	return v
}

// identityDecision maps the engine's decision onto httpx's.
func identityDecision(d decision) httpx.Decision {
	switch d {
	case allow:
		return httpx.Allowed
	case explicitDeny:
		return httpx.Denied
	}
	return httpx.NoDecision
}

// mentionsFunc reports whether any of the documents contains a text
// (lower-case), computed on first use.
func mentionsFunc(docs []PolicyDocument, boundary *PolicyDocument) func(string) bool {
	var once sync.Once
	var all string
	return func(text string) bool {
		once.Do(func() {
			var b strings.Builder
			for _, d := range docs {
				raw, _ := json.Marshal(d)
				b.Write(raw)
			}
			if boundary != nil {
				raw, _ := json.Marshal(*boundary)
				b.Write(raw)
			}
			all = strings.ToLower(b.String())
		})
		return strings.Contains(all, text)
	}
}

// identityFunc is a principal's Identity function over its policies.
func identityFunc(p *httpx.Principal, docs []PolicyDocument, boundary *PolicyDocument) func(action, resource string, keys map[string][]string) httpx.Decision {
	return func(action, resource string, keys map[string][]string) httpx.Decision {
		ctx := CondContext(p.Context)
		if len(keys) > 0 {
			ctx = CondContext{}
			for k, v := range p.Context {
				ctx[k] = v
			}
			for k, v := range keys {
				ctx[strings.ToLower(k)] = v
			}
		}
		return identityDecision(decide(docs, boundary, action, resource, ctx))
	}
}
