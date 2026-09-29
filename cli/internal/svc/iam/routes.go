package iam

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/store"
	"golang.org/x/crypto/bcrypt"
)

// Routes registers the native HomeCloud IAM API.
func (s *Service) Routes(r *httpx.Router) {
	iamRes := func(kind string) httpx.Opt { return httpx.Res("arn:aws:iam::{account}:" + kind) }

	r.Handle("POST /api/v1/auth/login", "", s.login, httpx.Public())
	r.Handle("POST /api/v1/auth/logout", "sts:Logout", s.logout)
	r.Handle("GET /api/v1/auth/whoami", "sts:GetCallerIdentity", s.whoami)

	r.Handle("GET /api/v1/iam/summary", "iam:GetAccountSummary", s.summary)
	r.Handle("GET /api/v1/iam/users", "iam:ListUsers", s.listUsers)
	r.Handle("POST /api/v1/iam/users", "iam:CreateUser", s.createUser)
	r.Handle("GET /api/v1/iam/users/{name}", "iam:GetUser", s.getUser, iamRes("user/{name}"))
	r.Handle("DELETE /api/v1/iam/users/{name}", "iam:DeleteUser", s.deleteUser, iamRes("user/{name}"))
	r.Handle("PUT /api/v1/iam/users/{name}/tags", "iam:TagUser", s.tagUser, iamRes("user/{name}"))
	r.Handle("PUT /api/v1/iam/users/{name}/password", "iam:UpdateLoginProfile", s.setUserPassword, iamRes("user/{name}"))
	r.Handle("DELETE /api/v1/iam/users/{name}/password", "iam:DeleteLoginProfile", s.deleteUserPassword, iamRes("user/{name}"))
	r.Handle("POST /api/v1/iam/users/{name}/policies", "iam:AttachUserPolicy", s.attachUserPolicy, iamRes("user/{name}"))
	r.Handle("DELETE /api/v1/iam/users/{name}/policies/{policy}", "iam:DetachUserPolicy", s.detachUserPolicy, iamRes("user/{name}"))
	r.Handle("PUT /api/v1/iam/users/{name}/inline-policies/{policy}", "iam:PutUserPolicy", s.putInline, iamRes("user/{name}"))
	r.Handle("DELETE /api/v1/iam/users/{name}/inline-policies/{policy}", "iam:DeleteUserPolicy", s.deleteInline, iamRes("user/{name}"))
	r.Handle("GET /api/v1/iam/users/{name}/access-keys", "iam:ListAccessKeys", s.listKeys, iamRes("user/{name}"))
	r.Handle("POST /api/v1/iam/users/{name}/access-keys", "iam:CreateAccessKey", s.createKeyRoute, iamRes("user/{name}"))
	r.Handle("PATCH /api/v1/iam/users/{name}/access-keys/{key}", "iam:UpdateAccessKey", s.updateKey, iamRes("user/{name}"))
	r.Handle("DELETE /api/v1/iam/users/{name}/access-keys/{key}", "iam:DeleteAccessKey", s.deleteKey, iamRes("user/{name}"))

	r.Handle("GET /api/v1/iam/groups", "iam:ListGroups", s.listGroups)
	r.Handle("POST /api/v1/iam/groups", "iam:CreateGroup", s.createGroup)
	r.Handle("GET /api/v1/iam/groups/{name}", "iam:GetGroup", s.getGroup, iamRes("group/{name}"))
	r.Handle("DELETE /api/v1/iam/groups/{name}", "iam:DeleteGroup", s.deleteGroup, iamRes("group/{name}"))
	r.Handle("POST /api/v1/iam/groups/{name}/members", "iam:AddUserToGroup", s.addMember, iamRes("group/{name}"))
	r.Handle("DELETE /api/v1/iam/groups/{name}/members/{user}", "iam:RemoveUserFromGroup", s.removeMember, iamRes("group/{name}"))
	r.Handle("POST /api/v1/iam/groups/{name}/policies", "iam:AttachGroupPolicy", s.attachGroupPolicy, iamRes("group/{name}"))
	r.Handle("DELETE /api/v1/iam/groups/{name}/policies/{policy}", "iam:DetachGroupPolicy", s.detachGroupPolicy, iamRes("group/{name}"))
	r.Handle("PUT /api/v1/iam/groups/{name}/inline-policies/{policy}", "iam:PutGroupPolicy", s.putGroupInline, iamRes("group/{name}"))
	r.Handle("DELETE /api/v1/iam/groups/{name}/inline-policies/{policy}", "iam:DeleteGroupPolicy", s.deleteGroupInline, iamRes("group/{name}"))

	r.Handle("GET /api/v1/iam/policies", "iam:ListPolicies", s.listPolicies)
	r.Handle("POST /api/v1/iam/policies", "iam:CreatePolicy", s.createPolicy)
	r.Handle("GET /api/v1/iam/policies/{name}", "iam:GetPolicy", s.getPolicy, iamRes("policy/{name}"))
	r.Handle("PUT /api/v1/iam/policies/{name}", "iam:CreatePolicyVersion", s.updatePolicy, iamRes("policy/{name}"))
	r.Handle("DELETE /api/v1/iam/policies/{name}", "iam:DeletePolicy", s.deletePolicy, iamRes("policy/{name}"))
	r.Handle("GET /api/v1/iam/policies/{name}/versions", "iam:ListPolicyVersions", s.listVersionsRoute, iamRes("policy/{name}"))
	r.Handle("POST /api/v1/iam/policies/{name}/versions", "iam:CreatePolicyVersion", s.createVersionRoute, iamRes("policy/{name}"))
	r.Handle("PUT /api/v1/iam/policies/{name}/default-version", "iam:SetDefaultPolicyVersion", s.setDefaultVersionRoute, iamRes("policy/{name}"))
	r.Handle("DELETE /api/v1/iam/policies/{name}/versions/{version}", "iam:DeletePolicyVersion", s.deleteVersionRoute, iamRes("policy/{name}"))
	r.Handle("POST /api/v1/iam/simulate", "iam:SimulatePrincipalPolicy", s.simulateRoute)
	s.roleRoutes(r)
	s.profileRoutes(r)
}

