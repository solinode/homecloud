package lambda

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

// RegisterAWS serves the AWS Lambda REST API (restJson1, signing name "lambda").
func (s *Service) RegisterAWS() {
	s.registerAPIGateway()
	awsapi.Register(&awsapi.Service{
		Name: "lambda",
		REST: s.serveAWS,
		ErrorCode: map[string]string{
			"ResourceNotFound":   "ResourceNotFoundException",
			"ResourceConflict":   "ResourceConflictException",
			"Conflict":           "ResourceConflictException",
			"AlreadyExists":      "ResourceConflictException",
			"ValidationError":    "InvalidParameterValueException",
			"BadRequest":         "InvalidParameterValueException",
			"TooManyRequests":    "TooManyRequestsException",
			"PreconditionFailed": "PreconditionFailedException",
			"AccessDenied":       "AccessDeniedException",
			"InternalError":      "ServiceException",
		},
	})
}

type awsHandler func(q *awsapi.Req, p map[string]string) error

type awsRoute struct {
	method, op string
	path       []string
	h          awsHandler
}

func (s *Service) awsRoutes() []awsRoute {
	r := func(method, pattern, op string, h awsHandler) awsRoute {
		return awsRoute{method: method, op: op, path: strings.Split(strings.Trim(pattern, "/"), "/"), h: h}
	}
	return []awsRoute{
		r("POST", "/2015-03-31/functions", "CreateFunction", s.awsCreateFunction),
		r("GET", "/2015-03-31/functions", "ListFunctions", s.awsListFunctions),
		r("GET", "/2015-03-31/functions/{fn}", "GetFunction", s.awsGetFunction),
		r("DELETE", "/2015-03-31/functions/{fn}", "DeleteFunction", s.awsDeleteFunction),
		r("GET", "/2015-03-31/functions/{fn}/configuration", "GetFunctionConfiguration", s.awsGetConfiguration),
		r("PUT", "/2015-03-31/functions/{fn}/configuration", "UpdateFunctionConfiguration", s.awsUpdateConfiguration),
		r("PUT", "/2015-03-31/functions/{fn}/code", "UpdateFunctionCode", s.awsUpdateCode),
		r("POST", "/2015-03-31/functions/{fn}/invocations", "Invoke", s.awsInvoke),
		r("POST", "/2014-11-13/functions/{fn}/invoke-async", "InvokeAsync", s.awsInvokeAsync),
		r("POST", "/2015-03-31/functions/{fn}/versions", "PublishVersion", s.awsPublishVersion),
		r("GET", "/2015-03-31/functions/{fn}/versions", "ListVersionsByFunction", s.awsListVersions),
		r("POST", "/2015-03-31/functions/{fn}/aliases", "CreateAlias", s.awsCreateAlias),
		r("GET", "/2015-03-31/functions/{fn}/aliases", "ListAliases", s.awsListAliases),
		r("GET", "/2015-03-31/functions/{fn}/aliases/{alias}", "GetAlias", s.awsGetAlias),
		r("PUT", "/2015-03-31/functions/{fn}/aliases/{alias}", "UpdateAlias", s.awsUpdateAlias),
		r("DELETE", "/2015-03-31/functions/{fn}/aliases/{alias}", "DeleteAlias", s.awsDeleteAlias),
		r("POST", "/2015-03-31/functions/{fn}/policy", "AddPermission", s.awsAddPermission),
		r("GET", "/2015-03-31/functions/{fn}/policy", "GetPolicy", s.awsGetPolicy),
		r("DELETE", "/2015-03-31/functions/{fn}/policy/{sid}", "RemovePermission", s.awsRemovePermission),
		r("PUT", "/2017-10-31/functions/{fn}/concurrency", "PutFunctionConcurrency", s.awsPutConcurrency),
		r("GET", "/2019-09-30/functions/{fn}/concurrency", "GetFunctionConcurrency", s.awsGetConcurrency),
		r("DELETE", "/2017-10-31/functions/{fn}/concurrency", "DeleteFunctionConcurrency", s.awsDeleteConcurrency),
		r("PUT", "/2019-09-25/functions/{fn}/event-invoke-config", "PutFunctionEventInvokeConfig", s.awsPutInvokeConfig(false)),
		r("POST", "/2019-09-25/functions/{fn}/event-invoke-config", "UpdateFunctionEventInvokeConfig", s.awsPutInvokeConfig(true)),
		r("GET", "/2019-09-25/functions/{fn}/event-invoke-config", "GetFunctionEventInvokeConfig", s.awsGetInvokeConfig),
		r("DELETE", "/2019-09-25/functions/{fn}/event-invoke-config", "DeleteFunctionEventInvokeConfig", s.awsDeleteInvokeConfig),
		r("GET", "/2019-09-25/functions/{fn}/event-invoke-config/list", "ListFunctionEventInvokeConfigs", s.awsListInvokeConfigs),
		r("POST", "/2015-03-31/event-source-mappings", "CreateEventSourceMapping", s.awsCreateMapping),
		r("GET", "/2015-03-31/event-source-mappings", "ListEventSourceMappings", s.awsListMappings),
		r("GET", "/2015-03-31/event-source-mappings/{id}", "GetEventSourceMapping", s.awsGetMapping),
		r("PUT", "/2015-03-31/event-source-mappings/{id}", "UpdateEventSourceMapping", s.awsUpdateMapping),
		r("DELETE", "/2015-03-31/event-source-mappings/{id}", "DeleteEventSourceMapping", s.awsDeleteMapping),
		r("POST", "/2021-10-31/functions/{fn}/url", "CreateFunctionUrlConfig", s.awsPutURL(false)),
		r("PUT", "/2021-10-31/functions/{fn}/url", "UpdateFunctionUrlConfig", s.awsPutURL(true)),
		r("GET", "/2021-10-31/functions/{fn}/url", "GetFunctionUrlConfig", s.awsGetURL),
		r("DELETE", "/2021-10-31/functions/{fn}/url", "DeleteFunctionUrlConfig", s.awsDeleteURL),
		r("GET", "/2021-10-31/functions/{fn}/urls", "ListFunctionUrlConfigs", s.awsListURLs),
		r("POST", "/2017-03-31/tags/{arn}", "TagResource", s.awsTag),
		r("DELETE", "/2017-03-31/tags/{arn}", "UntagResource", s.awsUntag),
		r("GET", "/2017-03-31/tags/{arn}", "ListTags", s.awsListTags),
		r("GET", "/2016-08-19/account-settings", "GetAccountSettings", s.awsAccountSettings),
		r("POST", "/2018-10-31/layers/{layer}/versions", "PublishLayerVersion", s.awsPublishLayer),
		r("GET", "/2018-10-31/layers/{layer}/versions", "ListLayerVersions", s.awsListLayerVersions),
		r("GET", "/2018-10-31/layers/{layer}/versions/{ver}", "GetLayerVersion", s.awsGetLayerVersion),
		r("DELETE", "/2018-10-31/layers/{layer}/versions/{ver}", "DeleteLayerVersion", s.awsDeleteLayerVersion),
		r("GET", "/2018-10-31/layers", "ListLayers", s.awsListLayers),
		// Read by Terraform's aws_lambda_function; HomeCloud has no code signing,
		// runtime pinning, recursion detection or provisioned concurrency.
		r("GET", "/2020-06-30/functions/{fn}/code-signing-config", "GetFunctionCodeSigningConfig", s.awsCodeSigning),
		r("GET", "/2021-07-20/functions/{fn}/runtime-management-config", "GetRuntimeManagementConfig", s.awsRuntimeManagement),
		r("GET", "/2024-08-31/functions/{fn}/recursion-config", "GetFunctionRecursionConfig", s.awsRecursion),
		r("GET", "/2019-09-30/functions/{fn}/provisioned-concurrency", "ListProvisionedConcurrencyConfigs", s.awsProvisioned),
	}
}

