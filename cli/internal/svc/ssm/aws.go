package ssm

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

// RegisterAWS serves Parameter Store over the AWS JSON 1.1 protocol (AmazonSSM).
func (s *Service) RegisterAWS() {
	awsapi.Register(&awsapi.Service{
		Name: "ssm", JSONPrefix: "AmazonSSM", JSONVersion: "1.1",
		ErrorCode: map[string]string{"ValidationError": "ValidationException", "BadRequest": "ValidationException",
			"ResourceNotFound": "ParameterNotFound", "Conflict": "InternalServerError", "AccessDenied": "AccessDeniedException"},
		Ops: map[string]awsapi.Op{
			"PutParameter":            s.awsPutParameter,
			"GetParameter":            s.awsGetParameter,
			"GetParameters":           s.awsGetParameters,
			"GetParametersByPath":     s.awsGetParametersByPath,
			"DescribeParameters":      s.awsDescribeParameters,
			"GetParameterHistory":     s.awsGetParameterHistory,
			"DeleteParameter":         s.awsDeleteParameter,
			"DeleteParameters":        s.awsDeleteParameters,
			"LabelParameterVersion":   s.awsLabelParameterVersion,
			"UnlabelParameterVersion": s.awsUnlabelParameterVersion,
			"AddTagsToResource":       s.awsAddTagsToResource,
			"RemoveTagsFromResource":  s.awsRemoveTagsFromResource,
			"ListTagsForResource":     s.awsListTagsForResource,
		},
	})
}

func policiesOut(raw string) []map[string]string {
	out := []map[string]string{}
	var list []json.RawMessage
	if raw == "" || json.Unmarshal([]byte(raw), &list) != nil {
		return out
	}
	for _, p := range list {
		var x struct{ Type string }
		_ = json.Unmarshal(p, &x)
		out = append(out, map[string]string{"PolicyText": string(p), "PolicyType": x.Type, "PolicyStatus": "Pending"})
	}
	return out
}

func validPolicies(raw string) error {
	if raw == "" {
		return nil
	}
	var list []struct {
		Type       string          `json:"Type"`
		Version    string          `json:"Version"`
		Attributes json.RawMessage `json:"Attributes"`
	}
	if err := json.Unmarshal([]byte(raw), &list); err != nil || len(list) == 0 {
		return errf("InvalidPolicyTypeException", "Policies must be a JSON array of parameter policies")
	}
	for _, p := range list {
		if !slices.Contains([]string{"Expiration", "ExpirationNotification", "NoChangeNotification"}, p.Type) {
			return errf("InvalidPolicyTypeException", "The policy type %q is not supported.", p.Type)
		}
		if len(p.Attributes) == 0 {
			return errf("InvalidPolicyAttributeException", "A %s policy needs Attributes.", p.Type)
		}
	}
	return nil
}

func paramOut(p Parameter, v version, val, sel string) map[string]any {
	m := map[string]any{"Name": p.Name, "Type": p.typeOf(v), "Value": val, "Version": v.Version,
		"LastModifiedDate": awsapi.Epoch(v.LastModified), "ARN": p.ARN, "DataType": dataTypeOf(p, v)}
	if sel != "" {
		m["Selector"] = ":" + sel
	}
	return m
}

func dataTypeOf(p Parameter, v version) string {
	if v.DataType != "" {
		return v.DataType
	}
	return p.dataType()
}

func metadataOut(p Parameter) map[string]any {
	v := p.current()
	m := map[string]any{"Name": p.Name, "ARN": p.ARN, "Type": p.typeOf(v), "LastModifiedDate": awsapi.Epoch(v.LastModified),
		"LastModifiedUser": v.ModifiedBy, "Version": v.Version, "Tier": p.tier(), "Policies": policiesOut(p.Policies), "DataType": p.dataType()}
	if k := p.keyRef(v); k != "" {
		m["KeyId"] = k
	}
	if p.Description != "" {
		m["Description"] = p.Description
	}
	if p.AllowedPattern != "" {
		m["AllowedPattern"] = p.AllowedPattern
	}
	return m
}

func historyOut(p Parameter, v version, val string) map[string]any {
	m := map[string]any{"Name": p.Name, "Type": p.typeOf(v), "Value": val, "Version": v.Version, "LastModifiedDate": awsapi.Epoch(v.LastModified),
		"LastModifiedUser": v.ModifiedBy, "Labels": v.Labels, "Tier": v.tier(), "Policies": policiesOut(v.Policies), "DataType": dataTypeOf(p, v)}
	if v.Labels == nil {
		m["Labels"] = []string{}
	}
	if k := p.keyRef(v); k != "" {
		m["KeyId"] = k
	}
	if d := v.description(p); d != "" {
		m["Description"] = d
	}
	if v.AllowedPattern != "" {
		m["AllowedPattern"] = v.AllowedPattern
	}
	return m
}

