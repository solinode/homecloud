package iam

import (
	"encoding/json"
	"log"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

const maxPolicyVersions = 5

// Policy is a managed policy: an AWS managed (built-in) policy at
// arn:aws:iam::aws:policy/<Name>, or a customer managed one in the account.
// Policies are stored by name, which is unique across both kinds.
type Policy struct {
	Name        string         `json:"name"`
	ID          string         `json:"id,omitempty"`
	ARN         string         `json:"arn"`
	Path        string         `json:"path,omitempty"`
	Description string         `json:"description"`
	Managed     bool           `json:"managed"`  // AWS managed (built in); cannot be edited
	Document    PolicyDocument `json:"document"` // the default version's document
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	Tags        core.Tags      `json:"tags,omitempty"`
	// Versions holds up to five versions; DefaultVersion is the one in effect.
	Versions       []PolicyVersion `json:"versions,omitempty"`
	DefaultVersion string          `json:"default_version,omitempty"`
	NextVersion    int             `json:"next_version,omitempty"`
}

// PolicyVersion is one version of a managed policy's document.
type PolicyVersion struct {
	ID        string         `json:"id"` // v1, v2, ...
	Document  PolicyDocument `json:"document"`
	CreatedAt time.Time      `json:"created_at"`
}

func (p Policy) path() string {
	if p.Path == "" {
		return "/"
	}
	return p.Path
}

func (p Policy) version(id string) (PolicyVersion, bool) {
	for _, v := range p.Versions {
		if v.ID == id {
			return v, true
		}
	}
	return PolicyVersion{}, false
}

func (s *Service) policyARN(name, path string) string {
	return s.env.ARN("iam", "policy"+path+name)
}

func awsPolicyARN(name, path string) string {
	return "arn:" + core.Partition + ":iam::aws:policy" + path + name
}

// policyName extracts a policy's stored name from a name or ARN, mapping
// HomeCloud's legacy names of managed policies to their AWS names.
func (s *Service) policyName(ref string) string {
	ref = core.CanonicalARN(ref)
	name := ref
	if strings.HasPrefix(ref, "arn:") {
		parts := strings.SplitN(ref, ":", 6)
		if len(parts) != 6 || parts[2] != "iam" || !strings.HasPrefix(parts[5], "policy/") {
			return ""
		}
		name = parts[5][strings.LastIndexByte(parts[5], '/')+1:]
	}
	if n, ok := legacyPolicyNames[name]; ok {
		return n
	}
	return name
}

// resolvePolicy finds a managed policy by name or ARN.
func (s *Service) resolvePolicy(ref string) (Policy, error) {
	missing := noSuchEntity("Policy %s does not exist or is not attachable.", ref)
	name := s.policyName(ref)
	if name == "" {
		return Policy{}, invalidInput("ARN %s is not valid.", ref)
	}
	p, err := store.Get[Policy](s.env.Store, cPolicies, name)
	if err != nil {
		return p, missing
	}
	if arn := core.CanonicalARN(ref); strings.HasPrefix(arn, "arn:") {
		acct := strings.SplitN(arn, ":", 6)[4]
		switch {
		case p.Managed && (acct == "aws" || acct == s.env.AccountID): // account form: HomeCloud's legacy ARN
		case !p.Managed && acct == s.env.AccountID && (arn == p.ARN || strings.HasSuffix(arn, ":policy/"+name)):
		default:
			return p, missing
		}
	}
	return p, nil
}

// installBuiltins (re)writes the AWS managed policies. A customer policy that
// already has a built-in's name keeps it.
func (s *Service) installBuiltins() error {
	for _, bp := range builtinPolicies {
		now := core.Now()
		p := Policy{Name: bp.Name, ID: builtinID(bp.Name), ARN: awsPolicyARN(bp.Name, bp.Path), Path: bp.Path, Description: bp.Description,
			Managed: true, Document: bp.Doc, CreatedAt: now, UpdatedAt: now, DefaultVersion: "v1", NextVersion: 2}
		if old, err := store.Get[Policy](s.env.Store, cPolicies, bp.Name); err == nil {
			if !old.Managed {
				log.Printf("iam: customer policy %q shadows the AWS managed policy of the same name", bp.Name)
				continue
			}
			p.CreatedAt, p.UpdatedAt = old.CreatedAt, old.UpdatedAt
			if docJSON(old.Document) != docJSON(bp.Doc) {
				p.UpdatedAt = now
			}
		}
		p.Versions = []PolicyVersion{{ID: "v1", Document: bp.Doc, CreatedAt: p.UpdatedAt}}
		if err := store.Put(s.env.Store, cPolicies, bp.Name, p); err != nil {
			return err
		}
	}
	return nil
}

func docJSON(d PolicyDocument) string {
	b, _ := json.Marshal(d)
	return string(b)
}

// builtinID is a stable policy ID for an AWS managed policy.
func builtinID(name string) string {
	return "ANPA" + strings.ToUpper(hashSecret("aws-managed:" + name)[:17])
}

// migrate upgrades data stored by earlier HomeCloud versions: managed policies
// renamed to their AWS names, and IDs, paths and versions added to entities.
func (s *Service) migrate() error {
	st := s.env.Store
	for old := range legacyPolicyNames {
		if p, err := store.Get[Policy](st, cPolicies, old); err == nil && p.Managed {
			_ = store.Delete(st, cPolicies, old)
		}
	}
	rename := func(list []string) ([]string, bool) {
		changed := false
		out := make([]string, 0, len(list))
		for _, n := range list {
			if nn, ok := legacyPolicyNames[n]; ok {
				n, changed = nn, true
			}
			out = addUnique(out, n)
		}
		return out, changed
	}
	for _, u := range store.List[User](st, cUsers) {
		if l, ok := rename(u.AttachedPolicies); ok {
			if _, err := store.Update(st, cUsers, u.Name, func(u *User) error { u.AttachedPolicies = l; return nil }); err != nil {
				return err
			}
		}
	}
	for _, g := range store.List[Group](st, cGroups) {
		l, ok := rename(g.AttachedPolicies)
		if ok || g.ID == "" {
			if _, err := store.Update(st, cGroups, g.Name, func(g *Group) error {
				g.AttachedPolicies = l
				if g.ID == "" {
					g.ID, g.Path = newID("HCGA"), "/"
				}
				return nil
			}); err != nil {
				return err
			}
		}
	}
	for _, r := range store.List[Role](st, cRoles) {
		if l, ok := rename(r.AttachedPolicies); ok {
			if _, err := store.Update(st, cRoles, r.Name, func(r *Role) error { r.AttachedPolicies = l; return nil }); err != nil {
				return err
			}
		}
	}
	for _, p := range store.List[Policy](st, cPolicies) {
		if p.Managed || len(p.Versions) > 0 {
			continue
		}
		if _, err := store.Update(st, cPolicies, p.Name, func(p *Policy) error {
			if p.ID == "" {
				p.ID = newID("HCPA")
			}
			if p.Path == "" {
				p.Path = "/"
			}
			p.ARN = s.policyARN(p.Name, p.Path)
			p.Versions = []PolicyVersion{{ID: "v1", Document: p.Document, CreatedAt: p.UpdatedAt}}
			p.DefaultVersion, p.NextVersion = "v1", 2
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

// PolicyInput creates a customer managed policy.
type PolicyInput struct {
	Name        string         `json:"name"`
	Path        string         `json:"path"`
	Description string         `json:"description"`
	Document    PolicyDocument `json:"document"`
	Tags        core.Tags      `json:"tags"`
}

const maxManagedPolicySize = 6144

func checkManaged(d PolicyDocument) error {
	if err := d.Validate(); err != nil {
		return err
	}
	n := 0
	for _, r := range docJSON(d) {
		if r != ' ' && r != '\n' && r != '\t' && r != '\r' {
			n++
		}
	}
	if n > maxManagedPolicySize {
		return limitExceeded("Cannot exceed quota for PolicySize: %d", maxManagedPolicySize)
	}
	return nil
}

// CreatePolicy creates a customer managed policy.
func (s *Service) CreatePolicy(in PolicyInput) (Policy, error) {
	if err := validName("policy", in.Name); err != nil {
		return Policy{}, err
	}
	path, err := validPath(in.Path)
	if err != nil {
		return Policy{}, err
	}
	if len(in.Description) > 1000 {
		return Policy{}, core.Errf(http.StatusBadRequest, "ValidationError", "description must be at most 1000 characters")
	}
	if err := checkManaged(in.Document); err != nil {
		return Policy{}, err
	}
	if err := checkTags(in.Tags); err != nil {
		return Policy{}, err
	}
	now := core.Now()
	p := Policy{Name: in.Name, ID: newID("HCPA"), ARN: s.policyARN(in.Name, path), Path: path, Description: in.Description,
		Document: in.Document, CreatedAt: now, UpdatedAt: now, Tags: in.Tags,
		Versions:       []PolicyVersion{{ID: "v1", Document: in.Document, CreatedAt: now}},
		DefaultVersion: "v1", NextVersion: 2}
	s.mu.Lock()
	defer s.mu.Unlock()
	legacy := false
	for old := range legacyPolicyNames {
		legacy = legacy || strings.EqualFold(old, in.Name)
	}
	if legacy || nameTaken(s, cPolicies, in.Name, "", func(p Policy) string { return p.Name }) {
		return Policy{}, alreadyExists("A policy called %s already exists. Duplicate names are not allowed.", in.Name)
	}
	return p, store.Put(s.env.Store, cPolicies, p.Name, p)
}

// UpdatePolicy applies fn to a customer managed policy.
func (s *Service) UpdatePolicy(ref string, fn func(*Policy) error) (Policy, error) {
	cur, err := s.resolvePolicy(ref)
	if err != nil {
		return cur, err
	}
	if cur.Managed {
		return cur, unmodifiable("AWS managed policy %s cannot be modified.", cur.ARN)
	}
	p, err := store.Update(s.env.Store, cPolicies, cur.Name, fn)
	if err == store.ErrNotFound {
		return p, noSuchEntity("Policy %s does not exist.", ref)
	}
	return p, err
}

// CreatePolicyVersion adds a version to a customer policy. With prune the
// oldest non-default version makes room when the policy has five (the
// console's edit); otherwise that is LimitExceeded, as in AWS.
func (s *Service) CreatePolicyVersion(ref string, d PolicyDocument, setDefault, prune bool) (Policy, PolicyVersion, error) {
	if err := checkManaged(d); err != nil {
		return Policy{}, PolicyVersion{}, err
	}
	var v PolicyVersion
	p, err := s.UpdatePolicy(ref, func(p *Policy) error {
		if len(p.Versions) >= maxPolicyVersions {
			if !prune {
				return limitExceeded("A managed policy can have up to %d versions. Before you create a new version, you must delete an existing version.", maxPolicyVersions)
			}
			for i, old := range p.Versions {
				if old.ID != p.DefaultVersion {
					p.Versions = slices.Delete(p.Versions, i, i+1)
					break
				}
			}
		}
		if p.NextVersion < 2 {
			p.NextVersion = len(p.Versions) + 1
		}
		v = PolicyVersion{ID: "v" + strconv.Itoa(p.NextVersion), Document: d, CreatedAt: core.Now()}
		p.NextVersion++
		p.Versions = append(p.Versions, v)
		if setDefault {
			p.DefaultVersion, p.Document, p.UpdatedAt = v.ID, d, v.CreatedAt
		}
		return nil
	})
	return p, v, err
}

// SetDefaultPolicyVersion makes a version the one in effect.
func (s *Service) SetDefaultPolicyVersion(ref, id string) (Policy, error) {
	return s.UpdatePolicy(ref, func(p *Policy) error {
		v, ok := p.version(id)
		if !ok {
			return noSuchEntity("Policy %s version %s does not exist.", p.ARN, id)
		}
		p.DefaultVersion, p.Document, p.UpdatedAt = v.ID, v.Document, core.Now()
		return nil
	})
}

// DeletePolicyVersion deletes a non-default version.
func (s *Service) DeletePolicyVersion(ref, id string) (Policy, error) {
	return s.UpdatePolicy(ref, func(p *Policy) error {
		if _, ok := p.version(id); !ok {
			return noSuchEntity("Policy %s version %s does not exist.", p.ARN, id)
		}
		if id == p.DefaultVersion {
			return deleteConflict("Cannot delete the default version of a policy.")
		}
		p.Versions = slices.DeleteFunc(p.Versions, func(v PolicyVersion) bool { return v.ID == id })
		return nil
	})
}

// Attachments lists who uses a managed policy (by stored name).
type Attachments struct {
	Users, Groups, Roles []string
	Boundaries           int // users and roles using it as a permissions boundary
}

func (a Attachments) count() int { return len(a.Users) + len(a.Groups) + len(a.Roles) }

func (s *Service) attachments(p Policy) Attachments {
	a := Attachments{Users: []string{}, Groups: []string{}, Roles: []string{}}
	for _, u := range store.List[User](s.env.Store, cUsers) {
		if slices.Contains(u.AttachedPolicies, p.Name) {
			a.Users = append(a.Users, u.Name)
		}
		if u.PermissionsBoundary != "" && s.policyName(u.PermissionsBoundary) == p.Name {
			a.Boundaries++
		}
	}
	for _, g := range store.List[Group](s.env.Store, cGroups) {
		if slices.Contains(g.AttachedPolicies, p.Name) {
			a.Groups = append(a.Groups, g.Name)
		}
	}
	for _, r := range store.List[Role](s.env.Store, cRoles) {
		if slices.Contains(r.AttachedPolicies, p.Name) {
			a.Roles = append(a.Roles, r.Name)
		}
		if r.PermissionsBoundary != "" && s.policyName(r.PermissionsBoundary) == p.Name {
			a.Boundaries++
		}
	}
	return a
}

// DeletePolicy deletes a customer policy. It must not be attached or used as a
// boundary; without force (the AWS API) its non-default versions must be
// deleted first, as in AWS.
func (s *Service) DeletePolicy(ref string, force bool) error {
	p, err := s.resolvePolicy(ref)
	if err != nil {
		return err
	}
	if p.Managed {
		return unmodifiable("AWS managed policy %s cannot be deleted.", p.ARN)
	}
	a := s.attachments(p)
	if a.count() > 0 {
		return deleteConflict("Cannot delete a policy attached to entities (%d user(s), %d group(s), %d role(s)).", len(a.Users), len(a.Groups), len(a.Roles))
	}
	if a.Boundaries > 0 {
		return deleteConflict("Cannot delete a policy used as a permissions boundary.")
	}
	if !force && len(p.Versions) > 1 {
		return deleteConflict("This policy has more than one version. Before you delete a policy, you must delete the policy's versions. The default version is deleted with the policy.")
	}
	return store.Delete(s.env.Store, cPolicies, p.Name)
}
