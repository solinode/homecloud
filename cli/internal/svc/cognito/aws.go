package cognito

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

// The Amazon Cognito user pools API (awsJson 1.1, signing name cognito-idp):
// pools, app clients, users and groups for administrators (SigV4 + IAM), and
// the application-facing operations (SignUp, InitiateAuth, GetUser, ...) that
// AWS accepts without a signature. Tokens are the same JWTs the native API
// issues, verifiable with the pool's JWKS.

// unsigned lists the operations callers make without AWS credentials; they
// are authorized by the client ID, the user's password, or an access token.
var unsigned = []string{"SignUp", "ConfirmSignUp", "ResendConfirmationCode", "ForgotPassword", "ConfirmForgotPassword", "InitiateAuth", "RespondToAuthChallenge", "GetUser", "GlobalSignOut",
	"ChangePassword", "UpdateUserAttributes", "DeleteUser", "RevokeToken"}

// RegisterAWS serves Cognito user pools over the AWS protocol.
func (s *Service) RegisterAWS() {
	ops := map[string]awsapi.Op{
		"CreateUserPool":              s.awsCreateUserPool,
		"DescribeUserPool":            s.awsDescribeUserPool,
		"ListUserPools":               s.awsListUserPools,
		"UpdateUserPool":              s.awsUpdateUserPool,
		"DeleteUserPool":              s.awsDeleteUserPool,
		"CreateUserPoolClient":        s.awsCreateClient,
		"DescribeUserPoolClient":      s.awsDescribeClient,
		"ListUserPoolClients":         s.awsListClients,
		"UpdateUserPoolClient":        s.awsUpdateClient,
		"DeleteUserPoolClient":        s.awsDeleteClient,
		"AdminCreateUser":             s.awsAdminCreateUser,
		"AdminGetUser":                s.awsAdminGetUser,
		"AdminDeleteUser":             s.awsAdminDeleteUser,
		"AdminSetUserPassword":        s.awsAdminSetPassword,
		"AdminUpdateUserAttributes":   s.awsAdminUpdateAttrs,
		"AdminDeleteUserAttributes":   s.awsAdminDeleteAttrs,
		"AdminEnableUser":             s.awsAdminEnable,
		"AdminDisableUser":            s.awsAdminEnable,
		"AdminConfirmSignUp":          s.awsAdminConfirm,
		"AdminResetUserPassword":      s.awsAdminResetPassword,
		"AdminUserGlobalSignOut":      s.awsAdminSignOut,
		"AdminInitiateAuth":           s.awsAdminInitiateAuth,
		"AdminRespondToAuthChallenge": s.awsAdminRespond,
		"ListUsers":                   s.awsListUsers,
		"CreateGroup":                 s.awsCreateGroup,
		"GetGroup":                    s.awsGetGroup,
		"UpdateGroup":                 s.awsUpdateGroup,
		"DeleteGroup":                 s.awsDeleteGroup,
		"ListGroups":                  s.awsListGroups,
		"AdminAddUserToGroup":         s.awsAdminGroupMember,
		"AdminRemoveUserFromGroup":    s.awsAdminGroupMember,
		"AdminListGroupsForUser":      s.awsAdminListGroupsForUser,
		"ListUsersInGroup":            s.awsListUsersInGroup,
		"TagResource":                 s.awsTagResource,
		"UntagResource":               s.awsUntagResource,
		"ListTagsForResource":         s.awsListTags,
		"GetUserPoolMfaConfig":        s.awsGetMfaConfig,
		"SetUserPoolMfaConfig":        s.awsSetMfaConfig,
		"SignUp":                      s.awsSignUp,
		"ConfirmSignUp":               s.awsConfirmSignUp,
		"ResendConfirmationCode":      s.awsResendCode,
		"ForgotPassword":              s.awsForgotPassword,
		"ConfirmForgotPassword":       s.awsConfirmForgotPassword,
		"InitiateAuth":                s.awsInitiateAuth,
		"RespondToAuthChallenge":      s.awsRespond,
		"GetUser":                     s.awsGetUser,
		"GlobalSignOut":               s.awsGlobalSignOut,
		"ChangePassword":              s.awsChangePassword,
		"UpdateUserAttributes":        s.awsUpdateUserAttributes,
		"DeleteUser":                  s.awsDeleteUser,
		"RevokeToken":                 s.awsRevokeToken,
	}
	svc := &awsapi.Service{Name: "cognito-idp", JSONPrefix: "AWSCognitoIdentityProviderService", JSONVersion: "1.1", Ops: map[string]awsapi.Op{},
		PublicOps: map[string]bool{},
		ErrorCode: map[string]string{"BadRequest": "InvalidParameterException", "ValidationError": "InvalidParameterException", "Conflict": "InvalidParameterException"}}
	for name, fn := range ops {
		svc.Ops[name] = func(q *awsapi.Req) (any, error) {
			out, err := fn(q)
			return out, cognitoError(err)
		}
	}
	for _, name := range unsigned {
		svc.PublicOps[name] = true
	}
	awsapi.Register(svc)
}

