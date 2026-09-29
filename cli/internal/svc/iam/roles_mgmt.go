package iam

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

// RoleInput creates a role.
type RoleInput struct {
	Name                string          `json:"name"`
	Path                string          `json:"path"`
	Description         string          `json:"description"`
	TrustPolicy         json.RawMessage `json:"assume_role_policy"`
	MaxSessionSeconds   int             `json:"max_session_duration"`
	Policies            []string        `json:"policies"` // managed policies to attach
	Tags                core.Tags       `json:"tags"`
	PermissionsBoundary string          `json:"permissions_boundary"`
	serviceLinked       string
}

// ParseTrust parses a trust policy from JSON (a document, or a JSON string holding one).
func ParseTrust(raw json.RawMessage) (PolicyDocument, error) {
	var str string
	if json.Unmarshal(raw, &str) == nil {
		raw = json.RawMessage(str)
	}
	d, err := ParsePolicy(string(raw))
	if err != nil {
		return d, err
	}
	return d, d.ValidateTrust()
}

func checkMaxSession(n int) error {
	if n < 3600 || n > 43200 {
		return core.Errf(http.StatusBadRequest, "ValidationError", "MaxSessionDuration must be 3600-43200 seconds")
	}
	return nil
}

// CreateRole creates a role. Everything set on it beyond the trust policy
// needs its own permission (attaching policies, tags, a boundary).
func (s *Service) CreateRole(in RoleInput, can authz) (Role, error) {
	if err := validName("role", in.Name); err != nil {
		return Role{}, err
	}
	path, err := validPath(in.Path)
	if err != nil {
		return Role{}, err
	}
	if len(in.TrustPolicy) == 0 {
		return Role{}, malformed("AssumeRolePolicyDocument is required")
	}
	trust, err := ParseTrust(in.TrustPolicy)
	if err != nil {
		return Role{}, err
	}
	if in.MaxSessionSeconds == 0 {
		in.MaxSessionSeconds = 3600
	}
	if err := checkMaxSession(in.MaxSessionSeconds); err != nil {
		return Role{}, err
	}
	if len(in.Description) > 1000 {
		return Role{}, core.Errf(http.StatusBadRequest, "ValidationError", "description must be at most 1000 characters")
	}
	if err := checkTags(in.Tags); err != nil {
		return Role{}, err
	}
	arn := s.roleARN(in.Name, path)
	r := Role{Name: in.Name, ID: newID("HCRO"), ARN: arn, Path: path, Description: in.Description, TrustPolicy: trust,
		InlinePolicies: map[string]PolicyDocument{}, MaxSessionSeconds: in.MaxSessionSeconds, CreatedAt: core.Now(), Tags: in.Tags,
		AttachedPolicies: []string{}, ServiceLinked: in.serviceLinked}
	for _, ref := range in.Policies {
		if err := can("iam:AttachRolePolicy", arn); err != nil {
			return Role{}, err
		}
		if r.AttachedPolicies, err = s.attachTo(r.AttachedPolicies, ref); err != nil {
			return Role{}, err
		}
	}
	if len(in.Tags) > 0 {
		if err := can("iam:TagRole", arn); err != nil {
			return Role{}, err
		}
	}
	if in.PermissionsBoundary != "" {
		if err := can("iam:PutRolePermissionsBoundary", arn); err != nil {
			return Role{}, err
		}
		b, err := s.resolvePolicy(in.PermissionsBoundary)
		if err != nil {
			return Role{}, err
		}
		r.PermissionsBoundary = b.ARN
	}
	s.roleMu.Lock()
	defer s.roleMu.Unlock()
	if nameTaken(s, cRoles, r.Name, "", func(r Role) string { return r.Name }) {
		return Role{}, alreadyExists("Role with name %s already exists.", r.Name)
	}
	return r, store.Put(s.env.Store, cRoles, r.Name, r)
}

