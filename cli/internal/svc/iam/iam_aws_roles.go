package iam

import (
	"encoding/json"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

// ---- roles ----

func (s *Service) awsCreateRole(q *awsapi.Req) (any, error) {
	if err := required(q, "RoleName", "AssumeRolePolicyDocument"); err != nil {
		return nil, err
	}
	path, err := validPath(q.Param("Path"))
	if err != nil {
		return nil, err
	}
	trust, _ := json.Marshal(q.Param("AssumeRolePolicyDocument"))
	in := RoleInput{Name: q.Param("RoleName"), Path: path, Description: q.Param("Description"), TrustPolicy: trust,
		MaxSessionSeconds: q.ParamInt("MaxSessionDuration", 0), Tags: tagsParam(q), PermissionsBoundary: q.Param("PermissionsBoundary")}
	keys := requestTagKeys(in.Tags)
	if in.PermissionsBoundary != "" {
		keys["iam:permissionsboundary"] = []string{core.CanonicalARN(in.PermissionsBoundary)}
	}
	if err := authorizeWith(q, "iam:CreateRole", s.roleARN(in.Name, path), keys); err != nil {
		return nil, err
	}
	r, err := s.CreateRole(in, authzWith(q, keys))
	if err != nil {
		return nil, err
	}
	return map[string]any{"Role": roleXML(r, true)}, nil
}

func (s *Service) roleOp(q *awsapi.Req, action string, extra ...CondContext) (Role, error) {
	if err := required(q, "RoleName"); err != nil {
		return Role{}, err
	}
	if err := s.authRole(q, action, q.Param("RoleName"), extra...); err != nil {
		return Role{}, err
	}
	return s.GetRole(q.Param("RoleName"))
}

func (s *Service) awsGetRole(q *awsapi.Req) (any, error) {
	r, err := s.roleOp(q, "iam:GetRole")
	if err != nil {
		return nil, err
	}
	return map[string]any{"Role": roleXML(r, true)}, nil
}

func (s *Service) awsListRoles(q *awsapi.Req) (any, error) {
	if err := q.Authorize("iam:ListRoles", q.ARN("iam", "role/")); err != nil {
		return nil, err
	}
	var roles []Role
	for _, r := range store.List[Role](s.env.Store, cRoles) {
		if pathPrefix(q, r.path()) {
			roles = append(roles, r)
		}
	}
	return page(q, roles, func(r Role) string { return r.Name }, "Roles", func(r Role) any { return roleXML(r, false) })
}

func (s *Service) awsDeleteRole(q *awsapi.Req) (any, error) {
	r, err := s.roleOp(q, "iam:DeleteRole")
	if err != nil {
		return nil, err
	}
	if r.ServiceLinked != "" {
		return nil, unmodifiable("Cannot perform the operation on the protected role '%s' - this role is only modifiable by AWS", r.Name)
	}
	return awsapi.NoResult{}, s.DeleteRole(r.Name)
}

func (s *Service) awsUpdateRole(q *awsapi.Req) (any, error) {
	if _, err := s.roleOp(q, "iam:UpdateRole"); err != nil {
		return nil, err
	}
	_, err := s.UpdateRole(q.Param("RoleName"), func(r *Role) error {
		if _, ok := q.Form["Description"]; ok {
			r.Description = q.Param("Description")
		}
		if n := q.ParamInt("MaxSessionDuration", 0); n != 0 {
			if err := checkMaxSession(n); err != nil {
				return err
			}
			r.MaxSessionSeconds = n
		}
		return nil
	})
	return map[string]any{}, err
}

func (s *Service) awsUpdateRoleDescription(q *awsapi.Req) (any, error) {
	if _, err := s.roleOp(q, "iam:UpdateRoleDescription"); err != nil {
		return nil, err
	}
	r, err := s.UpdateRole(q.Param("RoleName"), func(r *Role) error { r.Description = q.Param("Description"); return nil })
	if err != nil {
		return nil, err
	}
	return map[string]any{"Role": roleXML(r, true)}, nil
}

func (s *Service) awsUpdateAssumeRolePolicy(q *awsapi.Req) (any, error) {
	if err := required(q, "RoleName", "PolicyDocument"); err != nil {
		return nil, err
	}
	r, err := s.roleOp(q, "iam:UpdateAssumeRolePolicy")
	if err != nil {
		return nil, err
	}
	if r.ServiceLinked != "" {
		return nil, unmodifiable("Cannot perform the operation on the protected role '%s' - this role is only modifiable by AWS", r.Name)
	}
	_, err = s.SetTrustPolicy(r.Name, q.Param("PolicyDocument"))
	return awsapi.NoResult{}, err
}

func (s *Service) awsTagRole(q *awsapi.Req) (any, error) {
	tags := tagsParam(q)
	if _, err := s.roleOp(q, "iam:TagRole", requestTagKeys(tags)); err != nil {
		return nil, err
	}
	_, err := s.UpdateRole(q.Param("RoleName"), func(r *Role) (err error) { r.Tags, err = mergeTags(r.Tags, tags); return err })
	return awsapi.NoResult{}, err
}

func (s *Service) awsUntagRole(q *awsapi.Req) (any, error) {
	keys := q.List("TagKeys")
	if _, err := s.roleOp(q, "iam:UntagRole", CondContext{"aws:tagkeys": keys}); err != nil {
		return nil, err
	}
	_, err := s.UpdateRole(q.Param("RoleName"), func(r *Role) error { r.Tags = dropTags(r.Tags, keys); return nil })
	return awsapi.NoResult{}, err
}

func (s *Service) awsListRoleTags(q *awsapi.Req) (any, error) {
	r, err := s.roleOp(q, "iam:ListRoleTags")
	if err != nil {
		return nil, err
	}
	return tagsResult(r.Tags), nil
}

func (s *Service) awsPutRoleBoundary(q *awsapi.Req) (any, error) {
	if err := required(q, "RoleName", "PermissionsBoundary"); err != nil {
		return nil, err
	}
	b := q.Param("PermissionsBoundary")
	if _, err := s.roleOp(q, "iam:PutRolePermissionsBoundary", CondContext{"iam:permissionsboundary": {core.CanonicalARN(b)}}); err != nil {
		return nil, err
	}
	_, err := s.SetRoleBoundary(q.Param("RoleName"), b)
	return awsapi.NoResult{}, err
}

func (s *Service) awsDeleteRoleBoundary(q *awsapi.Req) (any, error) {
	if _, err := s.roleOp(q, "iam:DeleteRolePermissionsBoundary"); err != nil {
		return nil, err
	}
	_, err := s.SetRoleBoundary(q.Param("RoleName"), "")
	return awsapi.NoResult{}, err
}

func (s *Service) awsListInstanceProfilesForRole(q *awsapi.Req) (any, error) {
	r, err := s.roleOp(q, "iam:ListInstanceProfilesForRole")
	if err != nil {
		return nil, err
	}
	var profiles []InstanceProfile
	for _, n := range s.profilesForRole(r.Name) {
		if p, err := s.GetInstanceProfile(n); err == nil {
			profiles = append(profiles, p)
		}
	}
	return page(q, profiles, func(p InstanceProfile) string { return p.Name }, "InstanceProfiles", func(p InstanceProfile) any { return s.profileXML(p) })
}

func (s *Service) awsCreateServiceLinkedRole(q *awsapi.Req) (any, error) {
	if err := required(q, "AWSServiceName"); err != nil {
		return nil, err
	}
	service := q.Param("AWSServiceName")
	if err := authorizeWith(q, "iam:CreateServiceLinkedRole", q.ARN("iam", "role/aws-service-role/"+service+"/*"),
		CondContext{"iam:awsservicename": {service}}); err != nil {
		return nil, err
	}
	r, err := s.CreateServiceLinkedRole(service, q.Param("Description"), q.Param("CustomSuffix"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"Role": roleXML(r, true)}, nil
}

func (s *Service) awsDeleteServiceLinkedRole(q *awsapi.Req) (any, error) {
	r, err := s.roleOp(q, "iam:DeleteServiceLinkedRole")
	if err != nil {
		return nil, err
	}
	if err := s.DeleteServiceLinkedRole(r.Name); err != nil {
		return nil, err
	}
	return map[string]any{"DeletionTaskId": "task" + r.Path + r.Name + "/" + strings.ToLower(core.RandHex(16))}, nil
}

// Deletion is immediate, so every task has succeeded.
func (s *Service) awsGetServiceLinkedRoleDeletionStatus(q *awsapi.Req) (any, error) {
	if err := required(q, "DeletionTaskId"); err != nil {
		return nil, err
	}
	if err := q.Authorize("iam:GetServiceLinkedRoleDeletionStatus", "*"); err != nil {
		return nil, err
	}
	return map[string]any{"Status": "SUCCEEDED"}, nil
}

// ---- instance profiles ----

func (s *Service) awsCreateInstanceProfile(q *awsapi.Req) (any, error) {
	if err := required(q, "InstanceProfileName"); err != nil {
		return nil, err
	}
	path, err := validPath(q.Param("Path"))
	if err != nil {
		return nil, err
	}
	tags := tagsParam(q)
	if err := authorizeWith(q, "iam:CreateInstanceProfile", s.profileARN(q.Param("InstanceProfileName"), path), requestTagKeys(tags)); err != nil {
		return nil, err
	}
	p, err := s.CreateInstanceProfile(q.Param("InstanceProfileName"), path, tags)
	if err != nil {
		return nil, err
	}
	return map[string]any{"InstanceProfile": s.profileXML(p)}, nil
}

func (s *Service) profileOp(q *awsapi.Req, action string, extra ...CondContext) (InstanceProfile, error) {
	if err := required(q, "InstanceProfileName"); err != nil {
		return InstanceProfile{}, err
	}
	if err := s.authProfile(q, action, q.Param("InstanceProfileName"), extra...); err != nil {
		return InstanceProfile{}, err
	}
	return s.GetInstanceProfile(q.Param("InstanceProfileName"))
}

func (s *Service) awsGetInstanceProfile(q *awsapi.Req) (any, error) {
	p, err := s.profileOp(q, "iam:GetInstanceProfile")
	if err != nil {
		return nil, err
	}
	return map[string]any{"InstanceProfile": s.profileXML(p)}, nil
}

func (s *Service) awsListInstanceProfiles(q *awsapi.Req) (any, error) {
	if err := q.Authorize("iam:ListInstanceProfiles", q.ARN("iam", "instance-profile/")); err != nil {
		return nil, err
	}
	var out []InstanceProfile
	for _, p := range store.List[InstanceProfile](s.env.Store, cProfiles) {
		if pathPrefix(q, p.Path) {
			out = append(out, p)
		}
	}
	return page(q, out, func(p InstanceProfile) string { return p.Name }, "InstanceProfiles", func(p InstanceProfile) any { return s.profileXML(p) })
}

func (s *Service) awsDeleteInstanceProfile(q *awsapi.Req) (any, error) {
	p, err := s.profileOp(q, "iam:DeleteInstanceProfile")
	if err != nil {
		return nil, err
	}
	return awsapi.NoResult{}, s.DeleteInstanceProfile(p.Name, false)
}

// awsAddRoleToInstanceProfile needs iam:PassRole on the role, as in AWS:
// whoever launches an instance with the profile acts as the role.
func (s *Service) awsAddRoleToInstanceProfile(q *awsapi.Req) (any, error) {
	if err := required(q, "RoleName"); err != nil {
		return nil, err
	}
	p, err := s.profileOp(q, "iam:AddRoleToInstanceProfile")
	if err != nil {
		return nil, err
	}
	if err := s.authRole(q, "iam:PassRole", q.Param("RoleName")); err != nil {
		return nil, err
	}
	_, err = s.AddRoleToInstanceProfile(p.Name, q.Param("RoleName"))
	return awsapi.NoResult{}, err
}

func (s *Service) awsRemoveRoleFromInstanceProfile(q *awsapi.Req) (any, error) {
	if err := required(q, "RoleName"); err != nil {
		return nil, err
	}
	p, err := s.profileOp(q, "iam:RemoveRoleFromInstanceProfile")
	if err != nil {
		return nil, err
	}
	_, err = s.RemoveRoleFromInstanceProfile(p.Name, q.Param("RoleName"))
	return awsapi.NoResult{}, err
}

func (s *Service) awsTagInstanceProfile(q *awsapi.Req) (any, error) {
	tags := tagsParam(q)
	p, err := s.profileOp(q, "iam:TagInstanceProfile", requestTagKeys(tags))
	if err != nil {
		return nil, err
	}
	_, err = s.UpdateInstanceProfile(p.Name, func(p *InstanceProfile) (err error) { p.Tags, err = mergeTags(p.Tags, tags); return err })
	return awsapi.NoResult{}, err
}

func (s *Service) awsUntagInstanceProfile(q *awsapi.Req) (any, error) {
	keys := q.List("TagKeys")
	p, err := s.profileOp(q, "iam:UntagInstanceProfile", CondContext{"aws:tagkeys": keys})
	if err != nil {
		return nil, err
	}
	_, err = s.UpdateInstanceProfile(p.Name, func(p *InstanceProfile) error { p.Tags = dropTags(p.Tags, keys); return nil })
	return awsapi.NoResult{}, err
}

func (s *Service) awsListInstanceProfileTags(q *awsapi.Req) (any, error) {
	p, err := s.profileOp(q, "iam:ListInstanceProfileTags")
	if err != nil {
		return nil, err
	}
	return tagsResult(p.Tags), nil
}

// ---- account ----

func (s *Service) awsGetAccountSummary(q *awsapi.Req) (any, error) {
	if err := q.Authorize("iam:GetAccountSummary", "*"); err != nil {
		return nil, err
	}
	users := store.List[User](s.env.Store, cUsers)
	local, attached, mfa := 0, 0, 0
	for _, p := range store.List[Policy](s.env.Store, cPolicies) {
		if !p.Managed {
			local++
		}
		if s.attachments(p).count() > 0 {
			attached++
		}
	}
	root := 0
	for _, k := range s.UserKeys(RootUser) {
		if k.Status == "Active" {
			root = 1
		}
	}
	summary := map[string]int{
		"Users": len(users), "UsersQuota": 5000, "Groups": len(store.List[Group](s.env.Store, cGroups)), "GroupsQuota": 300,
		"Roles": len(store.List[Role](s.env.Store, cRoles)), "RolesQuota": 1000, "Policies": local, "PoliciesQuota": 1500,
		"InstanceProfiles": len(store.List[InstanceProfile](s.env.Store, cProfiles)), "InstanceProfilesQuota": 1000,
		"AttachedPoliciesPerUserQuota": maxAttached, "AttachedPoliciesPerGroupQuota": maxAttached, "AttachedPoliciesPerRoleQuota": maxAttached,
		"PolicyVersionsInUse": attached, "PolicyVersionsInUseQuota": 10000, "VersionsPerPolicyQuota": maxPolicyVersions,
		"AccessKeysPerUserQuota": maxAccessKeys, "GroupsPerUserQuota": 10, "PolicySizeQuota": maxManagedPolicySize,
		"UserPolicySizeQuota": 2048, "GroupPolicySizeQuota": 5120, "AssumeRolePolicySizeQuota": 2048,
		"AccountAccessKeysPresent": root, "AccountMFAEnabled": 0, "MFADevices": mfa, "MFADevicesInUse": 0,
		"ServerCertificates": 0, "ServerCertificatesQuota": 20, "SigningCertificatesPerUserQuota": 2,
		"GlobalEndpointTokenVersion": 1, "Providers": 0,
	}
	keys := make([]string, 0, len(summary))
	for k := range summary {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	entries := awsapi.Named{Name: "entry"}
	for _, k := range keys {
		entries.Values = append(entries.Values, awsapi.Ordered{{K: "key", V: k}, {K: "value", V: summary[k]}})
	}
	return map[string]any{"SummaryMap": entries}, nil
}

func (s *Service) awsGetAccountAuthorizationDetails(q *awsapi.Req) (any, error) {
	if err := q.Authorize("iam:GetAccountAuthorizationDetails", "*"); err != nil {
		return nil, err
	}
	filter := q.List("Filter")
	want := func(t string) bool { return len(filter) == 0 || slices.Contains(filter, t) }
	inlineList := func(m map[string]PolicyDocument) awsapi.Members {
		names := make([]string, 0, len(m))
		for n := range m {
			names = append(names, n)
		}
		sort.Strings(names)
		out := awsapi.Members{}
		for _, n := range names {
			out = append(out, map[string]any{"PolicyName": n, "PolicyDocument": encDoc(docJSON(m[n]))})
		}
		return out
	}
	attachedList := func(names []string) awsapi.Members {
		out := awsapi.Members{}
		for _, n := range names {
			if p, err := store.Get[Policy](s.env.Store, cPolicies, n); err == nil {
				out = append(out, map[string]any{"PolicyName": p.Name, "PolicyArn": p.ARN})
			}
		}
		return out
	}
	res := map[string]any{"IsTruncated": false}
	users, groups, roles, policies := awsapi.Members{}, awsapi.Members{}, awsapi.Members{}, awsapi.Members{}
	if want("User") {
		for _, u := range store.List[User](s.env.Store, cUsers) {
			m := userXML(u, true)
			delete(m, "PasswordLastUsed")
			m["UserPolicyList"], m["GroupList"], m["AttachedManagedPolicies"] = inlineList(u.InlinePolicies), awsapi.Members(awsapi.Strings(u.Groups)), attachedList(u.AttachedPolicies)
			users = append(users, m)
		}
	}
	if want("Group") {
		for _, g := range store.List[Group](s.env.Store, cGroups) {
			m := groupXML(g)
			m["GroupPolicyList"], m["AttachedManagedPolicies"] = inlineList(g.InlinePolicies), attachedList(g.AttachedPolicies)
			groups = append(groups, m)
		}
	}
	if want("Role") {
		for _, r := range store.List[Role](s.env.Store, cRoles) {
			m := roleXML(r, true)
			profiles := awsapi.Members{}
			for _, n := range s.profilesForRole(r.Name) {
				if p, err := s.GetInstanceProfile(n); err == nil {
					profiles = append(profiles, s.profileXML(p))
				}
			}
			m["RolePolicyList"], m["AttachedManagedPolicies"], m["InstanceProfileList"] = inlineList(r.InlinePolicies), attachedList(r.AttachedPolicies), profiles
			roles = append(roles, m)
		}
	}
	for _, p := range store.List[Policy](s.env.Store, cPolicies) {
		switch {
		case p.Managed && !want("AWSManagedPolicy"), !p.Managed && !want("LocalManagedPolicy"):
			continue
		case p.Managed && len(filter) == 0 && s.attachments(p).count() == 0:
			continue // like AWS, unfiltered results include only AWS managed policies in use
		}
		m := s.policyXML(p, false)
		if p.Description != "" {
			m["Description"] = p.Description
		}
		versions := awsapi.Members{}
		for _, v := range p.Versions {
			versions = append(versions, versionXML(p, v, true))
		}
		m["PolicyVersionList"] = versions
		policies = append(policies, m)
	}
	res["UserDetailList"], res["GroupDetailList"], res["RoleDetailList"], res["Policies"] = users, groups, roles, policies
	return res, nil
}

func (s *Service) awsListAccountAliases(q *awsapi.Req) (any, error) {
	if err := q.Authorize("iam:ListAccountAliases", "*"); err != nil {
		return nil, err
	}
	return map[string]any{"AccountAliases": awsapi.Members{}, "IsTruncated": false}, nil
}

func (s *Service) awsGetAccountPasswordPolicy(q *awsapi.Req) (any, error) {
	if err := q.Authorize("iam:GetAccountPasswordPolicy", "*"); err != nil {
		return nil, err
	}
	return nil, noSuchEntity("The Password Policy with domain name %s cannot be found.", s.env.AccountID)
}

// contextParam reads ContextEntries (SimulatePrincipalPolicy/SimulateCustomPolicy).
func contextParam(q *awsapi.Req) CondContext {
	ctx := CondContext{}
	for _, e := range q.Structs("ContextEntries") {
		var vals []string
		for i := 1; ; i++ {
			v, ok := e["ContextKeyValues.member."+strconv.Itoa(i)]
			if !ok {
				break
			}
			vals = append(vals, v)
		}
		if k := e["ContextKeyName"]; k != "" {
			ctx[strings.ToLower(k)] = vals
		}
	}
	return ctx
}

// simulate evaluates ActionNames x ResourceArns for a target.
func simulateResult(q *awsapi.Req, t simTarget) (any, error) {
	actions := q.List("ActionNames")
	if len(actions) == 0 {
		return nil, required(q, "ActionNames")
	}
	resources := q.List("ResourceArns")
	if len(resources) == 0 {
		resources = []string{"*"}
	}
	extra := contextParam(q)
	results := awsapi.Members{}
	for _, a := range actions {
		for _, r := range resources {
			results = append(results, map[string]any{"EvalActionName": a, "EvalResourceName": r,
				"EvalDecision": t.decide(a, r, extra).String(), "MatchedStatements": awsapi.Members{}, "MissingContextValues": awsapi.Members{}})
		}
	}
	return map[string]any{"EvaluationResults": results, "IsTruncated": false}, nil
}

func (s *Service) awsSimulatePrincipalPolicy(q *awsapi.Req) (any, error) {
	if err := required(q, "PolicySourceArn"); err != nil {
		return nil, err
	}
	src := q.Param("PolicySourceArn")
	if err := q.Authorize("iam:SimulatePrincipalPolicy", core.CanonicalARN(src)); err != nil {
		return nil, err
	}
	t, err := s.simTarget(src)
	if err != nil {
		return nil, err
	}
	extraDocs, err := policyListParam(q, "PolicyInputList")
	if err != nil {
		return nil, err
	}
	t.docs = append(t.docs, extraDocs...)
	return simulateResult(q, t)
}

func (s *Service) awsSimulateCustomPolicy(q *awsapi.Req) (any, error) {
	if err := q.Authorize("iam:SimulateCustomPolicy", "*"); err != nil {
		return nil, err
	}
	docs, err := policyListParam(q, "PolicyInputList")
	if err != nil {
		return nil, err
	}
	if len(docs) == 0 {
		return nil, required(q, "PolicyInputList")
	}
	return simulateResult(q, simTarget{docs: docs})
}

func policyListParam(q *awsapi.Req, name string) ([]PolicyDocument, error) {
	var docs []PolicyDocument
	for _, text := range q.List(name) {
		d, err := ParsePolicy(text)
		if err == nil {
			err = d.Validate()
		}
		if err != nil {
			return nil, err
		}
		docs = append(docs, d)
	}
	return docs, nil
}

// contextKeys lists the condition keys a set of policies uses.
func contextKeys(docs []PolicyDocument) awsapi.Members {
	seen := map[string]bool{}
	for _, d := range docs {
		for _, st := range d.Statement {
			conds, err := parseConditions(st.Condition)
			if len(st.Condition) == 0 || err != nil {
				continue
			}
			for _, c := range conds {
				seen[c.key] = true
			}
		}
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return awsapi.Members(awsapi.Strings(keys))
}

func (s *Service) awsGetContextKeysForPrincipalPolicy(q *awsapi.Req) (any, error) {
	if err := required(q, "PolicySourceArn"); err != nil {
		return nil, err
	}
	if err := q.Authorize("iam:GetContextKeysForPrincipalPolicy", core.CanonicalARN(q.Param("PolicySourceArn"))); err != nil {
		return nil, err
	}
	t, err := s.simTarget(q.Param("PolicySourceArn"))
	if err != nil {
		return nil, err
	}
	extra, err := policyListParam(q, "PolicyInputList")
	if err != nil {
		return nil, err
	}
	return map[string]any{"ContextKeyNames": contextKeys(append(t.docs, extra...))}, nil
}

func (s *Service) awsGetContextKeysForCustomPolicy(q *awsapi.Req) (any, error) {
	if err := q.Authorize("iam:GetContextKeysForCustomPolicy", "*"); err != nil {
		return nil, err
	}
	docs, err := policyListParam(q, "PolicyInputList")
	if err != nil {
		return nil, err
	}
	if len(docs) == 0 {
		return nil, awsapi.Errorf(http.StatusBadRequest, "ValidationError", "PolicyInputList is required")
	}
	return map[string]any{"ContextKeyNames": contextKeys(docs)}, nil
}