func (s *Service) login(c *httpx.Ctx) (any, error) {
	var in struct{ Username, Password string }
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	ip := httpx.ClientIP(c.R)
	if s.throttled(ip, false) {
		return nil, core.Errf(http.StatusTooManyRequests, "TooManyRequests", "too many failed sign-in attempts; try again in a few minutes")
	}
	u, err := store.Get[User](s.env.Store, cUsers, in.Username)
	if err != nil || u.PasswordHash == "" || bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(in.Password)) != nil {
		s.throttled(ip, true)
		time.Sleep(300 * time.Millisecond)
		return nil, core.Errf(http.StatusUnauthorized, "AuthFailure", "incorrect user name or password")
	}
	token := "hcs_" + core.NewSecret(40)
	exp := time.Now().Add(sessionTTL)
	if err := store.Put(s.env.Store, cSessions, hashSecret(token), session{Token: "", UserName: u.Name, UserID: u.ID, Expires: exp}); err != nil {
		return nil, err
	}
	_, _ = store.Update(s.env.Store, cUsers, u.Name, func(u *User) error { n := core.Now(); u.LastLogin = &n; return nil })
	s.pruneSessions()
	return map[string]any{"token": token, "expires": exp, "user": u.view(), "account_id": s.env.AccountID}, nil
}

func (s *Service) pruneSessions() {
	s.env.Store.Retain(cSessions, func(id string, raw json.RawMessage) bool {
		var ss session
		return json.Unmarshal(raw, &ss) == nil && time.Now().Before(ss.Expires)
	})
}

func (s *Service) logout(c *httpx.Ctx) (any, error) {
	h := strings.TrimPrefix(c.R.Header.Get("Authorization"), "Bearer ")
	if strings.HasPrefix(h, "hcs_") {
		_ = store.Delete(s.env.Store, cSessions, hashSecret(h))
	}
	return nil, nil
}