// DeleteRole deletes a role and revokes its sessions. Like AWS, a role with
// policies attached or in an instance profile must be cleaned up first.
func (s *Service) DeleteRole(name string) error {
	r, err := s.GetRole(name)
	if err != nil {
		return err
	}
	switch {
	case len(r.AttachedPolicies) > 0:
		return deleteConflict("Cannot delete entity, must detach all policies first.")
	case len(r.InlinePolicies) > 0:
		return deleteConflict("Cannot delete entity, must delete policies first.")
	case len(s.profilesForRole(r.Name)) > 0:
		return deleteConflict("Cannot delete entity, must remove roles from instance profile first.")
	}
	if err := store.Delete(s.env.Store, cRoles, r.Name); err != nil {
		return err
	}
	s.RevokeRoleSessions(r.Name)
	return nil
}

func (s *Service) roleARNByName(ref string) string {
	if r, err := s.GetRole(ref); err == nil {
		return r.ARN
	}
	return s.roleARN(roleName(ref), "/")
}

// UpdateRole applies fn to a role.
func (s *Service) UpdateRole(name string, fn func(*Role) error) (Role, error) {
	r, err := store.Update(s.env.Store, cRoles, roleName(name), fn)
	if err == store.ErrNotFound {
		return r, noSuchEntity("The role with name %s cannot be found.", roleName(name))
	}
	return r, err
}

// SetTrustPolicy replaces a role's trust policy (JSON text).
func (s *Service) SetTrustPolicy(name, text string) (Role, error) {
	d, err := ParseTrust(json.RawMessage(text))
	if err != nil {
		return Role{}, err
	}
	return s.UpdateRole(name, func(r *Role) error { r.TrustPolicy = d; return nil })
}

// AttachRolePolicy attaches a managed policy (name or ARN) to a role.
func (s *Service) AttachRolePolicy(role, policy string) (Role, error) {
	if _, err := s.resolvePolicy(policy); err != nil {
		return Role{}, err
	}
	return s.UpdateRole(role, func(r *Role) (err error) {
		r.AttachedPolicies, err = s.attachTo(r.AttachedPolicies, policy)
		return err
	})
}

// DetachRolePolicy detaches a managed policy (name or ARN) from a role.
func (s *Service) DetachRolePolicy(role, policy string) (Role, error) {
	return s.UpdateRole(role, func(r *Role) (err error) {
		r.AttachedPolicies, err = s.detachFrom(r.AttachedPolicies, policy, "role "+r.Name)
		return err
	})
}

// PutRolePolicy sets an inline policy on a role.
func (s *Service) PutRolePolicy(role, name string, d PolicyDocument) (Role, error) {
	if err := checkInline(name, d, 10240); err != nil {
		return Role{}, err
	}
	return s.UpdateRole(role, func(r *Role) error {
		if r.InlinePolicies == nil {
			r.InlinePolicies = map[string]PolicyDocument{}
		}
		r.InlinePolicies[name] = d
		return nil
	})
}

// DeleteRolePolicy removes an inline policy from a role.
func (s *Service) DeleteRolePolicy(role, name string) (Role, error) {
	return s.UpdateRole(role, func(r *Role) error {
		if _, ok := r.InlinePolicies[name]; !ok {
			return noSuchEntity("The role policy with name %s cannot be found.", name)
		}
		delete(r.InlinePolicies, name)
		return nil
	})
}

// SetRoleBoundary sets (or with ref "" removes) a role's permissions boundary.
func (s *Service) SetRoleBoundary(role, ref string) (Role, error) {
	arn := ""
	if ref != "" {
		p, err := s.resolvePolicy(ref)
		if err != nil {
			return Role{}, err
		}
		arn = p.ARN
	}
	return s.UpdateRole(role, func(r *Role) error { r.PermissionsBoundary = arn; return nil })
}

