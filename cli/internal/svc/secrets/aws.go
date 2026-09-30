package secrets

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
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

// errorCodes maps HomeCloud error codes to Secrets Manager's.
var errorCodes = map[string]string{
	"ResourceNotFound": "ResourceNotFoundException",
	"ValidationError":  "InvalidParameterException",
	"BadRequest":       "InvalidParameterException",
	"ResourceConflict": "ResourceExistsException",
	"Conflict":         "InvalidRequestException",
	"InvalidRequest":   "InvalidRequestException",
	"AccessDenied":     "AccessDeniedException",
}

// RegisterAWS serves Secrets Manager over the AWS JSON 1.1 protocol.
func (s *Service) RegisterAWS() {
	awsapi.Register(&awsapi.Service{
		Name: "secretsmanager", JSONPrefix: "secretsmanager", JSONVersion: "1.1",
		ErrorCode: errorCodes,
		Ops: map[string]awsapi.Op{
			"CreateSecret":             s.awsCreateSecret,
			"GetSecretValue":           s.awsGetSecretValue,
			"BatchGetSecretValue":      s.awsBatchGetSecretValue,
			"PutSecretValue":           s.awsPutSecretValue,
			"UpdateSecret":             s.awsUpdateSecret,
			"DescribeSecret":           s.awsDescribeSecret,
			"ListSecrets":              s.awsListSecrets,
			"DeleteSecret":             s.awsDeleteSecret,
			"RestoreSecret":            s.awsRestoreSecret,
			"UpdateSecretVersionStage": s.awsUpdateSecretVersionStage,
			"ListSecretVersionIds":     s.awsListSecretVersionIds,
			"TagResource":              s.awsTagResource,
			"UntagResource":            s.awsUntagResource,
			"GetRandomPassword":        s.awsGetRandomPassword,
			"PutResourcePolicy":        s.awsPutResourcePolicy,
			"GetResourcePolicy":        s.awsGetResourcePolicy,
			"DeleteResourcePolicy":     s.awsDeleteResourcePolicy,
			"ValidateResourcePolicy":   s.awsValidateResourcePolicy,
			"RotateSecret":             s.awsRotateSecret,
			"CancelRotateSecret":       s.awsCancelRotateSecret,

			"ReplicateSecretToRegions":     s.awsReplicateSecretToRegions,
			"RemoveRegionsFromReplication": s.awsRemoveRegionsFromReplication,
			"StopReplicationToReplica":     s.awsStopReplicationToReplica,
		},
	})
}

type awsTag struct {
	Key   string `json:"Key"`
	Value string `json:"Value"`
}