// page returns one page of items; tokens are opaque offsets.
func page[T any](items []T, token string, max, limit, def int) ([]T, string, error) {
	if max == 0 {
		max = def
	}
	if max < 1 || max > limit {
		return nil, "", awsapi.Errorf(http.StatusBadRequest, "ValidationException", "1 validation error detected: Value '%d' at 'maxResults' failed to satisfy constraint: Member must have value less than or equal to %d", max, limit)
	}
	off := 0
	if token != "" {
		b, err := base64.RawURLEncoding.DecodeString(token)
		n, err2 := strconv.Atoi(strings.TrimPrefix(string(b), "o:"))
		if err != nil || err2 != nil || !strings.HasPrefix(string(b), "o:") || n < 0 || n > len(items) {
			return nil, "", awsapi.Errorf(http.StatusBadRequest, "InvalidNextToken", "The specified token is not valid.")
		}
		off = n
	}
	end := off + max
	if end >= len(items) {
		return items[off:], "", nil
	}
	return items[off:end], base64.RawURLEncoding.EncodeToString([]byte("o:" + strconv.Itoa(end))), nil
}

type awsTag struct {
	Key   string `json:"Key"`
	Value string `json:"Value"`
}

func tagMap(list []awsTag) core.Tags {
	if len(list) == 0 {
		return nil
	}
	t := core.Tags{}
	for _, x := range list {
		t[x.Key] = x.Value
	}
	return t
}

