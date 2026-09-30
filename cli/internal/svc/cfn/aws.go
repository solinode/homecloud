package cfn

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

// The AWS CloudFormation API (awsQuery, 2010-05-15): stacks, change sets,
// exports and template inspection, on top of the same engine as the native API.

const xmlns = "http://cloudformation.amazonaws.com/doc/2010-05-15/"

type awsOp func(q *awsapi.Req) (any, error)

// RegisterAWS serves CloudFormation over the AWS protocol.
func (s *Service) RegisterAWS() {
	ops := map[string]awsOp{
		"CreateStack":                 s.awsCreateStack,
		"UpdateStack":                 s.awsUpdateStack,
		"DeleteStack":                 s.awsDeleteStack,
		"DescribeStacks":              s.awsDescribeStacks,
		"ListStacks":                  s.awsListStacks,
		"DescribeStackEvents":         s.awsDescribeStackEvents,
		"DescribeStackResources":      s.awsDescribeStackResources,
		"DescribeStackResource":       s.awsDescribeStackResource,
		"ListStackResources":          s.awsListStackResources,
		"GetTemplate":                 s.awsGetTemplate,
		"GetTemplateSummary":          s.awsGetTemplateSummary,
		"ValidateTemplate":            s.awsValidateTemplate,
		"CreateChangeSet":             s.awsCreateChangeSet,
		"DescribeChangeSet":           s.awsDescribeChangeSet,
		"ExecuteChangeSet":            s.awsExecuteChangeSet,
		"DeleteChangeSet":             s.awsDeleteChangeSet,
		"ListChangeSets":              s.awsListChangeSets,
		"ListExports":                 s.awsListExports,
		"ListImports":                 s.awsListImports,
		"UpdateTerminationProtection": s.awsUpdateTerminationProtection,
	}
	svc := &awsapi.Service{Name: "cloudformation", XMLNS: xmlns, Ops: map[string]awsapi.Op{}}
	for name, fn := range ops {
		svc.Ops[name] = func(q *awsapi.Req) (any, error) {
			out, err := fn(q)
			if err != nil {
				return nil, cfnError(err)
			}
			if out == nil {
				return map[string]any{}, nil
			}
			return out, nil
		}
	}
	awsapi.Register(svc)
}

// cfnError maps engine errors to CloudFormation error codes.
func cfnError(err error) error {
	var ae *awsapi.Error
	if errors.As(err, &ae) {
		return ae
	}
	var ce *core.Error
	if !errors.As(err, &ce) {
		return err
	}
	code, status := ce.Code, http.StatusBadRequest
	switch code {
	case "AlreadyExistsException", "InsufficientCapabilitiesException", "InvalidChangeSetStatus", "LimitExceededException":
	case "ChangeSetNotFound":
		status = http.StatusNotFound
	case "AccessDenied":
		return &awsapi.Error{Status: http.StatusForbidden, Code: "AccessDenied", Message: ce.Message}
	default:
		code = "ValidationError"
	}
	if ce.Status >= 500 {
		status = ce.Status
	}
	return &awsapi.Error{Status: status, Code: code, Message: ce.Message}
}

func verr(format string, a ...any) error {
	return awsapi.Errorf(http.StatusBadRequest, "ValidationError", format, a...)
}

// ---- request parsing ----

func has1(q *awsapi.Req, name string) bool { _, ok := q.Form[name]; return ok }

func paramsIn(q *awsapi.Req, prev map[string]any) map[string]any {
	out := map[string]any{}
	for _, p := range q.Structs("Parameters") {
		key := p["ParameterKey"]
		if key == "" {
			continue
		}
		if strings.EqualFold(p["UsePreviousValue"], "true") {
			if v, ok := prev[key]; ok {
				out[key] = v
			}
			continue
		}
		out[key] = p["ParameterValue"]
	}
	return out
}

func tagsIn(q *awsapi.Req) core.Tags {
	ts := q.Structs("Tags")
	if len(ts) == 0 {
		return nil
	}
	m := core.Tags{}
	for _, t := range ts {
		m[t["Key"]] = t["Value"]
	}
	return m
}

func endpoint(q *awsapi.Req) string {
	scheme := "http"
	if q.R.TLS != nil {
		scheme = "https"
	}
	if p := q.R.Header.Get("X-Forwarded-Proto"); p == "http" || p == "https" {
		scheme = p
	}
	return scheme + "://" + q.R.Host
}

