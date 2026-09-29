package iam

import (
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

// The IAM API over the AWS Query protocol (aws iam ..., boto3, Terraform).
// Every operation authorizes with q.Authorize (through the helpers below)
// before it acts, and calls the same methods as the native API.

const iamNS = "https://iam.amazonaws.com/doc/2010-05-08/"

func (s *Service) registerIAM() {
	ops := map[string]awsapi.Op{
		// users
		"CreateUser": s.awsCreateUser, "GetUser": s.awsGetUser, "ListUsers": s.awsListUsers, "DeleteUser": s.awsDeleteUser,
		"UpdateUser": s.awsUpdateUser, "TagUser": s.awsTagUser, "UntagUser": s.awsUntagUser, "ListUserTags": s.awsListUserTags,
		"CreateLoginProfile": s.awsCreateLoginProfile, "GetLoginProfile": s.awsGetLoginProfile,
		"UpdateLoginProfile": s.awsUpdateLoginProfile, "DeleteLoginProfile": s.awsDeleteLoginProfile, "ChangePassword": s.awsChangePassword,
		"PutUserPermissionsBoundary": s.awsPutUserBoundary, "DeleteUserPermissionsBoundary": s.awsDeleteUserBoundary,
		// access keys
		"CreateAccessKey": s.awsCreateAccessKey, "ListAccessKeys": s.awsListAccessKeys, "UpdateAccessKey": s.awsUpdateAccessKey,
		"DeleteAccessKey": s.awsDeleteAccessKey, "GetAccessKeyLastUsed": s.awsGetAccessKeyLastUsed,
		// groups
		"CreateGroup": s.awsCreateGroup, "GetGroup": s.awsGetGroup, "ListGroups": s.awsListGroups, "ListGroupsForUser": s.awsListGroupsForUser,
		"DeleteGroup": s.awsDeleteGroup, "UpdateGroup": s.awsUpdateGroup, "AddUserToGroup": s.awsAddUserToGroup, "RemoveUserFromGroup": s.awsRemoveUserFromGroup,
		// managed policies
		"CreatePolicy": s.awsCreatePolicy, "GetPolicy": s.awsGetPolicy, "ListPolicies": s.awsListPolicies, "DeletePolicy": s.awsDeletePolicy,
		"CreatePolicyVersion": s.awsCreatePolicyVersion, "GetPolicyVersion": s.awsGetPolicyVersion, "ListPolicyVersions": s.awsListPolicyVersions,
		"DeletePolicyVersion": s.awsDeletePolicyVersion, "SetDefaultPolicyVersion": s.awsSetDefaultPolicyVersion,
		"TagPolicy": s.awsTagPolicy, "UntagPolicy": s.awsUntagPolicy, "ListPolicyTags": s.awsListPolicyTags,
		"ListEntitiesForPolicy": s.awsListEntitiesForPolicy,
		// attachments and inline policies
		"AttachUserPolicy": s.attachOp(kindUser, true), "DetachUserPolicy": s.attachOp(kindUser, false),
		"AttachGroupPolicy": s.attachOp(kindGroup, true), "DetachGroupPolicy": s.attachOp(kindGroup, false),
		"AttachRolePolicy": s.attachOp(kindRole, true), "DetachRolePolicy": s.attachOp(kindRole, false),
		"ListAttachedUserPolicies": s.listAttachedOp(kindUser), "ListAttachedGroupPolicies": s.listAttachedOp(kindGroup),
		"ListAttachedRolePolicies": s.listAttachedOp(kindRole),
		"PutUserPolicy":            s.putInlineOp(kindUser), "GetUserPolicy": s.getInlineOp(kindUser), "DeleteUserPolicy": s.deleteInlineOp(kindUser), "ListUserPolicies": s.listInlineOp(kindUser),
		"PutGroupPolicy": s.putInlineOp(kindGroup), "GetGroupPolicy": s.getInlineOp(kindGroup), "DeleteGroupPolicy": s.deleteInlineOp(kindGroup), "ListGroupPolicies": s.listInlineOp(kindGroup),
		"PutRolePolicy": s.putInlineOp(kindRole), "GetRolePolicy": s.getInlineOp(kindRole), "DeleteRolePolicy": s.deleteInlineOp(kindRole), "ListRolePolicies": s.listInlineOp(kindRole),
		// roles
		"CreateRole": s.awsCreateRole, "GetRole": s.awsGetRole, "ListRoles": s.awsListRoles, "DeleteRole": s.awsDeleteRole,
		"UpdateRole": s.awsUpdateRole, "UpdateRoleDescription": s.awsUpdateRoleDescription, "UpdateAssumeRolePolicy": s.awsUpdateAssumeRolePolicy,
		"TagRole": s.awsTagRole, "UntagRole": s.awsUntagRole, "ListRoleTags": s.awsListRoleTags,
		"PutRolePermissionsBoundary": s.awsPutRoleBoundary, "DeleteRolePermissionsBoundary": s.awsDeleteRoleBoundary,
		"ListInstanceProfilesForRole": s.awsListInstanceProfilesForRole,
		"CreateServiceLinkedRole":     s.awsCreateServiceLinkedRole, "DeleteServiceLinkedRole": s.awsDeleteServiceLinkedRole,
		"GetServiceLinkedRoleDeletionStatus": s.awsGetServiceLinkedRoleDeletionStatus,
		// instance profiles
		"CreateInstanceProfile": s.awsCreateInstanceProfile, "GetInstanceProfile": s.awsGetInstanceProfile,
		"ListInstanceProfiles": s.awsListInstanceProfiles, "DeleteInstanceProfile": s.awsDeleteInstanceProfile,
		"AddRoleToInstanceProfile": s.awsAddRoleToInstanceProfile, "RemoveRoleFromInstanceProfile": s.awsRemoveRoleFromInstanceProfile,
		"TagInstanceProfile": s.awsTagInstanceProfile, "UntagInstanceProfile": s.awsUntagInstanceProfile, "ListInstanceProfileTags": s.awsListInstanceProfileTags,
		// account
		"GetAccountSummary": s.awsGetAccountSummary, "GetAccountAuthorizationDetails": s.awsGetAccountAuthorizationDetails,
		"ListAccountAliases": s.awsListAccountAliases, "GetAccountPasswordPolicy": s.awsGetAccountPasswordPolicy,
		"SimulatePrincipalPolicy": s.awsSimulatePrincipalPolicy, "SimulateCustomPolicy": s.awsSimulateCustomPolicy,
		"GetContextKeysForPrincipalPolicy": s.awsGetContextKeysForPrincipalPolicy, "GetContextKeysForCustomPolicy": s.awsGetContextKeysForCustomPolicy,
		// credentials HomeCloud does not have: always empty, so tools that clean them up work
		"ListMFADevices": s.emptyList("iam:ListMFADevices", "MFADevices", true), "ListVirtualMFADevices": s.emptyList("iam:ListVirtualMFADevices", "VirtualMFADevices", false),
		"ListSSHPublicKeys": s.emptyList("iam:ListSSHPublicKeys", "SSHPublicKeys", true), "ListSigningCertificates": s.emptyList("iam:ListSigningCertificates", "Certificates", true),
		"ListServiceSpecificCredentials": s.emptyList("iam:ListServiceSpecificCredentials", "ServiceSpecificCredentials", true),
		"ListOpenIDConnectProviders":     s.emptyList("iam:ListOpenIDConnectProviders", "OpenIDConnectProviderList", false),
		"ListSAMLProviders":              s.emptyList("iam:ListSAMLProviders", "SAMLProviderList", false),
		"ListServerCertificates":         s.emptyList("iam:ListServerCertificates", "ServerCertificateMetadataList", false),
	}
	for name, op := range ops {
		ops[name] = wrapIAM(op)
	}
	awsapi.Register(&awsapi.Service{Name: "iam", XMLNS: iamNS, Ops: ops})
}

// wrapIAM reports errors with IAM's codes and HTTP statuses (NoSuchEntity is
// a 404, EntityAlreadyExists a 409, ...), which awsapi would otherwise turn into 400s.
func wrapIAM(op awsapi.Op) awsapi.Op {
	return func(q *awsapi.Req) (any, error) {
		out, err := op(q)
		if err != nil {
			return nil, iamError(err)
		}
		return out, nil
	}
}

func iamError(err error) error {
	var ae *awsapi.Error
	if errors.As(err, &ae) {
		return ae
	}
	if errors.Is(err, store.ErrNotFound) {
		return &awsapi.Error{Status: http.StatusNotFound, Code: "NoSuchEntity", Message: "The entity cannot be found."}
	}
	if errors.Is(err, store.ErrConflict) {
		return &awsapi.Error{Status: http.StatusConflict, Code: "ConcurrentModification", Message: "The entity was modified concurrently; retry."}
	}
	var ce *core.Error
	if !errors.As(err, &ce) {
		return err
	}
	code, status := ce.Code, ce.Status
	switch code {
	case "ResourceNotFound":
		code, status = "NoSuchEntity", http.StatusNotFound
	case "ResourceConflict", "Conflict":
		code = "EntityAlreadyExists"
	case "BadRequest":
		code = "InvalidInput"
	}
	return &awsapi.Error{Status: status, Code: code, Message: ce.Message}
}

// ---- request helpers ----

func required(q *awsapi.Req, names ...string) error {
	for _, n := range names {
		if q.Param(n) == "" {
			return awsapi.Errorf(http.StatusBadRequest, "ValidationError",
				"1 validation error detected: Value null at '%s' failed to satisfy constraint: Member must not be null", lowerFirst(n))
		}
	}
	return nil
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// authorizeWith checks action on resource with extra condition keys (such as
// iam:PolicyARN or aws:ResourceTag/...) present for this one check.
func authorizeWith(q *awsapi.Req, action, resource string, keys CondContext) error {
	if q.P == nil || len(keys) == 0 {
		return q.Authorize(action, resource)
	}
	if q.P.Context == nil {
		q.P.Context = map[string][]string{}
	}
	saved := map[string][]string{}
	for k, v := range keys {
		if old, ok := q.P.Context[k]; ok {
			saved[k] = old
		}
		q.P.Context[k] = v
	}
	err := q.Authorize(action, resource)
	for k := range keys {
		if old, ok := saved[k]; ok {
			q.P.Context[k] = old
		} else {
			delete(q.P.Context, k)
		}
	}
	return err
}

// authz returns a checker for shared methods that adds keys to every check.
func authzWith(q *awsapi.Req, keys CondContext) authz {
	return func(action, resource string) error { return authorizeWith(q, action, resource, keys) }
}

func resourceTagKeys(t core.Tags) CondContext {
	c := CondContext{}
	for k, v := range t {
		c["aws:resourcetag/"+strings.ToLower(k)] = []string{v}
		c["iam:resourcetag/"+strings.ToLower(k)] = []string{v}
	}
	return c
}

func requestTagKeys(t core.Tags) CondContext {
	c := CondContext{}
	keys := []string{}
	for k, v := range t {
		c["aws:requesttag/"+strings.ToLower(k)] = []string{v}
		keys = append(keys, k)
	}
	if len(keys) > 0 {
		sort.Strings(keys)
		c["aws:tagkeys"] = keys
	}
	return c
}

func merge(cs ...CondContext) CondContext {
	out := CondContext{}
	for _, c := range cs {
		for k, v := range c {
			out[k] = v
		}
	}
	return out
}

func tagsParam(q *awsapi.Req) core.Tags {
	structs := q.Structs("Tags")
	if len(structs) == 0 {
		return nil
	}
	t := core.Tags{}
	for _, m := range structs {
		t[m["Key"]] = m["Value"]
	}
	return t
}

// callerUser is the user an operation without UserName acts on: the caller.
func (s *Service) callerUser(q *awsapi.Req) (string, error) {
	if n := q.Param("UserName"); n != "" {
		return n, nil
	}
	if q.P == nil || q.P.RoleName != "" {
		return "", awsapi.Errorf(http.StatusBadRequest, "ValidationError", "Must specify userName when calling with non-User credentials")
	}
	return q.P.UserName, nil
}

// authUser authorizes action on a user (its ARN and tags when it exists).
func (s *Service) authUser(q *awsapi.Req, action, name string, extra ...CondContext) error {
	arn := s.userARN(name, "/")
	var tags core.Tags
	if u, err := store.Get[User](s.env.Store, cUsers, name); err == nil {
		arn, tags = u.ARN, u.Tags
	}
	return authorizeWith(q, action, arn, merge(append([]CondContext{resourceTagKeys(tags)}, extra...)...))
}

func (s *Service) authGroup(q *awsapi.Req, action, name string, extra ...CondContext) error {
	return authorizeWith(q, action, s.groupARNByName(name), merge(extra...))
}

func (s *Service) authRole(q *awsapi.Req, action, name string, extra ...CondContext) error {
	name = roleName(name)
	arn := s.roleARN(name, "/")
	var tags core.Tags
	if r, err := store.Get[Role](s.env.Store, cRoles, name); err == nil {
		arn, tags = r.ARN, r.Tags
	}
	return authorizeWith(q, action, arn, merge(append([]CondContext{resourceTagKeys(tags)}, extra...)...))
}

// authPolicy authorizes action on a managed policy; the policy's ARN is used
// when it exists, else the ARN as given.
func (s *Service) authPolicy(q *awsapi.Req, action, ref string) error {
	arn := core.CanonicalARN(ref)
	var tags core.Tags
	if p, err := s.resolvePolicy(ref); err == nil {
		arn, tags = p.ARN, p.Tags
	}
	return authorizeWith(q, action, arn, resourceTagKeys(tags))
}

func (s *Service) authProfile(q *awsapi.Req, action, name string, extra ...CondContext) error {
	arn := s.profileARN(profileName(name), "/")
	var tags core.Tags
	if p, err := s.GetInstanceProfile(name); err == nil {
		arn, tags = p.ARN, p.Tags
	}
	return authorizeWith(q, action, arn, merge(append([]CondContext{resourceTagKeys(tags)}, extra...)...))
}

// ---- pagination ----

// page returns one page of items (sorted by key) as field, with IsTruncated and Marker.
func page[T any](q *awsapi.Req, items []T, key func(T) string, field string, render func(T) any) (map[string]any, error) {
	sort.SliceStable(items, func(i, j int) bool { return key(items[i]) < key(items[j]) })
	start := 0
	if m := q.Param("Marker"); m != "" {
		b, err := base64.RawURLEncoding.DecodeString(m)
		if err != nil {
			return nil, awsapi.Errorf(http.StatusBadRequest, "InvalidInput", "Marker is not valid")
		}
		start = sort.Search(len(items), func(i int) bool { return key(items[i]) >= string(b) })
	}
	max := q.ParamInt("MaxItems", 100)
	if max < 1 || max > 1000 {
		return nil, awsapi.Errorf(http.StatusBadRequest, "ValidationError", "MaxItems must be between 1 and 1000")
	}
	end := min(start+max, len(items))
	members := awsapi.Members{}
	for _, it := range items[start:end] {
		members = append(members, render(it))
	}
	out := map[string]any{field: members, "IsTruncated": end < len(items)}
	if end < len(items) {
		out["Marker"] = base64.RawURLEncoding.EncodeToString([]byte(key(items[end])))
	}
	return out, nil
}

func pathPrefix(q *awsapi.Req, path string) bool {
	p := q.Param("PathPrefix")
	return p == "" || strings.HasPrefix(path, p)
}

func (s *Service) emptyList(action, field string, perUser bool) awsapi.Op {
	return func(q *awsapi.Req) (any, error) {
		res := "*"
		if perUser {
			name, err := s.callerUser(q)
			if err != nil {
				return nil, err
			}
			if err := s.authUser(q, action, name); err != nil {
				return nil, err
			}
			if _, err := s.getUserOr404(name); err != nil {
				return nil, err
			}
		} else if err := q.Authorize(action, res); err != nil {
			return nil, err
		}
		return map[string]any{field: awsapi.Members{}, "IsTruncated": false}, nil
	}
}

// ---- rendering ----

// encDoc URL-encodes a policy document as IAM returns it (spaces as %20, so
// both QueryUnescape and unquote decode it).
func encDoc(text string) string { return strings.ReplaceAll(url.QueryEscape(text), "+", "%20") }

func tagsXML(t core.Tags) awsapi.Members {
	keys := make([]string, 0, len(t))
	for k := range t {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := awsapi.Members{}
	for _, k := range keys {
		out = append(out, awsapi.Ordered{{K: "Key", V: k}, {K: "Value", V: t[k]}})
	}
	return out
}

func boundaryXML(arn string) any {
	if arn == "" {
		return nil
	}
	return map[string]any{"PermissionsBoundaryType": "Policy", "PermissionsBoundaryArn": arn}
}

func userXML(u User, full bool) map[string]any {
	m := map[string]any{"Path": u.path(), "UserName": u.Name, "UserId": u.ID, "Arn": u.ARN, "CreateDate": u.CreatedAt}
	if u.LastLogin != nil {
		m["PasswordLastUsed"] = *u.LastLogin
	}
	if full {
		m["PermissionsBoundary"] = boundaryXML(u.PermissionsBoundary)
		if len(u.Tags) > 0 {
			m["Tags"] = tagsXML(u.Tags)
		}
	}
	return m
}

func groupXML(g Group) map[string]any {
	return map[string]any{"Path": g.path(), "GroupName": g.Name, "GroupId": g.ID, "Arn": g.ARN, "CreateDate": g.CreatedAt}
}

func roleXML(r Role, full bool) map[string]any {
	m := map[string]any{"Path": r.path(), "RoleName": r.Name, "RoleId": r.ID, "Arn": r.ARN, "CreateDate": r.CreatedAt,
		"AssumeRolePolicyDocument": encDoc(docJSON(r.TrustPolicy)), "MaxSessionDuration": r.MaxSessionSeconds}
	if r.Description != "" {
		m["Description"] = r.Description
	}
	if full {
		m["PermissionsBoundary"] = boundaryXML(r.PermissionsBoundary)
		if len(r.Tags) > 0 {
			m["Tags"] = tagsXML(r.Tags)
		}
		lu := map[string]any{}
		if r.LastUsed != nil {
			lu["LastUsedDate"], lu["Region"] = *r.LastUsed, r.LastUsedRegion
		}
		m["RoleLastUsed"] = lu
	}
	return m
}

func (s *Service) policyXML(p Policy, full bool) map[string]any {
	a := s.attachments(p)
	m := map[string]any{"PolicyName": p.Name, "PolicyId": p.ID, "Arn": p.ARN, "Path": p.path(), "DefaultVersionId": p.DefaultVersion,
		"AttachmentCount": a.count(), "PermissionsBoundaryUsageCount": a.Boundaries, "IsAttachable": true,
		"CreateDate": p.CreatedAt, "UpdateDate": p.UpdatedAt}
	if full {
		if p.Description != "" {
			m["Description"] = p.Description
		}
		if len(p.Tags) > 0 {
			m["Tags"] = tagsXML(p.Tags)
		}
	}
	return m
}

func versionXML(p Policy, v PolicyVersion, withDoc bool) map[string]any {
	m := map[string]any{"VersionId": v.ID, "IsDefaultVersion": v.ID == p.DefaultVersion, "CreateDate": v.CreatedAt}
	if withDoc {
		m["Document"] = encDoc(docJSON(v.Document))
	}
	return m
}

func keyXML(k AccessKey) map[string]any {
	return map[string]any{"UserName": k.UserName, "AccessKeyId": k.AccessKeyID, "Status": k.Status, "CreateDate": k.CreatedAt}
}

func (s *Service) profileXML(p InstanceProfile) map[string]any {
	roles := awsapi.Members{}
	for _, n := range p.Roles {
		if r, err := store.Get[Role](s.env.Store, cRoles, n); err == nil {
			roles = append(roles, roleXML(r, false))
		}
	}
	m := map[string]any{"Path": p.Path, "InstanceProfileName": p.Name, "InstanceProfileId": p.ID, "Arn": p.ARN, "CreateDate": p.CreatedAt, "Roles": roles}
	if len(p.Tags) > 0 {
		m["Tags"] = tagsXML(p.Tags)
	}
	return m
}

// docParam reads and parses a policy document parameter.
func docParam(q *awsapi.Req, name string) (PolicyDocument, error) {
	text := q.Param(name)
	if text == "" {
		return PolicyDocument{}, required(q, name)
	}
	return ParsePolicy(text)
}

func tagsResult(t core.Tags) map[string]any {
	return map[string]any{"Tags": tagsXML(t), "IsTruncated": false}
}
