// Package ssm implements Systems Manager Parameter Store: hierarchical
// configuration parameters (String, StringList, SecureString) with version
// history, labels, tiers and tags. SecureString values are encrypted with KMS.
// /aws/reference/secretsmanager/<secret> parameters read Secrets Manager.
//
// The same operations are served by the native API (native.go) and the AWS
// awsJson1.1 protocol (aws.go).
package ssm

import (
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/store"
	"github.com/homecloudhq/homecloud/cli/internal/svc"
	"github.com/homecloudhq/homecloud/cli/internal/svc/kms"
	"github.com/homecloudhq/homecloud/cli/internal/svc/secrets"
)

const (
	cParams       = "ssm_parameters"
	maxHistory    = 100
	maxStandard   = 4096
	maxAdvanced   = 8192
	maxLabels     = 10
	maxDepth      = 15
	defaultKey    = "alias/aws/ssm"
	secretsPrefix = "/aws/reference/secretsmanager/"

	tierStandard = "Standard"
	tierAdvanced = "Advanced"
	tierIntel    = "Intelligent-Tiering"
)

type version struct {
	Version        int       `json:"version"`
	Value          string    `json:"value"` // ciphertext blob for SecureString
	Type           string    `json:"type,omitempty"`
	KeyID          string    `json:"key_id,omitempty"`  // key ARN
	KeyRef         string    `json:"key_ref,omitempty"` // the KeyId as given (alias/aws/ssm by default)
	LastModified   time.Time `json:"last_modified"`
	ModifiedBy     string    `json:"modified_by"`
	Labels         []string  `json:"labels,omitempty"`
	Description    *string   `json:"description,omitempty"`
	AllowedPattern string    `json:"allowed_pattern,omitempty"`
	Tier           string    `json:"tier,omitempty"`
	DataType       string    `json:"data_type,omitempty"`
	Policies       string    `json:"policies,omitempty"`
}

type Parameter struct {
	Name           string    `json:"name"`
	ARN            string    `json:"arn"`
	Type           string    `json:"type"` // type of the current version
	KeyID          string    `json:"key_id,omitempty"`
	Description    string    `json:"description"`
	DataType       string    `json:"data_type"`
	Tier           string    `json:"tier,omitempty"`
	AllowedPattern string    `json:"allowed_pattern,omitempty"`
	Policies       string    `json:"policies,omitempty"`
	History        []version `json:"history"`
	Tags           core.Tags `json:"tags,omitempty"`
}

func (p Parameter) current() version { return p.History[len(p.History)-1] }

// typeOf and keyOf fall back to the parameter's fields for versions stored before
// they were recorded per version.
func (p Parameter) typeOf(v version) string {
	if v.Type != "" {
		return v.Type
	}
	return p.Type
}

func (p Parameter) keyOf(v version) string {
	if v.Type != "" {
		return v.KeyID
	}
	return p.KeyID
}

// keyRef is the KeyId shown for a SecureString version.
func (p Parameter) keyRef(v version) string {
	if p.typeOf(v) != "SecureString" {
		return ""
	}
	if v.KeyRef != "" {
		return v.KeyRef
	}
	return p.keyOf(v)
}

func (p Parameter) tier() string {
	if p.Tier == "" {
		return tierStandard
	}
	return p.Tier
}

func (p Parameter) dataType() string {
	if p.DataType == "" {
		return "text"
	}
	return p.DataType
}

func (v version) description(p Parameter) string {
	if v.Description != nil {
		return *v.Description
	}
	return p.Description
}

func (v version) tier() string {
	if v.Tier == "" {
		return tierStandard
	}
	return v.Tier
}

// SecretReader reads secrets for /aws/reference/secretsmanager/ parameters
// (implemented by secrets.Service).
type SecretReader interface {
	Reference(az func(action, resource string) error, ref, versionID, stage string) (secrets.Reference, error)
}