// stackARNFor is the ARN CloudFormation policies name for a stack that may not exist yet.
func (s *Service) stackARNPattern(name string) string {
	return s.env.ARN("cloudformation", "stack/"+name+"/*")
}

// stackRef finds the stack a request names (StackName is a name or an ID).
func (s *Service) stackRef(q *awsapi.Req, field string) (Stack, error) {
	ref := q.Param(field)
	if ref == "" {
		return Stack{}, verr("1 validation error detected: Value null at '%s' failed to satisfy constraint: Member must not be null", lowerFirst(field))
	}
	st, err := s.find(ref)
	if err != nil {
		return Stack{}, err
	}
	return st, nil
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// authStack finds the stack and authorizes the action on it.
func (s *Service) authStack(q *awsapi.Req, action, field string) (Stack, error) {
	st, err := s.stackRef(q, field)
	if err != nil {
		// Authorize first on a pattern so callers without access learn nothing.
		if aerr := q.Authorize(action, s.stackARNPattern(q.Param(field))); aerr != nil {
			return Stack{}, aerr
		}
		return Stack{}, err
	}
	if err := q.Authorize(action, st.ARN); err != nil {
		return Stack{}, err
	}
	return st, nil
}

// templateIn reads TemplateBody or TemplateURL (an object in a HomeCloud bucket).
func (s *Service) templateIn(q *awsapi.Req) (string, error) {
	body, u := q.Param("TemplateBody"), q.Param("TemplateURL")
	switch {
	case body != "" && u != "":
		return "", verr("Only one of TemplateBody and TemplateURL may be specified")
	case body != "":
		if len(body) > 51200 {
			return "", verr("1 validation error detected: Value '%s...' at 'templateBody' failed to satisfy constraint: Member must have length less than or equal to 51200", body[:20])
		}
		return body, nil
	case u != "":
		return s.fetchTemplate(q, u)
	}
	return "", verr("Either Template URL or Template Body must be specified.")
}

// bucketKey extracts the bucket and object key from an S3 URL: virtual-hosted
// (bucket.s3.amazonaws.com/key) or path style (host/bucket/key).
func bucketKey(raw, reqHost string) (bucket, key string, ok bool) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", "", false
	}
	path := strings.TrimPrefix(u.EscapedPath(), "/")
	host := u.Hostname()
	if i := strings.Index(host, ".s3"); i > 0 && (strings.HasSuffix(host, "amazonaws.com") || strings.HasSuffix(host, ".amazonaws.com.cn")) {
		bucket, key = host[:i], path
	} else if u.Host == reqHost || strings.HasPrefix(host, "s3.") || host == "localhost" || host == "127.0.0.1" {
		bucket, _, _ = strings.Cut(path, "/")
		key = strings.TrimPrefix(path, bucket+"/")
	} else {
		return "", "", false
	}
	k, err := url.PathUnescape(key)
	if err != nil || bucket == "" || k == "" {
		return "", "", false
	}
	return bucket, k, true
}

func (s *Service) fetchTemplate(q *awsapi.Req, raw string) (string, error) {
	bucket, key, ok := bucketKey(raw, q.R.Host)
	if !ok {
		return "", verr("TemplateURL must reference a valid S3 object to which you have access.")
	}
	b, err := s.fetch(context.Background(), q.P, "/api/v1/s3/buckets/"+esc(bucket)+"/object?key="+url.QueryEscape(key))
	if err != nil {
		return "", verr("TemplateURL must reference a valid S3 object to which you have access. (%v)", err)
	}
	if len(b) > 1<<20 {
		return "", verr("Template may not exceed 1 MB")
	}
	return string(b), nil
}

// capabilities reports the capabilities a template needs, and why.
func capabilities(t *Template) (need []string, reason string) {
	var iamRes, namedRes []string
	for _, id := range sortedKeys(t.Resources) {
		def := t.Resources[id]
		typ := def.Type
		a, known := awsTypes[typ]
		isIAM := (known && a.IAM) || strings.HasPrefix(typ, "HC::IAM::")
		if !isIAM {
			continue
		}
		iamRes = append(iamRes, id)
		if a.Named != "" && has(def.Properties, a.Named) {
			namedRes = append(namedRes, id)
		}
	}
	switch {
	case len(namedRes) > 0:
		return []string{"CAPABILITY_NAMED_IAM"}, "The following resource(s) require capabilities: [" + strings.Join(iamRes, ", ") + "]"
	case len(iamRes) > 0:
		return []string{"CAPABILITY_IAM"}, "The following resource(s) require capabilities: [" + strings.Join(iamRes, ", ") + "]"
	}
	return nil, ""
}

