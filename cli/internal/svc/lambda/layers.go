package lambda

import (
	"net/http"
	"os"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

const (
	cLayers   = "lambda_layers"    // "<layer>:<version>" -> LayerVersion
	cLayerSeq = "lambda_layer_seq" // "<layer>" -> last version number (never reused)
)

var layerNameRe = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,140}$`)

// LayerVersion is an immutable archive extracted into /opt of functions that use it.
type LayerVersion struct {
	Name                    string    `json:"name"`
	Version                 int       `json:"version"`
	ARN                     string    `json:"arn"`       // layer version ARN
	LayerARN                string    `json:"layer_arn"` // without the version
	Description             string    `json:"description"`
	CompatibleRuntimes      []string  `json:"compatible_runtimes,omitempty"`
	CompatibleArchitectures []string  `json:"compatible_architectures,omitempty"`
	LicenseInfo             string    `json:"license_info,omitempty"`
	CodeSHA256              string    `json:"code_sha256"`
	CodeSize                int64     `json:"code_size"`
	CreatedAt               time.Time `json:"created_at"`
	// Deleted versions stay available to functions that already use them.
	Deleted bool `json:"deleted,omitempty"`
}

type layerSeq struct {
	Last int `json:"last"`
}

func (s *Service) layerFile(lv LayerVersion) string {
	return s.env.Cfg.Path("lambda", "layers", lv.Name, strconv.Itoa(lv.Version)+".zip")
}

func (s *Service) layerARN(name string) string { return s.env.ARN("lambda", "layer:"+name) }

// parseLayerRef splits "arn:aws:lambda:<region>:<account>:layer:<name>:<version>" or "<name>:<version>".
func parseLayerRef(ref string) (string, int, bool) {
	ref = core.CanonicalARN(ref)
	if i := strings.Index(ref, ":layer:"); i >= 0 {
		ref = ref[i+len(":layer:"):]
	}
	name, v, ok := strings.Cut(ref, ":")
	n, err := strconv.Atoi(v)
	return name, n, ok && err == nil && n > 0
}

func layerNotFound(ref string) error {
	return core.Errf(http.StatusNotFound, "ResourceNotFound", "Layer version %s does not exist.", ref)
}

// layerByARN returns a layer version, including deleted ones still in use.
func (s *Service) layerByARN(ref string) (LayerVersion, error) {
	name, v, ok := parseLayerRef(ref)
	if !ok {
		return LayerVersion{}, core.BadRequest("Layer version ARN %q is invalid", ref)
	}
	lv, err := store.Get[LayerVersion](s.env.Store, cLayers, name+":"+strconv.Itoa(v))
	if err != nil {
		return lv, layerNotFound(ref)
	}
	return lv, nil
}

func (s *Service) getLayerVersion(name string, v int) (LayerVersion, error) {
	lv, err := store.Get[LayerVersion](s.env.Store, cLayers, name+":"+strconv.Itoa(v))
	if err != nil || lv.Deleted {
		return lv, layerNotFound(s.layerARN(name) + ":" + strconv.Itoa(v))
	}
	return lv, nil
}

type layerInput struct {
	Description             string   `json:"description"`
	CompatibleRuntimes      []string `json:"compatible_runtimes"`
	CompatibleArchitectures []string `json:"compatible_architectures"`
	LicenseInfo             string   `json:"license_info"`
}

func (s *Service) publishLayer(name string, zipped []byte, in layerInput) (LayerVersion, error) {
	if !layerNameRe.MatchString(name) {
		return LayerVersion{}, core.BadRequest("layer name must be 1-140 letters, digits, hyphens or underscores")
	}
	if len(zipped) == 0 {
		return LayerVersion{}, core.BadRequest("layer content is required")
	}
	if len(zipped) > maxCodeBytes {
		return LayerVersion{}, core.Errf(http.StatusRequestEntityTooLarge, "RequestEntityTooLargeException", "layer zip exceeds %d MB", maxCodeBytes>>20)
	}
	if err := checkZip(zipped); err != nil {
		return LayerVersion{}, err
	}
	for _, r := range in.CompatibleRuntimes {
		if _, ok := findRuntime(r); !ok {
			return LayerVersion{}, core.BadRequest("unsupported runtime %q in CompatibleRuntimes", r)
		}
	}
	for _, a := range in.CompatibleArchitectures {
		if a != "x86_64" && a != "arm64" {
			return LayerVersion{}, core.BadRequest("CompatibleArchitectures must be x86_64 or arm64")
		}
	}
	seq, err := store.Update(s.env.Store, cLayerSeq, name, func(q *layerSeq) error { q.Last++; return nil })
	if err != nil {
		seq = layerSeq{Last: 1}
		if err := store.Put(s.env.Store, cLayerSeq, name, seq); err != nil {
			return LayerVersion{}, err
		}
	}
	lv := LayerVersion{Name: name, Version: seq.Last, LayerARN: s.layerARN(name), Description: in.Description,
		CompatibleRuntimes: in.CompatibleRuntimes, CompatibleArchitectures: in.CompatibleArchitectures, LicenseInfo: in.LicenseInfo,
		CodeSHA256: sha256B64(zipped), CodeSize: int64(len(zipped)), CreatedAt: time.Now().UTC()}
	lv.ARN = lv.LayerARN + ":" + strconv.Itoa(lv.Version)
	if err := writeFile(s.layerFile(lv), zipped); err != nil {
		return lv, err
	}
	return lv, store.Put(s.env.Store, cLayers, name+":"+strconv.Itoa(lv.Version), lv)
}

func (lv LayerVersion) matches(runtime, arch string) bool {
	return (runtime == "" || slices.Contains(lv.CompatibleRuntimes, runtime)) &&
		(arch == "" || slices.Contains(lv.CompatibleArchitectures, arch))
}

// layerVersions lists a layer's versions, newest first.
func (s *Service) layerVersions(name, runtime, arch string) []LayerVersion {
	out := []LayerVersion{}
	for _, lv := range store.List[LayerVersion](s.env.Store, cLayers) {
		if lv.Name == name && !lv.Deleted && lv.matches(runtime, arch) {
			out = append(out, lv)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version > out[j].Version })
	return out
}

// layers lists the latest matching version of every layer.
func (s *Service) layers(runtime, arch string) []LayerVersion {
	latestByName := map[string]LayerVersion{}
	for _, lv := range store.List[LayerVersion](s.env.Store, cLayers) {
		if lv.Deleted || !lv.matches(runtime, arch) {
			continue
		}
		if cur, ok := latestByName[lv.Name]; !ok || lv.Version > cur.Version {
			latestByName[lv.Name] = lv
		}
	}
	out := make([]LayerVersion, 0, len(latestByName))
	for _, lv := range latestByName {
		out = append(out, lv)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// layerInUse reports whether any function version references a layer version.
func (s *Service) layerInUse(arn string) bool {
	for _, coll := range []string{cFunctions, cVersions} {
		for _, f := range store.List[Function](s.env.Store, coll) {
			if slices.Contains(f.Layers, arn) {
				return true
			}
		}
	}
	return false
}

// deleteLayerVersion deletes a layer version. Functions that use it keep
// working, as in AWS; it just can't be added to new configurations.
func (s *Service) deleteLayerVersion(name string, v int) error {
	lv, err := store.Get[LayerVersion](s.env.Store, cLayers, name+":"+strconv.Itoa(v))
	if err != nil || lv.Deleted {
		return nil // idempotent, as in AWS
	}
	if s.layerInUse(lv.ARN) {
		lv.Deleted = true
		return store.Put(s.env.Store, cLayers, name+":"+strconv.Itoa(v), lv)
	}
	_ = os.Remove(s.layerFile(lv))
	return store.Delete(s.env.Store, cLayers, name+":"+strconv.Itoa(v))
}