// cognitoError gives token failures Cognito's 400 status.
func cognitoError(err error) error {
	var ce *core.Error
	if err != nil && errors.As(err, &ce) && ce.Status == http.StatusUnauthorized {
		return awsapi.Errorf(http.StatusBadRequest, ce.Code, "%s", ce.Message)
	}
	return err
}

func invalid(format string, a ...any) error {
	return awsapi.Errorf(http.StatusBadRequest, "InvalidParameterException", format, a...)
}

// ---- value helpers ----

func str(m map[string]any, k string) string { s, _ := m[k].(string); return s }

func flagOf(m map[string]any, k string) (v, ok bool) { v, ok = m[k].(bool); return }

func intOf(m map[string]any, k string) (int, bool) {
	f, ok := m[k].(float64)
	return int(f), ok
}

func tagsOf(v any) core.Tags {
	m, _ := v.(map[string]any)
	if len(m) == 0 {
		return nil
	}
	t := core.Tags{}
	for k, x := range m {
		t[k], _ = x.(string)
	}
	return t
}

func (s *Service) poolARN(id string) string { return s.env.ARN("cognito-idp", "userpool/"+id) }

func (s *Service) authz(q *awsapi.Req, action, poolID string) error {
	return q.Authorize("cognito-idp:"+action, s.poolARN(poolID))
}

func epoch(t time.Time) float64 { return awsapi.Epoch(t) }

// page returns the slice bounds of the page after token (an offset).
func page(total int, token string, limit int) (from, to int, next string, err error) {
	if token != "" {
		b, derr := base64.RawURLEncoding.DecodeString(token)
		n, cerr := strconv.Atoi(string(b))
		if derr != nil || cerr != nil || n < 0 || n > total {
			return 0, 0, "", awsapi.Errorf(http.StatusBadRequest, "InvalidParameterException", "invalid pagination token")
		}
		from = n
	}
	to = min(from+limit, total)
	if to < total {
		next = base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(to)))
	}
	return from, to, next, nil
}

func limitOf(m map[string]any, k string, def int) (int, error) {
	n, ok := intOf(m, k)
	if !ok {
		return def, nil
	}
	if n < 1 || n > 60 {
		return 0, invalid("%s must be between 1 and 60", k)
	}
	return n, nil
}

// ---- user pools ----

// poolExtraKeys are the CreateUserPool/UpdateUserPool settings stored and
// echoed by DescribeUserPool.
var poolExtraKeys = []string{"LambdaConfig", "AutoVerifiedAttributes", "UsernameAttributes", "AliasAttributes", "VerificationMessageTemplate",
	"SmsVerificationMessage", "EmailVerificationMessage", "EmailVerificationSubject", "SmsAuthenticationMessage", "MfaConfiguration",
	"DeviceConfiguration", "EmailConfiguration", "SmsConfiguration", "UserPoolAddOns", "UsernameConfiguration", "AccountRecoverySetting",
	"DeletionProtection", "UserPoolTier", "Schema"}

func mergeExtras(dst map[string]any, in map[string]any) map[string]any {
	for _, k := range poolExtraKeys {
		if v, ok := in[k]; ok {
			if dst == nil {
				dst = map[string]any{}
			}
			dst[k] = v
		}
	}
	return dst
}