func checkCapabilities(t *Template, given []string) error {
	need, _ := capabilities(t)
	if len(need) == 0 {
		return nil
	}
	for _, g := range given {
		if g == "CAPABILITY_NAMED_IAM" || g == need[0] {
			return nil
		}
	}
	return core.Errf(http.StatusBadRequest, "InsufficientCapabilitiesException", "Requires capabilities : [%s]", need[0])
}

// ---- rendering ----

func nz(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func strList(ss []string) awsapi.Members {
	out := awsapi.Members{}
	for _, s := range ss {
		out = append(out, s)
	}
	return out
}

func awsResType(t string) string {
	if t == "HC::CloudFormation::Stack" {
		return stackType
	}
	return t
}

func tagMembers(t core.Tags) any {
	if len(t) == 0 {
		return nil
	}
	keys := make([]string, 0, len(t))
	for k := range t {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := awsapi.Members{}
	for _, k := range keys {
		out = append(out, map[string]any{"Key": k, "Value": t[k]})
	}
	return out
}

// paramMembers lists a stack's parameters, hiding NoEcho values.
func paramMembers(tmpl string, ps map[string]any) awsapi.Members {
	t, _ := Parse(tmpl)
	out := awsapi.Members{}
	for _, k := range sortedKeys(ps) {
		v := toStr(ps[k])
		if l, ok := ps[k].([]any); ok {
			parts := make([]string, len(l))
			for i, e := range l {
				parts[i] = toStr(e)
			}
			v = strings.Join(parts, ",")
		}
		if t != nil && t.Parameters[k].NoEcho {
			v = "****"
		}
		out = append(out, map[string]any{"ParameterKey": k, "ParameterValue": v})
	}
	return out
}

func (s *Service) awsStack(st Stack) map[string]any {
	m := map[string]any{
		"StackId": st.ARN, "StackName": st.Name, "Description": nz(st.Description), "CreationTime": st.CreatedAt,
		"StackStatus": st.Status, "StackStatusReason": nz(st.StatusReason), "DisableRollback": st.DisableRollback,
		"Parameters": paramMembers(st.Template, st.Parameters), "Capabilities": strList(st.Capabilities), "NotificationARNs": strList(st.NotificationARNs),
		"RoleARN": nz(st.RoleARN), "Tags": tagMembers(st.Tags), "EnableTerminationProtection": st.TerminationProtection,
		"DriftInformation":      map[string]any{"StackDriftStatus": "NOT_CHECKED"},
		"RollbackConfiguration": map[string]any{},
		"LastUpdatedTime":       st.LastUpdated,
		"DeletionTime":          st.DeletedAt,
	}
	if len(st.Capabilities) == 0 {
		delete(m, "Capabilities")
	}
	if len(st.NotificationARNs) == 0 {
		delete(m, "NotificationARNs")
	}
	if len(st.Outputs) > 0 {
		outs := awsapi.Members{}
		for _, k := range sortedKeys(st.Outputs) {
			o := map[string]any{"OutputKey": k, "OutputValue": toStr(st.Outputs[k])}
			if meta, ok := st.OutputMeta[k]; ok {
				o["Description"], o["ExportName"] = nz(meta.Description), nz(meta.Export)
			}
			outs = append(outs, o)
		}
		m["Outputs"] = outs
	}
	return m
}

// ---- stacks ----

func (s *Service) awsCreateStack(q *awsapi.Req) (any, error) {
	name := q.Param("StackName")
	if name == "" {
		return nil, verr("1 validation error detected: Value null at 'stackName' failed to satisfy constraint: Member must not be null")
	}
	if err := q.Authorize("cloudformation:CreateStack", s.stackARNPattern(name)); err != nil {
		return nil, err
	}
	if role := q.Param("RoleARN"); role != "" {
		if err := q.Authorize("iam:PassRole", role); err != nil {
			return nil, err
		}
	}
	body, err := s.templateIn(q)
	if err != nil {
		return nil, err
	}
	t, err := Parse(body)
	if err != nil {
		return nil, verr("%v", err)
	}
	caps := q.List("Capabilities")
	if err := checkCapabilities(t, caps); err != nil {
		return nil, err
	}
	onFailure := q.Param("OnFailure")
	if onFailure != "" && q.ParamBool("DisableRollback", false) {
		return nil, verr("DisableRollback and OnFailure cannot both be specified")
	}
	switch onFailure {
	case "", "ROLLBACK", "DO_NOTHING", "DELETE":
	default:
		return nil, verr("1 validation error detected: Value '%s' at 'onFailure' failed to satisfy constraint: Member must satisfy enum value set: [ROLLBACK, DELETE, DO_NOTHING]", onFailure)
	}
	st, err := s.CreateStack(q.R.Context(), q.P, StackReq{Name: name, Template: body, Params: paramsIn(q, nil), Tags: tagsIn(q), Capabilities: caps,
		RoleARN: q.Param("RoleARN"), NotificationARNs: q.List("NotificationARNs"), DisableRollback: q.ParamBool("DisableRollback", false),
		OnFailure: onFailure, Endpoint: endpoint(q), TerminationProtection: q.ParamBool("EnableTerminationProtection", false)})
	if err != nil {
		return nil, err
	}
	settle(st)
	return map[string]any{"StackId": st.ARN}, nil
}

func (s *Service) awsUpdateStack(q *awsapi.Req) (any, error) {
	st, err := s.authStack(q, "cloudformation:UpdateStack", "StackName")
	if err != nil {
		return nil, err
	}
	if role := q.Param("RoleARN"); role != "" {
		if err := q.Authorize("iam:PassRole", role); err != nil {
			return nil, err
		}
	}
	body := st.Template
	if !q.ParamBool("UsePreviousTemplate", false) {
		if body, err = s.templateIn(q); err != nil {
			return nil, err
		}
	}
	t, err := Parse(body)
	if err != nil {
		return nil, verr("%v", err)
	}
	caps := q.List("Capabilities")
	if err := checkCapabilities(t, caps); err != nil {
		return nil, err
	}
	out, err := s.UpdateStack(q.R.Context(), q.P, st.ARN, StackReq{Template: body, Params: paramsIn(q, st.Parameters), Tags: tagsIn(q), KeepTags: true,
		Capabilities: caps, RoleARN: q.Param("RoleARN"), NotificationARNs: q.List("NotificationARNs"), ErrIfNoChanges: true, Endpoint: endpoint(q)})
	if err != nil {
		return nil, err
	}
	settle(out)
	return map[string]any{"StackId": out.ARN}, nil
}

func (s *Service) awsDeleteStack(q *awsapi.Req) (any, error) {
	st, err := s.stackRef(q, "StackName")
	if err != nil {
		// DeleteStack succeeds for stacks that do not exist.
		if aerr := q.Authorize("cloudformation:DeleteStack", s.stackARNPattern(q.Param("StackName"))); aerr != nil {
			return nil, aerr
		}
		return nil, nil
	}
	if err := q.Authorize("cloudformation:DeleteStack", st.ARN); err != nil {
		return nil, err
	}
	if st.Status == "DELETE_COMPLETE" {
		return nil, nil
	}
	if role := q.Param("RoleARN"); role != "" {
		if err := q.Authorize("iam:PassRole", role); err != nil {
			return nil, err
		}
	}
	out, err := s.DeleteStack(q.P, st.ARN, q.Param("RoleARN"))
	if err == nil {
		settle(out)
	}
	return nil, err
}

func (s *Service) awsDescribeStacks(q *awsapi.Req) (any, error) {
	if q.Param("StackName") != "" {
		st, err := s.authStack(q, "cloudformation:DescribeStacks", "StackName")
		if err != nil {
			return nil, err
		}
		return map[string]any{"Stacks": awsapi.Members{s.awsStack(st)}}, nil
	}
	out := awsapi.Members{}
	for _, st := range s.allStacks(false) {
		if q.P.Permits("cloudformation:DescribeStacks", st.ARN, httpx.Access{}) {
			out = append(out, s.awsStack(st))
		}
	}
	if len(out) == 0 {
		if err := q.Authorize("cloudformation:DescribeStacks", s.stackARNPattern("*")); err != nil {
			return nil, err
		}
	}
	return map[string]any{"Stacks": out}, nil
}

func (s *Service) awsListStacks(q *awsapi.Req) (any, error) {
	if err := q.Authorize("cloudformation:ListStacks", "*"); err != nil {
		return nil, err
	}
	filter := q.List("StackStatusFilter")
	out := awsapi.Members{}
	for _, st := range s.allStacks(true) {
		if len(filter) > 0 && !slicesContains(filter, st.Status) {
			continue
		}
		out = append(out, map[string]any{"StackId": st.ARN, "StackName": st.Name, "TemplateDescription": nz(st.Description), "CreationTime": st.CreatedAt,
			"LastUpdatedTime": st.LastUpdated, "DeletionTime": st.DeletedAt, "StackStatus": st.Status, "StackStatusReason": nz(st.StatusReason),
			"DriftInformation": map[string]any{"StackDriftStatus": "NOT_CHECKED"}})
	}
	return map[string]any{"StackSummaries": out}, nil
}

func eventID(st *Stack, i int, e Event) string {
	if e.ID != "" {
		return e.ID
	}
	h := sha1.Sum([]byte(st.ARN + e.Time.String() + e.LogicalID + e.Status + string(rune(i))))
	return hex.EncodeToString(h[:16])
}

func (s *Service) awsDescribeStackEvents(q *awsapi.Req) (any, error) {
	st, err := s.authStack(q, "cloudformation:DescribeStackEvents", "StackName")
	if err != nil {
		return nil, err
	}
	out := awsapi.Members{}
	for i, e := range st.Events {
		out = append(out, map[string]any{"StackId": st.ARN, "EventId": eventID(&st, i, e), "StackName": st.Name, "LogicalResourceId": e.LogicalID,
			"PhysicalResourceId": nz(e.PhysicalID), "ResourceType": awsResType(e.Type), "Timestamp": e.Time, "ResourceStatus": e.Status,
			"ResourceStatusReason": nz(e.Reason)})
	}
	return map[string]any{"StackEvents": out}, nil
}

// orderedResources lists a stack's resources in creation order.
func orderedResources(st *Stack) []*Resource {
	var out []*Resource
	seen := map[string]bool{}
	for _, id := range st.Order {
		if r := st.Resources[id]; r != nil {
			out = append(out, r)
			seen[id] = true
		}
	}
	for _, id := range sortedKeys(st.Resources) {
		if !seen[id] {
			out = append(out, st.Resources[id])
		}
	}
	return out
}

func resStatus(r *Resource) string {
	if r.Status == "" {
		return "CREATE_COMPLETE"
	}
	return r.Status
}

func (s *Service) awsDescribeStackResources(q *awsapi.Req) (any, error) {
	var st Stack
	var err error
	if q.Param("StackName") == "" && q.Param("PhysicalResourceId") != "" {
		phys := q.Param("PhysicalResourceId")
		found := false
		for _, c := range s.allStacks(false) {
			for _, r := range c.Resources {
				if r.PhysicalID == phys {
					st, found = c, true
				}
			}
		}
		if !found {
			return nil, verr("Stack for %s does not exist", phys)
		}
		if err := q.Authorize("cloudformation:DescribeStackResources", st.ARN); err != nil {
			return nil, err
		}
	} else if st, err = s.authStack(q, "cloudformation:DescribeStackResources", "StackName"); err != nil {
		return nil, err
	}
	out := awsapi.Members{}
	for _, r := range orderedResources(&st) {
		if l := q.Param("LogicalResourceId"); l != "" && l != r.LogicalID {
			continue
		}
		if p := q.Param("PhysicalResourceId"); p != "" && p != r.PhysicalID {
			continue
		}
		out = append(out, map[string]any{"StackName": st.Name, "StackId": st.ARN, "LogicalResourceId": r.LogicalID, "PhysicalResourceId": nz(r.PhysicalID),
			"ResourceType": awsResType(r.Type), "Timestamp": r.UpdatedAt, "ResourceStatus": resStatus(r), "ResourceStatusReason": nz(r.Reason),
			"DriftInformation": map[string]any{"StackResourceDriftStatus": "NOT_CHECKED"}})
	}
	return map[string]any{"StackResources": out}, nil
}

func (s *Service) awsDescribeStackResource(q *awsapi.Req) (any, error) {
	st, err := s.authStack(q, "cloudformation:DescribeStackResource", "StackName")
	if err != nil {
		return nil, err
	}
	id := q.Param("LogicalResourceId")
	r := st.Resources[id]
	if r == nil {
		return nil, verr("Resource %s does not exist for stack %s", id, st.Name)
	}
	return map[string]any{"StackResourceDetail": map[string]any{"StackName": st.Name, "StackId": st.ARN, "LogicalResourceId": r.LogicalID,
		"PhysicalResourceId": nz(r.PhysicalID), "ResourceType": awsResType(r.Type), "LastUpdatedTimestamp": r.UpdatedAt, "ResourceStatus": resStatus(r),
		"ResourceStatusReason": nz(r.Reason), "DriftInformation": map[string]any{"StackResourceDriftStatus": "NOT_CHECKED"}}}, nil
}

func (s *Service) awsListStackResources(q *awsapi.Req) (any, error) {
	st, err := s.authStack(q, "cloudformation:ListStackResources", "StackName")
	if err != nil {
		return nil, err
	}
	out := awsapi.Members{}
	for _, r := range orderedResources(&st) {
		out = append(out, map[string]any{"LogicalResourceId": r.LogicalID, "PhysicalResourceId": nz(r.PhysicalID), "ResourceType": awsResType(r.Type),
			"LastUpdatedTimestamp": r.UpdatedAt, "ResourceStatus": resStatus(r), "ResourceStatusReason": nz(r.Reason),
			"DriftInformation": map[string]any{"StackResourceDriftStatus": "NOT_CHECKED"}})
	}
	return map[string]any{"StackResourceSummaries": out}, nil
}

func (s *Service) awsUpdateTerminationProtection(q *awsapi.Req) (any, error) {
	st, err := s.authStack(q, "cloudformation:UpdateTerminationProtection", "StackName")
	if err != nil {
		return nil, err
	}
	on := q.ParamBool("EnableTerminationProtection", false)
	_, err = store.Update(s.env.Store, cStacks, st.Name, func(c *Stack) error {
		c.TerminationProtection = on
		return nil
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"StackId": st.ARN}, nil
}

// ---- templates ----

func (s *Service) awsGetTemplate(q *awsapi.Req) (any, error) {
	if cs := q.Param("ChangeSetName"); cs != "" {
		c, err := s.findChangeSet(cs, q.Param("StackName"))
		if err != nil {
			return nil, err
		}
		if err := q.Authorize("cloudformation:GetTemplate", c.StackID); err != nil {
			return nil, err
		}
		return map[string]any{"TemplateBody": c.Template, "StagesAvailable": awsapi.Members{"Original", "Processed"}}, nil
	}
	st, err := s.authStack(q, "cloudformation:GetTemplate", "StackName")
	if err != nil {
		return nil, err
	}
	return map[string]any{"TemplateBody": st.Template, "StagesAvailable": awsapi.Members{"Original", "Processed"}}, nil
}

func (s *Service) awsGetTemplateSummary(q *awsapi.Req) (any, error) {
	var body string
	var err error
	if q.Param("TemplateBody") == "" && q.Param("TemplateURL") == "" && q.Param("StackName") != "" {
		st, err := s.authStack(q, "cloudformation:GetTemplateSummary", "StackName")
		if err != nil {
			return nil, err
		}
		body = st.Template
	} else {
		if err := q.Authorize("cloudformation:GetTemplateSummary", "*"); err != nil {
			return nil, err
		}
		if body, err = s.templateIn(q); err != nil {
			return nil, err
		}
	}
	t, err := Parse(body)
	if err != nil {
		return nil, verr("%v", err)
	}
	ps := awsapi.Members{}
	for _, k := range sortedKeys(t.Parameters) {
		d := t.Parameters[k]
		m := map[string]any{"ParameterKey": k, "ParameterType": d.Type, "NoEcho": bool(d.NoEcho), "Description": nz(d.Description)}
		if d.Default != nil {
			m["DefaultValue"] = toStr(d.Default)
		}
		if len(d.AllowedValues) > 0 {
			vals := awsapi.Members{}
			for _, v := range d.AllowedValues {
				vals = append(vals, toStr(v))
			}
			m["ParameterConstraints"] = map[string]any{"AllowedValues": vals}
		}
		ps = append(ps, m)
	}
	rts, seen := awsapi.Members{}, map[string]bool{}
	for _, id := range sortedKeys(t.Resources) {
		if typ := t.Resources[id].Type; !seen[typ] {
			seen[typ] = true
			rts = append(rts, typ)
		}
	}
	sort.Slice(rts, func(i, j int) bool { return rts[i].(string) < rts[j].(string) })
	out := map[string]any{"Parameters": ps, "Description": nz(t.Description), "ResourceTypes": rts, "Version": nz(t.AWSTemplateFormatVersion)}
	if need, reason := capabilities(t); len(need) > 0 {
		out["Capabilities"], out["CapabilitiesReason"] = strList(need), reason
	}
	return out, nil
}

func (s *Service) awsValidateTemplate(q *awsapi.Req) (any, error) {
	if err := q.Authorize("cloudformation:ValidateTemplate", "*"); err != nil {
		return nil, err
	}
	body, err := s.templateIn(q)
	if err != nil {
		return nil, err
	}
	t, err := Parse(body)
	if err != nil {
		return nil, verr("%v", err)
	}
	if _, err := order(t); err != nil {
		return nil, verr("Template format error: %v", err)
	}
	ps := awsapi.Members{}
	for _, k := range sortedKeys(t.Parameters) {
		d := t.Parameters[k]
		m := map[string]any{"ParameterKey": k, "NoEcho": bool(d.NoEcho), "Description": nz(d.Description)}
		if d.Default != nil {
			m["DefaultValue"] = toStr(d.Default)
		}
		ps = append(ps, m)
	}
	out := map[string]any{"Parameters": ps, "Description": nz(t.Description)}
	if need, reason := capabilities(t); len(need) > 0 {
		out["Capabilities"], out["CapabilitiesReason"] = strList(need), reason
	}
	return out, nil
}

// ---- change sets ----

func (s *Service) awsCreateChangeSet(q *awsapi.Req) (any, error) {
	name := q.Param("StackName")
	if name == "" {
		return nil, verr("1 validation error detected: Value null at 'stackName' failed to satisfy constraint: Member must not be null")
	}
	var arn string
	if st, err := s.find(name); err == nil {
		arn = st.ARN
	} else {
		arn = s.stackARNPattern(name)
	}
	if err := q.Authorize("cloudformation:CreateChangeSet", arn); err != nil {
		return nil, err
	}
	if role := q.Param("RoleARN"); role != "" {
		if err := q.Authorize("iam:PassRole", role); err != nil {
			return nil, err
		}
	}
	var prev map[string]any
	if st, err := s.find(name); err == nil {
		prev = st.Parameters
	}
	usePrev := q.ParamBool("UsePreviousTemplate", false)
	body := ""
	if !usePrev {
		var err error
		if body, err = s.templateIn(q); err != nil {
			return nil, err
		}
		t, err := Parse(body)
		if err != nil {
			return nil, verr("%v", err)
		}
		if err := checkCapabilities(t, q.List("Capabilities")); err != nil {
			return nil, err
		}
	}
	cs, err := s.CreateChangeSet(q.R.Context(), q.P, ChangeSetReq{
		StackReq: StackReq{Name: name, Template: body, Params: paramsIn(q, prev), Tags: tagsIn(q), Capabilities: q.List("Capabilities"), RoleARN: q.Param("RoleARN"),
			NotificationARNs: q.List("NotificationARNs"), Endpoint: endpoint(q), TerminationProtection: q.ParamBool("EnableTerminationProtection", false)},
		ChangeSetName: q.Param("ChangeSetName"), Description: q.Param("Description"), Type: q.Param("ChangeSetType"), UsePreviousTemplate: usePrev,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"Id": cs.ID, "StackId": cs.StackID}, nil
}

func (s *Service) csAuth(q *awsapi.Req, action string) (ChangeSet, error) {
	ref := q.Param("ChangeSetName")
	if ref == "" {
		return ChangeSet{}, verr("1 validation error detected: Value null at 'changeSetName' failed to satisfy constraint: Member must not be null")
	}
	cs, err := s.findChangeSet(ref, q.Param("StackName"))
	if err != nil {
		if aerr := q.Authorize(action, s.stackARNPattern(q.Param("StackName"))); aerr != nil {
			return ChangeSet{}, aerr
		}
		return ChangeSet{}, err
	}
	if err := q.Authorize(action, cs.StackID); err != nil {
		return ChangeSet{}, err
	}
	return cs, nil
}

func (s *Service) awsDescribeChangeSet(q *awsapi.Req) (any, error) {
	cs, err := s.csAuth(q, "cloudformation:DescribeChangeSet")
	if err != nil {
		return nil, err
	}
	changes := awsapi.Members{}
	for _, c := range cs.Changes {
		rc := map[string]any{"Action": c.Action, "LogicalResourceId": c.LogicalID, "PhysicalResourceId": nz(c.PhysicalID), "ResourceType": c.Type,
			"Replacement": nz(c.Replacement), "Scope": awsapi.Members{}, "Details": awsapi.Members{}}
		changes = append(changes, map[string]any{"Type": "Resource", "ResourceChange": rc})
	}
	tmpl := cs.Template
	out := map[string]any{"ChangeSetId": cs.ID, "ChangeSetName": cs.Name, "StackId": cs.StackID, "StackName": cs.StackName, "Description": nz(cs.Description),
		"Parameters": paramMembers(tmpl, cs.Params), "CreationTime": cs.CreatedAt, "ExecutionStatus": cs.ExecutionStatus, "Status": cs.Status,
		"StatusReason": nz(cs.StatusReason), "Capabilities": strList(cs.Capabilities), "Tags": tagMembers(cs.Tags), "Changes": changes,
		"IncludeNestedStacks": false, "RollbackConfiguration": map[string]any{}}
	return out, nil
}

func (s *Service) awsExecuteChangeSet(q *awsapi.Req) (any, error) {
	cs, err := s.csAuth(q, "cloudformation:ExecuteChangeSet")
	if err != nil {
		return nil, err
	}
	if cs.RoleARN != "" {
		if err := q.Authorize("iam:PassRole", cs.RoleARN); err != nil {
			return nil, err
		}
	}
	out, err := s.ExecuteChangeSet(q.P, cs.ID, "")
	if err == nil {
		settle(out)
	}
	return nil, err
}

func (s *Service) awsDeleteChangeSet(q *awsapi.Req) (any, error) {
	cs, err := s.csAuth(q, "cloudformation:DeleteChangeSet")
	if err != nil {
		return nil, err
	}
	return nil, s.DeleteChangeSet(cs.ID, "")
}

func (s *Service) awsListChangeSets(q *awsapi.Req) (any, error) {
	st, err := s.authStack(q, "cloudformation:ListChangeSets", "StackName")
	if err != nil {
		return nil, err
	}
	out := awsapi.Members{}
	for _, cs := range s.changeSetsOf(st.ARN) {
		out = append(out, map[string]any{"StackId": cs.StackID, "StackName": cs.StackName, "ChangeSetId": cs.ID, "ChangeSetName": cs.Name,
			"ExecutionStatus": cs.ExecutionStatus, "Status": cs.Status, "StatusReason": nz(cs.StatusReason), "CreationTime": cs.CreatedAt, "Description": nz(cs.Description)})
	}
	return map[string]any{"Summaries": out}, nil
}

// ---- exports ----

func (s *Service) awsListExports(q *awsapi.Req) (any, error) {
	if err := q.Authorize("cloudformation:ListExports", "*"); err != nil {
		return nil, err
	}
	out := awsapi.Members{}
	for _, e := range s.exports() {
		out = append(out, map[string]any{"ExportingStackId": e.StackID, "Name": e.Name, "Value": e.Value})
	}
	return map[string]any{"Exports": out}, nil
}

func (s *Service) awsListImports(q *awsapi.Req) (any, error) {
	if err := q.Authorize("cloudformation:ListImports", "*"); err != nil {
		return nil, err
	}
	name := q.Param("ExportName")
	found := false
	for _, e := range s.exports() {
		found = found || e.Name == name
	}
	if !found {
		return nil, verr("Export '%s' does not exist", name)
	}
	out := awsapi.Members{}
	for _, st := range s.allStacks(false) {
		if slicesContains(st.Imports, name) {
			out = append(out, st.Name)
		}
	}
	return map[string]any{"Imports": out}, nil
}