type Service struct {
	env *svc.Env
	kms *kms.Service
	mu  sync.Mutex // serialises creation of new parameters

	// Now is the clock (tests replace it); nil means time.Now.
	Now func() time.Time
	// Secrets serves /aws/reference/secretsmanager/ parameters; nil disables them.
	Secrets SecretReader
}

func New(env *svc.Env, k *kms.Service) *Service { return &Service{env: env, kms: k} }

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC().Truncate(time.Second)
	}
	return core.Now()
}

// Authz checks the caller may perform an IAM action on a resource.
type Authz func(action, resource string) error

var nameRe = regexp.MustCompile(`^/?[a-zA-Z0-9_.\-/]+$`)

func arnName(name string) string { return strings.TrimPrefix(name, "/") }

func (s *Service) arn(name string) string { return s.env.ARN("ssm", "parameter/"+arnName(name)) }

func errf(code, format string, a ...any) *core.Error {
	return core.Errf(http.StatusBadRequest, code, format, a...)
}

func notFound(name string) error {
	return core.Errf(http.StatusNotFound, "ParameterNotFound", "Parameter %s not found.", name)
}

func validName(name string) error {
	if name == "" || len(name) > 1011 || !nameRe.MatchString(name) || strings.Contains(name, "//") ||
		(strings.Contains(name, "/") && !strings.HasPrefix(name, "/")) || (len(name) > 1 && strings.HasSuffix(name, "/")) {
		return core.BadRequest("Parameter name must be 1-1011 characters of a-zA-Z0-9_.-/; a hierarchical name must begin with / (%q)", name)
	}
	first := strings.ToLower(strings.TrimPrefix(name, "/"))
	if strings.HasPrefix(first, "aws") || strings.HasPrefix(first, "ssm") {
		return core.BadRequest(`Parameter name: can't be prefixed with "aws" or "ssm" (case-insensitive).`)
	}
	if strings.Count(name, "/") > maxDepth {
		return errf("HierarchyLevelLimitExceededException", "A hierarchy can have a maximum of %d levels.", maxDepth)
	}
	return nil
}

// nameOf converts a parameter name or ARN to a stored name, and splits off a :version or :label selector.
func (s *Service) nameOf(ref string) (name, sel string) {
	if strings.HasPrefix(ref, "arn:") {
		_, rest, _ := strings.Cut(ref, ":parameter/")
		name, sel, _ = strings.Cut(rest, ":")
		if strings.Contains(name, "/") || store.Has(s.env.Store, cParams, "/"+name) && !store.Has(s.env.Store, cParams, name) {
			name = "/" + name
		}
		return name, sel
	}
	name, sel, _ = strings.Cut(ref, ":")
	return name, sel
}

// selectVersion picks a version by number or label.
func selectVersion(p Parameter, sel string) (version, error) {
	if sel == "" {
		return p.current(), nil
	}
	idx := slices.IndexFunc(p.History, func(v version) bool {
		return sel == strconv.Itoa(v.Version) || slices.Contains(v.Labels, sel)
	})
	if idx < 0 {
		return version{}, errf("ParameterVersionNotFound", "Systems Manager could not find version %s of %s.", sel, p.Name)
	}
	return p.History[idx], nil
}

// load reads a parameter (name, ARN, optionally with a selector) and authorizes action on it.
func (s *Service) load(az Authz, action, ref string) (Parameter, version, string, error) {
	name, sel := s.nameOf(ref)
	if err := az(action, s.arn(name)); err != nil {
		return Parameter{}, version{}, sel, err
	}
	p, err := store.Get[Parameter](s.env.Store, cParams, name)
	if err != nil {
		return p, version{}, sel, notFound(name)
	}
	p.ARN = core.CanonicalARN(p.ARN)
	v, err := selectVersion(p, sel)
	return p, v, sel, err
}