// awsPolicy reads Policies.PasswordPolicy; AWS requires every character class
// unless the policy turns it off.
func awsPolicy(in map[string]any, base PasswordPolicy) PasswordPolicy {
	pol, _ := in["Policies"].(map[string]any)
	pp, _ := pol["PasswordPolicy"].(map[string]any)
	if pp == nil {
		return base
	}
	out := PasswordPolicy{MinLength: 8, RequireUppercase: true, RequireLowercase: true, RequireNumbers: true, RequireSymbols: true}
	if n, ok := intOf(pp, "MinimumLength"); ok {
		out.MinLength = n
	}
	for k, dst := range map[string]*bool{"RequireUppercase": &out.RequireUppercase, "RequireLowercase": &out.RequireLowercase,
		"RequireNumbers": &out.RequireNumbers, "RequireSymbols": &out.RequireSymbols} {
		if v, ok := flagOf(pp, k); ok {
			*dst = v
		}
	}
	return out
}

func adminOnly(in map[string]any) (v, ok bool) {
	c, _ := in["AdminCreateUserConfig"].(map[string]any)
	return flagOf(c, "AllowAdminCreateUserOnly")
}

func (s *Service) awsCreateUserPool(q *awsapi.Req) (any, error) {
	in := map[string]any{}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("cognito-idp:CreateUserPool", "*"); err != nil {
		return nil, err
	}
	// Users sign up with their e-mail address as username when UsernameAttributes says so.
	p, err := s.newPool(str(in, "PoolName"), awsPolicy(in, PasswordPolicy{MinLength: 8, RequireUppercase: true, RequireLowercase: true, RequireNumbers: true, RequireSymbols: true}),
		true, func() bool { v, ok := adminOnly(in); return !(ok && v) }(), tagsOf(in["UserPoolTags"]), mergeExtras(nil, in))
	if err != nil {
		return nil, err
	}
	return map[string]any{"UserPool": s.poolDesc(p)}, nil
}

var standardAttrs = []struct {
	name, typ string
}{{"sub", "String"}, {"name", "String"}, {"given_name", "String"}, {"family_name", "String"}, {"middle_name", "String"}, {"nickname", "String"},
	{"preferred_username", "String"}, {"profile", "String"}, {"picture", "String"}, {"website", "String"}, {"email", "String"}, {"email_verified", "Boolean"},
	{"gender", "String"}, {"birthdate", "String"}, {"zoneinfo", "String"}, {"locale", "String"}, {"phone_number", "String"},
	{"phone_number_verified", "Boolean"}, {"address", "String"}, {"updated_at", "Number"}}

func schemaAttrs(extra map[string]any) []map[string]any {
	out := []map[string]any{}
	for _, a := range standardAttrs {
		m := map[string]any{"Name": a.name, "AttributeDataType": a.typ, "DeveloperOnlyAttribute": false, "Mutable": a.name != "sub", "Required": false}
		switch a.typ {
		case "String":
			m["StringAttributeConstraints"] = map[string]string{"MinLength": "0", "MaxLength": "2048"}
		case "Number":
			m["NumberAttributeConstraints"] = map[string]string{"MinValue": "0"}
		}
		switch a.name {
		case "sub":
			m["Required"] = true
			m["StringAttributeConstraints"] = map[string]string{"MinLength": "1", "MaxLength": "2048"}
		case "birthdate": // YYYY-MM-DD, as AWS reports it
			m["StringAttributeConstraints"] = map[string]string{"MinLength": "10", "MaxLength": "10"}
		}
		out = append(out, m)
	}
	if l, ok := extra["Schema"].([]any); ok {
		for _, x := range l {
			if m, ok := x.(map[string]any); ok {
				c := map[string]any{}
				for k, v := range m {
					c[k] = v
				}
				// A standard attribute in the schema (e.g. email required) changes that attribute.
				if i := slices.IndexFunc(out[:len(standardAttrs)], func(s map[string]any) bool { return s["Name"] == str(c, "Name") }); i >= 0 {
					for _, k := range []string{"Required", "Mutable", "StringAttributeConstraints", "NumberAttributeConstraints"} {
						if v, ok := c[k]; ok {
							out[i][k] = v
						}
					}
					continue
				}
				if n := str(c, "Name"); n != "" && !strings.HasPrefix(n, "custom:") {
					c["Name"] = "custom:" + n
				}
				if _, ok := c["Mutable"]; !ok {
					c["Mutable"] = true
				}
				c["DeveloperOnlyAttribute"] = false
				out = append(out, c)
			}
		}
	}
	return out
}

