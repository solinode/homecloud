// Package ecr implements a private container registry: HomeCloud runs a
// Docker Distribution (registry:2) server and manages repositories, tags and
// image deletion on top of its HTTP API. Push with the normal docker CLI:
//
//	docker tag myapp localhost:5500/myapp:1.0 && docker push localhost:5500/myapp:1.0
package ecr

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/runtime"
	"github.com/homecloudhq/homecloud/cli/internal/store"
	"github.com/homecloudhq/homecloud/cli/internal/svc"
)

const (
	containerName = "homecloud-ecr"
	dataVolume    = "homecloud-ecr-data"
	image         = "registry:2"
	cRepos        = "ecr_repositories"
	manifestTypes = "application/vnd.docker.distribution.manifest.v2+json, application/vnd.oci.image.manifest.v1+json, application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json"
)

type Repository struct {
	Name        string    `json:"name"`
	ARN         string    `json:"arn"`
	URI         string    `json:"uri"`
	TagMutable  bool      `json:"tag_mutable"`
	CreatedAt   time.Time `json:"created_at"`
	Description string    `json:"description,omitempty"`
	Tags        core.Tags `json:"tags,omitempty"`
}

type Service struct {
	env    *svc.Env
	http   *http.Client
	mu     sync.RWMutex
	status string
}

func New(env *svc.Env) *Service {
	return &Service{env: env, http: &http.Client{Timeout: 30 * time.Second}, status: "starting"}
}

func (s *Service) base() string { return fmt.Sprintf("http://127.0.0.1:%d/v2/", s.env.Cfg.ECRPort) }

// Host is the registry address images are tagged with.
func (s *Service) Host() string { return fmt.Sprintf("localhost:%d", s.env.Cfg.ECRPort) }

// Start ensures the registry container runs. The registry only listens on the
// host's loopback interface, so pushes come from this machine's Docker.
func (s *Service) Start(ctx context.Context) error {
	d := s.env.Docker
	switch d.State(containerName) {
	case "running":
	case "missing":
		if err := d.CreateVolume(dataVolume, runtime.Labels("ecr", "data", nil)); err != nil {
			return err
		}
		if _, err := d.Run(ctx, runtime.RunSpec{
			Name: containerName, Image: image, Labels: runtime.Labels("ecr", "server", nil),
			Env:     map[string]string{"REGISTRY_STORAGE_DELETE_ENABLED": "true"},
			Ports:   []runtime.Port{{ContainerPort: 5000, HostPort: s.env.Cfg.ECRPort, HostIP: "127.0.0.1"}},
			Mounts:  []runtime.Mount{{Volume: dataVolume, Target: "/var/lib/registry"}},
			Restart: "unless-stopped", Start: true,
		}); err != nil {
			return fmt.Errorf("start registry: %w", err)
		}
	default:
		if err := d.Start(containerName); err != nil {
			return err
		}
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		resp, err := s.http.Get(s.base())
		if err == nil {
			resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("registry did not become ready: %w", err)
		}
		time.Sleep(time.Second)
	}
	s.mu.Lock()
	s.status = "available"
	s.mu.Unlock()
	return nil
}

func (s *Service) ready() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.status != "available" {
		return core.Errf(http.StatusServiceUnavailable, "ServiceUnavailable", "the registry is %s", s.status)
	}
	return nil
}

func (s *Service) get(path string, accept string, out any) (*http.Response, error) {
	req, _ := http.NewRequest(http.MethodGet, s.base()+path, nil)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return resp, nil
	}
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return resp, fmt.Errorf("registry %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	if out != nil {
		return resp, json.NewDecoder(resp.Body).Decode(out)
	}
	return resp, nil
}

// sync records repositories that were pushed without being created first.
func (s *Service) sync() {
	var names []string
	last := ""
	for {
		var cat struct {
			Repositories []string `json:"repositories"`
		}
		if _, err := s.get("_catalog?n=1000&last="+last, "", &cat); err != nil {
			log.Printf("ecr: list catalog: %v", err)
			return
		}
		names = append(names, cat.Repositories...)
		if len(cat.Repositories) < 1000 {
			break
		}
		last = cat.Repositories[len(cat.Repositories)-1]
	}
	for _, name := range names {
		if !store.Has(s.env.Store, cRepos, name) {
			_ = store.Put(s.env.Store, cRepos, name, s.newRepo(name, true, ""))
		}
	}
}

func (s *Service) newRepo(name string, mutable bool, desc string) Repository {
	return Repository{Name: name, ARN: s.env.ARN("ecr", "repository/"+name), URI: s.Host() + "/" + name, TagMutable: mutable, CreatedAt: core.Now(), Description: desc}
}