func (s *Service) whoami(c *httpx.Ctx) (any, error) {
	return map[string]any{"account_id": c.P.AccountID, "user_name": c.P.UserName, "arn": c.P.ARN, "root": c.P.Root, "region": s.env.Cfg.Region,
		"role_name": c.P.RoleName, "session_name": c.P.SessionName}, nil
}

func (s *Service) summary(c *httpx.Ctx) (any, error) {
	policies := store.List[Policy](s.env.Store, cPolicies)
	customer := 0
	for _, p := range policies {
		if !p.Managed {
			customer++
		}
	}
	return map[string]any{
		"account_id":        s.env.AccountID,
		"users":             len(store.List[User](s.env.Store, cUsers)),
		"groups":            len(store.List[Group](s.env.Store, cGroups)),
		"policies":          len(policies),
		"customer_policies": customer,
		"managed_policies":  len(policies) - customer,
		"access_keys":       len(store.List[AccessKey](s.env.Store, cKeys)),
		"roles":             len(store.List[Role](s.env.Store, cRoles)),
		"instance_profiles": len(store.List[InstanceProfile](s.env.Store, cProfiles)),
	}, nil
}

// ---- users ----

func (s *Service) listUsers(c *httpx.Ctx) (any, error) {
	type keyInfo struct {
		count, active int
		lastUsed      *time.Time
	}
	keys := map[string]*keyInfo{}
	for _, k := range store.List[AccessKey](s.env.Store, cKeys) {
		ki := keys[k.UserName]
		if ki == nil {
			ki = &keyInfo{}
			keys[k.UserName] = ki
		}
		ki.count++
		if k.Status == "Active" {
			ki.active++
		}
		if k.LastUsed != nil && (ki.lastUsed == nil || k.LastUsed.After(*ki.lastUsed)) {
			ki.lastUsed = k.LastUsed
		}
	}
	out := []map[string]any{}
	for _, u := range store.List[User](s.env.Store, cUsers) {
		v := u.view()
		ki := keys[u.Name]
		if ki == nil {
			ki = &keyInfo{}
		}
		v["access_key_count"], v["active_access_keys"], v["access_key_last_used"] = ki.count, ki.active, ki.lastUsed
		out = append(out, v)
	}
	return out, nil
}

func (s *Service) createUser(c *httpx.Ctx) (any, error) {
	var in UserInput
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	u, err := s.CreateUser(in, c.Authorize)
	if err != nil {
		return nil, err
	}
	return u.view(), nil
}

func (s *Service) getUser(c *httpx.Ctx) (any, error) {
	u, err := s.getUserOr404(c.Param("name"))
	if err != nil {
		return nil, err
	}
	v := u.view()
	v["access_keys"] = s.UserKeys(u.Name)
	return v, nil
}

func (s *Service) deleteUser(c *httpx.Ctx) (any, error) {
	return nil, s.DeleteUser(c.Param("name"), true)
}

func userView(u User, err error) (any, error) {
	if err != nil {
		return nil, err
	}
	return u.view(), nil
}

