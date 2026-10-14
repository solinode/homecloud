package iam

import (
	"net/http"
	"regexp"
	"strconv"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

// Account-level IAM settings: the password policy and the account alias.
// Both are single records in cAccountSettings.

const (
	cAccountSettings = "iam_account_settings"
	idPasswordPolicy = "password_policy"
	idAccountAlias   = "alias"
)

// PasswordPolicy is the account password policy (UpdateAccountPasswordPolicy).
// HomeCloud stores and reports it; console passwords are not yet checked against it.
type PasswordPolicy struct {
	MinimumPasswordLength      int  `json:"minimum_password_length"`
	RequireSymbols             bool `json:"require_symbols"`
	RequireNumbers             bool `json:"require_numbers"`
	RequireUppercaseCharacters bool `json:"require_uppercase_characters"`
	RequireLowercaseCharacters bool `json:"require_lowercase_characters"`
	AllowUsersToChangePassword bool `json:"allow_users_to_change_password"`
	MaxPasswordAge             int  `json:"max_password_age,omitempty"`          // days; 0 = passwords do not expire
	PasswordReusePrevention    int  `json:"password_reuse_prevention,omitempty"` // 0 = not set
	HardExpiry                 bool `json:"hard_expiry,omitempty"`
}

type accountAlias struct {
	Alias string `json:"alias"`
}

// GetPasswordPolicy returns the account password policy, or NoSuchEntity.
func (s *Service) GetPasswordPolicy() (PasswordPolicy, error) {
	p, err := store.Get[PasswordPolicy](s.env.Store, cAccountSettings, idPasswordPolicy)
	if err != nil {
		return p, noSuchEntity("The Password Policy with domain name %s cannot be found.", s.env.AccountID)
	}
	return p, nil
}

// AccountAlias returns the account alias, or "" when none is set.
func (s *Service) AccountAlias() string {
	a, _ := store.Get[accountAlias](s.env.Store, cAccountSettings, idAccountAlias)
	return a.Alias
}

func rangeError(field string, v, lo, hi int) error {
	if v < lo {
		return awsapi.Errorf(http.StatusBadRequest, "ValidationError",
			"1 validation error detected: Value '%d' at '%s' failed to satisfy constraint: Member must have value greater than or equal to %d", v, field, lo)
	}
	return awsapi.Errorf(http.StatusBadRequest, "ValidationError",
		"1 validation error detected: Value '%d' at '%s' failed to satisfy constraint: Member must have value less than or equal to %d", v, field, hi)
}

// intParam reads an optional integer parameter within [lo, hi].
func intParam(q *awsapi.Req, name string, def, lo, hi int) (int, error) {
	raw := q.Param(name)
	if raw == "" {
		return def, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, awsapi.Errorf(http.StatusBadRequest, "ValidationError", "%s must be an integer", name)
	}
	if v < lo || v > hi {
		return 0, rangeError(lowerFirst(name), v, lo, hi)
	}
	return v, nil
}

func (s *Service) awsUpdateAccountPasswordPolicy(q *awsapi.Req) (any, error) {
	if err := q.Authorize("iam:UpdateAccountPasswordPolicy", "*"); err != nil {
		return nil, err
	}
	// Like AWS, parameters left out take their defaults (they do not keep
	// the previous policy's values).
	p := PasswordPolicy{
		RequireSymbols:             q.ParamBool("RequireSymbols", false),
		RequireNumbers:             q.ParamBool("RequireNumbers", false),
		RequireUppercaseCharacters: q.ParamBool("RequireUppercaseCharacters", false),
		RequireLowercaseCharacters: q.ParamBool("RequireLowercaseCharacters", false),
		AllowUsersToChangePassword: q.ParamBool("AllowUsersToChangePassword", false),
		HardExpiry:                 q.ParamBool("HardExpiry", false),
	}
	var err error
	if p.MinimumPasswordLength, err = intParam(q, "MinimumPasswordLength", 8, 6, 128); err != nil {
		return nil, err
	}
	if p.MaxPasswordAge, err = intParam(q, "MaxPasswordAge", 0, 1, 1095); err != nil {
		return nil, err
	}
	if p.PasswordReusePrevention, err = intParam(q, "PasswordReusePrevention", 0, 1, 24); err != nil {
		return nil, err
	}
	return awsapi.NoResult{}, store.Put(s.env.Store, cAccountSettings, idPasswordPolicy, p)
}

func (s *Service) awsGetAccountPasswordPolicy(q *awsapi.Req) (any, error) {
	if err := q.Authorize("iam:GetAccountPasswordPolicy", "*"); err != nil {
		return nil, err
	}
	p, err := s.GetPasswordPolicy()
	if err != nil {
		return nil, err
	}
	out := awsapi.Ordered{
		{K: "MinimumPasswordLength", V: p.MinimumPasswordLength},
		{K: "RequireSymbols", V: p.RequireSymbols},
		{K: "RequireNumbers", V: p.RequireNumbers},
		{K: "RequireUppercaseCharacters", V: p.RequireUppercaseCharacters},
		{K: "RequireLowercaseCharacters", V: p.RequireLowercaseCharacters},
		{K: "AllowUsersToChangePassword", V: p.AllowUsersToChangePassword},
		{K: "ExpirePasswords", V: p.MaxPasswordAge > 0},
	}
	if p.MaxPasswordAge > 0 {
		out = append(out, awsapi.KV{K: "MaxPasswordAge", V: p.MaxPasswordAge})
	}
	if p.PasswordReusePrevention > 0 {
		out = append(out, awsapi.KV{K: "PasswordReusePrevention", V: p.PasswordReusePrevention})
	}
	if p.HardExpiry {
		out = append(out, awsapi.KV{K: "HardExpiry", V: true})
	}
	return map[string]any{"PasswordPolicy": out}, nil
}

func (s *Service) awsDeleteAccountPasswordPolicy(q *awsapi.Req) (any, error) {
	if err := q.Authorize("iam:DeleteAccountPasswordPolicy", "*"); err != nil {
		return nil, err
	}
	if _, err := s.GetPasswordPolicy(); err != nil {
		return nil, err
	}
	return awsapi.NoResult{}, store.Delete(s.env.Store, cAccountSettings, idPasswordPolicy)
}

// AWS: 3-63 lowercase letters, digits and single hyphens, not at either end.
var aliasRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9]|-[a-z0-9])*$`)

func validAlias(a string) error {
	if len(a) < 3 || len(a) > 63 || !aliasRe.MatchString(a) {
		return awsapi.Errorf(http.StatusBadRequest, "ValidationError",
			"1 validation error detected: Value '%s' at 'accountAlias' failed to satisfy constraint: Member must have length between 3 and 63 and match ^[a-z0-9]([a-z0-9]|-(?!-)){1,61}[a-z0-9]$", a)
	}
	return nil
}

func (s *Service) awsCreateAccountAlias(q *awsapi.Req) (any, error) {
	if err := required(q, "AccountAlias"); err != nil {
		return nil, err
	}
	alias := q.Param("AccountAlias")
	if err := validAlias(alias); err != nil {
		return nil, err
	}
	if err := q.Authorize("iam:CreateAccountAlias", "*"); err != nil {
		return nil, err
	}
	// An account has one alias; a new one replaces it, as in AWS.
	return awsapi.NoResult{}, store.Put(s.env.Store, cAccountSettings, idAccountAlias, accountAlias{Alias: alias})
}

func (s *Service) awsDeleteAccountAlias(q *awsapi.Req) (any, error) {
	if err := required(q, "AccountAlias"); err != nil {
		return nil, err
	}
	alias := q.Param("AccountAlias")
	if err := q.Authorize("iam:DeleteAccountAlias", "*"); err != nil {
		return nil, err
	}
	if cur := s.AccountAlias(); cur == "" || cur != alias {
		return nil, noSuchEntity("The account alias %s cannot be found.", alias)
	}
	return awsapi.NoResult{}, store.Delete(s.env.Store, cAccountSettings, idAccountAlias)
}

func (s *Service) awsListAccountAliases(q *awsapi.Req) (any, error) {
	if err := q.Authorize("iam:ListAccountAliases", "*"); err != nil {
		return nil, err
	}
	aliases := awsapi.Members{}
	if a := s.AccountAlias(); a != "" {
		aliases = append(aliases, a)
	}
	return map[string]any{"AccountAliases": aliases, "IsTruncated": false}, nil
}