func (s *Service) Routes(r *httpx.Router) {
	res := httpx.Res("arn:hc:ecr:local-1:{account}:repository/{name}")
	r.Handle("GET /api/v1/ecr/status", "ecr:DescribeRegistry", s.registryStatus)
	r.Handle("GET /api/v1/ecr/repositories", "ecr:DescribeRepositories", s.list)
	r.Handle("POST /api/v1/ecr/repositories", "ecr:CreateRepository", s.create)
	r.Handle("GET /api/v1/ecr/repositories/{name...}", "ecr:DescribeImages", s.describe, res)
	r.Handle("DELETE /api/v1/ecr/repositories/{name...}", "ecr:DeleteRepository", s.delete, res)
	r.Handle("DELETE /api/v1/ecr/images", "ecr:BatchDeleteImage", s.deleteImage)
}

func (s *Service) registryStatus(c *httpx.Ctx) (any, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return map[string]any{"status": s.status, "registry": s.Host(),
		"push_example": fmt.Sprintf("docker tag myapp:latest %s/myapp:latest && docker push %s/myapp:latest", s.Host(), s.Host())}, nil
}

func (s *Service) list(c *httpx.Ctx) (any, error) {
	if s.ready() != nil {
		return store.List[Repository](s.env.Store, cRepos), nil
	}
	s.sync()
	out := []map[string]any{}
	for _, r := range store.List[Repository](s.env.Store, cRepos) {
		var tl struct {
			Tags []string `json:"tags"`
		}
		_, _ = s.get(r.Name+"/tags/list", "", &tl)
		out = append(out, map[string]any{"name": r.Name, "arn": r.ARN, "uri": r.URI, "tag_mutable": r.TagMutable,
			"created_at": r.CreatedAt, "description": r.Description, "tags": r.Tags, "image_tag_count": len(tl.Tags)})
	}
	return out, nil
}

var imageRefRe = regexp.MustCompile(`^([A-Za-z0-9_][A-Za-z0-9_.-]{0,127}|sha256:[a-f0-9]{64})$`)

var nameRe = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*(?:/[a-z0-9]+(?:[._-][a-z0-9]+)*)*$`)

func (s *Service) create(c *httpx.Ctx) (any, error) {
	var in struct {
		Name        string    `json:"name"`
		TagMutable  *bool     `json:"tag_mutable"`
		Description string    `json:"description"`
		Tags        core.Tags `json:"tags"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if len(in.Name) < 2 || len(in.Name) > 256 || !nameRe.MatchString(in.Name) {
		return nil, core.BadRequest("repository names are lowercase letters, digits and . _ - separators, optionally namespaced with /")
	}
	if store.Has(s.env.Store, cRepos, in.Name) {
		return nil, core.Errf(http.StatusConflict, "RepositoryAlreadyExistsException", "repository %q already exists", in.Name)
	}
	r := s.newRepo(in.Name, in.TagMutable == nil || *in.TagMutable, in.Description)
	r.Tags = in.Tags
	return r, store.Put(s.env.Store, cRepos, r.Name, r)
}

type manifest struct {
	MediaType string `json:"mediaType"`
	Config    struct {
		Size   int64  `json:"size"`
		Digest string `json:"digest"`
	} `json:"config"`
	Layers []struct {
		Size int64 `json:"size"`
	} `json:"layers"`
	Manifests []struct {
		Digest   string `json:"digest"`
		Platform struct {
			OS           string `json:"os"`
			Architecture string `json:"architecture"`
		} `json:"platform"`
	} `json:"manifests"`
}

type imageInfo struct {
	Platform  string    `json:"platform,omitempty"`
	Tags      []string  `json:"tags"`
	Digest    string    `json:"digest"`
	SizeBytes int64     `json:"size_bytes"`
	MediaType string    `json:"media_type"`
	PushedAt  time.Time `json:"pushed_at,omitempty"`
	URI       string    `json:"uri"`
}