func tagList(t core.Tags) []awsTag {
	out := make([]awsTag, 0, len(t))
	for k, v := range t {
		out = append(out, awsTag{k, v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func tagMap(list []awsTag) core.Tags {
	if list == nil {
		return nil
	}
	t := core.Tags{}
	for _, x := range list {
		t[x.Key] = x.Value
	}
	return t
}

func epoch(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return awsapi.Epoch(*t)
}

func stagesOf(sec Secret) map[string][]string {
	m := map[string][]string{}
	for _, v := range sec.Versions {
		if len(v.Stages) > 0 {
			m[v.ID] = slices.Clone(v.Stages)
		}
	}
	return m
}

// describeOut is DescribeSecret's response (and a ListSecrets entry).
func describeOut(sec Secret, list bool) map[string]any {
	m := map[string]any{"ARN": sec.ARN, "Name": sec.Name, "CreatedDate": awsapi.Epoch(sec.CreatedAt),
		"LastChangedDate": awsapi.Epoch(sec.UpdatedAt), "RotationEnabled": sec.RotationEnabled}
	if list {
		m["SecretVersionsToStages"] = stagesOf(sec)
	} else {
		m["VersionIdsToStages"] = stagesOf(sec)
	}
	set := func(k string, v any) {
		switch x := v.(type) {
		case string:
			if x == "" {
				return
			}
		case nil:
			return
		}
		m[k] = v
	}
	set("Description", sec.Description)
	set("KmsKeyId", sec.KMSKeyID)
	set("RotationLambdaARN", sec.RotationLambdaARN)
	if sec.RotationRules != nil {
		m["RotationRules"] = sec.RotationRules
	}
	set("LastRotatedDate", epoch(sec.LastRotated))
	set("NextRotationDate", epoch(sec.NextRotation))
	set("LastAccessedDate", epoch(sec.LastAccessed))
	set("DeletedDate", epoch(sec.DeletedAt))
	set("OwningService", sec.ManagedBy)
	if len(sec.Tags) > 0 {
		m["Tags"] = tagList(sec.Tags)
	}
	m["PrimaryRegion"] = core.Region
	return m
}

func valueOut(sec Secret, v Version, p []byte) map[string]any {
	m := map[string]any{"ARN": sec.ARN, "Name": sec.Name, "VersionId": v.ID, "VersionStages": v.Stages, "CreatedDate": awsapi.Epoch(v.CreatedAt)}
	if v.Binary {
		m["SecretBinary"] = p // []byte encodes as base64
	} else {
		m["SecretString"] = string(p)
	}
	return m
}

// secretValue reads SecretString / SecretBinary from a request.
type secretValue struct {
	SecretString *string `json:"SecretString"`
	SecretBinary []byte  `json:"SecretBinary"`
}

func (v secretValue) get() ([]byte, bool, bool, error) {
	switch {
	case v.SecretString != nil && v.SecretBinary != nil:
		return nil, false, false, awsapi.Errorf(http.StatusBadRequest, "InvalidParameterException", "You can't specify both SecretString and SecretBinary.")
	case v.SecretString != nil:
		return []byte(*v.SecretString), true, false, nil
	case v.SecretBinary != nil:
		return v.SecretBinary, true, true, nil
	}
	return nil, false, false, nil
}

func (s *Service) awsCreateSecret(q *awsapi.Req) (any, error) {
	var in struct {
		Name, Description, KmsKeyId, ClientRequestToken string
		secretValue
		Tags                        []awsTag
		ForceOverwriteReplicaSecret bool
		AddReplicaRegions           []any
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if len(in.AddReplicaRegions) > 0 {
		return nil, errNoReplication()
	}
	val, has, bin, err := in.get()
	if err != nil {
		return nil, err
	}
	sec, v, err := s.create(q.Authorize, CreateInput{Name: in.Name, Description: in.Description, KMSKeyID: in.KmsKeyId,
		Value: val, HasValue: has, Binary: bin, Token: in.ClientRequestToken, Tags: tagMap(in.Tags)})
	if err != nil {
		return nil, err
	}
	out := map[string]any{"ARN": sec.ARN, "Name": sec.Name, "ReplicationStatus": []any{}}
	if v != nil {
		out["VersionId"] = v.ID
	}
	return out, nil
}

func (s *Service) awsGetSecretValue(q *awsapi.Req) (any, error) {
	var in struct{ SecretId, VersionId, VersionStage string }
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	sec, v, p, err := s.getValue(q.Authorize, in.SecretId, in.VersionId, in.VersionStage)
	if err != nil {
		return nil, err
	}
	return valueOut(sec, v, p), nil
}

type awsFilter struct {
	Key    string
	Values []string
}

func (s *Service) awsBatchGetSecretValue(q *awsapi.Req) (any, error) {
	var in struct {
		SecretIdList []string
		Filters      []awsFilter
		MaxResults   int
		NextToken    string
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if (len(in.SecretIdList) == 0) == (len(in.Filters) == 0) {
		return nil, awsapi.Errorf(http.StatusBadRequest, "InvalidParameterException", "Specify either SecretIdList or Filters.")
	}
	if len(in.SecretIdList) > 20 {
		return nil, awsapi.Errorf(http.StatusBadRequest, "InvalidParameterException", "SecretIdList can have at most 20 entries.")
	}
	if len(in.SecretIdList) > 0 && in.MaxResults != 0 {
		return nil, awsapi.Errorf(http.StatusBadRequest, "InvalidParameterException", "MaxResults can only be used with Filters.")
	}
	if err := q.Authorize("secretsmanager:BatchGetSecretValue", "*"); err != nil {
		return nil, err
	}
	ids, next := in.SecretIdList, ""
	if len(in.Filters) > 0 {
		if err := q.Authorize("secretsmanager:ListSecrets", "*"); err != nil {
			return nil, err
		}
		max := in.MaxResults
		if max == 0 {
			max = 20
		}
		if max < 1 || max > 20 {
			return nil, awsapi.Errorf(http.StatusBadRequest, "InvalidParameterException", "MaxResults must be 1-20.")
		}
		list, err := s.filtered(in.Filters, false, "")
		if err != nil {
			return nil, err
		}
		list, next, err = page(list, in.NextToken, max)
		if err != nil {
			return nil, err
		}
		ids = nil
		for _, sec := range list {
			ids = append(ids, sec.ARN)
		}
	}
	values, errs := []any{}, []any{}
	for _, id := range ids {
		sec, v, p, err := s.getValue(q.Authorize, id, "", "")
		if err != nil {
			code, msg := errorCode(err)
			errs = append(errs, map[string]string{"SecretId": id, "ErrorCode": code, "Message": msg})
			continue
		}
		values = append(values, valueOut(sec, v, p))
	}
	out := map[string]any{"SecretValues": values, "Errors": errs}
	if next != "" {
		out["NextToken"] = next
	}
	return out, q.Authorize("secretsmanager:BatchGetSecretValue", "*")
}

func errorCode(err error) (string, string) {
	var ae *awsapi.Error
	var ce *core.Error
	switch {
	case errors.As(err, &ae):
		return ae.Code, ae.Message
	case errors.As(err, &ce):
		if c := errorCodes[ce.Code]; c != "" {
			return c, ce.Message
		}
		return ce.Code, ce.Message
	}
	return "InternalServiceError", err.Error()
}

func (s *Service) awsPutSecretValue(q *awsapi.Req) (any, error) {
	var in struct {
		SecretId, ClientRequestToken string
		secretValue
		VersionStages []string
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	val, has, bin, err := in.get()
	if err != nil {
		return nil, err
	}
	if !has {
		return nil, awsapi.Errorf(http.StatusBadRequest, "InvalidRequestException", "You must provide either SecretString or SecretBinary.")
	}
	sec, v, err := s.putValue(q.Authorize, in.SecretId, val, bin, in.ClientRequestToken, in.VersionStages)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ARN": sec.ARN, "Name": sec.Name, "VersionId": v.ID, "VersionStages": v.Stages}, nil
}

func (s *Service) awsUpdateSecret(q *awsapi.Req) (any, error) {
	var in struct {
		SecretId, ClientRequestToken string
		Description, KmsKeyId        *string
		secretValue
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	val, has, bin, err := in.get()
	if err != nil {
		return nil, err
	}
	sec, v, err := s.update(q.Authorize, in.SecretId, UpdateInput{Description: in.Description, KMSKeyID: in.KmsKeyId,
		Value: val, HasValue: has, Binary: bin, Token: in.ClientRequestToken})
	if err != nil {
		return nil, err
	}
	out := map[string]any{"ARN": sec.ARN, "Name": sec.Name}
	if v != nil {
		out["VersionId"] = v.ID
	}
	return out, nil
}

func (s *Service) awsDescribeSecret(q *awsapi.Req) (any, error) {
	var in struct{ SecretId string }
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	sec, err := s.describe(q.Authorize, in.SecretId)
	if err != nil {
		return nil, err
	}
	return describeOut(sec, false), nil
}

// page returns one page of items; tokens are opaque offsets.
func page[T any](items []T, token string, max int) ([]T, string, error) {
	off := 0
	if token != "" {
		b, err := base64.RawURLEncoding.DecodeString(token)
		n, err2 := strconv.Atoi(strings.TrimPrefix(string(b), "o:"))
		if err != nil || err2 != nil || n < 0 || n > len(items) || !strings.HasPrefix(string(b), "o:") {
			return nil, "", awsapi.Errorf(http.StatusBadRequest, "InvalidNextTokenException", "The NextToken value is invalid.")
		}
		off = n
	}
	end := off + max
	if end >= len(items) {
		return items[off:], "", nil
	}
	return items[off:end], base64.RawURLEncoding.EncodeToString([]byte("o:" + strconv.Itoa(end))), nil
}

// matches reports whether a filter value matches s: a prefix match, negated by a leading "!".
func matches(val, s string) bool {
	if neg, ok := strings.CutPrefix(val, "!"); ok {
		return !strings.HasPrefix(s, neg)
	}
	return strings.HasPrefix(s, val)
}

func filterMatch(f awsFilter, sec Secret) (bool, error) {
	var fields []string
	switch f.Key {
	case "name":
		fields = []string{sec.Name}
	case "description":
		fields = []string{sec.Description}
	case "tag-key":
		for k := range sec.Tags {
			fields = append(fields, k)
		}
	case "tag-value":
		for _, v := range sec.Tags {
			fields = append(fields, v)
		}
	case "primary-region":
		fields = []string{core.Region}
	case "owning-service":
		fields = []string{sec.ManagedBy}
	case "all":
		fields = []string{sec.Name, sec.Description}
		for k, v := range sec.Tags {
			fields = append(fields, k, v)
		}
	default:
		return false, awsapi.Errorf(http.StatusBadRequest, "InvalidParameterException", "Invalid filter key %q: use description, name, tag-key, tag-value, primary-region, owning-service or all.", f.Key)
	}
	for _, val := range f.Values {
		neg := strings.HasPrefix(val, "!")
		hit := neg // a negated value matches when no field has the prefix
		for _, fv := range fields {
			if neg && !matches(val, fv) {
				hit = false
			}
			if !neg && matches(val, fv) {
				hit = true
			}
		}
		if hit {
			return true, nil
		}
	}
	return false, nil
}

// filtered lists secrets matching every filter, ordered by creation date.
func (s *Service) filtered(filters []awsFilter, includeDeleted bool, order string) ([]Secret, error) {
	out := []Secret{}
	for _, sec := range store.List[Secret](s.env.Store, cSecrets) {
		sec.ARN = core.CanonicalARN(sec.ARN)
		if sec.DeletionDate != nil && !includeDeleted {
			continue
		}
		ok := true
		for _, f := range filters {
			hit, err := filterMatch(f, sec)
			if err != nil {
				return nil, err
			}
			ok = ok && hit
		}
		if ok {
			out = append(out, sec)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if order == "asc" {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

func (s *Service) awsListSecrets(q *awsapi.Req) (any, error) {
	var in struct {
		IncludePlannedDeletion bool
		MaxResults             int
		NextToken, SortOrder   string
		Filters                []awsFilter
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("secretsmanager:ListSecrets", "*"); err != nil {
		return nil, err
	}
	if in.MaxResults == 0 {
		in.MaxResults = 100
	}
	if in.MaxResults < 1 || in.MaxResults > 100 {
		return nil, awsapi.Errorf(http.StatusBadRequest, "InvalidParameterException", "MaxResults must be 1-100.")
	}
	if in.SortOrder != "" && in.SortOrder != "asc" && in.SortOrder != "desc" {
		return nil, awsapi.Errorf(http.StatusBadRequest, "InvalidParameterException", "SortOrder must be asc or desc.")
	}
	s.purgeDeleted()
	list, err := s.filtered(in.Filters, in.IncludePlannedDeletion, in.SortOrder)
	if err != nil {
		return nil, err
	}
	list, next, err := page(list, in.NextToken, in.MaxResults)
	if err != nil {
		return nil, err
	}
	items := make([]any, len(list))
	for i, sec := range list {
		items[i] = describeOut(sec, true)
	}
	out := map[string]any{"SecretList": items}
	if next != "" {
		out["NextToken"] = next
	}
	return out, nil
}

func (s *Service) awsDeleteSecret(q *awsapi.Req) (any, error) {
	var in struct {
		SecretId                   string
		RecoveryWindowInDays       *int64
		ForceDeleteWithoutRecovery bool
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	sec, err := s.remove(q.Authorize, in.SecretId, in.RecoveryWindowInDays, in.ForceDeleteWithoutRecovery, 7)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ARN": sec.ARN, "Name": sec.Name, "DeletionDate": epoch(sec.DeletionDate)}, nil
}

func (s *Service) awsRestoreSecret(q *awsapi.Req) (any, error) {
	var in struct{ SecretId string }
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	sec, err := s.restore(q.Authorize, in.SecretId)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ARN": sec.ARN, "Name": sec.Name}, nil
}

func (s *Service) awsUpdateSecretVersionStage(q *awsapi.Req) (any, error) {
	var in struct{ SecretId, VersionStage, RemoveFromVersionId, MoveToVersionId string }
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	sec, err := s.updateStage(q.Authorize, in.SecretId, in.VersionStage, in.RemoveFromVersionId, in.MoveToVersionId)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ARN": sec.ARN, "Name": sec.Name}, nil
}

func (s *Service) awsListSecretVersionIds(q *awsapi.Req) (any, error) {
	var in struct {
		SecretId, NextToken string
		MaxResults          int
		IncludeDeprecated   bool
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	sec, err := s.lookup(q.Authorize, "secretsmanager:ListSecretVersionIds", in.SecretId)
	if err != nil {
		return nil, err
	}
	if in.MaxResults == 0 {
		in.MaxResults = 100
	}
	if in.MaxResults < 1 || in.MaxResults > 100 {
		return nil, awsapi.Errorf(http.StatusBadRequest, "InvalidParameterException", "MaxResults must be 1-100.")
	}
	vs := []Version{}
	for _, v := range sec.Versions {
		if len(v.Stages) > 0 || in.IncludeDeprecated {
			vs = append(vs, v)
		}
	}
	vs, next, err := page(vs, in.NextToken, in.MaxResults)
	if err != nil {
		return nil, err
	}
	items := make([]any, len(vs))
	for i, v := range vs {
		m := map[string]any{"VersionId": v.ID, "VersionStages": v.Stages, "CreatedDate": awsapi.Epoch(v.CreatedAt)}
		if v.LastAccessed != nil {
			m["LastAccessedDate"] = awsapi.Epoch(*v.LastAccessed)
		}
		if v.KMSKey != "" {
			m["KmsKeyIds"] = []string{v.KMSKey}
		}
		items[i] = m
	}
	out := map[string]any{"ARN": sec.ARN, "Name": sec.Name, "Versions": items}
	if next != "" {
		out["NextToken"] = next
	}
	return out, nil
}

func (s *Service) awsTagResource(q *awsapi.Req) (any, error) {
	var in struct {
		SecretId string
		Tags     []awsTag
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	add := tagMap(in.Tags)
	if add == nil {
		add = core.Tags{}
	}
	_, err := s.tag(q.Authorize, in.SecretId, add, nil)
	return nil, err
}

func (s *Service) awsUntagResource(q *awsapi.Req) (any, error) {
	var in struct {
		SecretId string
		TagKeys  []string
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	_, err := s.tag(q.Authorize, in.SecretId, nil, in.TagKeys)
	return nil, err
}

// PasswordOptions are GetRandomPassword's parameters.
type PasswordOptions struct {
	PasswordLength          int64
	ExcludeCharacters       string
	ExcludeNumbers          bool
	ExcludePunctuation      bool
	ExcludeUppercase        bool
	ExcludeLowercase        bool
	IncludeSpace            bool
	RequireEachIncludedType *bool
}

const punctuation = "!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~"

// RandomPassword generates a password as GetRandomPassword does.
func RandomPassword(o PasswordOptions) (string, error) {
	n := o.PasswordLength
	if n == 0 {
		n = 32
	}
	if n < 1 || n > 4096 {
		return "", core.BadRequest("PasswordLength must be 1-4096")
	}
	keep := func(set string) string {
		return strings.Map(func(r rune) rune {
			if strings.ContainsRune(o.ExcludeCharacters, r) {
				return -1
			}
			return r
		}, set)
	}
	var sets []string
	add := func(skip bool, set string) {
		if set = keep(set); !skip && set != "" {
			sets = append(sets, set)
		}
	}
	add(o.ExcludeLowercase, "abcdefghijklmnopqrstuvwxyz")
	add(o.ExcludeUppercase, "ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	add(o.ExcludeNumbers, "0123456789")
	add(o.ExcludePunctuation, punctuation)
	add(!o.IncludeSpace, " ")
	if len(sets) == 0 {
		return "", core.BadRequest("the password can't be generated: every character type is excluded")
	}
	require := o.RequireEachIncludedType == nil || *o.RequireEachIncludedType
	if require && int(n) < len(sets) {
		return "", core.BadRequest("PasswordLength is too short to include each character type")
	}
	all := strings.Join(sets, "")
	pickFrom := func(set string) byte {
		k, _ := rand.Int(rand.Reader, big.NewInt(int64(len(set))))
		return set[k.Int64()]
	}
	for {
		b := make([]byte, n)
		for i := range b {
			b[i] = pickFrom(all)
		}
		ok := true
		if require {
			for _, set := range sets {
				if !strings.ContainsAny(string(b), set) {
					ok = false
				}
			}
		}
		if ok {
			return string(b), nil
		}
	}
}

func (s *Service) awsGetRandomPassword(q *awsapi.Req) (any, error) {
	var in PasswordOptions
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("secretsmanager:GetRandomPassword", "*"); err != nil {
		return nil, err
	}
	p, err := RandomPassword(in)
	if err != nil {
		return nil, err
	}
	return map[string]string{"RandomPassword": p}, nil
}

func (s *Service) awsPutResourcePolicy(q *awsapi.Req) (any, error) {
	var in struct {
		SecretId, ResourcePolicy string
		BlockPublicPolicy        *bool
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	block := in.BlockPublicPolicy == nil || *in.BlockPublicPolicy
	sec, err := s.putPolicy(q.Authorize, in.SecretId, in.ResourcePolicy, block)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ARN": sec.ARN, "Name": sec.Name}, nil
}

func (s *Service) awsGetResourcePolicy(q *awsapi.Req) (any, error) {
	var in struct{ SecretId string }
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	sec, err := s.lookup(q.Authorize, "secretsmanager:GetResourcePolicy", in.SecretId)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"ARN": sec.ARN, "Name": sec.Name}
	if sec.Policy != "" {
		out["ResourcePolicy"] = sec.Policy
	}
	return out, nil
}

func (s *Service) awsDeleteResourcePolicy(q *awsapi.Req) (any, error) {
	var in struct{ SecretId string }
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	sec, err := s.deletePolicy(q.Authorize, in.SecretId)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ARN": sec.ARN, "Name": sec.Name}, nil
}

func (s *Service) awsValidateResourcePolicy(q *awsapi.Req) (any, error) {
	var in struct{ SecretId, ResourcePolicy string }
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	res := "*"
	if in.SecretId != "" {
		sec, err := s.lookup(q.Authorize, "secretsmanager:ValidateResourcePolicy", in.SecretId)
		if err != nil {
			return nil, err
		}
		res = sec.ARN
	}
	if err := q.Authorize("secretsmanager:ValidateResourcePolicy", res); err != nil {
		return nil, err
	}
	public, problems := checkPolicy(in.ResourcePolicy)
	errs := []map[string]string{}
	for _, p := range problems {
		errs = append(errs, map[string]string{"CheckName": "SYNTAX_CHECK", "ErrorMessage": p})
	}
	if public {
		errs = append(errs, map[string]string{"CheckName": "PUBLIC_ACCESS_CHECK", "ErrorMessage": "The policy grants public access to the secret."})
	}
	return map[string]any{"PolicyValidationPassed": len(errs) == 0, "ValidationErrors": errs}, nil
}

func (s *Service) awsRotateSecret(q *awsapi.Req) (any, error) {
	var in struct {
		SecretId, ClientRequestToken, RotationLambdaARN string
		RotationRules                                   *RotationRules
		RotateImmediately                               *bool
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	now := in.RotateImmediately == nil || *in.RotateImmediately
	sec, token, err := s.rotate(q.Authorize, in.SecretId, RotateInput{Token: in.ClientRequestToken, RotationLambdaARN: in.RotationLambdaARN,
		Rules: in.RotationRules, RotateImmediately: now})
	if err != nil {
		return nil, err
	}
	out := map[string]any{"ARN": sec.ARN, "Name": sec.Name}
	if token != "" {
		out["VersionId"] = token
	}
	return out, nil
}

func (s *Service) awsCancelRotateSecret(q *awsapi.Req) (any, error) {
	var in struct{ SecretId string }
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	sec, err := s.cancelRotation(q.Authorize, in.SecretId)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"ARN": sec.ARN, "Name": sec.Name}
	if i := sec.staged(stagePending); i >= 0 {
		out["VersionId"] = sec.Versions[i].ID
	}
	return out, nil
}

// Reference is a secret read through Parameter Store's
// /aws/reference/secretsmanager/<secret> parameters.
type Reference struct {
	Name, ARN, Value string
	Binary           bool
	VersionID        string
	Stages           []string
	CreatedAt        time.Time
	Source           string // DescribeSecret's response as JSON (SSM's SourceResult)
}

// Reference reads a secret's value for Parameter Store, authorizing
// secretsmanager:GetSecretValue (and kms:Decrypt for customer keys) through az.
func (s *Service) Reference(az func(action, resource string) error, ref, versionID, stage string) (Reference, error) {
	sec, v, p, err := s.getValue(az, ref, versionID, stage)
	if err != nil {
		return Reference{}, err
	}
	src, _ := json.Marshal(describeOut(sec, false))
	val := string(p)
	if v.Binary {
		val = base64.StdEncoding.EncodeToString(p)
	}
	return Reference{Name: sec.Name, ARN: sec.ARN, Value: val, Binary: v.Binary, VersionID: v.ID, Stages: v.Stages, CreatedAt: v.CreatedAt, Source: string(src)}, nil
}

// errNoReplication is returned for every replication request: HomeCloud
// serves a single region, so a replica has nowhere to live.
func errNoReplication() error {
	return awsapi.Errorf(http.StatusBadRequest, "InvalidRequestException",
		"HomeCloud serves a single region (%s); secret replication is not available.", core.Region)
}

func (s *Service) awsReplicateSecretToRegions(q *awsapi.Req) (any, error) {
	var in struct {
		SecretId                    string
		AddReplicaRegions           []any
		ForceOverwriteReplicaSecret bool
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if len(in.AddReplicaRegions) == 0 {
		return nil, awsapi.Errorf(http.StatusBadRequest, "InvalidParameterException", "AddReplicaRegions must contain at least one region.")
	}
	if _, err := s.lookup(q.Authorize, "secretsmanager:ReplicateSecretToRegions", in.SecretId); err != nil {
		return nil, err
	}
	return nil, errNoReplication()
}

func (s *Service) awsRemoveRegionsFromReplication(q *awsapi.Req) (any, error) {
	var in struct {
		SecretId             string
		RemoveReplicaRegions []string
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if len(in.RemoveReplicaRegions) == 0 {
		return nil, awsapi.Errorf(http.StatusBadRequest, "InvalidParameterException", "RemoveReplicaRegions must contain at least one region.")
	}
	sec, err := s.lookup(q.Authorize, "secretsmanager:RemoveRegionsFromReplication", in.SecretId)
	if err != nil {
		return nil, err
	}
	return nil, awsapi.Errorf(http.StatusBadRequest, "InvalidParameterException",
		"Secret %s is not replicated to %s.", sec.Name, strings.Join(in.RemoveReplicaRegions, ", "))
}

func (s *Service) awsStopReplicationToReplica(q *awsapi.Req) (any, error) {
	var in struct{ SecretId string }
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	sec, err := s.lookup(q.Authorize, "secretsmanager:StopReplicationToReplica", in.SecretId)
	if err != nil {
		return nil, err
	}
	return nil, awsapi.Errorf(http.StatusBadRequest, "InvalidRequestException", "Secret %s is not a replica secret.", sec.Name)
}
