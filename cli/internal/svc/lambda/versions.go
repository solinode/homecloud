package lambda

import (
	"errors"
	"math/rand/v2"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

// Alias is a named pointer to a function version, optionally splitting
// traffic with a second version.
type Alias struct {
	Function        string             `json:"function"`
	Name            string             `json:"name"`
	ARN             string             `json:"arn"`
	FunctionVersion string             `json:"function_version"`
	Description     string             `json:"description"`
	Weights         map[string]float64 `json:"additional_version_weights,omitempty"`
	RevisionID      string             `json:"revision_id"`
}

func isVersionNumber(q string) bool {
	n, err := strconv.Atoi(q)
	return err == nil && n > 0 && strconv.Itoa(n) == q
}

// version returns a published version of a function.
func (s *Service) getVersion(name, v string) (Function, error) {
	f, err := store.Get[Function](s.env.Store, cVersions, name+":"+v)
	if err != nil {
		return f, fnNotFound(s.fnARN(name) + ":" + v)
	}
	return f, nil
}

func (s *Service) getAlias(name, alias string) (Alias, error) {
	a, err := store.Get[Alias](s.env.Store, cAliases, name+":"+alias)
	if err != nil {
		return a, core.Errf(http.StatusNotFound, "ResourceNotFound", "Alias not found: %s:%s", s.fnARN(name), alias)
	}
	return a, nil
}

// resolve returns the configuration a qualifier ("", $LATEST, a version or an
// alias) points at, and the alias name when one was used. Weighted aliases
// pick a version at random.
func (s *Service) resolve(name, qual string) (Function, string, error) {
	switch {
	case qual == "" || qual == latest:
		f, err := s.getLatest(name)
		return f, "", err
	case isVersionNumber(qual):
		if _, err := s.getLatest(name); err != nil {
			return Function{}, "", err
		}
		f, err := s.getVersion(name, qual)
		return f, "", err
	}
	if _, err := s.getLatest(name); err != nil {
		return Function{}, "", err
	}
	a, err := s.getAlias(name, qual)
	if err != nil {
		return Function{}, "", fnNotFound(s.fnARN(name) + ":" + qual)
	}
	v := pickVersion(a, rand.Float64())
	var f Function
	if v == latest {
		f, err = s.getLatest(name)
	} else {
		f, err = s.getVersion(name, v)
	}
	return f, a.Name, err
}

// pickVersion applies an alias's routing weights; r is uniform in [0,1).
func pickVersion(a Alias, r float64) string {
	acc := 0.0
	keys := make([]string, 0, len(a.Weights))
	for k := range a.Weights {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		acc += a.Weights[k]
		if r < acc {
			return k
		}
	}
	return a.FunctionVersion
}

// publishVersion snapshots $LATEST as a new numbered version. Publishing
// unchanged code and configuration returns the previous version, as in AWS.
func (s *Service) publishVersion(name, desc, codeSHA, revision string) (Function, error) {
	cur, err := s.getLatest(name)
	if err != nil {
		return cur, err
	}
	if codeSHA != "" && codeSHA != cur.CodeSHA256 {
		return cur, core.BadRequest("CodeSHA256 (%s) is different from current CodeSHA256 in $LATEST (%s). Please try again with the CodeSHA256 in $LATEST.", codeSHA, cur.CodeSHA256)
	}
	if revision != "" && revision != cur.RevisionID {
		return cur, core.Errf(http.StatusPreconditionFailed, "PreconditionFailed", "The Revision Id provided does not match the latest Revision Id.")
	}
	if cur.LastUpdateStatus == "InProgress" {
		return cur, core.Conflict("The operation cannot be performed at this time. An update is in progress for resource: %s", cur.ARN)
	}
	if cur.LastVersion > 0 {
		if prev, err := s.getVersion(name, strconv.Itoa(cur.LastVersion)); err == nil && prev.PublishedFrom == cur.RevisionID {
			return prev, nil
		}
	}
	var out Function
	_, err = store.Update(s.env.Store, cFunctions, name, func(f *Function) error {
		if f.RevisionID != cur.RevisionID {
			return core.Conflict("The function was modified while publishing; retry")
		}
		n := f.LastVersion + 1
		v := *f
		v.Version, v.LastVersion, v.Policy, v.ReservedConcurrency, v.URL = strconv.Itoa(n), 0, nil, nil, FunctionURL{}
		v.VersionDescription = desc
		if desc != "" {
			v.Description = desc
		}
		v.RevisionID, v.PublishedFrom = uuid(), f.RevisionID
		v.LastModified = core.Now()
		if !v.isImage() {
			b, err := os.ReadFile(s.codePath(name))
			if err != nil {
				return err
			}
			if err := writeFile(s.codeFile(v), b); err != nil {
				return err
			}
		}
		if err := store.Put(s.env.Store, cVersions, name+":"+v.Version, v); err != nil {
			return err
		}
		f.LastVersion = n
		out = v
		return nil
	})
	return out, err
}

// versions lists a function's $LATEST and published versions, oldest first.
func (s *Service) versions(name string) ([]Function, error) {
	cur, err := s.getLatest(name)
	if err != nil {
		return nil, err
	}
	out := []Function{cur}
	var vs []Function
	for _, v := range store.List[Function](s.env.Store, cVersions) {
		if v.Name == name {
			vs = append(vs, v)
		}
	}
	sort.Slice(vs, func(i, j int) bool {
		a, _ := strconv.Atoi(vs[i].Version)
		b, _ := strconv.Atoi(vs[j].Version)
		return a < b
	})
	return append(out, vs...), nil
}

func (s *Service) aliases(name string) []Alias {
	var out []Alias
	for _, a := range store.List[Alias](s.env.Store, cAliases) {
		if a.Function == name {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// aliasInput creates or updates an alias.
type aliasInput struct {
	Name            string             `json:"name"`
	FunctionVersion string             `json:"function_version"`
	Description     *string            `json:"description"`
	Weights         map[string]float64 `json:"additional_version_weights"`
	Revision        string             `json:"revision_id"`
}

func (s *Service) checkAlias(name string, a *Alias) error {
	if a.FunctionVersion != latest {
		if !isVersionNumber(a.FunctionVersion) {
			return core.BadRequest("FunctionVersion must be $LATEST or a published version number")
		}
		if _, err := s.getVersion(name, a.FunctionVersion); err != nil {
			return err
		}
	}
	if len(a.Weights) > 1 {
		return core.BadRequest("Number of items in AdditionalVersionWeights must be less than or equal to 1")
	}
	for v, w := range a.Weights {
		if w < 0 || w > 1 {
			return core.BadRequest("AdditionalVersionWeights values must be between 0.0 and 1.0")
		}
		if !isVersionNumber(v) || a.FunctionVersion == latest {
			return core.BadRequest("Alias with routing configuration can only point to and route to published versions")
		}
		if v == a.FunctionVersion {
			return core.BadRequest("Alias routing cannot route to its own primary version")
		}
		if _, err := s.getVersion(name, v); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) createAlias(name string, in aliasInput) (Alias, error) {
	if _, err := s.getLatest(name); err != nil {
		return Alias{}, err
	}
	if !aliasRe.MatchString(in.Name) || in.Name == latest || isVersionNumber(in.Name) {
		return Alias{}, core.BadRequest("alias name must start with a letter, - or _ and contain up to 128 letters, digits, - or _")
	}
	a := Alias{Function: name, Name: in.Name, ARN: s.fnARN(name) + ":" + in.Name, FunctionVersion: in.FunctionVersion,
		Weights: in.Weights, RevisionID: uuid()}
	if in.Description != nil {
		a.Description = *in.Description
	}
	if err := s.checkAlias(name, &a); err != nil {
		return a, err
	}
	if store.Has(s.env.Store, cAliases, name+":"+in.Name) {
		return a, core.Conflict("Alias already exists: %s", a.ARN)
	}
	return a, store.Put(s.env.Store, cAliases, name+":"+in.Name, a)
}

func (s *Service) updateAlias(name, alias string, in aliasInput) (Alias, error) {
	cur, err := s.getAlias(name, alias)
	if err != nil {
		return cur, err
	}
	if in.Revision != "" && in.Revision != cur.RevisionID {
		return cur, core.Errf(http.StatusPreconditionFailed, "PreconditionFailed", "The Revision Id provided does not match the latest Revision Id.")
	}
	if in.FunctionVersion != "" {
		cur.FunctionVersion = in.FunctionVersion
	}
	if in.Description != nil {
		cur.Description = *in.Description
	}
	if in.Weights != nil {
		cur.Weights = in.Weights
		if len(in.Weights) == 0 {
			cur.Weights = nil
		}
	}
	if err := s.checkAlias(name, &cur); err != nil {
		return cur, err
	}
	cur.RevisionID = uuid()
	return cur, store.Put(s.env.Store, cAliases, name+":"+alias, cur)
}

func (s *Service) deleteAlias(name, alias string) error {
	if _, err := s.getAlias(name, alias); err != nil {
		return err
	}
	_ = store.Delete(s.env.Store, cInvokeCfgs, name+":"+alias)
	_, _ = s.modify(name, func(f *Function) error {
		delete(f.Policy, alias)
		if f.URL.Qualifier == alias {
			f.URL = FunctionURL{AuthType: "NONE"}
		}
		return nil
	})
	err := store.Delete(s.env.Store, cAliases, name+":"+alias)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	return err
}

// qualifierKey normalises a qualifier for per-qualifier settings.
func qualifierKey(q string) string {
	if q == "" {
		return latest
	}
	return strings.TrimSpace(q)
}