func (s *Service) awsPutParameter(q *awsapi.Req) (any, error) {
	var in struct {
		Name, Value, Type, KeyId, Tier, AllowedPattern, DataType, Policies string
		Description                                                        *string
		Overwrite                                                          bool
		Tags                                                               []awsTag
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if strings.HasPrefix(in.Name, "arn:") {
		in.Name, _ = s.nameOf(in.Name)
	}
	if err := validPolicies(in.Policies); err != nil {
		return nil, err
	}
	p, err := s.put(q.Authorize, q.P.ARN, PutInput{Name: in.Name, Value: in.Value, Type: in.Type, KeyID: in.KeyId, Description: in.Description,
		Overwrite: in.Overwrite, Tier: in.Tier, AllowedPattern: in.AllowedPattern, DataType: in.DataType, Policies: in.Policies, Tags: tagMap(in.Tags)})
	if err != nil {
		return nil, err
	}
	return map[string]any{"Version": p.current().Version, "Tier": p.tier()}, nil
}

// get reads one parameter (or a Secrets Manager reference) for GetParameter(s).
// Value returns a parameter's decrypted value (callers authorize themselves).
func (s *Service) Value(ref string) (string, error) {
	p, err := s.get(func(string, string) error { return nil }, ref, true)
	if err != nil {
		return "", err
	}
	return fmt.Sprint(p["Value"]), nil
}

func (s *Service) get(az Authz, ref string, decrypt bool) (map[string]any, error) {
	name, sel := s.nameOf(ref)
	if strings.HasPrefix(name, secretsPrefix) {
		return s.reference(az, name, sel, decrypt)
	}
	p, v, sel, err := s.load(az, "ssm:GetParameter", ref)
	if err != nil {
		return nil, err
	}
	val, err := s.value(az, p, v, decrypt)
	if err != nil {
		return nil, err
	}
	return paramOut(p, v, val, sel), nil
}

// reference serves /aws/reference/secretsmanager/<secret>[:version-id or :stage].
func (s *Service) reference(az Authz, name, sel string, decrypt bool) (map[string]any, error) {
	if err := az("ssm:GetParameter", s.arn(name)); err != nil {
		return nil, err
	}
	if s.Secrets == nil {
		return nil, notFound(name)
	}
	if !decrypt {
		return nil, core.BadRequest("WithDecryption flag must be True for retrieving a Secret Manager secret.")
	}
	vid, stage := "", sel
	if len(sel) == 36 && strings.Count(sel, "-") == 4 {
		vid, stage = sel, ""
	}
	r, err := s.Secrets.Reference(az, strings.TrimPrefix(name, secretsPrefix), vid, stage)
	if err != nil {
		var ce *core.Error
		if errors.As(err, &ce) && ce.Code == "ResourceNotFound" {
			return nil, errf("ParameterNotFound", "Secret %s not found.", strings.TrimPrefix(name, secretsPrefix))
		}
		return nil, err
	}
	m := map[string]any{"Name": name, "Type": "SecureString", "Value": r.Value, "Version": 0, "SourceResult": r.Source,
		"LastModifiedDate": awsapi.Epoch(r.CreatedAt), "ARN": r.ARN, "DataType": "text"}
	if sel != "" {
		m["Selector"] = ":" + sel
	}
	return m, nil
}

func (s *Service) awsGetParameter(q *awsapi.Req) (any, error) {
	var in struct {
		Name           string
		WithDecryption bool
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	p, err := s.get(q.Authorize, in.Name, in.WithDecryption)
	if err != nil {
		return nil, err
	}
	return map[string]any{"Parameter": p}, nil
}

func (s *Service) awsGetParameters(q *awsapi.Req) (any, error) {
	var in struct {
		Names          []string
		WithDecryption bool
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if len(in.Names) == 0 || len(in.Names) > 10 {
		return nil, awsapi.Errorf(http.StatusBadRequest, "ValidationException", "Names must have 1-10 entries")
	}
	params, invalid := []any{}, []string{}
	for _, n := range in.Names {
		p, err := s.get(q.Authorize, n, in.WithDecryption)
		if err != nil {
			var ce *core.Error
			if errors.As(err, &ce) && (ce.Code == "ParameterNotFound" || ce.Code == "ParameterVersionNotFound") {
				invalid = append(invalid, n)
				continue
			}
			return nil, err
		}
		params = append(params, p)
	}
	return map[string]any{"Parameters": params, "InvalidParameters": invalid}, nil
}

func (s *Service) awsGetParametersByPath(q *awsapi.Req) (any, error) {
	var in struct {
		Path                      string
		Recursive, WithDecryption bool
		ParameterFilters          []Filter
		MaxResults                int
		NextToken                 string
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if strings.HasPrefix(in.Path, secretsPrefix) || strings.HasPrefix(in.Path, "/aws/") {
		return nil, core.BadRequest("The path %s is reserved.", in.Path)
	}
	ps, vs, vals, err := s.byPath(q.Authorize, in.Path, in.Recursive, in.ParameterFilters, in.WithDecryption, true)
	if err != nil {
		return nil, err
	}
	idx := make([]int, len(ps))
	for i := range idx {
		idx[i] = i
	}
	idx, next, err := page(idx, in.NextToken, in.MaxResults, 10, 10)
	if err != nil {
		return nil, err
	}
	items := []any{}
	for _, i := range idx {
		items = append(items, paramOut(ps[i], vs[i], vals[i], ""))
	}
	out := map[string]any{"Parameters": items}
	if next != "" {
		out["NextToken"] = next
	}
	return out, nil
}

func (s *Service) awsDescribeParameters(q *awsapi.Req) (any, error) {
	var in struct {
		Filters          []Filter
		ParameterFilters []Filter
		MaxResults       int
		NextToken        string
		Shared           bool
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("ssm:DescribeParameters", "*"); err != nil {
		return nil, err
	}
	if len(in.Filters) > 0 && len(in.ParameterFilters) > 0 {
		return nil, core.BadRequest("You can use either Filters or ParameterFilters in a single request, not both.")
	}
	fs := in.ParameterFilters
	for _, f := range in.Filters { // legacy ParametersFilter: Name (BeginsWith), Type, KeyId
		if f.Key != "Name" && f.Key != "Type" && f.Key != "KeyId" {
			return nil, errf("InvalidFilterKey", "The following filter key is not valid: %s. Valid filter keys include: [Name, Type, KeyId].", f.Key)
		}
		opt := "Equals"
		if f.Key == "Name" {
			opt = "BeginsWith"
		}
		fs = append(fs, Filter{Key: f.Key, Option: opt, Values: f.Values})
	}
	if err := validFilters(fs, []string{"Name", "Type", "KeyId", "Path", "Tier", "DataType", "Label", "tag-key", "tag:"}); err != nil {
		return nil, err
	}
	for i := range fs {
		if fs[i].Key == "Path" && fs[i].Option == "" {
			fs[i].Option = "OneLevel"
		}
	}
	var ps []Parameter
	for _, p := range s.all() {
		if _, ok := selected(p, fs); ok {
			ps = append(ps, p)
		}
	}
	sort.SliceStable(ps, func(i, j int) bool { return ps[i].Name < ps[j].Name })
	ps, next, err := page(ps, in.NextToken, in.MaxResults, 50, 50)
	if err != nil {
		return nil, err
	}
	items := []any{}
	for _, p := range ps {
		items = append(items, metadataOut(p))
	}
	out := map[string]any{"Parameters": items}
	if next != "" {
		out["NextToken"] = next
	}
	return out, nil
}

func (s *Service) awsGetParameterHistory(q *awsapi.Req) (any, error) {
	var in struct {
		Name           string
		WithDecryption bool
		MaxResults     int
		NextToken      string
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	name, _ := s.nameOf(in.Name)
	p, _, _, err := s.load(q.Authorize, "ssm:GetParameterHistory", name)
	if err != nil {
		return nil, err
	}
	vs, next, err := page(p.History, in.NextToken, in.MaxResults, 50, 50)
	if err != nil {
		return nil, err
	}
	items := []any{}
	for _, v := range vs {
		val, err := s.value(q.Authorize, p, v, in.WithDecryption)
		if err != nil {
			return nil, err
		}
		items = append(items, historyOut(p, v, val))
	}
	out := map[string]any{"Parameters": items}
	if next != "" {
		out["NextToken"] = next
	}
	return out, nil
}

func (s *Service) awsDeleteParameter(q *awsapi.Req) (any, error) {
	var in struct{ Name string }
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	return nil, s.remove(q.Authorize, in.Name)
}

func (s *Service) awsDeleteParameters(q *awsapi.Req) (any, error) {
	var in struct{ Names []string }
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if len(in.Names) == 0 || len(in.Names) > 10 {
		return nil, awsapi.Errorf(http.StatusBadRequest, "ValidationException", "Names must have 1-10 entries")
	}
	for _, n := range in.Names {
		name, _ := s.nameOf(n)
		if err := q.Authorize("ssm:DeleteParameters", s.arn(name)); err != nil {
			return nil, err
		}
	}
	deleted, invalid := []string{}, []string{}
	for _, n := range in.Names {
		if err := s.remove(func(string, string) error { return nil }, n); err != nil {
			invalid = append(invalid, n)
		} else {
			deleted = append(deleted, n)
		}
	}
	return map[string]any{"DeletedParameters": deleted, "InvalidParameters": invalid}, nil
}

func (s *Service) awsLabelParameterVersion(q *awsapi.Req) (any, error) {
	var in struct {
		Name             string
		ParameterVersion int
		Labels           []string
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	invalid, ver, err := s.label(q.Authorize, in.Name, in.ParameterVersion, in.Labels)
	if err != nil {
		return nil, err
	}
	if invalid == nil {
		invalid = []string{}
	}
	return map[string]any{"InvalidLabels": invalid, "ParameterVersion": ver}, nil
}

func (s *Service) awsUnlabelParameterVersion(q *awsapi.Req) (any, error) {
	var in struct {
		Name             string
		ParameterVersion int
		Labels           []string
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	removed, invalid, err := s.unlabel(q.Authorize, in.Name, in.ParameterVersion, in.Labels)
	if err != nil {
		return nil, err
	}
	return map[string]any{"RemovedLabels": removed, "InvalidLabels": invalid}, nil
}

func (s *Service) awsAddTagsToResource(q *awsapi.Req) (any, error) {
	var in struct {
		ResourceType, ResourceId string
		Tags                     []awsTag
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if len(in.Tags) == 0 {
		return nil, core.BadRequest("Tags must not be empty")
	}
	_, err := s.tag(q.Authorize, "ssm:AddTagsToResource", in.ResourceType, in.ResourceId, tagMap(in.Tags), nil)
	return nil, err
}

func (s *Service) awsRemoveTagsFromResource(q *awsapi.Req) (any, error) {
	var in struct {
		ResourceType, ResourceId string
		TagKeys                  []string
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	_, err := s.tag(q.Authorize, "ssm:RemoveTagsFromResource", in.ResourceType, in.ResourceId, nil, in.TagKeys)
	return nil, err
}

func (s *Service) awsListTagsForResource(q *awsapi.Req) (any, error) {
	var in struct{ ResourceType, ResourceId string }
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if in.ResourceType != "Parameter" {
		return nil, errf("InvalidResourceType", "The resource type %q is not supported; HomeCloud supports Parameter.", in.ResourceType)
	}
	name, _ := s.nameOf(in.ResourceId)
	if err := q.Authorize("ssm:ListTagsForResource", s.arn(name)); err != nil {
		return nil, err
	}
	p, err := store.Get[Parameter](s.env.Store, cParams, name)
	if err != nil {
		return nil, errf("InvalidResourceId", "The resource ID %q is not valid. Verify the ID and try again.", in.ResourceId)
	}
	list := []awsTag{}
	for k, v := range p.Tags {
		list = append(list, awsTag{k, v})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Key < list[j].Key })
	return map[string]any{"TagList": list}, nil
}
