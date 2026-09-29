package secrets

import (
	"bytes"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

// Operations shared by the native API and the AWS protocol. Each authorizes
// through az before acting.

// CreateInput describes a new secret.
type CreateInput struct {
	Name, Description string
	KMSKeyID          string
	Value             []byte
	HasValue, Binary  bool
	Token             string // ClientRequestToken: the first version's ID
	Tags              core.Tags
}

func validValue(v []byte) error {
	if len(v) > maxValue {
		return core.BadRequest("the secret value must be at most %d bytes", maxValue)
	}
	return nil
}

func validToken(t string) error {
	if t != "" && (len(t) < 32 || len(t) > 64) {
		return core.BadRequest("ClientRequestToken must be 32-64 characters")
	}
	return nil
}

func validTags(t core.Tags) error {
	if len(t) > 50 {
		return core.BadRequest("a secret can have at most 50 tags")
	}
	for k := range t {
		if k == "" || len(k) > 128 || strings.HasPrefix(strings.ToLower(k), "aws:") {
			return core.BadRequest("tag key %q is invalid (1-128 characters, not starting with aws:)", k)
		}
	}
	return nil
}

// keyARN resolves a KmsKeyId for a secret; "" (or the default alias) is the default key.
func (s *Service) keyARN(ref string) (string, error) {
	if ref == "" || ref == "alias/aws/secretsmanager" {
		return "", nil
	}
	if s.KMS == nil {
		return "", core.BadRequest("customer KMS keys are not available")
	}
	arn, err := s.KMS.KeyARN(ref)
	if err != nil {
		return "", core.BadRequest("the KMS key %q does not exist: %s", ref, errMessage(err))
	}
	return arn, nil
}

func (s *Service) create(az Authz, in CreateInput) (Secret, *Version, error) {
	if err := validName(in.Name); err != nil {
		return Secret{}, nil, err
	}
	if err := validValue(in.Value); err != nil {
		return Secret{}, nil, err
	}
	if err := validToken(in.Token); err != nil {
		return Secret{}, nil, err
	}
	if err := validTags(in.Tags); err != nil {
		return Secret{}, nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur, err := s.get(in.Name); err == nil {
		if err := az("secretsmanager:CreateSecret", cur.ARN); err != nil {
			return cur, nil, err
		}
		if cur.DeletionDate != nil {
			return cur, nil, core.Errf(http.StatusBadRequest, "InvalidRequestException",
				"You can't create this secret because a secret with this name is already scheduled for deletion.")
		}
		// A retried request (same token and value) succeeds without a change.
		if i := cur.version(in.Token); in.Token != "" && in.HasValue && i >= 0 {
			if old, err := s.open(cur, cur.Versions[i]); err == nil && bytes.Equal(old, in.Value) {
				v := cur.Versions[i]
				return cur, &v, nil
			}
		}
		return cur, nil, core.Errf(http.StatusConflict, "ResourceExistsException", "The operation failed because the secret %s already exists.", in.Name)
	}
	sec := Secret{Name: in.Name, ARN: s.newARN(in.Name), Description: in.Description, Tags: in.Tags, CreatedAt: s.now(), UpdatedAt: s.now()}
	if err := az("secretsmanager:CreateSecret", sec.ARN); err != nil {
		return sec, nil, err
	}
	key, err := s.keyARN(in.KMSKeyID)
	if err != nil {
		return sec, nil, err
	}
	sec.KMSKeyID = key
	var ver *Version
	if in.HasValue {
		if key != "" {
			if err := az("kms:GenerateDataKey", key); err != nil {
				return sec, nil, err
			}
		}
		id := in.Token
		if id == "" {
			id = uuid()
		}
		v := Version{ID: id, Stages: []string{stageCurrent}, Binary: in.Binary, CreatedAt: s.now()}
		if err := s.seal(sec, &v, in.Value); err != nil {
			return sec, nil, err
		}
		sec.Versions = []Version{v}
		ver = &v
	}
	return sec, ver, store.Put(s.env.Store, cSecrets, sec.Name, sec)
}

func cloneVersions(vs []Version) []Version {
	out := make([]Version, len(vs))
	for i, v := range vs {
		v.Stages = slices.Clone(v.Stages)
		out[i] = v
	}
	return out
}

// moveStages attaches stages to vs[idx], removing them from other versions.
// Moving AWSCURRENT gives its previous holder AWSPREVIOUS, as in AWS.
func moveStages(vs []Version, idx int, stages []string) {
	for _, st := range stages {
		for j := range vs {
			if j == idx || !slices.Contains(vs[j].Stages, st) {
				continue
			}
			vs[j].Stages = slices.DeleteFunc(vs[j].Stages, func(x string) bool { return x == st })
			if st == stageCurrent {
				for k := range vs {
					vs[k].Stages = slices.DeleteFunc(vs[k].Stages, func(x string) bool { return x == stagePrev })
				}
				vs[j].Stages = append(vs[j].Stages, stagePrev)
			}
		}
		if !slices.Contains(vs[idx].Stages, st) {
			vs[idx].Stages = append(vs[idx].Stages, st)
		}
	}
}

// prune drops the oldest versions without a staging label beyond maxVersions.
func prune(vs []Version) []Version {
	for i := len(vs) - 1; len(vs) > maxVersions && i >= 0; i-- {
		if len(vs[i].Stages) == 0 {
			vs = slices.Delete(vs, i, i+1)
		}
	}
	return vs
}

// addVersion stores value as a new version of the secret named name. token
// is the version ID (ClientRequestToken): repeating a request with the same
// token and value changes nothing, and it may fill the placeholder version a
// rotation created. stages defaults to AWSCURRENT. check validates the secret.
func (s *Service) addVersion(name string, value []byte, binary bool, token string, stages []string, check func(Secret) error) (Secret, Version, error) {
	if token == "" {
		token = uuid()
	}
	if len(stages) == 0 {
		stages = []string{stageCurrent}
	}
	if len(stages) > 20 {
		return Secret{}, Version{}, core.BadRequest("at most 20 staging labels")
	}
	sec, err := s.get(name)
	if err != nil {
		return sec, Version{}, err
	}
	if err := check(sec); err != nil {
		return sec, Version{}, err
	}
	exists := core.Errf(http.StatusConflict, "ResourceExistsException", "You can't modify an existing version, you can only create a new version.")
	if i := sec.version(token); i >= 0 && sec.Versions[i].Ciphertext != "" {
		old, err := s.open(sec, sec.Versions[i])
		if err == nil && bytes.Equal(old, value) && sec.Versions[i].Binary == binary {
			return sec, sec.Versions[i], nil
		}
		return sec, Version{}, exists
	}
	v := Version{ID: token, Binary: binary, CreatedAt: s.now()}
	if err := s.seal(sec, &v, value); err != nil {
		return sec, v, err
	}
	sec, err = store.Update(s.env.Store, cSecrets, name, func(x *Secret) error {
		if err := check(*x); err != nil {
			return err
		}
		if x.KMSKeyID != sec.KMSKeyID {
			return store.ErrConflict
		}
		vs := cloneVersions(x.Versions)
		nv := v
		if i := x.version(token); i >= 0 {
			if vs[i].Ciphertext != "" {
				return exists
			}
			nv.Stages, nv.CreatedAt = vs[i].Stages, vs[i].CreatedAt // fill the rotation placeholder
			vs = slices.Delete(vs, i, i+1)
		}
		vs = append([]Version{nv}, vs...)
		moveStages(vs, 0, stages)
		x.Versions = prune(vs)
		x.UpdatedAt = s.now()
		return nil
	})
	if err != nil {
		return sec, v, err
	}
	v = sec.Versions[sec.version(token)]
	return sec, v, nil
}

// pick selects a version by ID and/or staging label (AWSCURRENT by default).
func pick(sec Secret, versionID, stage string) (int, error) {
	if versionID == "" && stage == "" {
		stage = stageCurrent
	}
	if versionID != "" {
		i := sec.version(versionID)
		if i < 0 {
			return i, core.Errf(http.StatusNotFound, "ResourceNotFound", "Secrets Manager can't find the specified secret value for VersionId: %s", versionID)
		}
		if stage != "" && !slices.Contains(sec.Versions[i].Stages, stage) {
			return -1, core.Errf(http.StatusNotFound, "ResourceNotFound", "Secrets Manager can't find the specified secret value for VersionId: %s and staging label: %s", versionID, stage)
		}
		return i, nil
	}
	i := sec.staged(stage)
	if i < 0 {
		return i, core.Errf(http.StatusNotFound, "ResourceNotFound", "Secrets Manager can't find the specified secret value for staging label: %s", stage)
	}
	return i, nil
}

// value decrypts a version and records the access (by day, as AWS does).
func (s *Service) value(sec Secret, versionID, stage string) (Secret, Version, []byte, error) {
	if sec.DeletionDate != nil {
		return sec, Version{}, nil, deletedErr(sec, "")
	}
	i, err := pick(sec, versionID, stage)
	if err != nil {
		return sec, Version{}, nil, err
	}
	v := sec.Versions[i]
	p, err := s.open(sec, v)
	if err != nil {
		return sec, v, nil, err
	}
	day := s.now().Truncate(24 * time.Hour)
	if v.LastAccessed == nil || !v.LastAccessed.Equal(day) {
		_, _ = store.Update(s.env.Store, cSecrets, sec.Name, func(x *Secret) error {
			x.LastAccessed = &day
			if j := x.version(v.ID); j >= 0 {
				x.Versions = cloneVersions(x.Versions)
				x.Versions[j].LastAccessed = &day
			}
			return nil
		})
	}
	v.Ciphertext = ""
	return sec, v, p, nil
}

func (s *Service) getValue(az Authz, ref, versionID, stage string) (Secret, Version, []byte, error) {
	sec, err := s.lookup(az, "secretsmanager:GetSecretValue", ref)
	if err != nil {
		return sec, Version{}, nil, err
	}
	if err := readCheck(az, sec); err != nil {
		return sec, Version{}, nil, err
	}
	if i, err := pick(sec, versionID, stage); err == nil && sec.Versions[i].KMSKey != "" {
		if err := az("kms:Decrypt", sec.Versions[i].KMSKey); err != nil {
			return sec, Version{}, nil, err
		}
	}
	return s.value(sec, versionID, stage)
}

func writable(sec Secret) error {
	if err := managed(sec); err != nil {
		return err
	}
	if sec.DeletionDate != nil {
		return deletedErr(sec, "")
	}
	return nil
}

func (s *Service) putValue(az Authz, ref string, value []byte, binary bool, token string, stages []string) (Secret, Version, error) {
	if err := validValue(value); err != nil {
		return Secret{}, Version{}, err
	}
	if err := validToken(token); err != nil {
		return Secret{}, Version{}, err
	}
	sec, err := s.lookup(az, "secretsmanager:PutSecretValue", ref)
	if err != nil {
		return sec, Version{}, err
	}
	if err := writable(sec); err != nil {
		return sec, Version{}, err
	}
	if sec.KMSKeyID != "" {
		if err := az("kms:GenerateDataKey", sec.KMSKeyID); err != nil {
			return sec, Version{}, err
		}
	}
	return s.addVersion(sec.Name, value, binary, token, stages, writable)
}

// UpdateInput changes a secret's metadata and optionally adds a new value.
type UpdateInput struct {
	Description      *string
	KMSKeyID         *string
	Value            []byte
	HasValue, Binary bool
	Token            string
}

func (s *Service) update(az Authz, ref string, in UpdateInput) (Secret, *Version, error) {
	if err := validValue(in.Value); err != nil {
		return Secret{}, nil, err
	}
	if err := validToken(in.Token); err != nil {
		return Secret{}, nil, err
	}
	sec, err := s.lookup(az, "secretsmanager:UpdateSecret", ref)
	if err != nil {
		return sec, nil, err
	}
	if err := writable(sec); err != nil {
		return sec, nil, err
	}
	newKey := sec.KMSKeyID
	if in.KMSKeyID != nil {
		if newKey, err = s.keyARN(*in.KMSKeyID); err != nil {
			return sec, nil, err
		}
	}
	if newKey != sec.KMSKeyID {
		// Re-encrypt the existing versions under the new key.
		for _, v := range sec.Versions {
			if v.KMSKey != "" {
				if err := az("kms:Decrypt", v.KMSKey); err != nil {
					return sec, nil, err
				}
			}
		}
		if newKey != "" {
			if err := az("kms:GenerateDataKey", newKey); err != nil {
				return sec, nil, err
			}
		}
	}
	if in.Description != nil || newKey != sec.KMSKeyID {
		sec, err = store.Update(s.env.Store, cSecrets, sec.Name, func(x *Secret) error {
			if err := writable(*x); err != nil {
				return err
			}
			if in.Description != nil {
				x.Description = *in.Description
			}
			if newKey != x.KMSKeyID {
				old := *x
				old.ARN = core.CanonicalARN(old.ARN)
				x.KMSKeyID = newKey
				x.Versions = cloneVersions(x.Versions)
				for i, v := range x.Versions {
					if v.Ciphertext == "" {
						continue
					}
					p, err := s.open(old, v)
					if err != nil {
						return err
					}
					if err := s.seal(Secret{ARN: old.ARN, KMSKeyID: newKey}, &x.Versions[i], p); err != nil {
						return err
					}
				}
			}
			x.UpdatedAt = s.now()
			return nil
		})
		if err != nil {
			return sec, nil, err
		}
		sec.ARN = core.CanonicalARN(sec.ARN)
	}
	if !in.HasValue {
		return sec, nil, nil
	}
	sec, v, err := s.addVersion(sec.Name, in.Value, in.Binary, in.Token, nil, writable)
	return sec, &v, err
}

func (s *Service) describe(az Authz, ref string) (Secret, error) {
	return s.lookup(az, "secretsmanager:DescribeSecret", ref)
}

// remove schedules deletion after days (minDays-30, default 30) or deletes now.
func (s *Service) remove(az Authz, ref string, days *int64, force bool, minDays int64) (Secret, error) {
	sec, err := s.lookup(az, "secretsmanager:DeleteSecret", ref)
	if err != nil {
		return sec, err
	}
	if err := managed(sec); err != nil {
		return sec, err
	}
	if days != nil && force {
		return sec, core.BadRequest("You can't use ForceDeleteWithoutRecovery in conjunction with RecoveryWindowInDays.")
	}
	now := s.now()
	if force {
		sec.DeletionDate = &now
		return sec, store.Delete(s.env.Store, cSecrets, sec.Name)
	}
	if sec.DeletionDate != nil {
		return sec, core.Errf(http.StatusBadRequest, "InvalidRequestException", "You can't delete secret %s that is already scheduled for deletion.", sec.Name)
	}
	n := int64(30)
	if days != nil {
		n = *days
	}
	if n < minDays || n > 30 {
		return sec, core.BadRequest("RecoveryWindowInDays must be between %d and 30 days", minDays)
	}
	return store.Update(s.env.Store, cSecrets, sec.Name, func(x *Secret) error {
		d := now.Add(time.Duration(n) * 24 * time.Hour)
		x.DeletedAt, x.DeletionDate = &now, &d
		x.ARN = core.CanonicalARN(x.ARN)
		return nil
	})
}

func (s *Service) restore(az Authz, ref string) (Secret, error) {
	sec, err := s.lookup(az, "secretsmanager:RestoreSecret", ref)
	if err != nil {
		return sec, err
	}
	return store.Update(s.env.Store, cSecrets, sec.Name, func(x *Secret) error {
		x.DeletedAt, x.DeletionDate = nil, nil
		x.ARN = core.CanonicalARN(x.ARN)
		return nil
	})
}

// updateStage moves (or removes) a staging label between versions.
func (s *Service) updateStage(az Authz, ref, stage, removeFrom, moveTo string) (Secret, error) {
	sec, err := s.lookup(az, "secretsmanager:UpdateSecretVersionStage", ref)
	if err != nil {
		return sec, err
	}
	if err := writable(sec); err != nil {
		return sec, err
	}
	if stage == "" || len(stage) > 256 {
		return sec, core.BadRequest("VersionStage is required")
	}
	return store.Update(s.env.Store, cSecrets, sec.Name, func(x *Secret) error {
		vs := cloneVersions(x.Versions)
		holder := x.staged(stage)
		if removeFrom != "" {
			i := x.version(removeFrom)
			if i < 0 || !slices.Contains(vs[i].Stages, stage) {
				return core.BadRequest("The parameter RemoveFromVersionId can't be %s: the staging label %s is not attached to it.", removeFrom, stage)
			}
		} else if holder >= 0 && moveTo != "" && vs[holder].ID != moveTo {
			return core.BadRequest("The staging label %s is currently attached to version %s, but you have not specified it as RemoveFromVersionId.", stage, vs[holder].ID)
		}
		if moveTo == "" {
			if stage == stageCurrent {
				return core.BadRequest("You can't remove AWSCURRENT from a version without moving it to another version.")
			}
			i := x.version(removeFrom)
			vs[i].Stages = slices.DeleteFunc(vs[i].Stages, func(v string) bool { return v == stage })
		} else {
			i := x.version(moveTo)
			if i < 0 || vs[i].Ciphertext == "" && stage == stageCurrent {
				return core.BadRequest("The parameter MoveToVersionId can't be %s: the version does not exist or has no value.", moveTo)
			}
			moveStages(vs, i, []string{stage})
		}
		x.Versions = prune(vs)
		x.UpdatedAt = s.now()
		x.ARN = core.CanonicalARN(x.ARN)
		return nil
	})
}

func (s *Service) tag(az Authz, ref string, add core.Tags, remove []string) (Secret, error) {
	action := "secretsmanager:TagResource"
	if add == nil {
		action = "secretsmanager:UntagResource"
	}
	sec, err := s.lookup(az, action, ref)
	if err != nil {
		return sec, err
	}
	return store.Update(s.env.Store, cSecrets, sec.Name, func(x *Secret) error {
		tags := core.Tags{}
		for k, v := range x.Tags {
			tags[k] = v
		}
		for k, v := range add {
			tags[k] = v
		}
		for _, k := range remove {
			delete(tags, k)
		}
		if err := validTags(tags); err != nil {
			return err
		}
		x.Tags = tags
		x.ARN = core.CanonicalARN(x.ARN)
		return nil
	})
}

// ---- resource policies ----

type policyDoc struct {
	Version   string            `json:"Version"`
	Statement []json.RawMessage `json:"Statement"`
}

// checkPolicy validates a resource policy and reports whether it grants access to everyone.
func checkPolicy(doc string) (public bool, problems []string) {
	var p policyDoc
	if err := json.Unmarshal([]byte(doc), &p); err != nil {
		return false, []string{"the policy is not valid JSON: " + err.Error()}
	}
	if p.Version != "2012-10-17" && p.Version != "2008-10-17" {
		problems = append(problems, `Version must be "2012-10-17"`)
	}
	if len(p.Statement) == 0 {
		var one json.RawMessage
		var raw map[string]json.RawMessage
		if json.Unmarshal([]byte(doc), &raw) == nil && json.Unmarshal(raw["Statement"], &one) == nil && len(one) > 0 && one[0] == '{' {
			p.Statement = []json.RawMessage{one}
		} else {
			problems = append(problems, "the policy must have at least one Statement")
		}
	}
	for _, st := range p.Statement {
		var x struct {
			Effect    string          `json:"Effect"`
			Principal json.RawMessage `json:"Principal"`
			Action    json.RawMessage `json:"Action"`
			Condition json.RawMessage `json:"Condition"`
		}
		if err := json.Unmarshal(st, &x); err != nil {
			problems = append(problems, "a statement is not an object")
			continue
		}
		if x.Effect != "Allow" && x.Effect != "Deny" {
			problems = append(problems, `Effect must be "Allow" or "Deny"`)
		}
		if len(x.Principal) == 0 {
			problems = append(problems, "resource policy statements need a Principal")
		}
		if len(x.Action) == 0 {
			problems = append(problems, "statements need an Action")
		}
		pr := strings.ReplaceAll(string(x.Principal), " ", "")
		if x.Effect == "Allow" && len(x.Condition) == 0 && (pr == `"*"` || strings.Contains(pr, `"AWS":"*"`) || strings.Contains(pr, `"AWS":["*"`)) {
			public = true
		}
	}
	return public, problems
}

func (s *Service) putPolicy(az Authz, ref, doc string, blockPublic bool) (Secret, error) {
	sec, err := s.lookup(az, "secretsmanager:PutResourcePolicy", ref)
	if err != nil {
		return sec, err
	}
	if len(doc) == 0 || len(doc) > 20480 {
		return sec, core.BadRequest("ResourcePolicy must be 1-20480 characters")
	}
	public, problems := checkPolicy(doc)
	if len(problems) > 0 {
		return sec, core.Errf(http.StatusBadRequest, "MalformedPolicyDocumentException", "%s", strings.Join(problems, "; "))
	}
	if public && blockPublic {
		return sec, core.Errf(http.StatusBadRequest, "PublicPolicyException", "You can't add the resource policy because it grants public access to the secret.")
	}
	return store.Update(s.env.Store, cSecrets, sec.Name, func(x *Secret) error {
		x.Policy = doc
		x.ARN = core.CanonicalARN(x.ARN)
		return nil
	})
}
