package iam

import (
	"slices"
	"strings"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

const cProfiles = "iam_instance_profiles"

// InstanceProfile carries a role to an instance: an instance launched with the
// profile gets temporary credentials for the role. A profile holds at most one role.
type InstanceProfile struct {
	Name      string    `json:"name"`
	ID        string    `json:"id"`
	ARN       string    `json:"arn"`
	Path      string    `json:"path"`
	Roles     []string  `json:"roles"`
	CreatedAt time.Time `json:"created_at"`
	Tags      core.Tags `json:"tags,omitempty"`
}

func (s *Service) profileARN(name, path string) string {
	return s.env.ARN("iam", "instance-profile"+path+name)
}

// profileName accepts an instance profile name or ARN.
func profileName(ref string) string {
	ref = core.CanonicalARN(ref)
	if _, rest, ok := strings.Cut(ref, ":instance-profile/"); ok {
		return rest[strings.LastIndexByte(rest, '/')+1:]
	}
	return ref
}

// GetInstanceProfile returns an instance profile by name or ARN.
func (s *Service) GetInstanceProfile(ref string) (InstanceProfile, error) {
	p, err := store.Get[InstanceProfile](s.env.Store, cProfiles, profileName(ref))
	if err != nil {
		return p, noSuchEntity("Instance Profile %s cannot be found.", profileName(ref))
	}
	return p, nil
}

// InstanceProfileRole returns the role of an instance profile (for EC2).
func (s *Service) InstanceProfileRole(ref string) (Role, error) {
	p, err := s.GetInstanceProfile(ref)
	if err != nil {
		return Role{}, err
	}
	if len(p.Roles) == 0 {
		return Role{}, noSuchEntity("Instance Profile %s has no role.", p.Name)
	}
	return s.GetRole(p.Roles[0])
}

// CreateInstanceProfile creates an empty instance profile.
func (s *Service) CreateInstanceProfile(name, path string, tags core.Tags) (InstanceProfile, error) {
	if err := validName("instance profile", name); err != nil {
		return InstanceProfile{}, err
	}
	path, err := validPath(path)
	if err != nil {
		return InstanceProfile{}, err
	}
	if err := checkTags(tags); err != nil {
		return InstanceProfile{}, err
	}
	p := InstanceProfile{Name: name, ID: newID("HCIP"), ARN: s.profileARN(name, path), Path: path, Roles: []string{}, CreatedAt: core.Now(), Tags: tags}
	s.mu.Lock()
	defer s.mu.Unlock()
	if nameTaken(s, cProfiles, name, "", func(p InstanceProfile) string { return p.Name }) {
		return InstanceProfile{}, alreadyExists("Instance Profile %s already exists.", name)
	}
	return p, store.Put(s.env.Store, cProfiles, name, p)
}

// DeleteInstanceProfile deletes an instance profile; like AWS, its role must be removed first.
func (s *Service) DeleteInstanceProfile(ref string, force bool) error {
	p, err := s.GetInstanceProfile(ref)
	if err != nil {
		return err
	}
	if len(p.Roles) > 0 && !force {
		return deleteConflict("Cannot delete entity, must remove roles from instance profile first.")
	}
	return store.Delete(s.env.Store, cProfiles, p.Name)
}

// UpdateInstanceProfile applies fn to an instance profile.
func (s *Service) UpdateInstanceProfile(ref string, fn func(*InstanceProfile) error) (InstanceProfile, error) {
	p, err := store.Update(s.env.Store, cProfiles, profileName(ref), fn)
	if err == store.ErrNotFound {
		return p, noSuchEntity("Instance Profile %s cannot be found.", profileName(ref))
	}
	return p, err
}

// AddRoleToInstanceProfile puts a role in a profile (which holds at most one).
func (s *Service) AddRoleToInstanceProfile(profile, role string) (InstanceProfile, error) {
	r, err := s.GetRole(role)
	if err != nil {
		return InstanceProfile{}, err
	}
	return s.UpdateInstanceProfile(profile, func(p *InstanceProfile) error {
		if len(p.Roles) >= 1 {
			return limitExceeded("Cannot exceed quota for InstanceSessionsPerInstanceProfile: 1")
		}
		p.Roles = append(p.Roles, r.Name)
		return nil
	})
}

// RemoveRoleFromInstanceProfile takes a role out of a profile.
func (s *Service) RemoveRoleFromInstanceProfile(profile, role string) (InstanceProfile, error) {
	n := roleName(role)
	return s.UpdateInstanceProfile(profile, func(p *InstanceProfile) error {
		if !slices.Contains(p.Roles, n) {
			return noSuchEntity("Role %s is not in instance profile %s.", n, p.Name)
		}
		p.Roles = remove(p.Roles, n)
		return nil
	})
}

// profilesForRole lists the instance profiles that contain a role.
func (s *Service) profilesForRole(role string) []string {
	var out []string
	for _, p := range store.List[InstanceProfile](s.env.Store, cProfiles) {
		if slices.Contains(p.Roles, role) {
			out = append(out, p.Name)
		}
	}
	return out
}

// ---- native API ----

func (s *Service) profileRoutes(r *httpx.Router) {
	res := httpx.Res("arn:aws:iam::{account}:instance-profile/{name}")
	r.Handle("GET /api/v1/iam/instance-profiles", "iam:ListInstanceProfiles", s.listProfilesRoute)
	r.Handle("POST /api/v1/iam/instance-profiles", "iam:CreateInstanceProfile", s.createProfileRoute)
	r.Handle("GET /api/v1/iam/instance-profiles/{name}", "iam:GetInstanceProfile", s.getProfileRoute, res)
	r.Handle("DELETE /api/v1/iam/instance-profiles/{name}", "iam:DeleteInstanceProfile", s.deleteProfileRoute, res)
	r.Handle("POST /api/v1/iam/instance-profiles/{name}/roles", "iam:AddRoleToInstanceProfile", s.addProfileRoleRoute, res)
	r.Handle("DELETE /api/v1/iam/instance-profiles/{name}/roles/{role}", "iam:RemoveRoleFromInstanceProfile", s.removeProfileRoleRoute, res)
}

func (s *Service) listProfilesRoute(c *httpx.Ctx) (any, error) {
	return store.List[InstanceProfile](s.env.Store, cProfiles), nil
}

func (s *Service) createProfileRoute(c *httpx.Ctx) (any, error) {
	var in struct {
		Name string    `json:"name"`
		Path string    `json:"path"`
		Role string    `json:"role"`
		Tags core.Tags `json:"tags"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	arn := s.profileARN(in.Name, "/")
	if in.Role != "" {
		if err := c.Authorize("iam:AddRoleToInstanceProfile", arn); err != nil {
			return nil, err
		}
		r, err := s.GetRole(in.Role)
		if err != nil {
			return nil, err
		}
		if err := c.Authorize("iam:PassRole", r.ARN); err != nil {
			return nil, err
		}
	}
	p, err := s.CreateInstanceProfile(in.Name, in.Path, in.Tags)
	if err != nil || in.Role == "" {
		return p, err
	}
	return s.AddRoleToInstanceProfile(p.Name, in.Role)
}

func (s *Service) getProfileRoute(c *httpx.Ctx) (any, error) {
	return s.GetInstanceProfile(c.Param("name"))
}

func (s *Service) deleteProfileRoute(c *httpx.Ctx) (any, error) {
	return nil, s.DeleteInstanceProfile(c.Param("name"), c.Query("force") == "true")
}

func (s *Service) addProfileRoleRoute(c *httpx.Ctx) (any, error) {
	var in struct {
		Role string `json:"role"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	r, err := s.GetRole(in.Role)
	if err != nil {
		return nil, err
	}
	// Whoever launches an instance with the profile acts as the role.
	if err := c.Authorize("iam:PassRole", r.ARN); err != nil {
		return nil, err
	}
	return s.AddRoleToInstanceProfile(c.Param("name"), in.Role)
}

func (s *Service) removeProfileRoleRoute(c *httpx.Ctx) (any, error) {
	return s.RemoveRoleFromInstanceProfile(c.Param("name"), c.Param("role"))
}