func (s *Service) images(name string) ([]imageInfo, error) {
	var tl struct {
		Tags []string `json:"tags"`
	}
	resp, err := s.get(name+"/tags/list", "", &tl)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		return []imageInfo{}, nil
	}
	byDigest := map[string]*imageInfo{}
	for _, t := range tl.Tags {
		var m manifest
		resp, err := s.get(name+"/manifests/"+t, manifestTypes, &m)
		if err != nil || resp.StatusCode != http.StatusOK {
			continue
		}
		digest := resp.Header.Get("Docker-Content-Digest")
		img := byDigest[digest]
		if img == nil {
			img = &imageInfo{Digest: digest, MediaType: m.MediaType, URI: s.Host() + "/" + name + "@" + digest}
			// An image index (multi-platform or with attestations) points at per-platform
			// manifests; describe the first real platform.
			for _, sub := range m.Manifests {
				if sub.Platform.Architecture == "unknown" {
					continue
				}
				var pm manifest
				if r, err := s.get(name+"/manifests/"+sub.Digest, manifestTypes, &pm); err == nil && r.StatusCode == http.StatusOK {
					m.Config, m.Layers = pm.Config, pm.Layers
					img.Platform = sub.Platform.OS + "/" + sub.Platform.Architecture
				}
				break
			}
			img.SizeBytes = m.Config.Size
			for _, l := range m.Layers {
				img.SizeBytes += l.Size
			}
			if m.Config.Digest != "" {
				var cfg struct {
					Created      time.Time `json:"created"`
					OS           string    `json:"os"`
					Architecture string    `json:"architecture"`
				}
				if _, err := s.get(name+"/blobs/"+m.Config.Digest, "", &cfg); err == nil {
					img.PushedAt = cfg.Created
					if img.Platform == "" && cfg.OS != "" {
						img.Platform = cfg.OS + "/" + cfg.Architecture
					}
				}
			}
			byDigest[digest] = img
		}
		img.Tags = append(img.Tags, t)
	}
	out := make([]imageInfo, 0, len(byDigest))
	for _, i := range byDigest {
		sort.Strings(i.Tags)
		out = append(out, *i)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PushedAt.After(out[j].PushedAt) })
	return out, nil
}

func (s *Service) describe(c *httpx.Ctx) (any, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	name := c.Param("name")
	r, err := store.Get[Repository](s.env.Store, cRepos, name)
	if err != nil {
		return nil, core.Errf(http.StatusNotFound, "RepositoryNotFoundException", "repository %q does not exist", name)
	}
	imgs, err := s.images(name)
	if err != nil {
		return nil, err
	}
	return map[string]any{"repository": r, "images": imgs,
		"push_commands": []string{
			fmt.Sprintf("docker build -t %s .", r.Name),
			fmt.Sprintf("docker tag %s:latest %s:latest", r.Name, r.URI),
			fmt.Sprintf("docker push %s:latest", r.URI),
		}}, nil
}

func (s *Service) deleteDigest(name, digest string) error {
	req, _ := http.NewRequest(http.MethodDelete, s.base()+name+"/manifests/"+digest, nil)
	resp, err := s.http.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("registry refused delete: %s", resp.Status)
	}
	return nil
}

func (s *Service) delete(c *httpx.Ctx) (any, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	name := c.Param("name")
	if !store.Has(s.env.Store, cRepos, name) {
		return nil, core.Errf(http.StatusNotFound, "RepositoryNotFoundException", "repository %q does not exist", name)
	}
	imgs, err := s.images(name)
	if err != nil {
		return nil, err
	}
	if len(imgs) > 0 && c.Query("force") != "true" {
		return nil, core.Errf(http.StatusConflict, "RepositoryNotEmptyException", "repository %q has %d images; pass force=true", name, len(imgs))
	}
	for _, i := range imgs {
		if err := s.deleteDigest(name, i.Digest); err != nil {
			return nil, err
		}
	}
	return nil, store.Delete(s.env.Store, cRepos, name)
}

func (s *Service) deleteImage(c *httpx.Ctx) (any, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	repo, ref := c.Query("repository"), c.Query("image")
	if !imageRefRe.MatchString(ref) {
		return nil, core.BadRequest("image must be a tag or a sha256: digest")
	}
	r, err := store.Get[Repository](s.env.Store, cRepos, repo)
	if err != nil {
		return nil, core.Errf(http.StatusNotFound, "RepositoryNotFoundException", "repository %q does not exist", repo)
	}
	if err := c.Authorize("ecr:BatchDeleteImage", r.ARN); err != nil {
		return nil, err
	}
	digest := ref
	if !strings.HasPrefix(ref, "sha256:") {
		resp, err := s.get(repo+"/manifests/"+ref, manifestTypes, nil)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusNotFound {
			return nil, core.Errf(http.StatusNotFound, "ImageNotFoundException", "image %s:%s not found", repo, ref)
		}
		digest = resp.Header.Get("Docker-Content-Digest")
	}
	if err := s.deleteDigest(repo, digest); err != nil {
		return nil, err
	}
	log.Printf("ecr: deleted %s@%s", repo, digest)
	return map[string]string{"deleted": digest}, nil
}