func (s *Service) tagUser(c *httpx.Ctx) (any, error) {
	var in struct {
		Tags core.Tags `json:"tags"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if err := checkTags(in.Tags); err != nil {
		return nil, err
	}
	return userView(s.UpdateUser(c.Param("name"), func(u *User) error { u.Tags = in.Tags; return nil }))
}

func (s *Service) setUserPassword(c *httpx.Ctx) (any, error) {
	var in struct {
		Password              string
		PasswordResetRequired *bool `json:"password_reset_required"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	return userView(s.SetLoginProfile(c.Param("name"), in.Password, in.PasswordResetRequired, false, false))
}

func (s *Service) deleteUserPassword(c *httpx.Ctx) (any, error) {
	return userView(s.DeleteLoginProfile(c.Param("name"), false))
}

func (s *Service) policyArg(c *httpx.Ctx) (string, error) {
	var in struct {
		Policy string `json:"policy"`
	}
	if err := c.Bind(&in); err != nil {
		return "", err
	}
	return in.Policy, nil
}

func (s *Service) attachUserPolicy(c *httpx.Ctx) (any, error) {
	p, err := s.policyArg(c)
	if err != nil {
		return nil, err
	}
	return userView(s.AttachUserPolicy(c.Param("name"), p))
}

func (s *Service) detachUserPolicy(c *httpx.Ctx) (any, error) {
	return userView(s.DetachUserPolicy(c.Param("name"), c.Param("policy")))
}

// bindDoc reads a policy document body, defaulting the Version as the console expects.
func bindDoc(c *httpx.Ctx) (PolicyDocument, error) {
	var d PolicyDocument
	if err := c.Bind(&d); err != nil {
		return d, err
	}
	d.defaultVersion()
	return d, nil
}

func (s *Service) putInline(c *httpx.Ctx) (any, error) {
	d, err := bindDoc(c)
	if err != nil {
		return nil, err
	}
	return userView(s.PutUserPolicy(c.Param("name"), c.Param("policy"), d))
}

func (s *Service) deleteInline(c *httpx.Ctx) (any, error) {
	return userView(s.DeleteUserPolicy(c.Param("name"), c.Param("policy")))
}

func (s *Service) listKeys(c *httpx.Ctx) (any, error) {
	if _, err := s.getUserOr404(c.Param("name")); err != nil {
		return nil, err
	}
	return s.UserKeys(c.Param("name")), nil
}

func (s *Service) createKeyRoute(c *httpx.Ctx) (any, error) {
	name := c.Param("name")
	k, secret, err := s.CreateAccessKey(name)
	if err != nil {
		return nil, err
	}
	return map[string]any{"access_key_id": k.AccessKeyID, "secret_access_key": secret, "user_name": name, "status": k.Status, "created_at": k.CreatedAt}, nil
}

func (s *Service) updateKey(c *httpx.Ctx) (any, error) {
	var in struct{ Status string }
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	return s.UpdateAccessKey(c.Param("name"), c.Param("key"), in.Status)
}

func (s *Service) deleteKey(c *httpx.Ctx) (any, error) {
	return nil, s.DeleteAccessKey(c.Param("name"), c.Param("key"))
}

// ---- groups ----

func (s *Service) groupView(g Group) map[string]any {
	return map[string]any{"name": g.Name, "id": g.ID, "path": g.path(), "arn": g.ARN, "created_at": g.CreatedAt,
		"attached_policies": nz(g.AttachedPolicies), "inline_policies": g.InlinePolicies, "members": s.groupMembers(g.Name)}
}

func (s *Service) groupResult(g Group, err error) (any, error) {
	if err != nil {
		return nil, err
	}
	return s.groupView(g), nil
}

func (s *Service) listGroups(c *httpx.Ctx) (any, error) {
	out := []map[string]any{}
	for _, g := range store.List[Group](s.env.Store, cGroups) {
		out = append(out, s.groupView(g))
	}
	return out, nil
}

func (s *Service) createGroup(c *httpx.Ctx) (any, error) {
	var in struct {
		Name     string   `json:"name"`
		Path     string   `json:"path"`
		Policies []string `json:"policies"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	return s.groupResult(s.CreateGroup(in.Name, in.Path, in.Policies, c.Authorize))
}

func (s *Service) getGroup(c *httpx.Ctx) (any, error) {
	return s.groupResult(s.getGroupOr404(c.Param("name")))
}

func (s *Service) deleteGroup(c *httpx.Ctx) (any, error) {
	return nil, s.DeleteGroup(c.Param("name"), true)
}

func (s *Service) addMember(c *httpx.Ctx) (any, error) {
	var in struct {
		User string `json:"user"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	return userView(s.AddUserToGroup(c.Param("name"), in.User))
}

func (s *Service) removeMember(c *httpx.Ctx) (any, error) {
	return userView(s.RemoveUserFromGroup(c.Param("name"), c.Param("user")))
}

func (s *Service) attachGroupPolicy(c *httpx.Ctx) (any, error) {
	p, err := s.policyArg(c)
	if err != nil {
		return nil, err
	}
	return s.groupResult(s.AttachGroupPolicy(c.Param("name"), p))
}

func (s *Service) detachGroupPolicy(c *httpx.Ctx) (any, error) {
	return s.groupResult(s.DetachGroupPolicy(c.Param("name"), c.Param("policy")))
}

func (s *Service) putGroupInline(c *httpx.Ctx) (any, error) {
	d, err := bindDoc(c)
	if err != nil {
		return nil, err
	}
	return s.groupResult(s.PutGroupPolicy(c.Param("name"), c.Param("policy"), d))
}

func (s *Service) deleteGroupInline(c *httpx.Ctx) (any, error) {
	return s.groupResult(s.DeleteGroupPolicy(c.Param("name"), c.Param("policy")))
}

// ---- policies ----

func (a Attachments) view() map[string][]string {
	return map[string][]string{"users": a.Users, "groups": a.Groups, "roles": a.Roles}
}

func (s *Service) listPolicies(c *httpx.Ctx) (any, error) {
	scope := c.Query("scope") // all | managed | local
	out := []map[string]any{}
	for _, p := range store.List[Policy](s.env.Store, cPolicies) {
		if (scope == "managed" && !p.Managed) || (scope == "local" && p.Managed) {
			continue
		}
		out = append(out, map[string]any{"name": p.Name, "arn": p.ARN, "path": p.path(), "description": p.Description, "managed": p.Managed,
			"created_at": p.CreatedAt, "updated_at": p.UpdatedAt, "attachment_count": s.attachments(p).count(), "default_version": p.DefaultVersion})
	}
	return out, nil
}

func (s *Service) createPolicy(c *httpx.Ctx) (any, error) {
	var in PolicyInput
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	in.Document.defaultVersion()
	return s.CreatePolicy(in)
}

func (s *Service) getPolicy(c *httpx.Ctx) (any, error) {
	p, err := s.resolvePolicy(c.Param("name"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"policy": p, "attachments": s.attachments(p).view()}, nil
}

// updatePolicy replaces a customer policy's document (a new default version;
// the oldest version makes room when there are five) and description.
func (s *Service) updatePolicy(c *httpx.Ctx) (any, error) {
	var in struct {
		Description *string        `json:"description"`
		Document    PolicyDocument `json:"document"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	in.Document.defaultVersion()
	if _, _, err := s.CreatePolicyVersion(c.Param("name"), in.Document, true, true); err != nil {
		return nil, err
	}
	return s.UpdatePolicy(c.Param("name"), func(p *Policy) error {
		if in.Description != nil {
			p.Description = *in.Description
		}
		return nil
	})
}

func (s *Service) deletePolicy(c *httpx.Ctx) (any, error) {
	return nil, s.DeletePolicy(c.Param("name"), true)
}

func (s *Service) listVersionsRoute(c *httpx.Ctx) (any, error) {
	p, err := s.resolvePolicy(c.Param("name"))
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, v := range p.Versions {
		out = append(out, map[string]any{"version_id": v.ID, "is_default": v.ID == p.DefaultVersion, "created_at": v.CreatedAt, "document": v.Document})
	}
	return out, nil
}

func (s *Service) createVersionRoute(c *httpx.Ctx) (any, error) {
	var in struct {
		Document     PolicyDocument `json:"document"`
		SetAsDefault bool           `json:"set_as_default"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	in.Document.defaultVersion()
	_, v, err := s.CreatePolicyVersion(c.Param("name"), in.Document, in.SetAsDefault, false)
	return v, err
}

func (s *Service) setDefaultVersionRoute(c *httpx.Ctx) (any, error) {
	var in struct {
		Version string `json:"version"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	return s.SetDefaultPolicyVersion(c.Param("name"), in.Version)
}

func (s *Service) deleteVersionRoute(c *httpx.Ctx) (any, error) {
	_, err := s.DeletePolicyVersion(c.Param("name"), c.Param("version"))
	return nil, err
}

func (s *Service) simulateRoute(c *httpx.Ctx) (any, error) {
	var in struct {
		User     string              `json:"user"`
		Source   string              `json:"source_arn"`
		Actions  []string            `json:"actions"`
		Resource string              `json:"resource"`
		Context  map[string][]string `json:"context"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if in.Source == "" {
		in.Source = in.User
	}
	t, err := s.simTarget(in.Source)
	if err != nil {
		return nil, err
	}
	if in.Resource == "" {
		in.Resource = "*"
	}
	extra := CondContext{}
	for k, v := range in.Context {
		extra[strings.ToLower(k)] = v
	}
	results := []map[string]string{}
	for _, a := range in.Actions {
		results = append(results, map[string]string{"action": a, "resource": in.Resource, "decision": t.decide(a, in.Resource, extra).String()})
	}
	return results, nil
}

// ---- roles ----

func (s *Service) roleRoutes(r *httpx.Router) {
	res := httpx.Res("arn:aws:iam::{account}:role/{name}")
	r.Handle("GET /api/v1/iam/roles", "iam:ListRoles", s.listRoles)
	r.Handle("POST /api/v1/iam/roles", "iam:CreateRole", s.createRoleRoute)
	r.Handle("GET /api/v1/iam/roles/{name}", "iam:GetRole", s.getRoleRoute, res)
	r.Handle("DELETE /api/v1/iam/roles/{name}", "iam:DeleteRole", s.deleteRoleRoute, res)
	r.Handle("PATCH /api/v1/iam/roles/{name}", "iam:UpdateRole", s.updateRoleRoute, res)
	r.Handle("PUT /api/v1/iam/roles/{name}/trust-policy", "iam:UpdateAssumeRolePolicy", s.putTrustRoute, res)
	r.Handle("POST /api/v1/iam/roles/{name}/policies", "iam:AttachRolePolicy", s.attachRolePolicyRoute, res)
	r.Handle("DELETE /api/v1/iam/roles/{name}/policies/{policy}", "iam:DetachRolePolicy", s.detachRolePolicyRoute, res)
	r.Handle("PUT /api/v1/iam/roles/{name}/inline-policies/{policy}", "iam:PutRolePolicy", s.putRoleInlineRoute, res)
	r.Handle("DELETE /api/v1/iam/roles/{name}/inline-policies/{policy}", "iam:DeleteRolePolicy", s.deleteRoleInlineRoute, res)
	r.Handle("POST /api/v1/iam/roles/{name}/revoke-sessions", "iam:PutRolePolicy", s.revokeRoute, res)
	r.Handle("PUT /api/v1/iam/roles/{name}/tags", "iam:TagRole", s.tagRoleRoute, res)
	r.Handle("DELETE /api/v1/iam/roles/{name}/tags", "iam:UntagRole", s.untagRoleRoute, res)
	r.Handle("POST /api/v1/sts/assume-role", "sts:AssumeRole", s.assumeRoleRoute)
}

func (s *Service) roleResult(r Role, err error) (any, error) {
	if err != nil {
		return nil, err
	}
	return s.roleView(r), nil
}

func (s *Service) listRoles(c *httpx.Ctx) (any, error) {
	out := []map[string]any{}
	for _, r := range store.List[Role](s.env.Store, cRoles) {
		out = append(out, s.roleView(r))
	}
	return out, nil
}

func (s *Service) createRoleRoute(c *httpx.Ctx) (any, error) {
	var in RoleInput
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	return s.roleResult(s.CreateRole(in, c.Authorize))
}

func (s *Service) getRoleRoute(c *httpx.Ctx) (any, error) {
	return s.roleResult(s.GetRole(c.Param("name")))
}

func (s *Service) deleteRoleRoute(c *httpx.Ctx) (any, error) {
	if c.Query("force") == "true" {
		name := roleName(c.Param("name"))
		for _, p := range s.profilesForRole(name) {
			_, _ = s.RemoveRoleFromInstanceProfile(p, name)
		}
		_, _ = s.UpdateRole(name, func(r *Role) error {
			r.AttachedPolicies, r.InlinePolicies = []string{}, map[string]PolicyDocument{}
			return nil
		})
	}
	return nil, s.DeleteRole(c.Param("name"))
}

func (s *Service) updateRoleRoute(c *httpx.Ctx) (any, error) {
	var in struct {
		Description       *string    `json:"description"`
		MaxSessionSeconds *int       `json:"max_session_duration"`
		Tags              *core.Tags `json:"tags"` // replaces all tags
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if in.Tags != nil {
		if err := c.Authorize("iam:TagRole", s.roleARNByName(c.Param("name"))); err != nil {
			return nil, err
		}
		if err := c.Authorize("iam:UntagRole", s.roleARNByName(c.Param("name"))); err != nil {
			return nil, err
		}
		if err := checkTags(*in.Tags); err != nil {
			return nil, err
		}
	}
	return s.roleResult(s.UpdateRole(c.Param("name"), func(r *Role) error {
		if in.Tags != nil {
			r.Tags = *in.Tags
		}
		if in.Description != nil {
			r.Description = *in.Description
		}
		if in.MaxSessionSeconds != nil {
			if err := checkMaxSession(*in.MaxSessionSeconds); err != nil {
				return err
			}
			r.MaxSessionSeconds = *in.MaxSessionSeconds
		}
		return nil
	}))
}

func (s *Service) putTrustRoute(c *httpx.Ctx) (any, error) {
	var raw json.RawMessage
	if err := c.Bind(&raw); err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	var str string
	if json.Unmarshal(raw, &str) == nil {
		raw = json.RawMessage(str)
	}
	return s.roleResult(s.SetTrustPolicy(c.Param("name"), string(raw)))
}

func (s *Service) attachRolePolicyRoute(c *httpx.Ctx) (any, error) {
	p, err := s.policyArg(c)
	if err != nil {
		return nil, err
	}
	return s.roleResult(s.AttachRolePolicy(c.Param("name"), p))
}

func (s *Service) detachRolePolicyRoute(c *httpx.Ctx) (any, error) {
	return s.roleResult(s.DetachRolePolicy(c.Param("name"), c.Param("policy")))
}

func (s *Service) putRoleInlineRoute(c *httpx.Ctx) (any, error) {
	d, err := bindDoc(c)
	if err != nil {
		return nil, err
	}
	return s.roleResult(s.PutRolePolicy(c.Param("name"), c.Param("policy"), d))
}

func (s *Service) deleteRoleInlineRoute(c *httpx.Ctx) (any, error) {
	return s.roleResult(s.DeleteRolePolicy(c.Param("name"), c.Param("policy")))
}

func (s *Service) revokeRoute(c *httpx.Ctx) (any, error) {
	r, err := s.GetRole(c.Param("name"))
	if err != nil {
		return nil, err
	}
	s.RevokeRoleSessions(r.Name)
	return nil, nil
}

// tagRoleRoute merges tags into a role's tags.
func (s *Service) tagRoleRoute(c *httpx.Ctx) (any, error) {
	var in struct {
		Tags core.Tags `json:"tags"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	return s.roleResult(s.UpdateRole(c.Param("name"), func(r *Role) (err error) { r.Tags, err = mergeTags(r.Tags, in.Tags); return err }))
}

// untagRoleRoute removes the tags named in ?keys=a,b (repeatable).
func (s *Service) untagRoleRoute(c *httpx.Ctx) (any, error) {
	var keys []string
	for _, v := range c.R.URL.Query()["keys"] {
		for _, k := range strings.Split(v, ",") {
			if k = strings.TrimSpace(k); k != "" {
				keys = append(keys, k)
			}
		}
	}
	if len(keys) == 0 {
		return nil, core.BadRequest("name the tags to remove with ?keys=")
	}
	return s.roleResult(s.UpdateRole(c.Param("name"), func(r *Role) error { r.Tags = dropTags(r.Tags, keys); return nil }))
}

func (s *Service) assumeRoleRoute(c *httpx.Ctx) (any, error) {
	var in struct {
		Role            string `json:"role"`
		SessionName     string `json:"session_name"`
		DurationSeconds int    `json:"duration_seconds"`
		ExternalID      string `json:"external_id"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if in.SessionName == "" {
		in.SessionName = "homecloud-" + core.RandHex(8)
	}
	return s.AssumeRole(c.P, in.Role, in.SessionName, in.DurationSeconds, in.ExternalID)
}