func (s *Service) poolDesc(p Pool) map[string]any {
	users := 0
	for _, u := range store.List[User](s.env.Store, cUsers) {
		if u.PoolID == p.ID {
			users++
		}
	}
	m := map[string]any{"Id": p.ID, "Name": p.Name, "Arn": p.ARN, "CreationDate": epoch(p.CreatedAt), "LastModifiedDate": epoch(p.ModifiedAt),
		"Policies": map[string]any{"PasswordPolicy": map[string]any{"MinimumLength": p.PasswordPolicy.MinLength, "RequireUppercase": p.PasswordPolicy.RequireUppercase,
			"RequireLowercase": p.PasswordPolicy.RequireLowercase, "RequireNumbers": p.PasswordPolicy.RequireNumbers, "RequireSymbols": p.PasswordPolicy.RequireSymbols,
			"TemporaryPasswordValidityDays": 7}},
		"LambdaConfig": map[string]any{}, "AutoVerifiedAttributes": []string{}, "MfaConfiguration": "OFF", "EstimatedNumberOfUsers": users,
		"AdminCreateUserConfig": map[string]any{"AllowAdminCreateUserOnly": !p.SelfSignUp, "UnusedAccountValidityDays": 7},
		"UserPoolTags":          map[string]string(p.Tags), "DeletionProtection": "INACTIVE", "UserPoolTier": "ESSENTIALS",
		"SchemaAttributes": schemaAttrs(p.Extra), "AccountRecoverySetting": map[string]any{"RecoveryMechanisms": []any{
			map[string]any{"Priority": 1, "Name": "verified_email"}, map[string]any{"Priority": 2, "Name": "verified_phone_number"}}}}
	if p.Tags == nil {
		m["UserPoolTags"] = map[string]string{}
	}
	for k, v := range p.Extra {
		if k != "Schema" {
			m[k] = v
		}
	}
	return m
}

func (s *Service) awsDescribeUserPool(q *awsapi.Req) (any, error) {
	in := map[string]any{}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	id := str(in, "UserPoolId")
	if err := s.authz(q, "DescribeUserPool", id); err != nil {
		return nil, err
	}
	p, err := s.pool(id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"UserPool": s.poolDesc(p)}, nil
}

func (s *Service) sortedPools() []Pool {
	l := store.List[Pool](s.env.Store, cPools)
	sort.Slice(l, func(i, j int) bool {
		if !l[i].CreatedAt.Equal(l[j].CreatedAt) {
			return l[i].CreatedAt.Before(l[j].CreatedAt)
		}
		return l[i].ID < l[j].ID
	})
	return l
}

func (s *Service) awsListUserPools(q *awsapi.Req) (any, error) {
	in := map[string]any{}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("cognito-idp:ListUserPools", "*"); err != nil {
		return nil, err
	}
	if _, ok := intOf(in, "MaxResults"); !ok {
		return nil, invalid("MaxResults is required")
	}
	limit, err := limitOf(in, "MaxResults", 60)
	if err != nil {
		return nil, err
	}
	pools := s.sortedPools()
	from, to, next, err := page(len(pools), str(in, "NextToken"), limit)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, p := range pools[from:to] {
		out = append(out, map[string]any{"Id": p.ID, "Name": p.Name, "LambdaConfig": map[string]any{}, "CreationDate": epoch(p.CreatedAt), "LastModifiedDate": epoch(p.ModifiedAt)})
	}
	res := map[string]any{"UserPools": out}
	if next != "" {
		res["NextToken"] = next
	}
	return res, nil
}