// serviceRoleNames are the role names AWS gives service-linked roles.
var serviceRoleNames = map[string]string{
	"autoscaling.amazonaws.com":                      "AWSServiceRoleForAutoScaling",
	"ecs.amazonaws.com":                              "AWSServiceRoleForECS",
	"elasticloadbalancing.amazonaws.com":             "AWSServiceRoleForElasticLoadBalancing",
	"rds.amazonaws.com":                              "AWSServiceRoleForRDS",
	"elasticache.amazonaws.com":                      "AWSServiceRoleForElastiCache",
	"spot.amazonaws.com":                             "AWSServiceRoleForEC2Spot",
	"ssm.amazonaws.com":                              "AWSServiceRoleForAmazonSSM",
	"lambda.amazonaws.com":                           "AWSServiceRoleForLambda",
	"dynamodb.application-autoscaling.amazonaws.com": "AWSServiceRoleForApplicationAutoScaling_DynamoDBTable",
	"ecs.application-autoscaling.amazonaws.com":      "AWSServiceRoleForApplicationAutoScaling_ECSService",
	"events.amazonaws.com":                           "AWSServiceRoleForCloudWatchEvents",
	"cognito-idp.amazonaws.com":                      "AWSServiceRoleForAmazonCognitoIdp",
	"acm.amazonaws.com":                              "AWSServiceRoleForCertificateManager",
	"route53.amazonaws.com":                          "AWSServiceRoleForRoute53",
}

var serviceNameRe = regexp.MustCompile(`^[a-z0-9-]+(\.[a-z0-9-]+)*\.amazonaws\.com$`)

// CreateServiceLinkedRole creates the role an AWS service uses on the
// account's behalf: /aws-service-role/<service>/AWSServiceRoleFor<Service>,
// trusting only that service.
func (s *Service) CreateServiceLinkedRole(service, description, suffix string) (Role, error) {
	if !serviceNameRe.MatchString(service) {
		return Role{}, invalidInput("AWSServiceName %q is not a service principal (for example ecs.amazonaws.com)", service)
	}
	name := serviceRoleNames[service]
	if name == "" {
		label := strings.Split(service, ".")[0]
		parts := strings.Split(label, "-")
		for i, p := range parts {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
		name = "AWSServiceRoleFor" + strings.Join(parts, "")
	}
	if suffix != "" {
		name += "_" + suffix
	}
	trust, _ := json.Marshal(map[string]any{"Version": policyVersion, "Statement": []any{map[string]any{
		"Effect": "Allow", "Principal": map[string]any{"Service": service}, "Action": "sts:AssumeRole"}}})
	return s.CreateRole(RoleInput{Name: name, Path: "/aws-service-role/" + service + "/", Description: description,
		TrustPolicy: trust, serviceLinked: service}, func(string, string) error { return nil })
}

// DeleteServiceLinkedRole deletes a service-linked role (its policies go with it).
func (s *Service) DeleteServiceLinkedRole(name string) error {
	r, err := s.GetRole(name)
	if err != nil {
		return err
	}
	if r.ServiceLinked == "" {
		return invalidInput("Role %s is not a service-linked role.", r.Name)
	}
	if len(s.profilesForRole(r.Name)) > 0 {
		return deleteConflict("Cannot delete entity, must remove roles from instance profile first.")
	}
	if err := store.Delete(s.env.Store, cRoles, r.Name); err != nil {
		return err
	}
	s.RevokeRoleSessions(r.Name)
	return nil
}

func (s *Service) roleView(r Role) map[string]any {
	return map[string]any{"name": r.Name, "id": r.ID, "arn": r.ARN, "path": r.path(), "description": r.Description,
		"assume_role_policy": r.TrustPolicy, "attached_policies": nz(r.AttachedPolicies), "inline_policies": r.InlinePolicies,
		"max_session_duration": r.MaxSessionSeconds, "created_at": r.CreatedAt, "last_used": r.LastUsed, "tags": r.Tags,
		"trusted_services": trustedServices(r.TrustPolicy), "permissions_boundary": r.PermissionsBoundary,
		"instance_profiles": nz(s.profilesForRole(r.Name)), "service_linked": r.ServiceLinked}
}

func trustedServices(d PolicyDocument) []string {
	out := []string{}
	for _, st := range d.Statement {
		if st.Effect == "Allow" && st.Principal != nil {
			out = append(out, st.Principal.Service...)
		}
	}
	return out
}

func store_getUser(s *Service, name string) (User, error) {
	return store.Get[User](s.env.Store, cUsers, name)
}