// matchRoute matches a request path against a route pattern.
func matchRoute(pattern, segs []string) (map[string]string, bool) {
	if len(pattern) != len(segs) {
		return nil, false
	}
	p := map[string]string{}
	for i, t := range pattern {
		if strings.HasPrefix(t, "{") {
			if segs[i] == "" {
				return nil, false
			}
			p[t[1:len(t)-1]] = segs[i]
			continue
		}
		if t != segs[i] {
			return nil, false
		}
	}
	return p, true
}

func (s *Service) serveAWS(q *awsapi.Req) {
	path := q.R.URL.Path
	if strings.HasPrefix(path, "/lambda-url/") {
		s.awsServeURL(q)
		return
	}
	segs := strings.Split(strings.Trim(path, "/"), "/")
	methodOK := false
	for _, rt := range s.awsRoutes() {
		p, ok := matchRoute(rt.path, segs)
		if !ok {
			continue
		}
		methodOK = true
		if rt.method != q.R.Method {
			continue
		}
		q.Op = rt.op
		if err := rt.h(q, p); err != nil {
			q.Fail(err)
		}
		return
	}
	if methodOK {
		q.Fail(awsapi.Errorf(http.StatusMethodNotAllowed, "MethodNotAllowedException", "method %s is not allowed on %s", q.R.Method, path))
		return
	}
	q.Fail(awsapi.Errorf(http.StatusNotFound, "UnknownOperationException", "HomeCloud does not implement the Lambda operation %s %s yet", q.R.Method, path))
}

// ---- helpers ----

// target splits a path function reference and the Qualifier query parameter.
func target(q *awsapi.Req, p map[string]string) (string, string, error) {
	name, qual := parseRef(p["fn"])
	if qp := q.R.URL.Query().Get("Qualifier"); qp != "" {
		if qual != "" && qual != qp {
			return "", "", core.BadRequest("The derived qualifier from the function name does not match the specified qualifier.")
		}
		qual = qp
	}
	return name, qual, nil
}

func (s *Service) bind(q *awsapi.Req, v any) error {
	if len(strings.TrimSpace(string(q.Body))) == 0 {
		return nil
	}
	if err := json.Unmarshal(q.Body, v); err != nil {
		return awsapi.Errorf(http.StatusBadRequest, "InvalidRequestContentException", "Could not parse request body into json: %v", err)
	}
	return nil
}

func awsTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000-0700") }

// paginate applies Marker/MaxItems to n items; markers are item offsets.
func paginate(q *awsapi.Req, n int) (int, int, string) {
	start, _ := strconv.Atoi(q.R.URL.Query().Get("Marker"))
	limit, err := strconv.Atoi(q.R.URL.Query().Get("MaxItems"))
	if err != nil || limit <= 0 || limit > 10000 {
		limit = 50
	}
	start = min(max(start, 0), n)
	end := min(start+limit, n)
	next := ""
	if end < n {
		next = strconv.Itoa(end)
	}
	return start, end, next
}

func withMarker(out map[string]any, next string) map[string]any {
	if next != "" {
		out["NextMarker"] = next
	}
	return out
}

// functionConfig renders a function version as AWS's FunctionConfiguration.
func (s *Service) functionConfig(f Function) map[string]any {
	arn := f.ARN
	if f.version() != latest {
		arn = f.ARN + ":" + f.Version
	}
	c := map[string]any{
		"FunctionName": f.Name, "FunctionArn": arn, "Role": f.Role, "CodeSize": f.CodeSize, "Description": f.Description,
		"Timeout": f.TimeoutSec, "MemorySize": f.MemoryMB, "LastModified": awsTime(f.LastModified), "CodeSha256": f.CodeSHA256,
		"Version": f.version(), "RevisionId": f.RevisionID, "TracingConfig": map[string]string{"Mode": "PassThrough"},
		"State": f.State, "LastUpdateStatus": f.LastUpdateStatus, "PackageType": f.PackageType,
		"Architectures": f.Architectures, "EphemeralStorage": map[string]int{"Size": 512},
		"SnapStart":     map[string]string{"ApplyOn": "None", "OptimizationStatus": "Off"},
		"LoggingConfig": map[string]string{"LogFormat": "Text", "LogGroup": f.LogGroup},
	}
	if c["PackageType"] == "" {
		c["PackageType"] = "Zip"
	}
	if len(f.Architectures) == 0 {
		c["Architectures"] = []string{hostArch()}
	}
	if c["LastUpdateStatus"] == "" {
		c["LastUpdateStatus"] = "Successful"
	}
	if !f.isImage() {
		c["Runtime"], c["Handler"] = f.Runtime, f.Handler
	}
	if f.StateReason != "" {
		c["StateReason"], c["StateReasonCode"] = f.StateReason, f.StateReasonCode
	}
	if f.LastUpdateStatusReason != "" {
		c["LastUpdateStatusReason"] = f.LastUpdateStatusReason
	}
	if len(f.Environment) > 0 {
		c["Environment"] = map[string]any{"Variables": f.Environment}
	}
	if f.SubnetID != "" {
		vpcID := ""
		if s.vpc != nil {
			vpcID, _ = s.vpc.SubnetVPC(f.SubnetID)
		}
		sgs := f.SecurityGroupIDs
		if sgs == nil {
			sgs = []string{}
		}
		c["VpcConfig"] = map[string]any{"SubnetIds": []string{f.SubnetID}, "SecurityGroupIds": sgs, "VpcId": vpcID}
	}
	if f.DeadLetterTarget != "" {
		c["DeadLetterConfig"] = map[string]string{"TargetArn": f.DeadLetterTarget}
	}
	if f.ImageConfig != nil {
		c["ImageConfigResponse"] = map[string]any{"ImageConfig": f.ImageConfig}
	}
	if len(f.Layers) > 0 {
		ls := []map[string]any{}
		for _, l := range f.Layers {
			size := int64(0)
			if lv, err := s.layerByARN(l); err == nil {
				size = lv.CodeSize
			}
			ls = append(ls, map[string]any{"Arn": l, "CodeSize": size})
		}
		c["Layers"] = ls
	}
	return c
}

// awsConfig converts AWS configuration fields to the shared input.
type awsConfigIn struct {
	Role        *string `json:"Role"`
	Handler     string  `json:"Handler"`
	Description *string `json:"Description"`
	Timeout     int     `json:"Timeout"`
	MemorySize  int64   `json:"MemorySize"`
	Runtime     string  `json:"Runtime"`
	VpcConfig   *struct {
		SubnetIds        []string `json:"SubnetIds"`
		SecurityGroupIds []string `json:"SecurityGroupIds"`
	} `json:"VpcConfig"`
	Environment *struct {
		Variables map[string]string `json:"Variables"`
	} `json:"Environment"`
	DeadLetterConfig *struct {
		TargetArn string `json:"TargetArn"`
	} `json:"DeadLetterConfig"`
	Layers        *[]string    `json:"Layers"`
	ImageConfig   *ImageConfig `json:"ImageConfig"`
	Architectures []string     `json:"Architectures"`
	RevisionId    string       `json:"RevisionId"`
}