func (s *Service) awsUpdateUserPool(q *awsapi.Req) (any, error) {
	in := map[string]any{}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	id := str(in, "UserPoolId")
	if err := s.authz(q, "UpdateUserPool", id); err != nil {
		return nil, err
	}
	_, err := s.modPool(id, func(p *Pool) error {
		p.PasswordPolicy = awsPolicy(in, p.PasswordPolicy)
		if err := checkPolicy(&p.PasswordPolicy); err != nil {
			return err
		}
		if v, ok := adminOnly(in); ok {
			p.SelfSignUp = !v
		}
		if _, ok := in["UserPoolTags"]; ok {
			p.Tags = tagsOf(in["UserPoolTags"])
		}
		p.Extra = mergeExtras(p.Extra, in)
		return nil
	})
	return nil, err
}

func (s *Service) awsDeleteUserPool(q *awsapi.Req) (any, error) {
	in := map[string]any{}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	id := str(in, "UserPoolId")
	if err := s.authz(q, "DeleteUserPool", id); err != nil {
		return nil, err
	}
	return nil, s.removePool(id)
}

func (s *Service) awsTagResource(q *awsapi.Req) (any, error) {
	in := map[string]any{}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	arn := str(in, "ResourceArn")
	if err := q.Authorize("cognito-idp:TagResource", arn); err != nil {
		return nil, err
	}
	_, err := s.modPool(poolOfARN(arn), func(p *Pool) error {
		if p.Tags == nil {
			p.Tags = core.Tags{}
		}
		for k, v := range tagsOf(in["Tags"]) {
			p.Tags[k] = v
		}
		return nil
	})
	return nil, err
}

func (s *Service) awsUntagResource(q *awsapi.Req) (any, error) {
	in := map[string]any{}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	arn := str(in, "ResourceArn")
	if err := q.Authorize("cognito-idp:UntagResource", arn); err != nil {
		return nil, err
	}
	keys, _ := in["TagKeys"].([]any)
	_, err := s.modPool(poolOfARN(arn), func(p *Pool) error {
		for _, k := range keys {
			delete(p.Tags, k.(string))
		}
		return nil
	})
	return nil, err
}

func (s *Service) awsListTags(q *awsapi.Req) (any, error) {
	in := map[string]any{}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	arn := str(in, "ResourceArn")
	if err := q.Authorize("cognito-idp:ListTagsForResource", arn); err != nil {
		return nil, err
	}
	p, err := s.pool(poolOfARN(arn))
	if err != nil {
		return nil, err
	}
	t := map[string]string(p.Tags)
	if t == nil {
		t = map[string]string{}
	}
	return map[string]any{"Tags": t}, nil
}

// poolOfARN returns the pool ID of a user pool ARN.
func poolOfARN(arn string) string {
	if i := strings.LastIndex(arn, ":userpool/"); i >= 0 {
		return arn[i+len(":userpool/"):]
	}
	return arn
}

func (s *Service) awsGetMfaConfig(q *awsapi.Req) (any, error) {
	in := map[string]any{}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	id := str(in, "UserPoolId")
	if err := s.authz(q, "GetUserPoolMfaConfig", id); err != nil {
		return nil, err
	}
	p, err := s.pool(id)
	if err != nil {
		return nil, err
	}
	mfa := "OFF"
	if v := str(p.Extra, "MfaConfiguration"); v != "" {
		mfa = v
	}
	return map[string]any{"MfaConfiguration": mfa}, nil
}

func (s *Service) awsSetMfaConfig(q *awsapi.Req) (any, error) {
	in := map[string]any{}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	id := str(in, "UserPoolId")
	if err := s.authz(q, "SetUserPoolMfaConfig", id); err != nil {
		return nil, err
	}
	// Multi-factor sign-in is not implemented: the setting is stored and echoed.
	_, err := s.modPool(id, func(p *Pool) error { p.Extra = mergeExtras(p.Extra, in); return nil })
	if err != nil {
		return nil, err
	}
	mfa := str(in, "MfaConfiguration")
	if mfa == "" {
		mfa = "OFF"
	}
	return map[string]any{"MfaConfiguration": mfa}, nil
}