// value returns a version's value, decrypting SecureStrings when asked and allowed.
func (s *Service) value(az Authz, p Parameter, v version, decrypt bool) (string, error) {
	if p.typeOf(v) != "SecureString" || !decrypt {
		return v.Value, nil
	}
	if err := az("kms:Decrypt", core.CanonicalARN(p.keyOf(v))); err != nil {
		return "", err
	}
	pt, _, err := s.kms.Decrypt(v.Value, map[string]string{"PARAMETER_ARN": p.ARN})
	if err != nil {
		return "", err
	}
	return string(pt), nil
}

// ---- put ----

// PutInput is a PutParameter request.
type PutInput struct {
	Name, Value, Type, KeyID string
	Description              *string
	Overwrite                bool
	Tier, AllowedPattern     string
	DataType, Policies       string
	Tags                     core.Tags
}

var amiRe = regexp.MustCompile(`^ami-[0-9a-f]{8,17}$`)

func (s *Service) put(az Authz, caller string, in PutInput) (Parameter, error) {
	if err := validName(in.Name); err != nil {
		return Parameter{}, err
	}
	if in.Value == "" {
		return Parameter{}, core.BadRequest("Parameter value must not be empty")
	}
	if in.Type != "" && in.Type != "String" && in.Type != "StringList" && in.Type != "SecureString" {
		return Parameter{}, errf("UnsupportedParameterType", "Type must be String, StringList or SecureString")
	}
	if in.Tier != "" && in.Tier != tierStandard && in.Tier != tierAdvanced && in.Tier != tierIntel {
		return Parameter{}, core.BadRequest("Tier must be Standard, Advanced or Intelligent-Tiering")
	}
	if in.Overwrite && len(in.Tags) > 0 {
		return Parameter{}, core.BadRequest("Invalid request: tags and overwrite can't be used together. To create a parameter with tags, please remove overwrite flag. To update tags for an existing parameter, please use AddTagsToResource or RemoveTagsFromResource.")
	}
	if in.KeyID != "" && in.Type != "SecureString" {
		return Parameter{}, core.BadRequest("KeyId is required for SecureString type parameter only.")
	}
	if len(in.Tags) > 0 {
		if err := validTags(in.Tags); err != nil {
			return Parameter{}, err
		}
	}
	if in.AllowedPattern != "" {
		if len(in.AllowedPattern) > 1024 {
			return Parameter{}, errf("InvalidAllowedPatternException", "AllowedPattern must be at most 1024 characters")
		}
		re, err := regexp.Compile(in.AllowedPattern)
		if err != nil {
			return Parameter{}, errf("InvalidAllowedPatternException", "The request does not meet the regular expression requirement: %v", err)
		}
		if !re.MatchString(in.Value) {
			return Parameter{}, errf("ParameterPatternMismatchException", "Parameter value, cannot be validated against allowedPattern: %s", in.AllowedPattern)
		}
	}
	switch in.DataType {
	case "", "text":
	case "aws:ec2:image":
		if !amiRe.MatchString(in.Value) {
			return Parameter{}, core.BadRequest("Parameter value, %s, is not a valid image ID for data type aws:ec2:image.", in.Value)
		}
	case "aws:ssm:integration":
		if in.Type != "SecureString" {
			return Parameter{}, core.BadRequest("aws:ssm:integration parameters must be SecureString")
		}
	default:
		return Parameter{}, core.BadRequest("The following data type is not supported: %s (Data type names are all lowercase.)", in.DataType)
	}
	arn := s.arn(in.Name)
	if err := az("ssm:PutParameter", arn); err != nil {
		return Parameter{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := store.Get[Parameter](s.env.Store, cParams, in.Name)
	exists := err == nil
	if exists && !in.Overwrite {
		return Parameter{}, errf("ParameterAlreadyExists", "The parameter already exists. To overwrite this value, set the overwrite option in the request to true.")
	}
	if !exists {
		cur = Parameter{Name: in.Name, ARN: arn, Type: "String", DataType: "text", Tags: in.Tags}
	}
	cur.ARN = core.CanonicalARN(cur.ARN)

	typ := in.Type
	if typ == "" {
		typ = cur.Type
		if exists {
			typ = cur.typeOf(cur.current())
		}
	}
	tier, err := pickTier(cur, exists, in.Tier, len(in.Value), in.Policies)
	if err != nil {
		return Parameter{}, err
	}
	v := version{Value: in.Value, Type: typ, LastModified: s.now(), ModifiedBy: caller, Description: in.Description,
		AllowedPattern: in.AllowedPattern, Tier: tier, DataType: in.DataType, Policies: in.Policies}
	if v.DataType == "" {
		v.DataType = cur.dataType()
	}
	if typ == "SecureString" {
		ref := in.KeyID
		if ref == "" && exists && cur.typeOf(cur.current()) == "SecureString" {
			ref = cur.keyRef(cur.current())
		}
		if ref == "" {
			ref = defaultKey
		}
		keyARN, err := s.kms.KeyARN(ref)
		if err != nil {
			return Parameter{}, errf("InvalidKeyId", "The KeyId %s is not valid: %v", ref, err)
		}
		if err := az("kms:Encrypt", keyARN); err != nil {
			return Parameter{}, err
		}
		blob, _, err := s.kms.Encrypt(keyARN, []byte(in.Value), map[string]string{"PARAMETER_ARN": cur.ARN})
		if err != nil {
			return Parameter{}, err
		}
		v.Value, v.KeyID, v.KeyRef = blob, keyARN, ref
	}
	if in.Description != nil && len(*in.Description) > 1024 {
		return Parameter{}, core.BadRequest("Description must be at most 1024 characters")
	}

	apply := func(p *Parameter) error {
		p.History = slices.Clone(p.History)
		v.Version = 1
		if len(p.History) > 0 {
			v.Version = p.current().Version + 1
			if v.Description == nil {
				d := p.current().description(*p)
				v.Description = &d
			}
			if in.AllowedPattern == "" {
				v.AllowedPattern = p.AllowedPattern
				if v.AllowedPattern != "" {
					if re, err := regexp.Compile(v.AllowedPattern); err == nil && !re.MatchString(in.Value) {
						return errf("ParameterPatternMismatchException", "Parameter value, cannot be validated against allowedPattern: %s", v.AllowedPattern)
					}
				}
			}
		}
		if len(p.History) >= maxHistory {
			if len(p.History[0].Labels) > 0 {
				return errf("ParameterMaxVersionLimitExceeded", "You attempted to create a new version of %s by calling the PutParameter API with the overwrite flag. Version %d, the oldest version, can't be deleted because it has a label associated with it. Move the label to another version of the parameter, and try again.", p.Name, p.History[0].Version)
			}
			p.History = p.History[1:]
		}
		p.History = append(p.History, v)
		p.Type, p.KeyID, p.Tier, p.AllowedPattern, p.Policies, p.DataType = v.Type, v.KeyID, v.Tier, v.AllowedPattern, v.Policies, v.DataType
		if v.Description != nil {
			p.Description = *v.Description
		}
		return nil
	}
	if exists {
		return store.Update(s.env.Store, cParams, in.Name, apply)
	}
	if err := apply(&cur); err != nil {
		return cur, err
	}
	return cur, store.Put(s.env.Store, cParams, cur.Name, cur)
}

func pickTier(cur Parameter, exists bool, want string, size int, policies string) (string, error) {
	curTier := tierStandard
	if exists {
		curTier = cur.tier()
	}
	tier := want
	switch want {
	case "":
		tier = curTier
	case tierIntel:
		tier = tierStandard
		if size > maxStandard || policies != "" || curTier == tierAdvanced {
			tier = tierAdvanced
		}
	}
	if tier == tierStandard && curTier == tierAdvanced {
		return "", core.BadRequest("This parameter uses the advanced-parameter tier. You can't downgrade a parameter from the advanced-parameter tier to the standard-parameter tier.")
	}
	if tier == tierStandard && size > maxStandard {
		return "", core.BadRequest("Standard tier parameters support a maximum parameter value of %d characters. To create a larger parameter value, upgrade the parameter to use the advanced-parameter tier.", maxStandard)
	}
	if size > maxAdvanced {
		return "", core.BadRequest("Parameter value must be at most %d characters", maxAdvanced)
	}
	if policies != "" && tier != tierAdvanced {
		return "", core.BadRequest("Parameter policies are supported only for advanced-tier parameters.")
	}
	return tier, nil
}

func validTags(t core.Tags) error {
	if len(t) > 50 {
		return errf("TooManyTagsError", "The Tags parameter can have at most 50 tags.")
	}
	for k, v := range t {
		if k == "" || len(k) > 128 || len(v) > 256 || strings.HasPrefix(strings.ToLower(k), "aws:") {
			return core.BadRequest("tag key %q is invalid (1-128 characters, not starting with aws:)", k)
		}
	}
	return nil
}

// ---- labels ----

var labelRe = regexp.MustCompile(`^[a-zA-Z0-9_.\-]{1,100}$`)

func validLabel(l string) bool {
	low := strings.ToLower(l)
	return labelRe.MatchString(l) && !(l[0] >= '0' && l[0] <= '9') && !strings.HasPrefix(low, "aws") && !strings.HasPrefix(low, "ssm")
}

// label attaches labels to a version (the latest when ver is 0). Invalid labels are returned, not applied.
func (s *Service) label(az Authz, ref string, ver int, labels []string) (invalid []string, labeled int, err error) {
	name, _ := s.nameOf(ref)
	if err := az("ssm:LabelParameterVersion", s.arn(name)); err != nil {
		return nil, 0, err
	}
	if len(labels) == 0 || len(labels) > maxLabels {
		return nil, 0, core.BadRequest("Labels must have 1-%d entries", maxLabels)
	}
	valid := []string{}
	for _, l := range labels {
		if validLabel(l) {
			valid = append(valid, l)
		} else {
			invalid = append(invalid, l)
		}
	}
	_, err = store.Update(s.env.Store, cParams, name, func(p *Parameter) error {
		p.History = slices.Clone(p.History)
		target := ver
		if target == 0 {
			target = p.current().Version
		}
		idx := slices.IndexFunc(p.History, func(v version) bool { return v.Version == target })
		if idx < 0 {
			return errf("ParameterVersionNotFound", "Systems Manager could not find version %d of %s.", target, name)
		}
		for i := range p.History { // a label points at one version at a time
			if i == idx {
				continue
			}
			p.History[i].Labels = slices.DeleteFunc(slices.Clone(p.History[i].Labels), func(x string) bool { return slices.Contains(valid, x) })
		}
		ls := slices.Clone(p.History[idx].Labels)
		for _, l := range valid {
			if !slices.Contains(ls, l) {
				ls = append(ls, l)
			}
		}
		if len(ls) > maxLabels {
			return errf("ParameterVersionLabelLimitExceeded", "A parameter version can have a maximum of %d labels. Move one or more labels to another version and try again.", maxLabels)
		}
		p.History[idx].Labels = ls
		labeled = target
		return nil
	})
	if err == store.ErrNotFound {
		return nil, 0, notFound(name)
	}
	return invalid, labeled, err
}

func (s *Service) unlabel(az Authz, ref string, ver int, labels []string) (removed, invalid []string, err error) {
	name, _ := s.nameOf(ref)
	if err := az("ssm:UnlabelParameterVersion", s.arn(name)); err != nil {
		return nil, nil, err
	}
	if len(labels) == 0 || len(labels) > maxLabels {
		return nil, nil, core.BadRequest("Labels must have 1-%d entries", maxLabels)
	}
	_, err = store.Update(s.env.Store, cParams, name, func(p *Parameter) error {
		removed, invalid = []string{}, []string{}
		p.History = slices.Clone(p.History)
		idx := slices.IndexFunc(p.History, func(v version) bool { return v.Version == ver })
		if idx < 0 {
			return errf("ParameterVersionNotFound", "Systems Manager could not find version %d of %s.", ver, name)
		}
		ls := slices.Clone(p.History[idx].Labels)
		for _, l := range labels {
			if slices.Contains(ls, l) {
				removed = append(removed, l)
				ls = slices.DeleteFunc(ls, func(x string) bool { return x == l })
			} else {
				invalid = append(invalid, l)
			}
		}
		p.History[idx].Labels = ls
		return nil
	})
	if err == store.ErrNotFound {
		return nil, nil, notFound(name)
	}
	return removed, invalid, err
}

// ---- delete, tags ----

func (s *Service) remove(az Authz, ref string) error {
	name, _ := s.nameOf(ref)
	if err := az("ssm:DeleteParameter", s.arn(name)); err != nil {
		return err
	}
	if err := store.Delete(s.env.Store, cParams, name); err != nil {
		return notFound(name)
	}
	return nil
}

func (s *Service) tag(az Authz, action, resourceType, ref string, add core.Tags, remove []string) (Parameter, error) {
	if resourceType != "Parameter" {
		return Parameter{}, errf("InvalidResourceType", "The resource type %q is not supported; HomeCloud supports Parameter.", resourceType)
	}
	name, _ := s.nameOf(ref)
	if err := az(action, s.arn(name)); err != nil {
		return Parameter{}, err
	}
	p, err := store.Update(s.env.Store, cParams, name, func(p *Parameter) error {
		t := core.Tags{}
		for k, v := range p.Tags {
			t[k] = v
		}
		for k, v := range add {
			t[k] = v
		}
		for _, k := range remove {
			delete(t, k)
		}
		if err := validTags(t); err != nil {
			return err
		}
		p.Tags = t
		return nil
	})
	if err == store.ErrNotFound {
		return p, errf("InvalidResourceId", "The resource ID %q is not valid. Verify the ID and try again.", ref)
	}
	return p, err
}

// ---- paths and filters ----

// Filter is a ParameterStringFilter (or a legacy ParametersFilter).
type Filter struct {
	Key    string   `json:"Key"`
	Option string   `json:"Option"`
	Values []string `json:"Values"`
}

func filterErr(code, format string, a ...any) error { return errf(code, format, a...) }

// validFilters checks filter keys and options; allowed lists the permitted keys (tag:* is always allowed).
func validFilters(fs []Filter, allowed []string) error {
	for _, f := range fs {
		key := f.Key
		if strings.HasPrefix(key, "tag:") {
			key = "tag:"
		}
		if !slices.Contains(allowed, key) {
			return filterErr("InvalidFilterKey", "The following filter key is not valid: %s. Valid filter keys include: [%s].", f.Key, strings.Join(allowed, ", "))
		}
		var options []string
		switch key {
		case "Name":
			options = []string{"Equals", "BeginsWith", "Contains"}
		case "Path":
			options = []string{"Recursive", "OneLevel"}
		case "Type", "KeyId", "DataType", "Tier":
			options = []string{"Equals", "BeginsWith"}
		case "Label":
			options = []string{"Equals"}
		case "tag:":
			options = []string{"Equals", "BeginsWith", "Contains"}
		default:
			options = []string{"Equals"}
		}
		if f.Option != "" && !slices.Contains(options, f.Option) {
			return filterErr("InvalidFilterOption", "The filter option %q is not valid for key %s. Valid options include: [%s].", f.Option, f.Key, strings.Join(options, ", "))
		}
		if len(f.Values) == 0 && key != "tag:" && key != "tag-key" {
			return filterErr("InvalidFilterValue", "The filter %s needs at least one value.", f.Key)
		}
		if key == "Path" {
			for _, v := range f.Values {
				if !strings.HasPrefix(v, "/") {
					return filterErr("InvalidFilterValue", "The Path filter value %q must begin with /.", v)
				}
			}
		}
	}
	return nil
}

func matchOpt(opt, want, got string) bool {
	switch opt {
	case "BeginsWith":
		return strings.HasPrefix(got, want)
	case "Contains":
		return strings.Contains(got, want)
	}
	return got == want
}

// inPath reports whether name lies under path (one level unless recursive).
func inPath(name, path string, recursive bool) bool {
	if !strings.HasSuffix(path, "/") {
		path += "/"
	}
	rest, ok := strings.CutPrefix(name, path)
	return ok && rest != "" && (recursive || !strings.Contains(rest, "/"))
}

// filterMatch applies one filter to a parameter's version v.
func filterMatch(f Filter, p Parameter, v version) bool {
	hit := func(got string) bool {
		for _, want := range f.Values {
			if matchOpt(f.Option, want, got) {
				return true
			}
		}
		return false
	}
	switch {
	case f.Key == "Name":
		return hit(p.Name)
	case f.Key == "Type":
		return hit(p.typeOf(v))
	case f.Key == "KeyId":
		return p.typeOf(v) == "SecureString" && (hit(p.keyRef(v)) || hit(p.keyOf(v)))
	case f.Key == "DataType":
		return hit(p.dataType())
	case f.Key == "Tier":
		return hit(p.tier())
	case f.Key == "Label":
		for _, l := range v.Labels {
			if hit(l) {
				return true
			}
		}
		return false
	case f.Key == "Path":
		for _, path := range f.Values {
			if inPath(p.Name, path, f.Option == "Recursive") {
				return true
			}
		}
		return false
	case f.Key == "tag-key":
		for _, want := range f.Values {
			if _, ok := p.Tags[want]; ok {
				return true
			}
		}
		return false
	case strings.HasPrefix(f.Key, "tag:"):
		val, ok := p.Tags[strings.TrimPrefix(f.Key, "tag:")]
		return ok && (len(f.Values) == 0 || hit(val))
	}
	return false
}

// selected returns the version a set of filters picks (a Label filter selects the labeled version).
func selected(p Parameter, fs []Filter) (version, bool) {
	cands := []version{p.current()}
	for _, f := range fs {
		if f.Key == "Label" {
			cands = p.History
		}
	}
	for i := len(cands) - 1; i >= 0; i-- {
		ok := true
		for _, f := range fs {
			ok = ok && filterMatch(f, p, cands[i])
		}
		if ok {
			return cands[i], true
		}
	}
	return version{}, false
}

func (s *Service) all() []Parameter {
	ps := store.List[Parameter](s.env.Store, cParams)
	for i := range ps {
		ps[i].ARN = core.CanonicalARN(ps[i].ARN)
	}
	return ps
}

// byPath lists parameters under path that the caller may read. Parameters the
// caller cannot read are left out; a failed decryption fails the call when strict.
func (s *Service) byPath(az Authz, path string, recursive bool, fs []Filter, decrypt, strict bool) ([]Parameter, []version, []string, error) {
	if !strings.HasPrefix(path, "/") || len(path) > 2048 {
		return nil, nil, nil, core.BadRequest("The parameter path must begin with /: %q", path)
	}
	if strings.Count(path, "/") > maxDepth+1 {
		return nil, nil, nil, errf("HierarchyLevelLimitExceededException", "A hierarchy can have a maximum of %d levels.", maxDepth)
	}
	if err := validFilters(fs, []string{"Type", "KeyId", "Label", "tag:"}); err != nil {
		return nil, nil, nil, err
	}
	var ps []Parameter
	var vs []version
	var vals []string
	for _, p := range s.all() {
		if !inPath(p.Name, path, recursive) {
			continue
		}
		if az("ssm:GetParametersByPath", p.ARN) != nil {
			continue
		}
		v, ok := selected(p, fs)
		if !ok {
			continue
		}
		val, err := s.value(az, p, v, decrypt)
		if err != nil {
			if strict {
				return nil, nil, nil, err
			}
			continue
		}
		ps, vs, vals = append(ps, p), append(vs, v), append(vals, val)
	}
	return ps, vs, vals, nil
}
