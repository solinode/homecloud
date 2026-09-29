package iam

import (
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

// ---- managed policies ----

func (s *Service) awsCreatePolicy(q *awsapi.Req) (any, error) {
	if err := required(q, "PolicyName", "PolicyDocument"); err != nil {
		return nil, err
	}
	d, err := docParam(q, "PolicyDocument")
	if err != nil {
		return nil, err
	}
	path, err := validPath(q.Param("Path"))
	if err != nil {
		return nil, err
	}
	in := PolicyInput{Name: q.Param("PolicyName"), Path: path, Description: q.Param("Description"), Document: d, Tags: tagsParam(q)}
	arn := s.policyARN(in.Name, path)
	keys := requestTagKeys(in.Tags)
	if err := authorizeWith(q, "iam:CreatePolicy", arn, keys); err != nil {
		return nil, err
	}
	if len(in.Tags) > 0 {
		if err := authorizeWith(q, "iam:TagPolicy", arn, keys); err != nil {
			return nil, err
		}
	}
	p, err := s.CreatePolicy(in)
	if err != nil {
		return nil, err
	}
	return map[string]any{"Policy": s.policyXML(p, true)}, nil
}

// policyOp authorizes action on the PolicyArn parameter and resolves the policy.
func (s *Service) policyOp(q *awsapi.Req, action string) (Policy, error) {
	if err := required(q, "PolicyArn"); err != nil {
		return Policy{}, err
	}
	ref := q.Param("PolicyArn")
	if err := s.authPolicy(q, action, ref); err != nil {
		return Policy{}, err
	}
	if !strings.HasPrefix(core.CanonicalARN(ref), "arn:") {
		return Policy{}, invalidInput("ARN %s is not valid.", ref)
	}
	return s.resolvePolicy(ref)
}

func (s *Service) awsGetPolicy(q *awsapi.Req) (any, error) {
	p, err := s.policyOp(q, "iam:GetPolicy")
	if err != nil {
		return nil, err
	}
	return map[string]any{"Policy": s.policyXML(p, true)}, nil
}

func (s *Service) awsListPolicies(q *awsapi.Req) (any, error) {
	if err := q.Authorize("iam:ListPolicies", q.ARN("iam", "policy/")); err != nil {
		return nil, err
	}
	scope := q.Param("Scope")
	if scope != "" && scope != "All" && scope != "AWS" && scope != "Local" {
		return nil, awsapi.Errorf(http.StatusBadRequest, "ValidationError", "Scope must be All, AWS or Local")
	}
	onlyAttached := q.ParamBool("OnlyAttached", false)
	usage := q.Param("PolicyUsageFilter")
	var out []Policy
	for _, p := range store.List[Policy](s.env.Store, cPolicies) {
		if (scope == "AWS" && !p.Managed) || (scope == "Local" && p.Managed) || !pathPrefix(q, p.path()) {
			continue
		}
		if onlyAttached {
			a := s.attachments(p)
			n := a.count() + a.Boundaries
			switch usage {
			case "PermissionsPolicy":
				n = a.count()
			case "PermissionsBoundary":
				n = a.Boundaries
			}
			if n == 0 {
				continue
			}
		}
		out = append(out, p)
	}
	return page(q, out, func(p Policy) string { return p.Name }, "Policies", func(p Policy) any { return s.policyXML(p, false) })
}

func (s *Service) awsDeletePolicy(q *awsapi.Req) (any, error) {
	p, err := s.policyOp(q, "iam:DeletePolicy")
	if err != nil {
		return nil, err
	}
	return awsapi.NoResult{}, s.DeletePolicy(p.ARN, false)
}

func (s *Service) awsCreatePolicyVersion(q *awsapi.Req) (any, error) {
	p, err := s.policyOp(q, "iam:CreatePolicyVersion")
	if err != nil {
		return nil, err
	}
	d, err := docParam(q, "PolicyDocument")
	if err != nil {
		return nil, err
	}
	p, v, err := s.CreatePolicyVersion(p.ARN, d, q.ParamBool("SetAsDefault", false), false)
	if err != nil {
		return nil, err
	}
	return map[string]any{"PolicyVersion": versionXML(p, v, false)}, nil
}

func (s *Service) awsGetPolicyVersion(q *awsapi.Req) (any, error) {
	p, err := s.policyOp(q, "iam:GetPolicyVersion")
	if err != nil {
		return nil, err
	}
	if err := required(q, "VersionId"); err != nil {
		return nil, err
	}
	v, ok := p.version(q.Param("VersionId"))
	if !ok {
		return nil, noSuchEntity("Policy %s version %s does not exist or is not attachable.", p.ARN, q.Param("VersionId"))
	}
	return map[string]any{"PolicyVersion": versionXML(p, v, true)}, nil
}

func (s *Service) awsListPolicyVersions(q *awsapi.Req) (any, error) {
	p, err := s.policyOp(q, "iam:ListPolicyVersions")
	if err != nil {
		return nil, err
	}
	// Newest first, as in AWS.
	key := func(v PolicyVersion) string {
		n, _ := strconv.Atoi(strings.TrimPrefix(v.ID, "v"))
		return fmt.Sprintf("%09d", 999999999-n)
	}
	return page(q, slices.Clone(p.Versions), key, "Versions", func(v PolicyVersion) any { return versionXML(p, v, false) })
}

func (s *Service) awsDeletePolicyVersion(q *awsapi.Req) (any, error) {
	p, err := s.policyOp(q, "iam:DeletePolicyVersion")
	if err != nil {
		return nil, err
	}
	if err := required(q, "VersionId"); err != nil {
		return nil, err
	}
	_, err = s.DeletePolicyVersion(p.ARN, q.Param("VersionId"))
	return awsapi.NoResult{}, err
}

func (s *Service) awsSetDefaultPolicyVersion(q *awsapi.Req) (any, error) {
	p, err := s.policyOp(q, "iam:SetDefaultPolicyVersion")
	if err != nil {
		return nil, err
	}
	if err := required(q, "VersionId"); err != nil {
		return nil, err
	}
	_, err = s.SetDefaultPolicyVersion(p.ARN, q.Param("VersionId"))
	return awsapi.NoResult{}, err
}

func (s *Service) awsTagPolicy(q *awsapi.Req) (any, error) {
	p, err := s.policyOp(q, "iam:TagPolicy")
	if err != nil {
		return nil, err
	}
	tags := tagsParam(q)
	if err := authorizeWith(q, "iam:TagPolicy", p.ARN, requestTagKeys(tags)); err != nil {
		return nil, err
	}
	_, err = s.UpdatePolicy(p.ARN, func(p *Policy) (err error) { p.Tags, err = mergeTags(p.Tags, tags); return err })
	return awsapi.NoResult{}, err
}

func (s *Service) awsUntagPolicy(q *awsapi.Req) (any, error) {
	p, err := s.policyOp(q, "iam:UntagPolicy")
	if err != nil {
		return nil, err
	}
	keys := q.List("TagKeys")
	_, err = s.UpdatePolicy(p.ARN, func(p *Policy) error { p.Tags = dropTags(p.Tags, keys); return nil })
	return awsapi.NoResult{}, err
}

func (s *Service) awsListPolicyTags(q *awsapi.Req) (any, error) {
	p, err := s.policyOp(q, "iam:ListPolicyTags")
	if err != nil {
		return nil, err
	}
	return tagsResult(p.Tags), nil
}

func (s *Service) awsListEntitiesForPolicy(q *awsapi.Req) (any, error) {
	p, err := s.policyOp(q, "iam:ListEntitiesForPolicy")
	if err != nil {
		return nil, err
	}
	filter := q.Param("EntityFilter")
	a := s.attachments(p)
	users, groups, roles := awsapi.Members{}, awsapi.Members{}, awsapi.Members{}
	if filter == "" || filter == "User" {
		for _, n := range a.Users {
			if u, err := s.getUserOr404(n); err == nil && pathPrefix(q, u.path()) {
				users = append(users, map[string]any{"UserName": u.Name, "UserId": u.ID})
			}
		}
	}
	if filter == "" || filter == "Group" {
		for _, n := range a.Groups {
			if g, err := s.getGroupOr404(n); err == nil && pathPrefix(q, g.path()) {
				groups = append(groups, map[string]any{"GroupName": g.Name, "GroupId": g.ID})
			}
		}
	}
	if filter == "" || filter == "Role" {
		for _, n := range a.Roles {
			if r, err := s.GetRole(n); err == nil && pathPrefix(q, r.path()) {
				roles = append(roles, map[string]any{"RoleName": r.Name, "RoleId": r.ID})
			}
		}
	}
	return map[string]any{"PolicyUsers": users, "PolicyGroups": groups, "PolicyRoles": roles, "IsTruncated": false}, nil
}

// ---- attachments and inline policies (users, groups, roles) ----

type entityKind int

const (
	kindUser entityKind = iota
	kindGroup
	kindRole
)

func (k entityKind) word() string  { return [...]string{"User", "Group", "Role"}[k] }
func (k entityKind) param() string { return k.word() + "Name" }

func (s *Service) authEntity(q *awsapi.Req, k entityKind, action, name string, extra ...CondContext) error {
	switch k {
	case kindUser:
		return s.authUser(q, action, name, extra...)
	case kindGroup:
		return s.authGroup(q, action, name, extra...)
	}
	return s.authRole(q, action, name, extra...)
}

// entityPolicies returns an entity's attached policy names and inline policies.
func (s *Service) entityPolicies(k entityKind, name string) ([]string, map[string]PolicyDocument, error) {
	switch k {
	case kindUser:
		u, err := s.getUserOr404(name)
		return u.AttachedPolicies, u.InlinePolicies, err
	case kindGroup:
		g, err := s.getGroupOr404(name)
		return g.AttachedPolicies, g.InlinePolicies, err
	}
	r, err := s.GetRole(name)
	return r.AttachedPolicies, r.InlinePolicies, err
}

func (s *Service) attachOp(k entityKind, attach bool) awsapi.Op {
	verb := "Detach"
	if attach {
		verb = "Attach"
	}
	action := "iam:" + verb + k.word() + "Policy"
	return func(q *awsapi.Req) (any, error) {
		if err := required(q, k.param(), "PolicyArn"); err != nil {
			return nil, err
		}
		name, ref := q.Param(k.param()), q.Param("PolicyArn")
		if err := s.authEntity(q, k, action, name, CondContext{"iam:policyarn": {core.CanonicalARN(ref)}}); err != nil {
			return nil, err
		}
		var err error
		switch {
		case k == kindUser && attach:
			_, err = s.AttachUserPolicy(name, ref)
		case k == kindUser:
			_, err = s.DetachUserPolicy(name, ref)
		case k == kindGroup && attach:
			_, err = s.AttachGroupPolicy(name, ref)
		case k == kindGroup:
			_, err = s.DetachGroupPolicy(name, ref)
		case attach:
			_, err = s.AttachRolePolicy(name, ref)
		default:
			_, err = s.DetachRolePolicy(name, ref)
		}
		return awsapi.NoResult{}, err
	}
}

func (s *Service) listAttachedOp(k entityKind) awsapi.Op {
	action := "iam:ListAttached" + k.word() + "Policies"
	return func(q *awsapi.Req) (any, error) {
		if err := required(q, k.param()); err != nil {
			return nil, err
		}
		name := q.Param(k.param())
		if err := s.authEntity(q, k, action, name); err != nil {
			return nil, err
		}
		names, _, err := s.entityPolicies(k, name)
		if err != nil {
			return nil, err
		}
		var policies []Policy
		for _, n := range names {
			if p, err := store.Get[Policy](s.env.Store, cPolicies, n); err == nil && pathPrefix(q, p.path()) {
				policies = append(policies, p)
			}
		}
		return page(q, policies, func(p Policy) string { return p.Name }, "AttachedPolicies",
			func(p Policy) any { return map[string]any{"PolicyName": p.Name, "PolicyArn": p.ARN} })
	}
}

func (s *Service) putInlineOp(k entityKind) awsapi.Op {
	action := "iam:Put" + k.word() + "Policy"
	return func(q *awsapi.Req) (any, error) {
		if err := required(q, k.param(), "PolicyName", "PolicyDocument"); err != nil {
			return nil, err
		}
		name, policy := q.Param(k.param()), q.Param("PolicyName")
		if err := s.authEntity(q, k, action, name); err != nil {
			return nil, err
		}
		d, err := docParam(q, "PolicyDocument")
		if err != nil {
			return nil, err
		}
		switch k {
		case kindUser:
			_, err = s.PutUserPolicy(name, policy, d)
		case kindGroup:
			_, err = s.PutGroupPolicy(name, policy, d)
		default:
			_, err = s.PutRolePolicy(name, policy, d)
		}
		return awsapi.NoResult{}, err
	}
}

func (s *Service) getInlineOp(k entityKind) awsapi.Op {
	action := "iam:Get" + k.word() + "Policy"
	return func(q *awsapi.Req) (any, error) {
		if err := required(q, k.param(), "PolicyName"); err != nil {
			return nil, err
		}
		name, policy := q.Param(k.param()), q.Param("PolicyName")
		if err := s.authEntity(q, k, action, name); err != nil {
			return nil, err
		}
		_, inline, err := s.entityPolicies(k, name)
		if err != nil {
			return nil, err
		}
		d, ok := inline[policy]
		if !ok {
			return nil, noSuchEntity("The %s policy with name %s cannot be found.", strings.ToLower(k.word()), policy)
		}
		return map[string]any{k.param(): roleName(name), "PolicyName": policy, "PolicyDocument": encDoc(docJSON(d))}, nil
	}
}

func (s *Service) deleteInlineOp(k entityKind) awsapi.Op {
	action := "iam:Delete" + k.word() + "Policy"
	return func(q *awsapi.Req) (any, error) {
		if err := required(q, k.param(), "PolicyName"); err != nil {
			return nil, err
		}
		name, policy := q.Param(k.param()), q.Param("PolicyName")
		if err := s.authEntity(q, k, action, name); err != nil {
			return nil, err
		}
		var err error
		switch k {
		case kindUser:
			_, err = s.DeleteUserPolicy(name, policy)
		case kindGroup:
			_, err = s.DeleteGroupPolicy(name, policy)
		default:
			_, err = s.DeleteRolePolicy(name, policy)
		}
		return awsapi.NoResult{}, err
	}
}

func (s *Service) listInlineOp(k entityKind) awsapi.Op {
	action := "iam:List" + k.word() + "Policies"
	return func(q *awsapi.Req) (any, error) {
		if err := required(q, k.param()); err != nil {
			return nil, err
		}
		name := q.Param(k.param())
		if err := s.authEntity(q, k, action, name); err != nil {
			return nil, err
		}
		_, inline, err := s.entityPolicies(k, name)
		if err != nil {
			return nil, err
		}
		names := make([]string, 0, len(inline))
		for n := range inline {
			names = append(names, n)
		}
		sort.Strings(names)
		return page(q, names, func(n string) string { return n }, "PolicyNames", func(n string) any { return n })
	}
}
