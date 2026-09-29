package ecr

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

// The Amazon ECR API (awsJson 1.1): repositories, images, authorization
// tokens, lifecycle and repository policies.

type tag struct{ Key, Value string }

type imageID struct {
	ImageDigest string `json:"imageDigest,omitempty"`
	ImageTag    string `json:"imageTag,omitempty"`
}

// RegisterAWS serves ECR over the AWS protocol.
func (s *Service) RegisterAWS() {
	svc := &awsapi.Service{Name: "ecr", JSONPrefix: "AmazonEC2ContainerRegistry_V20150921", JSONVersion: "1.1", Ops: map[string]awsapi.Op{}}
	ops := map[string]awsapi.Op{
		"CreateRepository":              s.awsCreateRepository,
		"DescribeRepositories":          s.awsDescribeRepositories,
		"DeleteRepository":              s.awsDeleteRepository,
		"ListImages":                    s.awsListImages,
		"DescribeImages":                s.awsDescribeImages,
		"BatchDeleteImage":              s.awsBatchDeleteImage,
		"BatchGetImage":                 s.awsBatchGetImage,
		"GetAuthorizationToken":         s.awsGetAuthorizationToken,
		"PutLifecyclePolicy":            s.awsPutLifecyclePolicy,
		"GetLifecyclePolicy":            s.awsGetLifecyclePolicy,
		"DeleteLifecyclePolicy":         s.awsDeleteLifecyclePolicy,
		"SetRepositoryPolicy":           s.awsSetRepositoryPolicy,
		"GetRepositoryPolicy":           s.awsGetRepositoryPolicy,
		"DeleteRepositoryPolicy":        s.awsDeleteRepositoryPolicy,
		"PutImageTagMutability":         s.awsPutImageTagMutability,
		"PutImageScanningConfiguration": s.awsPutImageScanningConfiguration,
		"TagResource":                   s.awsTagResource,
		"UntagResource":                 s.awsUntagResource,
		"ListTagsForResource":           s.awsListTags,
		"DescribeRegistry":              s.awsDescribeRegistry,
	}
	for name, fn := range ops {
		svc.Ops[name] = func(q *awsapi.Req) (any, error) {
			out, err := fn(q)
			return out, ecrError(err)
		}
	}
	svc.ErrorCode = map[string]string{"ResourceNotFound": "RepositoryNotFoundException", "Conflict": "RepositoryAlreadyExistsException"}
	awsapi.Register(svc)
}

func apiErr(code, format string, a ...any) error {
	return awsapi.Errorf(http.StatusBadRequest, code, format, a...)
}

func ecrError(err error) error {
	var ce *core.Error
	if err == nil || !errors.As(err, &ce) {
		return err
	}
	switch ce.Code {
	case "BadRequest", "ValidationError":
		return awsapi.Errorf(http.StatusBadRequest, "InvalidParameterException", "%s", ce.Message)
	case "ServiceUnavailable":
		return awsapi.Errorf(http.StatusInternalServerError, "ServerException", "%s", ce.Message)
	}
	return &awsapi.Error{Status: http.StatusBadRequest, Code: ce.Code, Message: ce.Message}
}

func (s *Service) repo(name string) (Repository, error) {
	r, err := store.Get[Repository](s.env.Store, cRepos, name)
	if err != nil {
		return r, apiErr("RepositoryNotFoundException", "The repository with name '%s' does not exist in the registry with id '%s'", name, s.env.AccountID)
	}
	return r, nil
}

func (s *Service) repoARN(name string) string { return s.env.ARN("ecr", "repository/"+name) }

func (r Repository) view(account string) map[string]any {
	mut := "IMMUTABLE"
	if r.TagMutable {
		mut = "MUTABLE"
	}
	enc := r.Encryption
	if enc == "" {
		enc = "AES256"
	}
	e := map[string]any{"encryptionType": enc}
	if r.KMSKey != "" {
		e["kmsKey"] = r.KMSKey
	}
	return map[string]any{"repositoryArn": r.ARN, "registryId": account, "repositoryName": r.Name, "repositoryUri": r.URI,
		"createdAt": awsapi.T(r.CreatedAt), "imageTagMutability": mut,
		"imageScanningConfiguration": map[string]any{"scanOnPush": r.ScanOnPush}, "encryptionConfiguration": e}
}