// ---- app clients ----

func unitMinutes(unit string, def string) int {
	if unit == "" {
		unit = def
	}
	switch unit {
	case "seconds":
		return 0 // handled by caller (fractions of a minute)
	case "minutes":
		return 1
	case "hours":
		return 60
	case "days":
		return 1440
	}
	return -1
}

// clientLifetimes reads token validities and converts them to the native
// units (minutes for access tokens, whole days for refresh tokens).
func clientLifetimes(in map[string]any) (accessMinutes, refreshDays int, err error) {
	units, _ := in["TokenValidityUnits"].(map[string]any)
	if v, ok := intOf(in, "AccessTokenValidity"); ok {
		f := unitMinutes(str(units, "AccessToken"), "hours")
		if f < 1 {
			return 0, 0, invalid("invalid TokenValidityUnits.AccessToken")
		}
		accessMinutes = v * f
		if accessMinutes < 5 || accessMinutes > 1440 {
			return 0, 0, invalid("AccessTokenValidity must be between 5 minutes and 1 day")
		}
	}
	if v, ok := intOf(in, "RefreshTokenValidity"); ok {
		unit := str(units, "RefreshToken")
		if unit == "" {
			unit = "days"
		}
		f := unitMinutes(unit, "days")
		if f < 1 {
			return 0, 0, invalid("invalid TokenValidityUnits.RefreshToken")
		}
		refreshDays = (v*f + 1439) / 1440
		if refreshDays < 1 || refreshDays > 3650 {
			return 0, 0, invalid("RefreshTokenValidity must be between 1 day and 10 years")
		}
	}
	return accessMinutes, refreshDays, nil
}

var clientExtraKeys = []string{"AccessTokenValidity", "IdTokenValidity", "RefreshTokenValidity", "TokenValidityUnits", "ExplicitAuthFlows", "CallbackURLs", "LogoutURLs",
	"DefaultRedirectURI", "AllowedOAuthFlows", "AllowedOAuthScopes", "AllowedOAuthFlowsUserPoolClient", "SupportedIdentityProviders", "ReadAttributes",
	"WriteAttributes", "PreventUserExistenceErrors", "EnableTokenRevocation", "EnablePropagateAdditionalUserContextData", "AuthSessionValidity", "AnalyticsConfiguration"}

func clientExtras(dst, in map[string]any) map[string]any {
	for _, k := range clientExtraKeys {
		if v, ok := in[k]; ok {
			if dst == nil {
				dst = map[string]any{}
			}
			dst[k] = v
		}
	}
	return dst
}

func (s *Service) clientDesc(cl Client) map[string]any {
	m := map[string]any{"UserPoolId": cl.PoolID, "ClientName": cl.Name, "ClientId": cl.ID, "CreationDate": epoch(cl.CreatedAt),
		"LastModifiedDate": epoch(cl.ModifiedAt), "RefreshTokenValidity": cl.RefreshTokenDays, "AllowedOAuthFlowsUserPoolClient": false}
	for k, v := range cl.Extra {
		m[k] = v
	}
	if cl.SecretCT != "" {
		if b, err := s.secrets.Decrypt(cl.SecretCT); err == nil {
			m["ClientSecret"] = string(b)
		}
	}
	return m
}

func (s *Service) awsCreateClient(q *awsapi.Req) (any, error) {
	in := map[string]any{}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	id := str(in, "UserPoolId")
	if err := s.authz(q, "CreateUserPoolClient", id); err != nil {
		return nil, err
	}
	if str(in, "ClientName") == "" {
		return nil, invalid("ClientName is required")
	}
	am, rd, err := clientLifetimes(in)
	if err != nil {
		return nil, err
	}
	gen, _ := flagOf(in, "GenerateSecret")
	cl, _, err := s.newClient(id, str(in, "ClientName"), gen, am, rd, clientExtras(nil, in))
	if err != nil {
		return nil, err
	}
	return map[string]any{"UserPoolClient": s.clientDesc(cl)}, nil
}

