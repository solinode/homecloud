package kms

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

// RegisterAWS serves KMS over the AWS JSON 1.1 protocol (TrentService).
func (s *Service) RegisterAWS() {
	awsapi.Register(&awsapi.Service{
		Name: "kms", JSONPrefix: "TrentService", JSONVersion: "1.1",
		ErrorCode: map[string]string{"ResourceNotFound": "NotFoundException", "ValidationError": "ValidationException",
			"BadRequest": "ValidationException", "Conflict": "KMSInternalException", "AccessDenied": "AccessDeniedException"},
		Ops: map[string]awsapi.Op{
			"CreateKey":                           s.awsCreateKey,
			"DescribeKey":                         s.awsDescribeKey,
			"ListKeys":                            s.awsListKeys,
			"EnableKey":                           s.awsEnableKey,
			"DisableKey":                          s.awsDisableKey,
			"UpdateKeyDescription":                s.awsUpdateKeyDescription,
			"ScheduleKeyDeletion":                 s.awsScheduleKeyDeletion,
			"CancelKeyDeletion":                   s.awsCancelKeyDeletion,
			"Encrypt":                             s.awsEncrypt,
			"Decrypt":                             s.awsDecrypt,
			"ReEncrypt":                           s.awsReEncrypt,
			"GenerateDataKey":                     s.awsGenerateDataKey(true),
			"GenerateDataKeyWithoutPlaintext":     s.awsGenerateDataKey(false),
			"GenerateDataKeyPair":                 s.awsGenerateDataKeyPair(true),
			"GenerateDataKeyPairWithoutPlaintext": s.awsGenerateDataKeyPair(false),
			"GenerateRandom":                      s.awsGenerateRandom,
			"Sign":                                s.awsSign,
			"Verify":                              s.awsVerify,
			"GetPublicKey":                        s.awsGetPublicKey,
			"GenerateMac":                         s.awsGenerateMac,
			"VerifyMac":                           s.awsVerifyMac,
			"CreateAlias":                         s.awsCreateAlias,
			"UpdateAlias":                         s.awsUpdateAlias,
			"DeleteAlias":                         s.awsDeleteAlias,
			"ListAliases":                         s.awsListAliases,
			"EnableKeyRotation":                   s.awsEnableKeyRotation,
			"DisableKeyRotation":                  s.awsDisableKeyRotation,
			"GetKeyRotationStatus":                s.awsGetKeyRotationStatus,
			"RotateKeyOnDemand":                   s.awsRotateKeyOnDemand,
			"ListKeyRotations":                    s.awsListKeyRotations,
			"GetKeyPolicy":                        s.awsGetKeyPolicy,
			"PutKeyPolicy":                        s.awsPutKeyPolicy,
			"ListKeyPolicies":                     s.awsListKeyPolicies,
			"TagResource":                         s.awsTagResource,
			"UntagResource":                       s.awsUntagResource,
			"ListResourceTags":                    s.awsListResourceTags,
			"CreateGrant":                         s.awsCreateGrant,
			"ListGrants":                          s.awsListGrants,
			"RevokeGrant":                         s.awsRevokeGrant,
			"RetireGrant":                         s.awsRetireGrant,
		},
	})
}

func metadata(k Key) map[string]any {
	m := map[string]any{"AWSAccountId": strings.Split(k.ARN, ":")[4], "KeyId": k.ID, "Arn": k.ARN, "CreationDate": epochT(k.CreatedAt),
		"Enabled": k.State == stateEnabled, "Description": k.Description, "KeyUsage": k.KeyUsage, "KeyState": k.State,
		"Origin": "AWS_KMS", "KeyManager": "CUSTOMER", "CustomerMasterKeySpec": k.KeySpec, "KeySpec": k.KeySpec, "MultiRegion": false}
	if k.KeySpec == "" {
		m["KeySpec"], m["CustomerMasterKeySpec"] = specSym, specSym
	}
	if k.Managed {
		m["KeyManager"] = "AWS"
	}
	if k.DeletionDate != nil {
		m["DeletionDate"] = epochT(*k.DeletionDate)
		m["PendingDeletionWindowInDays"] = k.PendingWindow
	}
	if a := encryptionAlgorithms(k); a != nil {
		m["EncryptionAlgorithms"] = a
	}
	if a := signingAlgorithms(k); a != nil {
		m["SigningAlgorithms"] = a
	}
	if a := macAlgorithms(k); a != nil {
		m["MacAlgorithms"] = a
	}
	return m
}