func (a awsConfigIn) config() configInput {
	in := configInput{Runtime: a.Runtime, Handler: a.Handler, Description: a.Description, MemoryMB: a.MemorySize,
		TimeoutSec: a.Timeout, Role: a.Role, Layers: a.Layers, ImageConfig: a.ImageConfig, Architectures: a.Architectures}
	if a.VpcConfig != nil {
		sn := ""
		if len(a.VpcConfig.SubnetIds) > 0 {
			sn = a.VpcConfig.SubnetIds[0]
		}
		in.SubnetID, in.SecurityGroupIDs = &sn, a.VpcConfig.SecurityGroupIds
		if in.SecurityGroupIDs == nil {
			in.SecurityGroupIDs = []string{}
		}
	}
	if a.Environment != nil {
		in.Environment = a.Environment.Variables
		if in.Environment == nil {
			in.Environment = map[string]string{}
		}
	}
	if a.DeadLetterConfig != nil {
		in.DeadLetterTarget = &a.DeadLetterConfig.TargetArn
	}
	return in
}

// s3Code fetches a code package from S3 for the caller.
func (s *Service) s3Code(q *awsapi.Req, bucket, key, version string) ([]byte, error) {
	if bucket == "" || key == "" {
		return nil, core.BadRequest("S3Bucket and S3Key are both required")
	}
	if err := q.Authorize("s3:GetObject", "arn:aws:s3:::"+bucket+"/"+key); err != nil {
		return nil, err
	}
	if s.GetObject == nil {
		return nil, core.BadRequest("S3 is not available")
	}
	b, err := s.GetObject(q.R.Context(), bucket, key, version)
	if err != nil {
		return nil, core.BadRequest("Error occurred while GetObject. S3 Error Code: NoSuchKey. S3 Error Message: %v", err)
	}
	if len(b) > maxCodeBytes {
		return nil, core.BadRequest("Unzipped size must be smaller than %d bytes", maxCodeBytes)
	}
	return b, checkZip(b)
}

// ---- functions ----