func (s *Service) awsCreateRepository(q *awsapi.Req) (any, error) {
	var in struct {
		RepositoryName             string
		Tags                       []tag
		ImageTagMutability         string
		ImageScanningConfiguration struct{ ScanOnPush bool }
		EncryptionConfiguration    struct{ EncryptionType, KmsKey string }
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("ecr:CreateRepository", s.repoARN(in.RepositoryName)); err != nil {
		return nil, err
	}
	if in.ImageTagMutability != "" && in.ImageTagMutability != "MUTABLE" && in.ImageTagMutability != "IMMUTABLE" {
		return nil, apiErr("InvalidParameterException", "Invalid parameter at 'imageTagMutability' failed to satisfy constraint")
	}
	var tags core.Tags
	if len(in.Tags) > 0 {
		tags = core.Tags{}
		for _, t := range in.Tags {
			tags[t.Key] = t.Value
		}
	}
	r, err := s.createRepo(in.RepositoryName, in.ImageTagMutability != "IMMUTABLE", "", tags)
	if err != nil {
		return nil, err
	}
	r, err = store.Update(s.env.Store, cRepos, r.Name, func(x *Repository) error {
		x.ScanOnPush, x.Encryption, x.KMSKey = in.ImageScanningConfiguration.ScanOnPush, in.EncryptionConfiguration.EncryptionType, in.EncryptionConfiguration.KmsKey
		return nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"repository": r.view(s.env.AccountID)}, nil
}

func (s *Service) awsDescribeRepositories(q *awsapi.Req) (any, error) {
	var in struct{ RepositoryNames []string }
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("ecr:DescribeRepositories", s.repoARN("*")); err != nil {
		return nil, err
	}
	out := []map[string]any{}
	if len(in.RepositoryNames) > 0 {
		for _, n := range in.RepositoryNames {
			r, err := s.repo(n)
			if err != nil {
				return nil, err
			}
			out = append(out, r.view(s.env.AccountID))
		}
	} else {
		if s.ready() == nil {
			s.sync()
		}
		for _, r := range store.List[Repository](s.env.Store, cRepos) {
			out = append(out, r.view(s.env.AccountID))
		}
		sort.Slice(out, func(i, j int) bool { return out[i]["repositoryName"].(string) < out[j]["repositoryName"].(string) })
	}
	return map[string]any{"repositories": out}, nil
}

func (s *Service) awsDeleteRepository(q *awsapi.Req) (any, error) {
	var in struct {
		RepositoryName string
		Force          bool
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("ecr:DeleteRepository", s.repoARN(in.RepositoryName)); err != nil {
		return nil, err
	}
	r, err := s.repo(in.RepositoryName)
	if err != nil {
		return nil, err
	}
	if err := s.deleteRepo(in.RepositoryName, in.Force); err != nil {
		return nil, err
	}
	return map[string]any{"repository": r.view(s.env.AccountID)}, nil
}

// images lists the repository's images after authorizing action.
func (s *Service) authImages(q *awsapi.Req, action, name string) ([]imageInfo, error) {
	if err := q.Authorize(action, s.repoARN(name)); err != nil {
		return nil, err
	}
	if _, err := s.repo(name); err != nil {
		return nil, err
	}
	if err := s.ready(); err != nil {
		return nil, err
	}
	return s.images(name)
}

func (s *Service) awsListImages(q *awsapi.Req) (any, error) {
	var in struct {
		RepositoryName string
		Filter         struct{ TagStatus string }
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	imgs, err := s.authImages(q, "ecr:ListImages", in.RepositoryName)
	if err != nil {
		return nil, err
	}
	ids := []imageID{}
	for _, i := range imgs {
		if len(i.Tags) == 0 {
			if in.Filter.TagStatus != "TAGGED" {
				ids = append(ids, imageID{ImageDigest: i.Digest})
			}
			continue
		}
		if in.Filter.TagStatus == "UNTAGGED" {
			continue
		}
		for _, t := range i.Tags {
			ids = append(ids, imageID{ImageDigest: i.Digest, ImageTag: t})
		}
	}
	return map[string]any{"imageIds": ids}, nil
}

func (s *Service) awsDescribeImages(q *awsapi.Req) (any, error) {
	var in struct {
		RepositoryName string
		ImageIds       []imageID
		Filter         struct{ TagStatus string }
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	imgs, err := s.authImages(q, "ecr:DescribeImages", in.RepositoryName)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, i := range imgs {
		if in.Filter.TagStatus == "TAGGED" && len(i.Tags) == 0 || in.Filter.TagStatus == "UNTAGGED" && len(i.Tags) > 0 {
			continue
		}
		if len(in.ImageIds) > 0 {
			match := false
			for _, id := range in.ImageIds {
				if id.ImageDigest == i.Digest || id.ImageTag != "" && containsStr(i.Tags, id.ImageTag) {
					match = true
				}
			}
			if !match {
				continue
			}
		}
		d := map[string]any{"registryId": s.env.AccountID, "repositoryName": in.RepositoryName, "imageDigest": i.Digest,
			"imageSizeInBytes": i.SizeBytes, "imagePushedAt": awsapi.T(i.PushedAt), "imageManifestMediaType": i.MediaType}
		if len(i.Tags) > 0 {
			d["imageTags"] = i.Tags
		}
		out = append(out, d)
	}
	if len(in.ImageIds) > 0 && len(out) == 0 {
		return nil, apiErr("ImageNotFoundException", "The image requested does not exist in the specified repository")
	}
	return map[string]any{"imageDetails": out}, nil
}

func containsStr(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func (s *Service) awsBatchDeleteImage(q *awsapi.Req) (any, error) {
	var in struct {
		RepositoryName string
		ImageIds       []imageID
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("ecr:BatchDeleteImage", s.repoARN(in.RepositoryName)); err != nil {
		return nil, err
	}
	if _, err := s.repo(in.RepositoryName); err != nil {
		return nil, err
	}
	deleted, failures := []imageID{}, []map[string]any{}
	for _, id := range in.ImageIds {
		ref := id.ImageDigest
		if ref == "" {
			ref = id.ImageTag
		}
		digest, err := s.deleteRef(in.RepositoryName, ref)
		var ce *core.Error
		switch {
		case errors.As(err, &ce) && ce.Code == "ImageNotFoundException":
			failures = append(failures, map[string]any{"imageId": id, "failureCode": "ImageNotFound", "failureReason": "Requested image not found"})
		case err != nil:
			return nil, err
		default:
			deleted = append(deleted, imageID{ImageDigest: digest, ImageTag: id.ImageTag})
		}
	}
	return map[string]any{"imageIds": deleted, "failures": failures}, nil
}

func (s *Service) awsBatchGetImage(q *awsapi.Req) (any, error) {
	var in struct {
		RepositoryName string
		ImageIds       []imageID
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("ecr:BatchGetImage", s.repoARN(in.RepositoryName)); err != nil {
		return nil, err
	}
	if _, err := s.repo(in.RepositoryName); err != nil {
		return nil, err
	}
	if err := s.ready(); err != nil {
		return nil, err
	}
	images, failures := []map[string]any{}, []map[string]any{}
	for _, id := range in.ImageIds {
		ref := id.ImageDigest
		if ref == "" {
			ref = id.ImageTag
		}
		body, digest, mt, err := s.rawManifest(in.RepositoryName, ref)
		if err != nil {
			return nil, err
		}
		if body == nil {
			failures = append(failures, map[string]any{"imageId": id, "failureCode": "ImageNotFound", "failureReason": "Requested image not found"})
			continue
		}
		images = append(images, map[string]any{"registryId": s.env.AccountID, "repositoryName": in.RepositoryName,
			"imageId": imageID{ImageDigest: digest, ImageTag: id.ImageTag}, "imageManifest": string(body), "imageManifestMediaType": mt})
	}
	return map[string]any{"images": images, "failures": failures}, nil
}

// rawManifest fetches a manifest verbatim; body is nil when it does not exist.
func (s *Service) rawManifest(repo, ref string) (body []byte, digest, mediaType string, err error) {
	if !imageRefRe.MatchString(ref) {
		return nil, "", "", core.BadRequest("image must be a tag or a sha256: digest")
	}
	req, _ := http.NewRequest(http.MethodGet, s.base()+repo+"/manifests/"+ref, nil)
	req.Header.Set("Accept", manifestTypes)
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, "", "", nil
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode >= 300 {
		return nil, "", "", errors.New("registry: " + resp.Status)
	}
	return b, resp.Header.Get("Docker-Content-Digest"), strings.Split(resp.Header.Get("Content-Type"), ";")[0], nil
}

// The registry does not check credentials (it only listens on the host's
// loopback interface), so an authorization token is a short-lived opaque
// secret that `docker login` accepts.
func (s *Service) awsGetAuthorizationToken(q *awsapi.Req) (any, error) {
	if err := q.Authorize("ecr:GetAuthorizationToken", "*"); err != nil {
		return nil, err
	}
	exp := core.Now().Add(12 * time.Hour)
	tok := base64.StdEncoding.EncodeToString([]byte("AWS:" + core.RandHex(48)))
	return map[string]any{"authorizationData": []map[string]any{{
		"authorizationToken": tok, "expiresAt": awsapi.T(exp), "proxyEndpoint": "http://" + s.Host()}}}, nil
}

func validPolicyJSON(name, text string) error {
	var v map[string]any
	if json.Unmarshal([]byte(text), &v) != nil || v == nil {
		return apiErr("InvalidParameterException", "Invalid parameter at '%s' failed to satisfy constraint: must be valid JSON", name)
	}
	return nil
}

type policyIn struct {
	RepositoryName      string
	LifecyclePolicyText string
	PolicyText          string
}

func (s *Service) awsPutLifecyclePolicy(q *awsapi.Req) (any, error) {
	var in policyIn
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("ecr:PutLifecyclePolicy", s.repoARN(in.RepositoryName)); err != nil {
		return nil, err
	}
	if err := validPolicyJSON("lifecyclePolicyText", in.LifecyclePolicyText); err != nil {
		return nil, err
	}
	var doc struct{ Rules []struct{ RulePriority int } }
	if json.Unmarshal([]byte(in.LifecyclePolicyText), &doc) != nil || len(doc.Rules) == 0 {
		return nil, apiErr("InvalidParameterException", "Invalid parameter at 'lifecyclePolicyText' failed to satisfy constraint: 'Lifecycle policy validation failure: rules are required'")
	}
	if err := s.setField(in.RepositoryName, func(r *Repository) { r.Lifecycle = in.LifecyclePolicyText }); err != nil {
		return nil, err
	}
	return map[string]any{"registryId": s.env.AccountID, "repositoryName": in.RepositoryName, "lifecyclePolicyText": in.LifecyclePolicyText}, nil
}

func (s *Service) setField(name string, f func(*Repository)) error {
	_, err := store.Update(s.env.Store, cRepos, name, func(r *Repository) error { f(r); return nil })
	if err == store.ErrNotFound {
		return apiErr("RepositoryNotFoundException", "The repository with name '%s' does not exist in the registry with id '%s'", name, s.env.AccountID)
	}
	return err
}

func (s *Service) awsGetLifecyclePolicy(q *awsapi.Req) (any, error) {
	var in policyIn
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("ecr:GetLifecyclePolicy", s.repoARN(in.RepositoryName)); err != nil {
		return nil, err
	}
	r, err := s.repo(in.RepositoryName)
	if err != nil {
		return nil, err
	}
	if r.Lifecycle == "" {
		return nil, apiErr("LifecyclePolicyNotFoundException", "Lifecycle policy does not exist for the repository with name '%s' in the registry with id '%s'", r.Name, s.env.AccountID)
	}
	return map[string]any{"registryId": s.env.AccountID, "repositoryName": r.Name, "lifecyclePolicyText": r.Lifecycle, "lastEvaluatedAt": awsapi.T(r.CreatedAt)}, nil
}

func (s *Service) awsDeleteLifecyclePolicy(q *awsapi.Req) (any, error) {
	var in policyIn
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("ecr:DeleteLifecyclePolicy", s.repoARN(in.RepositoryName)); err != nil {
		return nil, err
	}
	r, err := s.repo(in.RepositoryName)
	if err != nil {
		return nil, err
	}
	if r.Lifecycle == "" {
		return nil, apiErr("LifecyclePolicyNotFoundException", "Lifecycle policy does not exist for the repository with name '%s' in the registry with id '%s'", r.Name, s.env.AccountID)
	}
	_ = s.setField(r.Name, func(x *Repository) { x.Lifecycle = "" })
	return map[string]any{"registryId": s.env.AccountID, "repositoryName": r.Name, "lifecyclePolicyText": r.Lifecycle, "lastEvaluatedAt": awsapi.T(r.CreatedAt)}, nil
}

func (s *Service) awsSetRepositoryPolicy(q *awsapi.Req) (any, error) {
	var in policyIn
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("ecr:SetRepositoryPolicy", s.repoARN(in.RepositoryName)); err != nil {
		return nil, err
	}
	if err := validPolicyJSON("policyText", in.PolicyText); err != nil {
		return nil, err
	}
	if err := s.setField(in.RepositoryName, func(r *Repository) { r.Policy = in.PolicyText }); err != nil {
		return nil, err
	}
	return map[string]any{"registryId": s.env.AccountID, "repositoryName": in.RepositoryName, "policyText": in.PolicyText}, nil
}

func (s *Service) awsGetRepositoryPolicy(q *awsapi.Req) (any, error) {
	var in policyIn
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("ecr:GetRepositoryPolicy", s.repoARN(in.RepositoryName)); err != nil {
		return nil, err
	}
	r, err := s.repo(in.RepositoryName)
	if err != nil {
		return nil, err
	}
	if r.Policy == "" {
		return nil, apiErr("RepositoryPolicyNotFoundException", "Repository policy does not exist for the repository with name '%s' in the registry with id '%s'", r.Name, s.env.AccountID)
	}
	return map[string]any{"registryId": s.env.AccountID, "repositoryName": r.Name, "policyText": r.Policy}, nil
}

func (s *Service) awsDeleteRepositoryPolicy(q *awsapi.Req) (any, error) {
	var in policyIn
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("ecr:DeleteRepositoryPolicy", s.repoARN(in.RepositoryName)); err != nil {
		return nil, err
	}
	r, err := s.repo(in.RepositoryName)
	if err != nil {
		return nil, err
	}
	if r.Policy == "" {
		return nil, apiErr("RepositoryPolicyNotFoundException", "Repository policy does not exist for the repository with name '%s' in the registry with id '%s'", r.Name, s.env.AccountID)
	}
	_ = s.setField(r.Name, func(x *Repository) { x.Policy = "" })
	return map[string]any{"registryId": s.env.AccountID, "repositoryName": r.Name, "policyText": r.Policy}, nil
}

func (s *Service) awsPutImageTagMutability(q *awsapi.Req) (any, error) {
	var in struct{ RepositoryName, ImageTagMutability string }
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("ecr:PutImageTagMutability", s.repoARN(in.RepositoryName)); err != nil {
		return nil, err
	}
	if in.ImageTagMutability != "MUTABLE" && in.ImageTagMutability != "IMMUTABLE" {
		return nil, apiErr("InvalidParameterException", "Invalid parameter at 'imageTagMutability' failed to satisfy constraint")
	}
	if err := s.setField(in.RepositoryName, func(r *Repository) { r.TagMutable = in.ImageTagMutability == "MUTABLE" }); err != nil {
		return nil, err
	}
	return map[string]any{"registryId": s.env.AccountID, "repositoryName": in.RepositoryName, "imageTagMutability": in.ImageTagMutability}, nil
}

func (s *Service) awsPutImageScanningConfiguration(q *awsapi.Req) (any, error) {
	var in struct {
		RepositoryName             string
		ImageScanningConfiguration struct{ ScanOnPush bool }
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("ecr:PutImageScanningConfiguration", s.repoARN(in.RepositoryName)); err != nil {
		return nil, err
	}
	if err := s.setField(in.RepositoryName, func(r *Repository) { r.ScanOnPush = in.ImageScanningConfiguration.ScanOnPush }); err != nil {
		return nil, err
	}
	return map[string]any{"registryId": s.env.AccountID, "repositoryName": in.RepositoryName,
		"imageScanningConfiguration": map[string]any{"scanOnPush": in.ImageScanningConfiguration.ScanOnPush}}, nil
}

// repoOfARN extracts the repository name of an ECR repository ARN.
func (s *Service) repoOfARN(arn string) (string, error) {
	_, name, ok := strings.Cut(arn, ":repository/")
	if !ok || !strings.HasPrefix(arn, "arn:") {
		return "", apiErr("InvalidParameterException", "Invalid parameter at 'resourceArn' failed to satisfy constraint: not a repository ARN")
	}
	return name, nil
}

func (s *Service) awsTagResource(q *awsapi.Req) (any, error) {
	var in struct {
		ResourceArn string
		Tags        []tag
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	name, err := s.repoOfARN(in.ResourceArn)
	if err != nil {
		return nil, err
	}
	if err := q.Authorize("ecr:TagResource", s.repoARN(name)); err != nil {
		return nil, err
	}
	if err := s.setField(name, func(r *Repository) {
		if r.Tags == nil {
			r.Tags = core.Tags{}
		}
		for _, t := range in.Tags {
			r.Tags[t.Key] = t.Value
		}
	}); err != nil {
		return nil, err
	}
	return map[string]any{}, nil
}

func (s *Service) awsUntagResource(q *awsapi.Req) (any, error) {
	var in struct {
		ResourceArn string
		TagKeys     []string
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	name, err := s.repoOfARN(in.ResourceArn)
	if err != nil {
		return nil, err
	}
	if err := q.Authorize("ecr:UntagResource", s.repoARN(name)); err != nil {
		return nil, err
	}
	if err := s.setField(name, func(r *Repository) {
		for _, k := range in.TagKeys {
			delete(r.Tags, k)
		}
	}); err != nil {
		return nil, err
	}
	return map[string]any{}, nil
}

func (s *Service) awsListTags(q *awsapi.Req) (any, error) {
	var in struct{ ResourceArn string }
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	name, err := s.repoOfARN(in.ResourceArn)
	if err != nil {
		return nil, err
	}
	if err := q.Authorize("ecr:ListTagsForResource", s.repoARN(name)); err != nil {
		return nil, err
	}
	r, err := s.repo(name)
	if err != nil {
		return nil, err
	}
	tags := []map[string]string{}
	for k, v := range r.Tags {
		tags = append(tags, map[string]string{"Key": k, "Value": v})
	}
	sort.Slice(tags, func(i, j int) bool { return tags[i]["Key"] < tags[j]["Key"] })
	return map[string]any{"tags": tags}, nil
}

func (s *Service) awsDescribeRegistry(q *awsapi.Req) (any, error) {
	if err := q.Authorize("ecr:DescribeRegistry", "*"); err != nil {
		return nil, err
	}
	return map[string]any{"registryId": s.env.AccountID, "replicationConfiguration": map[string]any{"rules": []any{}}}, nil
}
