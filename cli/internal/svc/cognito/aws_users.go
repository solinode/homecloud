package cognito

import (
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

// ---- shapes ----

func attrsIn(v any) map[string]string {
	l, _ := v.([]any)
	out := map[string]string{}
	for _, x := range l {
		m, _ := x.(map[string]any)
		if n := str(m, "Name"); n != "" {
			out[n] = str(m, "Value")
		}
	}
	return out
}

func attrsOut(u User) []map[string]string {
	out := []map[string]string{{"Name": "sub", "Value": u.Sub}}
	names := make([]string, 0, len(u.Attributes))
	for k := range u.Attributes {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		out = append(out, map[string]string{"Name": k, "Value": u.Attributes[k]})
	}
	return out
}

func userType(u User, attrsKey string) map[string]any {
	return map[string]any{"Username": u.Username, attrsKey: attrsOut(u), "UserCreateDate": epoch(u.CreatedAt), "UserLastModifiedDate": epoch(u.ModifiedAt),
		"Enabled": u.Enabled, "UserStatus": u.Status}
}

// usernameAttr makes the username double as the e-mail attribute in pools
// whose UsernameAttributes include email.
func (s *Service) usernameAttr(p Pool, username string, attrs map[string]string) {
	l, _ := p.Extra["UsernameAttributes"].([]any)
	for _, a := range l {
		switch a {
		case "email":
			if _, ok := attrs["email"]; !ok && strings.Contains(username, "@") {
				attrs["email"] = username
			}
		case "phone_number":
			if _, ok := attrs["phone_number"]; !ok && strings.HasPrefix(username, "+") {
				attrs["phone_number"] = username
			}
		}
	}
}

// ---- admin: users ----

func (s *Service) awsAdminCreateUser(q *awsapi.Req) (any, error) {
	in := map[string]any{}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	id := str(in, "UserPoolId")
	if err := s.authz(q, "AdminCreateUser", id); err != nil {
		return nil, err
	}
	p, err := s.pool(id)
	if err != nil {
		return nil, err
	}
	attrs := attrsIn(in["UserAttributes"])
	name := str(in, "Username")
	s.usernameAttr(p, name, attrs)
	// Without a temporary password one is generated; nothing is e-mailed, so set
	// one with AdminSetUserPassword or pass TemporaryPassword.
	u, _, err := s.adminCreate(p, name, str(in, "TemporaryPassword"), true, attrs, nil)
	if err != nil {
		return nil, err
	}
	return map[string]any{"User": userType(u, "Attributes")}, nil
}

func (s *Service) awsAdminGetUser(q *awsapi.Req) (any, error) {
	in := map[string]any{}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	id := str(in, "UserPoolId")
	if err := s.authz(q, "AdminGetUser", id); err != nil {
		return nil, err
	}
	if _, err := s.pool(id); err != nil {
		return nil, err
	}
	u, err := s.getUser(id, str(in, "Username"))
	if err != nil {
		return nil, err
	}
	return userType(u, "UserAttributes"), nil
}

func (s *Service) awsAdminDeleteUser(q *awsapi.Req) (any, error) {
	in := map[string]any{}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	id := str(in, "UserPoolId")
	if err := s.authz(q, "AdminDeleteUser", id); err != nil {
		return nil, err
	}
	if _, err := s.pool(id); err != nil {
		return nil, err
	}
	return nil, s.removeUser(id, str(in, "Username"))
}

func (s *Service) awsAdminSetPassword(q *awsapi.Req) (any, error) {
	in := map[string]any{}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	id := str(in, "UserPoolId")
	if err := s.authz(q, "AdminSetUserPassword", id); err != nil {
		return nil, err
	}
	perm, _ := flagOf(in, "Permanent")
	_, err := s.setPassword(id, str(in, "Username"), str(in, "Password"), perm)
	return nil, err
}

func (s *Service) awsAdminUpdateAttrs(q *awsapi.Req) (any, error) {
	in := map[string]any{}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	id := str(in, "UserPoolId")
	if err := s.authz(q, "AdminUpdateUserAttributes", id); err != nil {
		return nil, err
	}
	// An empty value removes the attribute (as AdminDeleteUserAttributes does).
	_, err := s.patchUser(id, str(in, "Username"), attrsIn(in["UserAttributes"]), nil, nil, false)
	return nil, err
}

func (s *Service) awsAdminDeleteAttrs(q *awsapi.Req) (any, error) {
	in := map[string]any{}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	id := str(in, "UserPoolId")
	if err := s.authz(q, "AdminDeleteUserAttributes", id); err != nil {
		return nil, err
	}
	names, _ := in["UserAttributeNames"].([]any)
	attrs := map[string]string{}
	for _, n := range names {
		if k, _ := n.(string); k != "" {
			attrs[k] = ""
		}
	}
	_, err := s.patchUser(id, str(in, "Username"), attrs, nil, nil, false)
	return nil, err
}

func (s *Service) awsAdminEnable(q *awsapi.Req) (any, error) {
	in := map[string]any{}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	id := str(in, "UserPoolId")
	if err := s.authz(q, q.Op, id); err != nil {
		return nil, err
	}
	on := q.Op == "AdminEnableUser"
	_, err := s.patchUser(id, str(in, "Username"), nil, &on, nil, false)
	return nil, err
}

func (s *Service) awsAdminConfirm(q *awsapi.Req) (any, error) {
	in := map[string]any{}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	id := str(in, "UserPoolId")
	if err := s.authz(q, "AdminConfirmSignUp", id); err != nil {
		return nil, err
	}
	u, err := s.getUser(id, str(in, "Username"))
	if err != nil {
		return nil, err
	}
	if u.Status != "UNCONFIRMED" {
		return nil, core.Errf(http.StatusBadRequest, "NotAuthorizedException", "user cannot be confirmed. Current status is %s", u.Status)
	}
	_, err = s.patchUser(id, u.Username, nil, nil, nil, true)
	return nil, err
}

func (s *Service) awsAdminSignOut(q *awsapi.Req) (any, error) {
	in := map[string]any{}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	id := str(in, "UserPoolId")
	if err := s.authz(q, "AdminUserGlobalSignOut", id); err != nil {
		return nil, err
	}
	u, err := s.getUser(id, str(in, "Username"))
	if err != nil {
		return nil, err
	}
	s.revokeAll(id, u.Username)
	return nil, nil
}

var filterRe = regexp.MustCompile(`^\s*([\w:]+)\s*(\^?=)\s*"((?:[^"\\]|\\.)*)"\s*$`)

func userFilter(f string) (func(User) bool, error) {
	if strings.TrimSpace(f) == "" {
		return func(User) bool { return true }, nil
	}
	m := filterRe.FindStringSubmatch(f)
	if m == nil {
		return nil, invalid("Error while parsing filter. Expected: attribute (=|^=) \"value\"")
	}
	attr, op := m[1], m[2]
	val := strings.NewReplacer(`\"`, `"`, `\\`, `\`).Replace(m[3])
	switch attr {
	case "username", "email", "phone_number", "name", "given_name", "family_name", "preferred_username", "cognito:user_status", "status", "sub":
	default:
		return nil, invalid("Invalid search attribute: %s", attr)
	}
	return func(u User) bool {
		var v string
		switch attr {
		case "username":
			v = u.Username
		case "sub":
			v = u.Sub
		case "cognito:user_status":
			v = u.Status
		case "status":
			v = "Disabled"
			if u.Enabled {
				v = "Enabled"
			}
		default:
			v = u.Attributes[attr]
		}
		if op == "=" {
			return v == val
		}
		return strings.HasPrefix(v, val)
	}, nil
}

func (s *Service) poolUsers(poolID string, keep func(User) bool) []User {
	var us []User
	for _, u := range store.List[User](s.env.Store, cUsers) {
		if u.PoolID == poolID && keep(u) {
			us = append(us, u)
		}
	}
	sort.Slice(us, func(i, j int) bool {
		if !us[i].CreatedAt.Equal(us[j].CreatedAt) {
			return us[i].CreatedAt.Before(us[j].CreatedAt)
		}
		return us[i].Username < us[j].Username
	})
	return us
}

func (s *Service) usersPage(q *awsapi.Req, in map[string]any, poolID string, keep func(User) bool, tokenKey string) (any, error) {
	limit, err := limitOf(in, "Limit", 60)
	if err != nil {
		return nil, err
	}
	us := s.poolUsers(poolID, keep)
	from, to, next, err := page(len(us), str(in, tokenKey), limit)
	if err != nil {
		return nil, err
	}
	only, _ := in["AttributesToGet"].([]any)
	out := []map[string]any{}
	for _, u := range us[from:to] {
		t := userType(u, "Attributes")
		if len(only) > 0 {
			var a []map[string]string
			for _, x := range attrsOut(u) {
				if slices.Contains(only, any(x["Name"])) {
					a = append(a, x)
				}
			}
			if a == nil {
				a = []map[string]string{}
			}
			t["Attributes"] = a
		}
		out = append(out, t)
	}
	res := map[string]any{"Users": out}
	if next != "" {
		res[tokenKey] = next
	}
	return res, nil
}

func (s *Service) awsListUsers(q *awsapi.Req) (any, error) {
	in := map[string]any{}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	id := str(in, "UserPoolId")
	if err := s.authz(q, "ListUsers", id); err != nil {
		return nil, err
	}
	if _, err := s.pool(id); err != nil {
		return nil, err
	}
	keep, err := userFilter(str(in, "Filter"))
	if err != nil {
		return nil, err
	}
	return s.usersPage(q, in, id, keep, "PaginationToken")
}

// ---- admin: groups ----

func groupDesc(pool string, g Group) map[string]any {
	m := map[string]any{"GroupName": g.Name, "UserPoolId": pool, "Description": g.Description, "CreationDate": epoch(g.CreatedAt), "LastModifiedDate": epoch(g.CreatedAt)}
	if g.Precedence != 0 {
		m["Precedence"] = g.Precedence
	}
	if g.RoleARN != "" {
		m["RoleArn"] = g.RoleARN
	}
	return m
}

func (s *Service) awsCreateGroup(q *awsapi.Req) (any, error) {
	in := map[string]any{}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	id := str(in, "UserPoolId")
	if err := s.authz(q, "CreateGroup", id); err != nil {
		return nil, err
	}
	g := Group{Name: str(in, "GroupName"), Description: str(in, "Description"), RoleARN: str(in, "RoleArn")}
	g.Precedence, _ = intOf(in, "Precedence")
	p, err := s.newGroup(id, g)
	if err != nil {
		return nil, err
	}
	return map[string]any{"Group": groupDesc(p.ID, p.Groups[len(p.Groups)-1])}, nil
}

func (s *Service) groupOf(poolID, name string) (Group, error) {
	p, err := s.pool(poolID)
	if err != nil {
		return Group{}, err
	}
	for _, g := range p.Groups {
		if g.Name == name {
			return g, nil
		}
	}
	return Group{}, core.Errf(http.StatusNotFound, "ResourceNotFoundException", "group %q does not exist", name)
}

func (s *Service) awsGetGroup(q *awsapi.Req) (any, error) {
	in := map[string]any{}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	id := str(in, "UserPoolId")
	if err := s.authz(q, "GetGroup", id); err != nil {
		return nil, err
	}
	g, err := s.groupOf(id, str(in, "GroupName"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"Group": groupDesc(id, g)}, nil
}

func (s *Service) awsUpdateGroup(q *awsapi.Req) (any, error) {
	in := map[string]any{}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	id := str(in, "UserPoolId")
	if err := s.authz(q, "UpdateGroup", id); err != nil {
		return nil, err
	}
	name := str(in, "GroupName")
	if _, err := s.groupOf(id, name); err != nil {
		return nil, err
	}
	_, err := s.modPool(id, func(p *Pool) error {
		for i := range p.Groups {
			if p.Groups[i].Name == name {
				if d, ok := in["Description"].(string); ok {
					p.Groups[i].Description = d
				}
				if n, ok := intOf(in, "Precedence"); ok {
					p.Groups[i].Precedence = n
				}
				if r, ok := in["RoleArn"].(string); ok {
					p.Groups[i].RoleARN = r
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	g, _ := s.groupOf(id, name)
	return map[string]any{"Group": groupDesc(id, g)}, nil
}

func (s *Service) awsDeleteGroup(q *awsapi.Req) (any, error) {
	in := map[string]any{}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	id := str(in, "UserPoolId")
	if err := s.authz(q, "DeleteGroup", id); err != nil {
		return nil, err
	}
	_, err := s.removeGroup(id, str(in, "GroupName"))
	return nil, err
}

func (s *Service) awsListGroups(q *awsapi.Req) (any, error) {
	in := map[string]any{}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	id := str(in, "UserPoolId")
	if err := s.authz(q, "ListGroups", id); err != nil {
		return nil, err
	}
	p, err := s.pool(id)
	if err != nil {
		return nil, err
	}
	limit, err := limitOf(in, "Limit", 60)
	if err != nil {
		return nil, err
	}
	from, to, next, err := page(len(p.Groups), str(in, "NextToken"), limit)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, g := range p.Groups[from:to] {
		out = append(out, groupDesc(id, g))
	}
	res := map[string]any{"Groups": out}
	if next != "" {
		res["NextToken"] = next
	}
	return res, nil
}

func (s *Service) awsAdminGroupMember(q *awsapi.Req) (any, error) {
	in := map[string]any{}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	id := str(in, "UserPoolId")
	if err := s.authz(q, q.Op, id); err != nil {
		return nil, err
	}
	g, err := s.groupOf(id, str(in, "GroupName"))
	if err != nil {
		return nil, err
	}
	add := q.Op == "AdminAddUserToGroup"
	_, err = s.modUser(id, str(in, "Username"), func(u *User) error {
		if add && !slices.Contains(u.Groups, g.Name) {
			u.Groups = append(u.Groups, g.Name)
		}
		if !add {
			u.Groups = slices.DeleteFunc(u.Groups, func(x string) bool { return x == g.Name })
		}
		return nil
	})
	return nil, err
}

func (s *Service) awsAdminListGroupsForUser(q *awsapi.Req) (any, error) {
	in := map[string]any{}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	id := str(in, "UserPoolId")
	if err := s.authz(q, "AdminListGroupsForUser", id); err != nil {
		return nil, err
	}
	p, err := s.pool(id)
	if err != nil {
		return nil, err
	}
	u, err := s.getUser(id, str(in, "Username"))
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, g := range p.Groups {
		if slices.Contains(u.Groups, g.Name) {
			out = append(out, groupDesc(id, g))
		}
	}
	return map[string]any{"Groups": out}, nil
}

func (s *Service) awsListUsersInGroup(q *awsapi.Req) (any, error) {
	in := map[string]any{}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	id := str(in, "UserPoolId")
	if err := s.authz(q, "ListUsersInGroup", id); err != nil {
		return nil, err
	}
	g, err := s.groupOf(id, str(in, "GroupName"))
	if err != nil {
		return nil, err
	}
	return s.usersPage(q, in, id, func(u User) bool { return slices.Contains(u.Groups, g.Name) }, "NextToken")
}

// ---- application-facing operations ----

// clientByID finds an app client (and its pool) from the ClientId alone, as
// unsigned operations only carry that.
func (s *Service) clientByID(id string) (Pool, Client, error) {
	cl, err := store.Get[Client](s.env.Store, cClients, id)
	if err != nil {
		return Pool{}, cl, core.Errf(http.StatusNotFound, "ResourceNotFoundException", "user pool client %s does not exist", id)
	}
	p, err := s.pool(cl.PoolID)
	return p, cl, err
}

func (s *Service) awsSignUp(q *awsapi.Req) (any, error) {
	in := map[string]any{}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	p, cl, err := s.clientByID(str(in, "ClientId"))
	if err != nil {
		return nil, err
	}
	username := str(in, "Username")
	secret, err := s.secretHash(cl, username, str(in, "SecretHash"))
	if err != nil {
		return nil, err
	}
	attrs := attrsIn(in["UserAttributes"])
	s.usernameAttr(p, username, attrs)
	u, err := s.selfSignUp(p.ID, authInput{ClientID: cl.ID, ClientSecret: secret, Username: username, Password: str(in, "Password"), Attributes: attrs})
	if err != nil {
		return nil, err
	}
	out := map[string]any{"UserConfirmed": u.Status == "CONFIRMED", "UserSub": u.Sub}
	if u.Status != "CONFIRMED" {
		out["CodeDeliveryDetails"] = codeDelivery(u)
	}
	return out, nil
}

func codeDelivery(u User) map[string]string {
	medium, attr := "EMAIL", "email"
	if u.Attributes["email"] == "" && u.Attributes["phone_number"] != "" {
		medium, attr = "SMS", "phone_number"
	}
	return map[string]string{"Destination": "***", "DeliveryMedium": medium, "AttributeName": attr}
}

func (s *Service) awsConfirmSignUp(q *awsapi.Req) (any, error) {
	in := map[string]any{}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	p, cl, err := s.clientByID(str(in, "ClientId"))
	if err != nil {
		return nil, err
	}
	if _, err := s.secretHash(cl, str(in, "Username"), str(in, "SecretHash")); err != nil {
		return nil, err
	}
	return nil, s.confirmSignUp(p.ID, str(in, "Username"), str(in, "ConfirmationCode"))
}

func (s *Service) awsResendCode(q *awsapi.Req) (any, error) {
	in := map[string]any{}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	p, cl, err := s.clientByID(str(in, "ClientId"))
	if err != nil {
		return nil, err
	}
	name := str(in, "Username")
	if _, err := s.secretHash(cl, name, str(in, "SecretHash")); err != nil {
		return nil, err
	}
	key := p.ID + "/resend/" + strings.ToLower(name)
	if !s.attempt(key) {
		return nil, core.Errf(http.StatusTooManyRequests, "TooManyRequestsException", "too many requests; try again later")
	}
	u, err := s.getUser(p.ID, name)
	if err != nil {
		return nil, err
	}
	if u.Status != "UNCONFIRMED" {
		return nil, invalid("User is already confirmed.")
	}
	if err := s.issueCode(p.ID, u.Username); err != nil {
		return nil, err
	}
	return map[string]any{"CodeDeliveryDetails": codeDelivery(u)}, nil
}

// flowAllowed applies the client's ExplicitAuthFlows when it has any.
func flowAllowed(cl Client, names ...string) bool {
	flows, ok := cl.Extra["ExplicitAuthFlows"].([]any)
	if !ok {
		return true
	}
	for _, f := range flows {
		if slices.Contains(names, f.(string)) {
			return true
		}
	}
	return false
}

// authResponse renders a sign-in result: tokens, or the new-password challenge.
func authResponse(res any) any {
	switch r := res.(type) {
	case *tokens:
		ar := map[string]any{"AccessToken": r.AccessToken, "IdToken": r.IDToken, "ExpiresIn": r.ExpiresIn, "TokenType": "Bearer"}
		if r.RefreshToken != "" {
			ar["RefreshToken"] = r.RefreshToken
		}
		return map[string]any{"AuthenticationResult": ar, "ChallengeParameters": map[string]string{}}
	case map[string]any:
		return map[string]any{"ChallengeName": r["challenge"], "Session": r["session"], "ChallengeParameters": map[string]string{
			"USER_ID_FOR_SRP": r["username"].(string), "requiredAttributes": "[]", "userAttributes": "{}"}}
	}
	return res
}

func strMap(v any) map[string]string {
	m, _ := v.(map[string]any)
	out := map[string]string{}
	for k, x := range m {
		out[k], _ = x.(string)
	}
	return out
}

// initiateAuth runs USER_PASSWORD_AUTH (or the admin variant) and
// REFRESH_TOKEN_AUTH for a client.
func (s *Service) initiateAuth(q *awsapi.Req, in map[string]any, admin bool) (any, error) {
	p, cl, err := s.clientByID(str(in, "ClientId"))
	if err != nil {
		return nil, err
	}
	if admin && str(in, "UserPoolId") != p.ID {
		return nil, core.Errf(http.StatusNotFound, "ResourceNotFoundException", "user pool client %s does not exist", cl.ID)
	}
	params := strMap(in["AuthParameters"])
	ai := authInput{ClientID: cl.ID, Username: params["USERNAME"], Password: params["PASSWORD"], RefreshToken: params["REFRESH_TOKEN"]}
	switch flow := str(in, "AuthFlow"); flow {
	case "USER_PASSWORD_AUTH", "ADMIN_USER_PASSWORD_AUTH", "ADMIN_NO_SRP_AUTH":
		want := "ALLOW_USER_PASSWORD_AUTH"
		if strings.HasPrefix(flow, "ADMIN") {
			want = "ALLOW_ADMIN_USER_PASSWORD_AUTH"
			if !admin {
				return nil, invalid("%s requires AdminInitiateAuth", flow)
			}
		} else if admin {
			return nil, invalid("%s is not an admin flow", flow)
		}
		if !flowAllowed(cl, want, "ADMIN_NO_SRP_AUTH", "USER_PASSWORD_AUTH") {
			return nil, invalid("%s flow not enabled for this client", flow)
		}
		if ai.Username == "" || ai.Password == "" {
			return nil, invalid("Missing required parameter USERNAME or PASSWORD")
		}
		ai.Flow = "USER_PASSWORD_AUTH"
	case "REFRESH_TOKEN_AUTH", "REFRESH_TOKEN":
		if !flowAllowed(cl, "ALLOW_REFRESH_TOKEN_AUTH") {
			return nil, invalid("%s flow not enabled for this client", flow)
		}
		if ai.RefreshToken == "" {
			return nil, invalid("Missing required parameter REFRESH_TOKEN")
		}
		ai.Flow = "REFRESH_TOKEN_AUTH"
	default:
		return nil, invalid("AuthFlow %s is not supported: HomeCloud supports USER_PASSWORD_AUTH and REFRESH_TOKEN_AUTH (enable ALLOW_USER_PASSWORD_AUTH on the app client)", flow)
	}
	// SECRET_HASH covers the username; for refresh flows the token stands in for it.
	who := ai.Username
	if who == "" {
		who = params["USERNAME"]
	}
	secret, err := s.secretHash(cl, who, params["SECRET_HASH"])
	if err != nil {
		return nil, err
	}
	ai.ClientSecret = secret
	res, err := s.authenticate(p.ID, ai, httpx.ClientIP(q.R))
	if err != nil {
		return nil, err
	}
	return authResponse(res), nil
}

func (s *Service) awsInitiateAuth(q *awsapi.Req) (any, error) {
	in := map[string]any{}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	return s.initiateAuth(q, in, false)
}

func (s *Service) awsAdminInitiateAuth(q *awsapi.Req) (any, error) {
	in := map[string]any{}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := s.authz(q, "AdminInitiateAuth", str(in, "UserPoolId")); err != nil {
		return nil, err
	}
	return s.initiateAuth(q, in, true)
}

func (s *Service) respondToChallenge(in map[string]any) (any, error) {
	p, cl, err := s.clientByID(str(in, "ClientId"))
	if err != nil {
		return nil, err
	}
	if name := str(in, "ChallengeName"); name != "NEW_PASSWORD_REQUIRED" {
		return nil, invalid("challenge %q is not supported: only NEW_PASSWORD_REQUIRED", name)
	}
	resp := strMap(in["ChallengeResponses"])
	secret, err := s.secretHash(cl, resp["USERNAME"], resp["SECRET_HASH"])
	if err != nil {
		return nil, err
	}
	if resp["USERNAME"] == "" || resp["NEW_PASSWORD"] == "" {
		return nil, invalid("Missing required parameter USERNAME or NEW_PASSWORD")
	}
	res, err := s.respondChallenge(p.ID, authInput{ClientID: cl.ID, ClientSecret: secret, Session: str(in, "Session"), Username: resp["USERNAME"], NewPassword: resp["NEW_PASSWORD"]})
	if err != nil {
		return nil, err
	}
	return authResponse(res), nil
}

func (s *Service) awsRespond(q *awsapi.Req) (any, error) {
	in := map[string]any{}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	return s.respondToChallenge(in)
}

func (s *Service) awsAdminRespond(q *awsapi.Req) (any, error) {
	in := map[string]any{}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := s.authz(q, "AdminRespondToAuthChallenge", str(in, "UserPoolId")); err != nil {
		return nil, err
	}
	return s.respondToChallenge(in)
}

// accessUser verifies the AccessToken of an unsigned request.
func (s *Service) accessUser(in map[string]any) (Pool, User, error) {
	tok := str(in, "AccessToken")
	bad := core.Errf(http.StatusBadRequest, "NotAuthorizedException", "Invalid Access Token")
	pool := s.poolOfToken(tok)
	if pool == "" {
		return Pool{}, User{}, bad
	}
	p, u, err := s.verifyAccess(pool, tok)
	if err != nil {
		return p, u, bad
	}
	return p, u, nil
}

func (s *Service) awsGetUser(q *awsapi.Req) (any, error) {
	in := map[string]any{}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	_, u, err := s.accessUser(in)
	if err != nil {
		return nil, err
	}
	return map[string]any{"Username": u.Username, "UserAttributes": attrsOut(u)}, nil
}

func (s *Service) awsGlobalSignOut(q *awsapi.Req) (any, error) {
	in := map[string]any{}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	p, u, err := s.accessUser(in)
	if err != nil {
		return nil, err
	}
	s.revokeAll(p.ID, u.Username)
	return nil, nil
}

func (s *Service) awsChangePassword(q *awsapi.Req) (any, error) {
	in := map[string]any{}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	p, u, err := s.accessUser(in)
	if err != nil {
		return nil, err
	}
	return nil, s.changePasswordOf(p, u, str(in, "PreviousPassword"), str(in, "ProposedPassword"))
}

func (s *Service) awsUpdateUserAttributes(q *awsapi.Req) (any, error) {
	in := map[string]any{}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	p, u, err := s.accessUser(in)
	if err != nil {
		return nil, err
	}
	attrs := attrsIn(in["UserAttributes"])
	for k := range attrs {
		if k == "email_verified" || k == "phone_number_verified" {
			// Only administrators mark an address as verified.
			return nil, core.Errf(http.StatusBadRequest, "NotAuthorizedException", "%s can only be changed by an administrator", k)
		}
	}
	if _, err := s.patchUser(p.ID, u.Username, attrs, nil, nil, false); err != nil {
		return nil, err
	}
	return map[string]any{"CodeDeliveryDetailsList": []any{}}, nil
}

func (s *Service) awsDeleteUser(q *awsapi.Req) (any, error) {
	in := map[string]any{}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	p, u, err := s.accessUser(in)
	if err != nil {
		return nil, err
	}
	return nil, s.removeUser(p.ID, u.Username)
}

func (s *Service) awsRevokeToken(q *awsapi.Req) (any, error) {
	in := map[string]any{}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	p, cl, err := s.clientByID(str(in, "ClientId"))
	if err != nil {
		return nil, err
	}
	if cl.SecretHash != "" {
		sec := str(in, "ClientSecret")
		if _, err := s.client(p, cl.ID, sec); err != nil {
			return nil, err
		}
	}
	key := hashToken(str(in, "Token"))
	rt, err := store.Get[refresh](s.env.Store, cRefresh, key)
	if err != nil || rt.ClientID != cl.ID {
		return nil, core.Errf(http.StatusBadRequest, "UnauthorizedException", "Invalid refresh token")
	}
	return nil, store.Delete(s.env.Store, cRefresh, key)
}