type kmsTag struct {
	TagKey   string `json:"TagKey"`
	TagValue string `json:"TagValue"`
}

func tagMap(list []kmsTag) core.Tags {
	if list == nil {
		return nil
	}
	t := core.Tags{}
	for _, x := range list {
		t[x.TagKey] = x.TagValue
	}
	return t
}

func tagList(t core.Tags) []kmsTag {
	out := make([]kmsTag, 0, len(t))
	for k, v := range t {
		out = append(out, kmsTag{k, v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TagKey < out[j].TagKey })
	return out
}

// page returns one page of items with KMS's Marker / NextMarker / Truncated.
func page[T any](items []T, marker string, limit int) ([]T, map[string]any, error) {
	if limit == 0 {
		limit = 100
	}
	if limit < 1 || limit > 1000 {
		return nil, nil, awsapi.Errorf(http.StatusBadRequest, "ValidationException", "Limit must be 1-1000")
	}
	off := 0
	if marker != "" {
		b, err := base64.RawURLEncoding.DecodeString(marker)
		n, err2 := strconv.Atoi(strings.TrimPrefix(string(b), "o:"))
		if err != nil || err2 != nil || !strings.HasPrefix(string(b), "o:") || n < 0 || n > len(items) {
			return nil, nil, awsapi.Errorf(http.StatusBadRequest, "InvalidMarkerException", "The marker is invalid.")
		}
		off = n
	}
	end := off + limit
	if end >= len(items) {
		return items[off:], map[string]any{"Truncated": false}, nil
	}
	return items[off:end], map[string]any{"Truncated": true, "NextMarker": base64.RawURLEncoding.EncodeToString([]byte("o:" + strconv.Itoa(end)))}, nil
}

func dryRun(q *awsapi.Req, dry bool) error {
	if dry {
		return awsapi.Errorf(http.StatusPreconditionFailed, "DryRunOperationException", "The request would have succeeded, but the DryRun option is set.")
	}
	return nil
}

type keyIn struct {
	KeyId  string
	DryRun bool
}

func (s *Service) awsCreateKey(q *awsapi.Req) (any, error) {
	var in struct {
		Description, KeyUsage, KeySpec, CustomerMasterKeySpec, Policy, Origin string
		Tags                                                                  []kmsTag
		MultiRegion, BypassPolicyLockoutSafetyCheck                           bool
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if in.KeySpec == "" {
		in.KeySpec = in.CustomerMasterKeySpec
	}
	k, err := s.createCustomerKey(q.Authorize, CreateKeyInput{Description: in.Description, KeySpec: in.KeySpec, KeyUsage: in.KeyUsage,
		Policy: in.Policy, Origin: in.Origin, MultiRegion: in.MultiRegion, Tags: tagMap(in.Tags)})
	if err != nil {
		return nil, err
	}
	return map[string]any{"KeyMetadata": metadata(k)}, nil
}

func (s *Service) awsDescribeKey(q *awsapi.Req) (any, error) {
	var in keyIn
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	k, err := s.keyFor(q.Authorize, "kms:DescribeKey", in.KeyId)
	if err != nil {
		return nil, err
	}
	return map[string]any{"KeyMetadata": metadata(k)}, nil
}

func (s *Service) awsListKeys(q *awsapi.Req) (any, error) {
	var in struct {
		Limit  int
		Marker string
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("kms:ListKeys", "*"); err != nil {
		return nil, err
	}
	keys := store.List[Key](s.env.Store, cKeys)
	sort.SliceStable(keys, func(i, j int) bool { return keys[i].CreatedAt.Before(keys[j].CreatedAt) })
	list, out, err := page(keys, in.Marker, in.Limit)
	if err != nil {
		return nil, err
	}
	items := []any{}
	for _, k := range list {
		items = append(items, map[string]string{"KeyId": k.ID, "KeyArn": core.CanonicalARN(k.ARN)})
	}
	out["Keys"] = items
	return out, nil
}

func (s *Service) awsEnableKey(q *awsapi.Req) (any, error) {
	var in keyIn
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	_, err := s.setEnabled(q.Authorize, in.KeyId, true)
	return nil, err
}

func (s *Service) awsDisableKey(q *awsapi.Req) (any, error) {
	var in keyIn
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	_, err := s.setEnabled(q.Authorize, in.KeyId, false)
	return nil, err
}

func (s *Service) awsUpdateKeyDescription(q *awsapi.Req) (any, error) {
	var in struct{ KeyId, Description string }
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	_, err := s.updateDescription(q.Authorize, in.KeyId, in.Description)
	return nil, err
}

func (s *Service) awsScheduleKeyDeletion(q *awsapi.Req) (any, error) {
	var in struct {
		KeyId               string
		PendingWindowInDays int
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	k, err := s.scheduleDeletion(q.Authorize, in.KeyId, in.PendingWindowInDays)
	if err != nil {
		return nil, err
	}
	return map[string]any{"KeyId": k.ARN, "DeletionDate": epochT(*k.DeletionDate), "KeyState": k.State, "PendingWindowInDays": k.PendingWindow}, nil
}

func (s *Service) awsCancelKeyDeletion(q *awsapi.Req) (any, error) {
	var in keyIn
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	k, err := s.cancelDeletion(q.Authorize, in.KeyId)
	if err != nil {
		return nil, err
	}
	return map[string]any{"KeyId": k.ARN}, nil
}

func (s *Service) awsEncrypt(q *awsapi.Req) (any, error) {
	var in struct {
		KeyId, EncryptionAlgorithm string
		Plaintext                  []byte
		EncryptionContext          map[string]string
		GrantTokens                []string
		DryRun                     bool
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	k, err := s.keyFor(q.Authorize, "kms:Encrypt", in.KeyId)
	if err != nil {
		return nil, err
	}
	if err := checkPlaintext(in.Plaintext); err != nil {
		return nil, err
	}
	if err := dryRun(q, in.DryRun); err != nil {
		return nil, err
	}
	blob, alg, err := s.encrypt(k, in.Plaintext, in.EncryptionContext, in.EncryptionAlgorithm)
	if err != nil {
		return nil, err
	}
	return map[string]any{"CiphertextBlob": blob, "KeyId": k.ARN, "EncryptionAlgorithm": alg}, nil
}

func (s *Service) awsDecrypt(q *awsapi.Req) (any, error) {
	var in struct {
		KeyId, EncryptionAlgorithm string
		CiphertextBlob             []byte
		EncryptionContext          map[string]string
		GrantTokens                []string
		DryRun                     bool
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if len(in.CiphertextBlob) == 0 {
		return nil, awsapi.Errorf(http.StatusBadRequest, "ValidationException", "CiphertextBlob is required")
	}
	if in.DryRun {
		if _, err := s.keyFor(q.Authorize, "kms:Decrypt", orBlobKey(in.KeyId, in.CiphertextBlob)); err != nil {
			return nil, err
		}
		return nil, dryRun(q, true)
	}
	pt, k, alg, err := s.decrypt(q.Authorize, "kms:Decrypt", in.CiphertextBlob, in.EncryptionContext, in.KeyId, in.EncryptionAlgorithm)
	if err != nil {
		return nil, err
	}
	return map[string]any{"Plaintext": pt, "KeyId": k.ARN, "EncryptionAlgorithm": alg}, nil
}

func orBlobKey(ref string, blob []byte) string {
	if ref != "" {
		return ref
	}
	return blobKey(blob)
}

func (s *Service) awsReEncrypt(q *awsapi.Req) (any, error) {
	var in struct {
		CiphertextBlob                                                                           []byte
		SourceEncryptionContext, DestinationEncryptionContext                                    map[string]string
		SourceKeyId, DestinationKeyId, SourceEncryptionAlgorithm, DestinationEncryptionAlgorithm string
		DryRun                                                                                   bool
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	pt, src, salg, err := s.decrypt(q.Authorize, "kms:ReEncryptFrom", in.CiphertextBlob, in.SourceEncryptionContext, in.SourceKeyId, in.SourceEncryptionAlgorithm)
	if err != nil {
		return nil, err
	}
	dst, err := s.keyFor(q.Authorize, "kms:ReEncryptTo", in.DestinationKeyId)
	if err != nil {
		return nil, err
	}
	if err := dryRun(q, in.DryRun); err != nil {
		return nil, err
	}
	blob, dalg, err := s.encrypt(dst, pt, in.DestinationEncryptionContext, in.DestinationEncryptionAlgorithm)
	if err != nil {
		return nil, err
	}
	return map[string]any{"CiphertextBlob": blob, "SourceKeyId": src.ARN, "KeyId": dst.ARN,
		"SourceEncryptionAlgorithm": salg, "DestinationEncryptionAlgorithm": dalg}, nil
}

func (s *Service) awsGenerateDataKey(plain bool) awsapi.Op {
	action := "kms:GenerateDataKey"
	if !plain {
		action = "kms:GenerateDataKeyWithoutPlaintext"
	}
	return func(q *awsapi.Req) (any, error) {
		var in struct {
			KeyId, KeySpec    string
			NumberOfBytes     int
			EncryptionContext map[string]string
			GrantTokens       []string
			DryRun            bool
		}
		if err := q.Bind(&in); err != nil {
			return nil, err
		}
		if in.DryRun {
			if _, err := s.keyFor(q.Authorize, action, in.KeyId); err != nil {
				return nil, err
			}
			return nil, dryRun(q, true)
		}
		dk, blob, k, err := s.dataKey(q.Authorize, action, in.KeyId, in.NumberOfBytes, in.KeySpec, in.EncryptionContext)
		if err != nil {
			return nil, err
		}
		out := map[string]any{"CiphertextBlob": blob, "KeyId": k.ARN}
		if plain {
			out["Plaintext"] = dk
		}
		return out, nil
	}
}

func (s *Service) awsGenerateDataKeyPair(plain bool) awsapi.Op {
	action := "kms:GenerateDataKeyPair"
	if !plain {
		action = "kms:GenerateDataKeyPairWithoutPlaintext"
	}
	return func(q *awsapi.Req) (any, error) {
		var in struct {
			KeyId, KeyPairSpec string
			EncryptionContext  map[string]string
			DryRun             bool
		}
		if err := q.Bind(&in); err != nil {
			return nil, err
		}
		if in.DryRun {
			if _, err := s.keyFor(q.Authorize, action, in.KeyId); err != nil {
				return nil, err
			}
			return nil, dryRun(q, true)
		}
		priv, pub, blob, k, err := s.dataKeyPair(q.Authorize, action, in.KeyId, in.KeyPairSpec, in.EncryptionContext)
		if err != nil {
			return nil, err
		}
		out := map[string]any{"PrivateKeyCiphertextBlob": blob, "PublicKey": pub, "KeyId": k.ARN, "KeyPairSpec": in.KeyPairSpec}
		if plain {
			out["PrivateKeyPlaintext"] = priv
		}
		return out, nil
	}
}

func (s *Service) awsGenerateRandom(q *awsapi.Req) (any, error) {
	var in struct{ NumberOfBytes int }
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("kms:GenerateRandom", "*"); err != nil {
		return nil, err
	}
	if in.NumberOfBytes < 1 || in.NumberOfBytes > 1024 {
		return nil, awsapi.Errorf(http.StatusBadRequest, "ValidationException", "NumberOfBytes must be 1-1024")
	}
	b := make([]byte, in.NumberOfBytes)
	_, _ = rand.Read(b)
	return map[string]any{"Plaintext": b}, nil
}

type signIn struct {
	KeyId, MessageType, SigningAlgorithm string
	Message, Signature                   []byte
	DryRun                               bool
}

func (s *Service) awsSign(q *awsapi.Req) (any, error) {
	var in signIn
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	sig, k, err := s.sign(q.Authorize, in.KeyId, in.Message, in.MessageType, in.SigningAlgorithm)
	if err != nil {
		return nil, err
	}
	if err := dryRun(q, in.DryRun); err != nil {
		return nil, err
	}
	return map[string]any{"KeyId": k.ARN, "Signature": sig, "SigningAlgorithm": in.SigningAlgorithm}, nil
}

func (s *Service) awsVerify(q *awsapi.Req) (any, error) {
	var in signIn
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	k, err := s.verify(q.Authorize, in.KeyId, in.Message, in.MessageType, in.SigningAlgorithm, in.Signature)
	if err != nil {
		return nil, err
	}
	if err := dryRun(q, in.DryRun); err != nil {
		return nil, err
	}
	return map[string]any{"KeyId": k.ARN, "SignatureValid": true, "SigningAlgorithm": in.SigningAlgorithm}, nil
}

func (s *Service) awsGetPublicKey(q *awsapi.Req) (any, error) {
	var in keyIn
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	k, err := s.keyFor(q.Authorize, "kms:GetPublicKey", in.KeyId)
	if err != nil {
		return nil, err
	}
	if !k.rsa() && !k.ecc() {
		return nil, awsapi.Errorf(http.StatusBadRequest, "UnsupportedOperationException", "%s is not an asymmetric key", k.ARN)
	}
	if err := usable(k); err != nil {
		return nil, err
	}
	pub, err := s.publicKeyDER(k)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"KeyId": k.ARN, "PublicKey": pub, "CustomerMasterKeySpec": k.KeySpec, "KeySpec": k.KeySpec, "KeyUsage": k.KeyUsage}
	if a := encryptionAlgorithms(k); a != nil {
		out["EncryptionAlgorithms"] = a
	}
	if a := signingAlgorithms(k); a != nil {
		out["SigningAlgorithms"] = a
	}
	return out, nil
}

type macIn struct {
	KeyId, MacAlgorithm string
	Message, Mac        []byte
	DryRun              bool
}

func (s *Service) awsGenerateMac(q *awsapi.Req) (any, error) {
	var in macIn
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	mac, k, err := s.mac(q.Authorize, "kms:GenerateMac", in.KeyId, in.Message, in.MacAlgorithm)
	if err != nil {
		return nil, err
	}
	if err := dryRun(q, in.DryRun); err != nil {
		return nil, err
	}
	return map[string]any{"Mac": mac, "MacAlgorithm": in.MacAlgorithm, "KeyId": k.ARN}, nil
}

func (s *Service) awsVerifyMac(q *awsapi.Req) (any, error) {
	var in macIn
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	k, err := s.verifyMac(q.Authorize, in.KeyId, in.Message, in.MacAlgorithm, in.Mac)
	if err != nil {
		return nil, err
	}
	if err := dryRun(q, in.DryRun); err != nil {
		return nil, err
	}
	return map[string]any{"KeyId": k.ARN, "MacValid": true, "MacAlgorithm": in.MacAlgorithm}, nil
}

func (s *Service) awsCreateAlias(q *awsapi.Req) (any, error) {
	var in struct{ AliasName, TargetKeyId string }
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	_, err := s.createAlias(q.Authorize, in.AliasName, in.TargetKeyId)
	return nil, err
}

func (s *Service) awsUpdateAlias(q *awsapi.Req) (any, error) {
	var in struct{ AliasName, TargetKeyId string }
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	_, err := s.updateAlias(q.Authorize, in.AliasName, in.TargetKeyId)
	return nil, err
}

func (s *Service) awsDeleteAlias(q *awsapi.Req) (any, error) {
	var in struct{ AliasName string }
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	return nil, s.deleteAlias(q.Authorize, in.AliasName)
}

func (s *Service) awsListAliases(q *awsapi.Req) (any, error) {
	var in struct {
		KeyId, Marker string
		Limit         int
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	keyID := ""
	if in.KeyId != "" {
		k, err := s.keyFor(q.Authorize, "kms:ListAliases", in.KeyId)
		if err != nil {
			return nil, err
		}
		keyID = k.ID
	} else if err := q.Authorize("kms:ListAliases", "*"); err != nil {
		return nil, err
	}
	all := []alias{}
	for _, a := range store.List[alias](s.env.Store, cAliases) {
		if keyID == "" || a.KeyID == keyID {
			all = append(all, a)
		}
	}
	list, out, err := page(all, in.Marker, in.Limit)
	if err != nil {
		return nil, err
	}
	items := []any{}
	for _, a := range list {
		m := map[string]any{"AliasName": a.Name, "AliasArn": s.aliasARN(a.Name), "TargetKeyId": a.KeyID}
		if !a.CreatedAt.IsZero() {
			m["CreationDate"], m["LastUpdatedDate"] = epochT(a.CreatedAt), epochT(a.UpdatedAt)
		}
		items = append(items, m)
	}
	out["Aliases"] = items
	return out, nil
}

func (s *Service) awsEnableKeyRotation(q *awsapi.Req) (any, error) {
	var in struct {
		KeyId                string
		RotationPeriodInDays int
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	_, err := s.setRotation(q.Authorize, in.KeyId, true, in.RotationPeriodInDays)
	return nil, err
}

func (s *Service) awsDisableKeyRotation(q *awsapi.Req) (any, error) {
	var in keyIn
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	_, err := s.setRotation(q.Authorize, in.KeyId, false, 0)
	return nil, err
}

func (s *Service) awsGetKeyRotationStatus(q *awsapi.Req) (any, error) {
	var in keyIn
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	k, err := s.keyFor(q.Authorize, "kms:GetKeyRotationStatus", in.KeyId)
	if err != nil {
		return nil, err
	}
	if !k.symmetric() {
		return nil, awsapi.Errorf(http.StatusBadRequest, "UnsupportedOperationException", "%s does not support rotation", k.ARN)
	}
	out := map[string]any{"KeyId": k.ARN, "KeyRotationEnabled": k.RotationEnabled}
	if k.RotationEnabled {
		out["RotationPeriodInDays"] = k.period()
		if k.NextRotation != nil {
			out["NextRotationDate"] = epochT(*k.NextRotation)
		}
	}
	return out, nil
}

func (s *Service) awsRotateKeyOnDemand(q *awsapi.Req) (any, error) {
	var in keyIn
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	k, err := s.rotateOnDemand(q.Authorize, in.KeyId)
	if err != nil {
		return nil, err
	}
	return map[string]any{"KeyId": k.ARN}, nil
}

func (s *Service) awsListKeyRotations(q *awsapi.Req) (any, error) {
	var in struct {
		KeyId, Marker string
		Limit         int
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	k, err := s.keyFor(q.Authorize, "kms:ListKeyRotations", in.KeyId)
	if err != nil {
		return nil, err
	}
	if !k.symmetric() {
		return nil, awsapi.Errorf(http.StatusBadRequest, "UnsupportedOperationException", "%s does not support rotation", k.ARN)
	}
	var rot []keyVersion
	for _, v := range k.Versions {
		if v.Version > 1 {
			rot = append(rot, v)
		}
	}
	list, out, err := page(rot, in.Marker, in.Limit)
	if err != nil {
		return nil, err
	}
	items := []any{}
	for _, v := range list {
		t := v.Rotation
		if t == "" {
			t = "AUTOMATIC"
		}
		items = append(items, map[string]any{"KeyId": k.ARN, "RotationDate": epochT(v.CreatedAt), "RotationType": t})
	}
	out["Rotations"] = items
	return out, nil
}

func (s *Service) awsGetKeyPolicy(q *awsapi.Req) (any, error) {
	var in struct{ KeyId, PolicyName string }
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	k, err := s.keyFor(q.Authorize, "kms:GetKeyPolicy", in.KeyId)
	if err != nil {
		return nil, err
	}
	if err := checkPolicyName(in.PolicyName); err != nil {
		return nil, err
	}
	return map[string]any{"Policy": s.policyOf(k), "PolicyName": "default"}, nil
}

func (s *Service) awsPutKeyPolicy(q *awsapi.Req) (any, error) {
	var in struct {
		KeyId, PolicyName, Policy      string
		BypassPolicyLockoutSafetyCheck bool
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	return nil, s.putPolicy(q.Authorize, in.KeyId, in.PolicyName, in.Policy)
}

func (s *Service) awsListKeyPolicies(q *awsapi.Req) (any, error) {
	var in keyIn
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if _, err := s.keyFor(q.Authorize, "kms:ListKeyPolicies", in.KeyId); err != nil {
		return nil, err
	}
	return map[string]any{"PolicyNames": []string{"default"}, "Truncated": false}, nil
}

func (s *Service) awsTagResource(q *awsapi.Req) (any, error) {
	var in struct {
		KeyId string
		Tags  []kmsTag
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	add := tagMap(in.Tags)
	if add == nil {
		add = core.Tags{}
	}
	_, err := s.tag(q.Authorize, in.KeyId, add, nil)
	return nil, err
}

func (s *Service) awsUntagResource(q *awsapi.Req) (any, error) {
	var in struct {
		KeyId   string
		TagKeys []string
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	_, err := s.tag(q.Authorize, in.KeyId, nil, in.TagKeys)
	return nil, err
}

func (s *Service) awsListResourceTags(q *awsapi.Req) (any, error) {
	var in struct {
		KeyId, Marker string
		Limit         int
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	k, err := s.keyFor(q.Authorize, "kms:ListResourceTags", in.KeyId)
	if err != nil {
		return nil, err
	}
	if in.Limit > 50 {
		in.Limit = 50
	}
	list, out, err := page(tagList(k.Tags), in.Marker, in.Limit)
	if err != nil {
		return nil, err
	}
	out["Tags"] = list
	return out, nil
}

func (s *Service) awsCreateGrant(q *awsapi.Req) (any, error) {
	var in struct {
		KeyId, GranteePrincipal, RetiringPrincipal, Name string
		Operations                                       []string
		Constraints                                      json.RawMessage
		DryRun                                           bool
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if in.DryRun {
		if _, err := s.keyFor(q.Authorize, "kms:CreateGrant", in.KeyId); err != nil {
			return nil, err
		}
		return nil, dryRun(q, true)
	}
	g, err := s.createGrant(q.Authorize, in.KeyId, Grant{Name: in.Name, GranteePrincipal: in.GranteePrincipal,
		RetiringPrincipal: in.RetiringPrincipal, Operations: in.Operations, Constraints: in.Constraints})
	if err != nil {
		return nil, err
	}
	return map[string]any{"GrantId": g.ID, "GrantToken": g.Token}, nil
}

func (s *Service) awsListGrants(q *awsapi.Req) (any, error) {
	var in struct {
		KeyId, Marker, GrantId, GranteePrincipal string
		Limit                                    int
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	k, err := s.keyFor(q.Authorize, "kms:ListGrants", in.KeyId)
	if err != nil {
		return nil, err
	}
	var gs []Grant
	for _, g := range k.Grants {
		if (in.GrantId == "" || g.ID == in.GrantId) && (in.GranteePrincipal == "" || g.GranteePrincipal == in.GranteePrincipal) {
			gs = append(gs, g)
		}
	}
	list, out, err := page(gs, in.Marker, in.Limit)
	if err != nil {
		return nil, err
	}
	items := []any{}
	for _, g := range list {
		items = append(items, grantOut(k, g))
	}
	out["Grants"] = items
	return out, nil
}

func (s *Service) awsRevokeGrant(q *awsapi.Req) (any, error) {
	var in struct {
		KeyId, GrantId string
		DryRun         bool
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if in.GrantId == "" {
		return nil, awsapi.Errorf(http.StatusBadRequest, "ValidationException", "GrantId is required")
	}
	return nil, s.removeGrant(q.Authorize, "kms:RevokeGrant", in.KeyId, in.GrantId, "")
}

func (s *Service) awsRetireGrant(q *awsapi.Req) (any, error) {
	var in struct {
		KeyId, GrantId, GrantToken string
		DryRun                     bool
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if in.GrantToken == "" && (in.KeyId == "" || in.GrantId == "") {
		return nil, awsapi.Errorf(http.StatusBadRequest, "ValidationException", "specify GrantToken, or KeyId and GrantId")
	}
	return nil, s.removeGrant(q.Authorize, "kms:RetireGrant", in.KeyId, in.GrantId, in.GrantToken)
}