func (s *Service) clientOf(poolID, clientID string) (Client, error) {
	cl, err := store.Get[Client](s.env.Store, cClients, clientID)
	if err != nil || cl.PoolID != poolID {
		return cl, core.Errf(http.StatusNotFound, "ResourceNotFoundException", "user pool client %s does not exist", clientID)
	}
	return cl, nil
}

func (s *Service) awsDescribeClient(q *awsapi.Req) (any, error) {
	in := map[string]any{}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	id := str(in, "UserPoolId")
	if err := s.authz(q, "DescribeUserPoolClient", id); err != nil {
		return nil, err
	}
	cl, err := s.clientOf(id, str(in, "ClientId"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"UserPoolClient": s.clientDesc(cl)}, nil
}

func (s *Service) awsListClients(q *awsapi.Req) (any, error) {
	in := map[string]any{}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	id := str(in, "UserPoolId")
	if err := s.authz(q, "ListUserPoolClients", id); err != nil {
		return nil, err
	}
	if _, err := s.pool(id); err != nil {
		return nil, err
	}
	limit, err := limitOf(in, "MaxResults", 60)
	if err != nil {
		return nil, err
	}
	var cls []Client
	for _, c := range store.List[Client](s.env.Store, cClients) {
		if c.PoolID == id {
			cls = append(cls, c)
		}
	}
	sort.Slice(cls, func(i, j int) bool {
		if !cls[i].CreatedAt.Equal(cls[j].CreatedAt) {
			return cls[i].CreatedAt.Before(cls[j].CreatedAt)
		}
		return cls[i].ID < cls[j].ID
	})
	from, to, next, err := page(len(cls), str(in, "NextToken"), limit)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, c := range cls[from:to] {
		out = append(out, map[string]any{"ClientId": c.ID, "UserPoolId": c.PoolID, "ClientName": c.Name})
	}
	res := map[string]any{"UserPoolClients": out}
	if next != "" {
		res["NextToken"] = next
	}
	return res, nil
}

func (s *Service) awsUpdateClient(q *awsapi.Req) (any, error) {
	in := map[string]any{}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	id := str(in, "UserPoolId")
	if err := s.authz(q, "UpdateUserPoolClient", id); err != nil {
		return nil, err
	}
	if _, err := s.clientOf(id, str(in, "ClientId")); err != nil {
		return nil, err
	}
	am, rd, err := clientLifetimes(in)
	if err != nil {
		return nil, err
	}
	cl, err := store.Update(s.env.Store, cClients, str(in, "ClientId"), func(c *Client) error {
		if n := str(in, "ClientName"); n != "" {
			c.Name = n
		}
		if am != 0 {
			c.AccessTokenMinutes = am
		}
		if rd != 0 {
			c.RefreshTokenDays = rd
		}
		c.Extra = clientExtras(c.Extra, in)
		c.ModifiedAt = core.Now()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"UserPoolClient": s.clientDesc(cl)}, nil
}

func (s *Service) awsDeleteClient(q *awsapi.Req) (any, error) {
	in := map[string]any{}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	id := str(in, "UserPoolId")
	if err := s.authz(q, "DeleteUserPoolClient", id); err != nil {
		return nil, err
	}
	return nil, s.removeClient(id, str(in, "ClientId"))
}

// secretHash checks SECRET_HASH (base64 HMAC-SHA256 of username+clientId keyed
// with the client secret), required for clients that have a secret. It returns
// the secret so the shared client check passes.
func (s *Service) secretHash(cl Client, username, given string) (string, error) {
	if cl.SecretHash == "" {
		return "", nil
	}
	deny := core.Errf(http.StatusBadRequest, "NotAuthorizedException", "Client %s is configured with secret but SECRET_HASH was not received", cl.ID)
	if given == "" || cl.SecretCT == "" {
		return "", deny
	}
	b, err := s.secrets.Decrypt(cl.SecretCT)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, b)
	mac.Write([]byte(username + cl.ID))
	want := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(want), []byte(given)) != 1 {
		return "", core.Errf(http.StatusBadRequest, "NotAuthorizedException", "Unable to verify secret hash for client %s", cl.ID)
	}
	return string(b), nil
}
