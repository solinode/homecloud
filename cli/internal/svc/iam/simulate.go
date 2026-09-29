package iam

import (
	"strings"

	"github.com/homecloudhq/homecloud/cli/internal/core"
)

// simTarget is an identity whose policies are simulated (SimulatePrincipalPolicy).
type simTarget struct {
	docs     []PolicyDocument
	boundary *PolicyDocument
	root     bool
	ctx      CondContext
}

// simTarget resolves a user name, or the ARN of a user, role or group.
func (s *Service) simTarget(ref string) (simTarget, error) {
	ref = core.CanonicalARN(ref)
	last := ref[strings.LastIndexByte(ref, '/')+1:]
	switch {
	case strings.Contains(ref, ":role/"):
		r, err := s.GetRole(ref)
		if err != nil {
			return simTarget{}, err
		}
		return simTarget{docs: s.roleDocs(r), boundary: s.boundaryDoc(r.PermissionsBoundary), ctx: s.roleContext(r, tempCred{SessionName: "simulation"})}, nil
	case strings.Contains(ref, ":group/"):
		g, err := s.getGroupOr404(last)
		if err != nil {
			return simTarget{}, err
		}
		docs := s.managedDocs(g.AttachedPolicies)
		for _, d := range g.InlinePolicies {
			docs = append(docs, d)
		}
		return simTarget{docs: docs, ctx: CondContext{"aws:principalaccount": {s.env.AccountID}}}, nil
	case strings.Contains(ref, ":user/"), !strings.HasPrefix(ref, "arn:"):
		u, err := s.getUserOr404(last)
		if err != nil {
			return simTarget{}, err
		}
		return simTarget{docs: s.effectiveDocs(u), boundary: s.boundaryDoc(u.PermissionsBoundary), root: u.Root, ctx: s.userContext(u)}, nil
	}
	return simTarget{}, invalidInput("PolicySourceArn %s must be the ARN of a user, group or role.", ref)
}

func (t simTarget) decide(action, resource string, extra CondContext) decision {
	if t.root {
		return allow
	}
	ctx := CondContext{}
	for k, v := range t.ctx {
		ctx[k] = v
	}
	for k, v := range extra {
		ctx[k] = v
	}
	return decide(t.docs, t.boundary, action, resource, ctx)
}