// authorizeImage requires the caller to be allowed to pull an image of
// HomeCloud's own registry: the function runs it, and its code can read it.
func (s *Service) authorizeImage(q *awsapi.Req, image string) error {
	repo, ok := core.LocalImageRepo(image, s.env.Cfg.ECRPort)
	if !ok {
		return nil
	}
	arn := s.env.ARN("ecr", "repository/"+repo)
	for _, action := range []string{"ecr:BatchGetImage", "ecr:GetDownloadUrlForLayer"} {
		if err := q.Check(action, arn); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) awsCreateFunction(q *awsapi.Req, _ map[string]string) error {
	var in struct {
		awsConfigIn
		FunctionName string `json:"FunctionName"`
		Code         struct {
			ZipFile         []byte `json:"ZipFile"`
			S3Bucket        string `json:"S3Bucket"`
			S3Key           string `json:"S3Key"`
			S3ObjectVersion string `json:"S3ObjectVersion"`
			ImageUri        string `json:"ImageUri"`
		} `json:"Code"`
		PackageType string            `json:"PackageType"`
		Publish     bool              `json:"Publish"`
		Tags        map[string]string `json:"Tags"`
	}
	if err := s.bind(q, &in); err != nil {
		return err
	}
	if err := q.Authorize("lambda:CreateFunction", s.fnARN(in.FunctionName)); err != nil {
		return err
	}
	if err := s.authorizeImage(q, in.Code.ImageUri); err != nil {
		return err
	}
	spec := createSpec{Name: in.FunctionName, configInput: in.config(), PackageType: in.PackageType, ImageURI: in.Code.ImageUri, Publish: in.Publish}
	spec.Tags = in.Tags
	switch {
	case in.Code.ZipFile != nil:
		if err := checkZip(in.Code.ZipFile); err != nil {
			return err
		}
		spec.Zip = in.Code.ZipFile
	case in.Code.S3Bucket != "" || in.Code.S3Key != "":
		b, err := s.s3Code(q, in.Code.S3Bucket, in.Code.S3Key, in.Code.S3ObjectVersion)
		if err != nil {
			return err
		}
		spec.Zip = b
	case in.Code.ImageUri == "" && in.PackageType != "Image":
		return core.BadRequest("Please provide a source for function code (ZipFile, S3Bucket/S3Key or ImageUri).")
	}
	f, err := s.createFunction(q.R.Context(), q.Authorize, spec)
	if err != nil {
		return err
	}
	out := f
	if in.Publish {
		if v, err := s.getVersion(f.Name, strconv.Itoa(f.LastVersion)); err == nil {
			out = v
		}
	}
	q.WriteJSON(http.StatusCreated, s.functionConfig(out))
	return nil
}

func (s *Service) awsListFunctions(q *awsapi.Req, _ map[string]string) error {
	if err := q.Authorize("lambda:ListFunctions", "*"); err != nil {
		return err
	}
	fns := store.List[Function](s.env.Store, cFunctions)
	sort.Slice(fns, func(i, j int) bool { return fns[i].Name < fns[j].Name })
	var all []Function
	for _, f := range fns {
		f, _ = s.getLatest(f.Name)
		if q.R.URL.Query().Get("FunctionVersion") == "ALL" {
			vs, _ := s.versions(f.Name)
			all = append(all, vs...)
		} else {
			all = append(all, f)
		}
	}
	start, end, next := paginate(q, len(all))
	out := []map[string]any{}
	for _, f := range all[start:end] {
		out = append(out, s.functionConfig(f))
	}
	q.WriteJSON(http.StatusOK, withMarker(map[string]any{"Functions": out}, next))
	return nil
}

func (s *Service) awsGetFunction(q *awsapi.Req, p map[string]string) error {
	name, qual, err := target(q, p)
	if err != nil {
		return err
	}
	if err := q.Authorize("lambda:GetFunction", s.fnARN(name)); err != nil {
		return err
	}
	f, err := s.qualified(name, qual)
	if err != nil {
		return err
	}
	out := map[string]any{"Configuration": s.functionConfig(f)}
	if f.isImage() {
		out["Code"] = map[string]string{"RepositoryType": "ECR", "ImageUri": f.ImageURI, "ResolvedImageUri": s.imageFor(f)}
	} else {
		out["Code"] = map[string]string{"RepositoryType": "S3", "Location": s.codeURL(f)}
	}
	if qual == "" {
		head, _ := s.getLatest(name)
		tags := map[string]string(head.Tags)
		if tags == nil {
			tags = map[string]string{}
		}
		out["Tags"] = tags
		if head.ReservedConcurrency != nil {
			out["Concurrency"] = map[string]int{"ReservedConcurrentExecutions": *head.ReservedConcurrency}
		}
	}
	q.WriteJSON(http.StatusOK, out)
	return nil
}

// qualified returns the configuration a qualifier names without routing
// weights (GetFunction and friends on an alias describe its primary version).
func (s *Service) qualified(name, qual string) (Function, error) {
	switch {
	case qual == "" || qual == latest:
		return s.getLatest(name)
	case isVersionNumber(qual):
		if _, err := s.getLatest(name); err != nil {
			return Function{}, err
		}
		return s.getVersion(name, qual)
	}
	if _, err := s.getLatest(name); err != nil {
		return Function{}, err
	}
	a, err := s.getAlias(name, qual)
	if err != nil {
		return Function{}, fnNotFound(s.fnARN(name) + ":" + qual)
	}
	if a.FunctionVersion == latest {
		return s.getLatest(name)
	}
	return s.getVersion(name, a.FunctionVersion)
}

func (s *Service) awsGetConfiguration(q *awsapi.Req, p map[string]string) error {
	name, qual, err := target(q, p)
	if err != nil {
		return err
	}
	if err := q.Authorize("lambda:GetFunctionConfiguration", s.fnARN(name)); err != nil {
		return err
	}
	f, err := s.qualified(name, qual)
	if err != nil {
		return err
	}
	q.WriteJSON(http.StatusOK, s.functionConfig(f))
	return nil
}

func (s *Service) awsDeleteFunction(q *awsapi.Req, p map[string]string) error {
	name, qual, err := target(q, p)
	if err != nil {
		return err
	}
	if err := q.Authorize("lambda:DeleteFunction", s.fnARN(name)); err != nil {
		return err
	}
	if err := s.deleteFunction(name, qual); err != nil {
		return err
	}
	q.W.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Service) awsUpdateConfiguration(q *awsapi.Req, p map[string]string) error {
	name, _ := parseRef(p["fn"])
	var in awsConfigIn
	if err := s.bind(q, &in); err != nil {
		return err
	}
	if err := q.Authorize("lambda:UpdateFunctionConfiguration", s.fnARN(name)); err != nil {
		return err
	}
	f, err := s.updateConfiguration(q.Authorize, name, in.RevisionId, in.config())
	if err != nil {
		return err
	}
	q.WriteJSON(http.StatusOK, s.functionConfig(f))
	return nil
}

func (s *Service) awsUpdateCode(q *awsapi.Req, p map[string]string) error {
	name, _ := parseRef(p["fn"])
	var in struct {
		ZipFile         []byte   `json:"ZipFile"`
		S3Bucket        string   `json:"S3Bucket"`
		S3Key           string   `json:"S3Key"`
		S3ObjectVersion string   `json:"S3ObjectVersion"`
		ImageUri        string   `json:"ImageUri"`
		Publish         bool     `json:"Publish"`
		DryRun          bool     `json:"DryRun"`
		RevisionId      string   `json:"RevisionId"`
		Architectures   []string `json:"Architectures"`
	}
	if err := s.bind(q, &in); err != nil {
		return err
	}
	if err := q.Authorize("lambda:UpdateFunctionCode", s.fnARN(name)); err != nil {
		return err
	}
	if err := s.authorizeImage(q, in.ImageUri); err != nil {
		return err
	}
	spec := codeSpec{ImageURI: in.ImageUri, Publish: in.Publish, Revision: in.RevisionId}
	switch {
	case in.ZipFile != nil:
		if err := checkZip(in.ZipFile); err != nil {
			return err
		}
		spec.Zip = in.ZipFile
	case in.S3Bucket != "" || in.S3Key != "":
		b, err := s.s3Code(q, in.S3Bucket, in.S3Key, in.S3ObjectVersion)
		if err != nil {
			return err
		}
		spec.Zip = b
	}
	if in.DryRun {
		f, err := s.getLatest(name)
		if err != nil {
			return err
		}
		q.WriteJSON(http.StatusOK, s.functionConfig(f))
		return nil
	}
	if in.Architectures != nil {
		if _, err := s.updateConfiguration(q.Authorize, name, in.RevisionId, configInput{Architectures: in.Architectures}); err != nil {
			return err
		}
		spec.Revision = ""
	}
	f, err := s.updateCode(name, spec)
	if err != nil {
		return err
	}
	q.WriteJSON(http.StatusOK, s.functionConfig(f))
	return nil
}

// ---- invocation ----

func (s *Service) authorizeInvoke(q *awsapi.Req, name, qual string) error {
	err := q.Authorize("lambda:InvokeFunction", s.fnARN(name))
	if err != nil && s.policyAllows(name, qual, q.P, "lambda:InvokeFunction") {
		return nil
	}
	return err
}

func (s *Service) awsInvoke(q *awsapi.Req, p map[string]string) error {
	name, qual, err := target(q, p)
	if err != nil {
		return err
	}
	if err := s.authorizeInvoke(q, name, qual); err != nil {
		return err
	}
	h := q.R.Header
	clientCtx := h.Get("X-Amz-Client-Context")
	if clientCtx != "" {
		if b, err := base64.StdEncoding.DecodeString(clientCtx); err != nil || !json.Valid(b) {
			return awsapi.Errorf(http.StatusBadRequest, "InvalidRequestContentException", "Client context must be a valid Base64-encoded JSON object.")
		}
	}
	switch it := h.Get("X-Amz-Invocation-Type"); it {
	case "", "RequestResponse":
	case "Event":
		if h.Get("X-Amz-Log-Type") == "Tail" {
			return core.BadRequest("LogType Tail is only supported for synchronous invocations")
		}
		id, err := s.InvokeAsync(name, qual, q.Body, clientCtx)
		if err != nil {
			return err
		}
		q.W.Header().Set("x-amzn-RequestId", id)
		q.W.WriteHeader(http.StatusAccepted)
		return nil
	case "DryRun":
		if _, _, err := s.resolve(name, qual); err != nil {
			return err
		}
		q.W.WriteHeader(http.StatusNoContent)
		return nil
	default:
		return core.BadRequest("InvocationType must be RequestResponse, Event or DryRun")
	}
	res, err := s.InvokeWith(q.R.Context(), name, q.Body, InvokeOptions{Qualifier: qual, ClientContext: clientCtx})
	if err != nil {
		return err
	}
	w := q.W.Header()
	w.Set("Content-Type", "application/json")
	w.Set("X-Amz-Executed-Version", res.ExecutedVersion)
	w.Set("x-amzn-RequestId", res.RequestID)
	if res.FunctionError != "" {
		w.Set("X-Amz-Function-Error", res.FunctionError)
	}
	if h.Get("X-Amz-Log-Type") == "Tail" {
		w.Set("X-Amz-Log-Result", base64.StdEncoding.EncodeToString([]byte(res.Logs)))
	}
	q.W.WriteHeader(http.StatusOK)
	q.W.Write(res.Body())
	return nil
}

func (s *Service) awsInvokeAsync(q *awsapi.Req, p map[string]string) error {
	name, qual, err := target(q, p)
	if err != nil {
		return err
	}
	if err := s.authorizeInvoke(q, name, qual); err != nil {
		return err
	}
	if _, err := s.InvokeAsync(name, qual, q.Body, ""); err != nil {
		return err
	}
	q.WriteJSON(http.StatusAccepted, map[string]int{"Status": 202})
	return nil
}

// ---- versions & aliases ----

func (s *Service) awsPublishVersion(q *awsapi.Req, p map[string]string) error {
	name, _ := parseRef(p["fn"])
	var in struct{ CodeSha256, Description, RevisionId string }
	if err := s.bind(q, &in); err != nil {
		return err
	}
	if err := q.Authorize("lambda:PublishVersion", s.fnARN(name)); err != nil {
		return err
	}
	v, err := s.publishVersion(name, in.Description, in.CodeSha256, in.RevisionId)
	if err != nil {
		return err
	}
	q.WriteJSON(http.StatusCreated, s.functionConfig(v))
	return nil
}

func (s *Service) awsListVersions(q *awsapi.Req, p map[string]string) error {
	name, _ := parseRef(p["fn"])
	if err := q.Authorize("lambda:ListVersionsByFunction", s.fnARN(name)); err != nil {
		return err
	}
	vs, err := s.versions(name)
	if err != nil {
		return err
	}
	start, end, next := paginate(q, len(vs))
	out := []map[string]any{}
	for _, v := range vs[start:end] {
		out = append(out, s.functionConfig(v))
	}
	q.WriteJSON(http.StatusOK, withMarker(map[string]any{"Versions": out}, next))
	return nil
}

func aliasOut(a Alias) map[string]any {
	out := map[string]any{"AliasArn": a.ARN, "Name": a.Name, "FunctionVersion": a.FunctionVersion,
		"Description": a.Description, "RevisionId": a.RevisionID}
	if len(a.Weights) > 0 {
		out["RoutingConfig"] = map[string]any{"AdditionalVersionWeights": a.Weights}
	}
	return out
}

type awsAliasIn struct {
	Name            string  `json:"Name"`
	FunctionVersion string  `json:"FunctionVersion"`
	Description     *string `json:"Description"`
	RoutingConfig   *struct {
		AdditionalVersionWeights map[string]float64 `json:"AdditionalVersionWeights"`
	} `json:"RoutingConfig"`
	RevisionId string `json:"RevisionId"`
}

func (a awsAliasIn) input() aliasInput {
	in := aliasInput{Name: a.Name, FunctionVersion: a.FunctionVersion, Description: a.Description, Revision: a.RevisionId}
	if a.RoutingConfig != nil {
		in.Weights = a.RoutingConfig.AdditionalVersionWeights
		if in.Weights == nil {
			in.Weights = map[string]float64{}
		}
	}
	return in
}

func (s *Service) awsCreateAlias(q *awsapi.Req, p map[string]string) error {
	name, _ := parseRef(p["fn"])
	var in awsAliasIn
	if err := s.bind(q, &in); err != nil {
		return err
	}
	if err := q.Authorize("lambda:CreateAlias", s.fnARN(name)); err != nil {
		return err
	}
	a, err := s.createAlias(name, in.input())
	if err != nil {
		return err
	}
	q.WriteJSON(http.StatusCreated, aliasOut(a))
	return nil
}

func (s *Service) awsListAliases(q *awsapi.Req, p map[string]string) error {
	name, _ := parseRef(p["fn"])
	if err := q.Authorize("lambda:ListAliases", s.fnARN(name)); err != nil {
		return err
	}
	if _, err := s.getLatest(name); err != nil {
		return err
	}
	var as []Alias
	for _, a := range s.aliases(name) {
		if v := q.R.URL.Query().Get("FunctionVersion"); v == "" || a.FunctionVersion == v {
			as = append(as, a)
		}
	}
	start, end, next := paginate(q, len(as))
	out := []map[string]any{}
	for _, a := range as[start:end] {
		out = append(out, aliasOut(a))
	}
	q.WriteJSON(http.StatusOK, withMarker(map[string]any{"Aliases": out}, next))
	return nil
}

func (s *Service) awsGetAlias(q *awsapi.Req, p map[string]string) error {
	name, _ := parseRef(p["fn"])
	if err := q.Authorize("lambda:GetAlias", s.fnARN(name)); err != nil {
		return err
	}
	if _, err := s.getLatest(name); err != nil {
		return err
	}
	a, err := s.getAlias(name, p["alias"])
	if err != nil {
		return err
	}
	q.WriteJSON(http.StatusOK, aliasOut(a))
	return nil
}

func (s *Service) awsUpdateAlias(q *awsapi.Req, p map[string]string) error {
	name, _ := parseRef(p["fn"])
	var in awsAliasIn
	if err := s.bind(q, &in); err != nil {
		return err
	}
	if err := q.Authorize("lambda:UpdateAlias", s.fnARN(name)); err != nil {
		return err
	}
	if _, err := s.getLatest(name); err != nil {
		return err
	}
	a, err := s.updateAlias(name, p["alias"], in.input())
	if err != nil {
		return err
	}
	q.WriteJSON(http.StatusOK, aliasOut(a))
	return nil
}

func (s *Service) awsDeleteAlias(q *awsapi.Req, p map[string]string) error {
	name, _ := parseRef(p["fn"])
	if err := q.Authorize("lambda:DeleteAlias", s.fnARN(name)); err != nil {
		return err
	}
	if _, err := s.getLatest(name); err != nil {
		return err
	}
	if err := s.deleteAlias(name, p["alias"]); err != nil {
		return err
	}
	q.W.WriteHeader(http.StatusNoContent)
	return nil
}

// ---- permissions ----

func (s *Service) awsAddPermission(q *awsapi.Req, p map[string]string) error {
	name, qual, err := target(q, p)
	if err != nil {
		return err
	}
	var in permissionInput
	if err := s.bind(q, &in); err != nil {
		return err
	}
	if err := q.Authorize("lambda:AddPermission", s.fnARN(name)); err != nil {
		return err
	}
	st, err := s.addPermission(name, qual, in)
	if err != nil {
		return err
	}
	b, _ := json.Marshal(st)
	q.WriteJSON(http.StatusCreated, map[string]string{"Statement": string(b)})
	return nil
}

func (s *Service) awsGetPolicy(q *awsapi.Req, p map[string]string) error {
	name, qual, err := target(q, p)
	if err != nil {
		return err
	}
	if err := q.Authorize("lambda:GetPolicy", s.fnARN(name)); err != nil {
		return err
	}
	doc, rev, err := s.policyDoc(name, qual)
	if err != nil {
		return err
	}
	q.WriteJSON(http.StatusOK, map[string]string{"Policy": doc, "RevisionId": rev})
	return nil
}

func (s *Service) awsRemovePermission(q *awsapi.Req, p map[string]string) error {
	name, qual, err := target(q, p)
	if err != nil {
		return err
	}
	if err := q.Authorize("lambda:RemovePermission", s.fnARN(name)); err != nil {
		return err
	}
	if err := s.removePermission(name, qual, p["sid"], q.R.URL.Query().Get("RevisionId")); err != nil {
		return err
	}
	q.W.WriteHeader(http.StatusNoContent)
	return nil
}

// ---- concurrency ----

func (s *Service) awsPutConcurrency(q *awsapi.Req, p map[string]string) error {
	name, _ := parseRef(p["fn"])
	var in struct {
		ReservedConcurrentExecutions *int `json:"ReservedConcurrentExecutions"`
	}
	if err := s.bind(q, &in); err != nil {
		return err
	}
	if err := q.Authorize("lambda:PutFunctionConcurrency", s.fnARN(name)); err != nil {
		return err
	}
	if in.ReservedConcurrentExecutions == nil {
		return core.BadRequest("ReservedConcurrentExecutions is required")
	}
	if _, err := s.putConcurrency(name, *in.ReservedConcurrentExecutions); err != nil {
		return err
	}
	q.WriteJSON(http.StatusOK, map[string]int{"ReservedConcurrentExecutions": *in.ReservedConcurrentExecutions})
	return nil
}

func (s *Service) awsGetConcurrency(q *awsapi.Req, p map[string]string) error {
	name, _ := parseRef(p["fn"])
	if err := q.Authorize("lambda:GetFunctionConcurrency", s.fnARN(name)); err != nil {
		return err
	}
	f, err := s.getLatest(name)
	if err != nil {
		return err
	}
	out := map[string]int{}
	if f.ReservedConcurrency != nil {
		out["ReservedConcurrentExecutions"] = *f.ReservedConcurrency
	}
	q.WriteJSON(http.StatusOK, out)
	return nil
}

func (s *Service) awsDeleteConcurrency(q *awsapi.Req, p map[string]string) error {
	name, _ := parseRef(p["fn"])
	if err := q.Authorize("lambda:DeleteFunctionConcurrency", s.fnARN(name)); err != nil {
		return err
	}
	if err := s.deleteConcurrency(name); err != nil {
		return err
	}
	q.W.WriteHeader(http.StatusNoContent)
	return nil
}

// ---- asynchronous invocation config ----

func (s *Service) invokeConfigOut(c EventInvokeConfig) map[string]any {
	out := map[string]any{"LastModified": awsapi.Epoch(c.LastModified), "FunctionArn": s.fnARN(c.Function) + ":" + c.Qualifier}
	if c.MaximumRetryAttempts != nil {
		out["MaximumRetryAttempts"] = *c.MaximumRetryAttempts
	}
	if c.MaximumEventAgeInSeconds != nil {
		out["MaximumEventAgeInSeconds"] = *c.MaximumEventAgeInSeconds
	}
	dc := map[string]any{}
	if c.OnSuccess != "" {
		dc["OnSuccess"] = map[string]string{"Destination": c.OnSuccess}
	}
	if c.OnFailure != "" {
		dc["OnFailure"] = map[string]string{"Destination": c.OnFailure}
	}
	out["DestinationConfig"] = dc
	return out
}

func (s *Service) awsPutInvokeConfig(merge bool) awsHandler {
	return func(q *awsapi.Req, p map[string]string) error {
		name, qual, err := target(q, p)
		if err != nil {
			return err
		}
		var in struct {
			MaximumRetryAttempts     *int `json:"MaximumRetryAttempts"`
			MaximumEventAgeInSeconds *int `json:"MaximumEventAgeInSeconds"`
			DestinationConfig        *struct {
				OnSuccess *struct{ Destination string } `json:"OnSuccess"`
				OnFailure *struct{ Destination string } `json:"OnFailure"`
			} `json:"DestinationConfig"`
		}
		if err := s.bind(q, &in); err != nil {
			return err
		}
		action := "lambda:PutFunctionEventInvokeConfig"
		if merge {
			action = "lambda:UpdateFunctionEventInvokeConfig"
		}
		if err := q.Authorize(action, s.fnARN(name)); err != nil {
			return err
		}
		ci := invokeConfigInput{MaximumRetryAttempts: in.MaximumRetryAttempts, MaximumEventAgeInSeconds: in.MaximumEventAgeInSeconds}
		if d := in.DestinationConfig; d != nil {
			if d.OnSuccess != nil {
				ci.OnSuccess = &d.OnSuccess.Destination
			}
			if d.OnFailure != nil {
				ci.OnFailure = &d.OnFailure.Destination
			}
		}
		c, err := s.putInvokeConfig(q.Authorize, name, qual, ci, merge)
		if err != nil {
			return err
		}
		q.WriteJSON(http.StatusOK, s.invokeConfigOut(c))
		return nil
	}
}

func (s *Service) awsGetInvokeConfig(q *awsapi.Req, p map[string]string) error {
	name, qual, err := target(q, p)
	if err != nil {
		return err
	}
	if err := q.Authorize("lambda:GetFunctionEventInvokeConfig", s.fnARN(name)); err != nil {
		return err
	}
	if _, _, err := s.resolve(name, qual); err != nil {
		return err
	}
	c, ok := s.invokeConfig(name, qual)
	if !ok {
		return core.Errf(http.StatusNotFound, "ResourceNotFound", "The function %s doesn't have an EventInvokeConfig", s.fnARN(name)+":"+qualifierKey(qual))
	}
	q.WriteJSON(http.StatusOK, s.invokeConfigOut(c))
	return nil
}

func (s *Service) awsDeleteInvokeConfig(q *awsapi.Req, p map[string]string) error {
	name, qual, err := target(q, p)
	if err != nil {
		return err
	}
	if err := q.Authorize("lambda:DeleteFunctionEventInvokeConfig", s.fnARN(name)); err != nil {
		return err
	}
	if err := s.deleteInvokeConfig(name, qual); err != nil {
		return err
	}
	q.W.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Service) awsListInvokeConfigs(q *awsapi.Req, p map[string]string) error {
	name, _ := parseRef(p["fn"])
	if err := q.Authorize("lambda:ListFunctionEventInvokeConfigs", s.fnARN(name)); err != nil {
		return err
	}
	if _, err := s.getLatest(name); err != nil {
		return err
	}
	cs := s.listInvokeConfigs(name)
	start, end, next := paginate(q, len(cs))
	out := []map[string]any{}
	for _, c := range cs[start:end] {
		out = append(out, s.invokeConfigOut(c))
	}
	q.WriteJSON(http.StatusOK, withMarker(map[string]any{"FunctionEventInvokeConfigs": out}, next))
	return nil
}

// ---- function URLs ----

func urlAuthOut(a string) string {
	if a == "HC_IAM" {
		return "AWS_IAM"
	}
	return a
}

func (s *Service) urlOut(f Function) map[string]any {
	u := f.URL
	out := map[string]any{"FunctionUrl": u.URL, "FunctionArn": f.qualifiedARN(u.Qualifier), "AuthType": urlAuthOut(u.AuthType),
		"InvokeMode": u.InvokeMode}
	if out["InvokeMode"] == "" {
		out["InvokeMode"] = "BUFFERED"
	}
	if u.Cors != nil {
		out["Cors"] = u.Cors
	}
	if u.CreatedAt != nil {
		out["CreationTime"] = awsTime(*u.CreatedAt)
	}
	if u.LastModified != nil {
		out["LastModifiedTime"] = awsTime(*u.LastModified)
	}
	return out
}

func (s *Service) awsPutURL(update bool) awsHandler {
	return func(q *awsapi.Req, p map[string]string) error {
		name, qual, err := target(q, p)
		if err != nil {
			return err
		}
		var in struct {
			AuthType   string   `json:"AuthType"`
			Cors       *URLCors `json:"Cors"`
			InvokeMode string   `json:"InvokeMode"`
		}
		if err := s.bind(q, &in); err != nil {
			return err
		}
		action := "lambda:CreateFunctionUrlConfig"
		if update {
			action = "lambda:UpdateFunctionUrlConfig"
		}
		if err := q.Authorize(action, s.fnARN(name)); err != nil {
			return err
		}
		f, err := s.putURL(name, qual, urlInput{AuthType: in.AuthType, Cors: in.Cors, InvokeMode: in.InvokeMode}, update)
		if err != nil {
			return err
		}
		status := http.StatusCreated
		if update {
			status = http.StatusOK
		}
		q.WriteJSON(status, s.urlOut(f))
		return nil
	}
}

func (s *Service) awsGetURL(q *awsapi.Req, p map[string]string) error {
	name, qual, err := target(q, p)
	if err != nil {
		return err
	}
	if err := q.Authorize("lambda:GetFunctionUrlConfig", s.fnARN(name)); err != nil {
		return err
	}
	f, err := s.getLatest(name)
	if err != nil {
		return err
	}
	if !f.URL.Enabled || f.URL.Qualifier != qual {
		return core.Errf(http.StatusNotFound, "ResourceNotFound", "The resource you requested does not exist.")
	}
	q.WriteJSON(http.StatusOK, s.urlOut(f))
	return nil
}

func (s *Service) awsDeleteURL(q *awsapi.Req, p map[string]string) error {
	name, qual, err := target(q, p)
	if err != nil {
		return err
	}
	if err := q.Authorize("lambda:DeleteFunctionUrlConfig", s.fnARN(name)); err != nil {
		return err
	}
	f, err := s.getLatest(name)
	if err != nil {
		return err
	}
	if !f.URL.Enabled || f.URL.Qualifier != qual {
		return core.Errf(http.StatusNotFound, "ResourceNotFound", "The resource you requested does not exist.")
	}
	if _, err := s.modify(name, func(f *Function) error { f.URL = FunctionURL{AuthType: "NONE"}; return nil }); err != nil {
		return err
	}
	q.W.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Service) awsListURLs(q *awsapi.Req, p map[string]string) error {
	name, _ := parseRef(p["fn"])
	if err := q.Authorize("lambda:ListFunctionUrlConfigs", s.fnARN(name)); err != nil {
		return err
	}
	f, err := s.getLatest(name)
	if err != nil {
		return err
	}
	out := []map[string]any{}
	if f.URL.Enabled {
		out = append(out, s.urlOut(f))
	}
	q.WriteJSON(http.StatusOK, map[string]any{"FunctionUrlConfigs": out})
	return nil
}

// awsServeURL serves a SigV4-signed function URL request (auth type AWS_IAM).
func (s *Service) awsServeURL(q *awsapi.Req) {
	rest := strings.TrimPrefix(q.R.URL.Path, "/lambda-url/")
	name, sub, _ := strings.Cut(rest, "/")
	f, err := s.getLatest(name)
	if err != nil || !f.URL.Enabled {
		q.Fail(core.Errf(http.StatusNotFound, "ResourceNotFound", "no function URL is configured for %q", name))
		return
	}
	q.Op = "InvokeFunctionUrl"
	if f.URL.AuthType == "HC_IAM" {
		if err := q.Authorize("lambda:InvokeFunctionUrl", f.ARN); err != nil && !s.policyAllows(name, f.URL.Qualifier, q.P, "lambda:InvokeFunctionUrl") {
			q.Fail(err)
			return
		}
	}
	if err := s.invokeURL(q.W, q.R, f, "/"+sub); err != nil {
		q.Fail(err)
	}
}

// ---- tags ----

// tagTarget resolves the ARN of a taggable resource, a function or an event source mapping. For a
// mapping it returns the mapping ID and true.
func (s *Service) tagTarget(q *awsapi.Req, p map[string]string, action string) (id string, mapping bool, err error) {
	arn := p["arn"]
	if i := strings.Index(arn, ":event-source-mapping:"); i >= 0 {
		id = arn[i+len(":event-source-mapping:"):]
		if err := q.Authorize(action, s.mappingARN(id)); err != nil {
			return "", true, err
		}
		if _, err := store.Get[Mapping](s.env.Store, cMappings, id); err != nil {
			return "", true, mappingNotFound(id)
		}
		return id, true, nil
	}
	if !strings.Contains(arn, ":function:") {
		return "", false, core.BadRequest("only functions and event source mappings can be tagged: %s", arn)
	}
	name, _ := parseRef(arn)
	if err := q.Authorize(action, s.fnARN(name)); err != nil {
		return "", false, err
	}
	_, err = s.getLatest(name)
	return name, false, err
}

// editTags applies fn to the tags of a function or event source mapping.
func (s *Service) editTags(id string, mapping bool, fn func(core.Tags) (core.Tags, error)) error {
	if mapping {
		_, err := store.Update(s.env.Store, cMappings, id, func(m *Mapping) error {
			t, err := fn(m.Tags)
			m.Tags = t
			return err
		})
		return err
	}
	_, err := s.modify(id, func(f *Function) error {
		t, err := fn(f.Tags)
		f.Tags = t
		return err
	})
	return err
}

func (s *Service) awsTag(q *awsapi.Req, p map[string]string) error {
	id, mapping, err := s.tagTarget(q, p, "lambda:TagResource")
	if err != nil {
		return err
	}
	var in struct{ Tags map[string]string }
	if err := s.bind(q, &in); err != nil {
		return err
	}
	if err := s.editTags(id, mapping, func(t core.Tags) (core.Tags, error) {
		if t == nil {
			t = core.Tags{}
		}
		for k, v := range in.Tags {
			t[k] = v
		}
		if len(t) > 50 {
			return t, core.BadRequest("a function can have at most 50 tags")
		}
		return t, nil
	}); err != nil {
		return err
	}
	q.W.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Service) awsUntag(q *awsapi.Req, p map[string]string) error {
	id, mapping, err := s.tagTarget(q, p, "lambda:UntagResource")
	if err != nil {
		return err
	}
	keys := q.R.URL.Query()["tagKeys"]
	if err := s.editTags(id, mapping, func(t core.Tags) (core.Tags, error) {
		for _, k := range keys {
			delete(t, k)
		}
		return t, nil
	}); err != nil {
		return err
	}
	q.W.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Service) awsListTags(q *awsapi.Req, p map[string]string) error {
	id, mapping, err := s.tagTarget(q, p, "lambda:ListTags")
	if err != nil {
		return err
	}
	var tags core.Tags
	if mapping {
		m, _ := store.Get[Mapping](s.env.Store, cMappings, id)
		tags = m.Tags
	} else {
		f, _ := s.getLatest(id)
		tags = f.Tags
	}
	out := map[string]string(tags)
	if out == nil {
		out = map[string]string{}
	}
	q.WriteJSON(http.StatusOK, map[string]any{"Tags": out})
	return nil
}

// ---- account ----

func (s *Service) accountSettings() map[string]any {
	fns := store.List[Function](s.env.Store, cFunctions)
	var total int64
	for _, f := range fns {
		total += f.CodeSize
	}
	for _, v := range store.List[Function](s.env.Store, cVersions) {
		total += v.CodeSize
	}
	return map[string]any{
		"AccountLimit": map[string]any{"TotalCodeSize": int64(75) << 30, "CodeSizeUnzipped": 5 * maxCodeBytes, "CodeSizeZipped": maxCodeBytes,
			"ConcurrentExecutions": AccountConcurrency, "UnreservedConcurrentExecutions": AccountConcurrency - s.reservedTotal("")},
		"AccountUsage": map[string]any{"TotalCodeSize": total, "FunctionCount": len(fns)},
	}
}

func (s *Service) awsAccountSettings(q *awsapi.Req, _ map[string]string) error {
	if err := q.Authorize("lambda:GetAccountSettings", "*"); err != nil {
		return err
	}
	q.WriteJSON(http.StatusOK, s.accountSettings())
	return nil
}

// ---- layers ----

func (s *Service) layerOut(lv LayerVersion, content bool) map[string]any {
	out := map[string]any{"LayerVersionArn": lv.ARN, "Version": lv.Version, "Description": lv.Description,
		"CreatedDate": awsTime(lv.CreatedAt)}
	if len(lv.CompatibleRuntimes) > 0 {
		out["CompatibleRuntimes"] = lv.CompatibleRuntimes
	}
	if len(lv.CompatibleArchitectures) > 0 {
		out["CompatibleArchitectures"] = lv.CompatibleArchitectures
	}
	if lv.LicenseInfo != "" {
		out["LicenseInfo"] = lv.LicenseInfo
	}
	if content {
		out["LayerArn"] = lv.LayerARN
		out["Content"] = map[string]any{"Location": s.layerURL(lv), "CodeSha256": lv.CodeSHA256, "CodeSize": lv.CodeSize}
	}
	return out
}

func (s *Service) awsPublishLayer(q *awsapi.Req, p map[string]string) error {
	name := p["layer"]
	var in struct {
		Description string
		Content     struct {
			ZipFile                          []byte
			S3Bucket, S3Key, S3ObjectVersion string
		}
		CompatibleRuntimes      []string
		CompatibleArchitectures []string
		LicenseInfo             string
	}
	if err := s.bind(q, &in); err != nil {
		return err
	}
	if err := q.Authorize("lambda:PublishLayerVersion", s.layerARN(name)); err != nil {
		return err
	}
	zipped := in.Content.ZipFile
	if zipped == nil && (in.Content.S3Bucket != "" || in.Content.S3Key != "") {
		b, err := s.s3Code(q, in.Content.S3Bucket, in.Content.S3Key, in.Content.S3ObjectVersion)
		if err != nil {
			return err
		}
		zipped = b
	}
	lv, err := s.publishLayer(name, zipped, layerInput{Description: in.Description, CompatibleRuntimes: in.CompatibleRuntimes,
		CompatibleArchitectures: in.CompatibleArchitectures, LicenseInfo: in.LicenseInfo})
	if err != nil {
		return err
	}
	q.WriteJSON(http.StatusCreated, s.layerOut(lv, true))
	return nil
}

func layerVersionParam(p map[string]string) (int, error) {
	v, err := strconv.Atoi(p["ver"])
	if err != nil || v <= 0 {
		return 0, core.BadRequest("invalid layer version %q", p["ver"])
	}
	return v, nil
}

func (s *Service) awsGetLayerVersion(q *awsapi.Req, p map[string]string) error {
	name, _, _ := parseLayerRef(p["layer"] + ":1")
	v, err := layerVersionParam(p)
	if err != nil {
		return err
	}
	if err := q.Authorize("lambda:GetLayerVersion", s.layerARN(name)+":"+strconv.Itoa(v)); err != nil {
		return err
	}
	lv, err := s.getLayerVersion(name, v)
	if err != nil {
		return err
	}
	q.WriteJSON(http.StatusOK, s.layerOut(lv, true))
	return nil
}

func (s *Service) awsDeleteLayerVersion(q *awsapi.Req, p map[string]string) error {
	name, _, _ := parseLayerRef(p["layer"] + ":1")
	v, err := layerVersionParam(p)
	if err != nil {
		return err
	}
	if err := q.Authorize("lambda:DeleteLayerVersion", s.layerARN(name)+":"+strconv.Itoa(v)); err != nil {
		return err
	}
	if err := s.deleteLayerVersion(name, v); err != nil {
		return err
	}
	q.W.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Service) awsListLayerVersions(q *awsapi.Req, p map[string]string) error {
	name, _, _ := parseLayerRef(p["layer"] + ":1")
	if err := q.Authorize("lambda:ListLayerVersions", s.layerARN(name)); err != nil {
		return err
	}
	qs := q.R.URL.Query()
	lvs := s.layerVersions(name, qs.Get("CompatibleRuntime"), qs.Get("CompatibleArchitecture"))
	start, end, next := paginate(q, len(lvs))
	out := []map[string]any{}
	for _, lv := range lvs[start:end] {
		out = append(out, s.layerOut(lv, false))
	}
	q.WriteJSON(http.StatusOK, withMarker(map[string]any{"LayerVersions": out}, next))
	return nil
}

func (s *Service) awsListLayers(q *awsapi.Req, _ map[string]string) error {
	if err := q.Authorize("lambda:ListLayers", "*"); err != nil {
		return err
	}
	qs := q.R.URL.Query()
	if qs.Get("find") == "LayerVersion" { // GetLayerVersionByArn
		lv, err := s.layerByARN(qs.Get("Arn"))
		if err != nil || lv.Deleted {
			return layerNotFound(qs.Get("Arn"))
		}
		q.Op = "GetLayerVersionByArn"
		q.WriteJSON(http.StatusOK, s.layerOut(lv, true))
		return nil
	}
	lvs := s.layers(qs.Get("CompatibleRuntime"), qs.Get("CompatibleArchitecture"))
	start, end, next := paginate(q, len(lvs))
	out := []map[string]any{}
	for _, lv := range lvs[start:end] {
		out = append(out, map[string]any{"LayerName": lv.Name, "LayerArn": lv.LayerARN, "LatestMatchingVersion": s.layerOut(lv, false)})
	}
	q.WriteJSON(http.StatusOK, withMarker(map[string]any{"Layers": out}, next))
	return nil
}

// ---- operations HomeCloud answers with defaults ----

func (s *Service) awsReadOnly(q *awsapi.Req, p map[string]string, action string) (Function, error) {
	name, _ := parseRef(p["fn"])
	if err := q.Authorize(action, s.fnARN(name)); err != nil {
		return Function{}, err
	}
	return s.getLatest(name)
}

func (s *Service) awsCodeSigning(q *awsapi.Req, p map[string]string) error {
	f, err := s.awsReadOnly(q, p, "lambda:GetFunctionCodeSigningConfig")
	if err != nil {
		return err
	}
	q.WriteJSON(http.StatusOK, map[string]string{"CodeSigningConfigArn": "", "FunctionName": f.Name})
	return nil
}

func (s *Service) awsRuntimeManagement(q *awsapi.Req, p map[string]string) error {
	f, err := s.awsReadOnly(q, p, "lambda:GetRuntimeManagementConfig")
	if err != nil {
		return err
	}
	q.WriteJSON(http.StatusOK, map[string]string{"UpdateRuntimeOn": "Auto", "FunctionArn": f.ARN})
	return nil
}

func (s *Service) awsRecursion(q *awsapi.Req, p map[string]string) error {
	if _, err := s.awsReadOnly(q, p, "lambda:GetFunctionRecursionConfig"); err != nil {
		return err
	}
	q.WriteJSON(http.StatusOK, map[string]string{"RecursiveLoop": "Terminate"})
	return nil
}

func (s *Service) awsProvisioned(q *awsapi.Req, p map[string]string) error {
	if _, err := s.awsReadOnly(q, p, "lambda:GetProvisionedConcurrencyConfig"); err != nil {
		return err
	}
	if q.R.URL.Query().Get("List") == "ALL" {
		q.Op = "ListProvisionedConcurrencyConfigs"
		q.WriteJSON(http.StatusOK, map[string]any{"ProvisionedConcurrencyConfigs": []any{}})
		return nil
	}
	q.Op = "GetProvisionedConcurrencyConfig"
	return awsapi.Errorf(http.StatusNotFound, "ProvisionedConcurrencyConfigNotFoundException", "No Provisioned Concurrency Config found for this function")
}
