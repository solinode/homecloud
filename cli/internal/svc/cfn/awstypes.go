package cfn

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/homecloudhq/homecloud/cli/internal/core"
)

// AWS::* resource types. Each one translates CloudFormation properties (the
// AWS resource specification) into the native HomeCloud API request, so a
// stack creates its resources through the same code, and the same permission
// checks, as the API call that would make them by hand. Ref and Fn::GetAtt
// values follow the AWS documentation for the type.

// attrView is what Ref and Fn::GetAtt see of a created resource.
type attrView struct {
	ID       string // native ID
	Physical string // the Ref value
	Attrs    map[string]any
	Props    map[string]any // the template's properties, resolved
	Account  string
	Stack    string
	Endpoint string
}

type awsType struct {
	// HC is the native type whose create/get/delete spec is used ("" for a type
	// with nothing behind it).
	HC string
	// Props turns CloudFormation properties into the native request body.
	Props func(x *xctx, in map[string]any) (map[string]any, error)
	// Create, Delete and Post replace or extend the single-request lifecycle.
	Create func(x *xctx, in map[string]any) (id string, attrs map[string]any, err error)
	Delete func(x *xctx, r *Resource) error
	Post   func(x *xctx, id string, in, attrs map[string]any) error
	// Ref is the physical ID (default: the native ID).
	Ref func(v *attrView) string
	// Att resolves Fn::GetAtt.
	Att func(v *attrView, name string) (any, bool)
	// IAM marks types that need CAPABILITY_IAM; Named lists the properties that
	// name the resource and so need CAPABILITY_NAMED_IAM.
	IAM   bool
	Named string
}

var awsTypes = map[string]awsType{}

func physicalID(x *xctx, typ string, c created, props map[string]any) string {
	if a, ok := awsTypes[typ]; ok && a.Ref != nil && c.Native != "" {
		return a.Ref(&attrView{ID: c.Native, Attrs: c.Attrs, Props: props, Account: x.Account, Stack: x.Stack, Endpoint: x.Endpoint})
	}
	return c.Native
}

// ---- property helpers ----

func sv(m map[string]any, k string) string {
	if v, ok := m[k]; ok && v != nil {
		return toStr(v)
	}
	return ""
}

func mv(m map[string]any, k string) map[string]any {
	v, _ := m[k].(map[string]any)
	return v
}

func lv(m map[string]any, k string) []any {
	v, _ := m[k].([]any)
	return v
}

func has(m map[string]any, k string) bool { v, ok := m[k]; return ok && v != nil }

func bv(m map[string]any, k string) bool {
	switch x := m[k].(type) {
	case bool:
		return x
	case string:
		return strings.EqualFold(x, "true")
	}
	return false
}

// iv reads an integer that may arrive as a number or a string.
func iv(m map[string]any, k string) (int, bool) {
	f, ok := num(m[k])
	return int(f), ok && m[k] != nil
}

// setInt copies an integer property under another name.
func setInt(dst map[string]any, name string, src map[string]any, k string) {
	if n, ok := iv(src, k); ok {
		dst[name] = n
	}
}

// tagMap reads CloudFormation tags: a list of {Key, Value} (or a map, as SSM uses).
func tagMap(v any) core.Tags {
	out := core.Tags{}
	switch t := v.(type) {
	case []any:
		for _, e := range t {
			if m, ok := e.(map[string]any); ok {
				out[sv(m, "Key")] = sv(m, "Value")
			}
		}
	case map[string]any:
		for k, e := range t {
			out[k] = toStr(e)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// doc reads a JSON document given as an object or as a JSON string.
func doc(v any) any {
	if s, ok := v.(string); ok {
		var out any
		if json.Unmarshal([]byte(s), &out) == nil {
			return out
		}
	}
	return v
}

func docString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func lastSeg(arn string) string {
	if i := strings.LastIndexAny(arn, ":/"); i >= 0 {
		return arn[i+1:]
	}
	return arn
}

func req(in map[string]any, names ...string) error {
	for _, n := range names {
		if !has(in, n) {
			return fmt.Errorf("Property validation failure: [The property {/%s} is required]", n)
		}
	}
	return nil
}

func arnAtt(name string, arn func(v *attrView) string, more map[string]func(v *attrView) any) func(v *attrView, n string) (any, bool) {
	return func(v *attrView, n string) (any, bool) {
		if n == "Arn" && arn != nil {
			return arn(v), true
		}
		if n == name {
			return v.ID, true
		}
		if f, ok := more[n]; ok {
			return f(v), true
		}
		return nil, false
	}
}

func attrStr(v *attrView, k string) string { return sv(v.Attrs, k) }

func init() {
	// A resource with nothing behind it: CDK adds AWS::CDK::Metadata to every stack.
	awsTypes["AWS::CDK::Metadata"] = awsType{}

	awsTypes["AWS::S3::Bucket"] = awsType{
		HC: "HC::S3::Bucket",
		Props: func(x *xctx, in map[string]any) (map[string]any, error) {
			name := sv(in, "BucketName")
			if name == "" {
				name = x.GenName(63, true)
			}
			out := map[string]any{"name": name}
			if mv(in, "VersioningConfiguration") != nil && sv(mv(in, "VersioningConfiguration"), "Status") == "Enabled" {
				out["versioning"] = true
			}
			if bv(in, "ObjectLockEnabled") {
				out["object_lock"] = true
			}
			if sv(in, "AccessControl") == "PublicRead" {
				out["public"] = true
			}
			if t := tagMap(in["Tags"]); t != nil {
				out["tags"] = t
			}
			return out, nil
		},
		Post: func(x *xctx, id string, in, attrs map[string]any) error {
			if w := mv(in, "WebsiteConfiguration"); w != nil {
				_, err := x.Call("PUT", "/api/v1/s3/buckets/"+esc(id)+"/website", map[string]any{"enabled": true,
					"index_document": sv(w, "IndexDocument"), "error_document": sv(w, "ErrorDocument")})
				return err
			}
			return nil
		},
		// Like AWS, a bucket that still holds objects is not deleted (no force).
		Delete: func(x *xctx, r *Resource) error {
			_, err := x.Call("DELETE", "/api/v1/s3/buckets/"+esc(r.native()), nil)
			return err
		},
		Att: arnAtt("", func(v *attrView) string { return "arn:aws:s3:::" + v.ID }, map[string]func(v *attrView) any{
			"DomainName":         func(v *attrView) any { return v.ID + ".s3.amazonaws.com" },
			"RegionalDomainName": func(v *attrView) any { return v.ID + ".s3." + core.Region + ".amazonaws.com" },
			"DualStackDomainName": func(v *attrView) any {
				return v.ID + ".s3.dualstack." + core.Region + ".amazonaws.com"
			},
			"WebsiteURL": func(v *attrView) any { return "http://" + v.ID + ".s3-website-" + core.Region + ".amazonaws.com" },
		}),
	}

	awsTypes["AWS::S3::BucketPolicy"] = awsType{
		Create: func(x *xctx, in map[string]any) (string, map[string]any, error) {
			if err := req(in, "Bucket", "PolicyDocument"); err != nil {
				return "", nil, err
			}
			b := sv(in, "Bucket")
			_, err := x.Call("PUT", "/api/v1/s3/buckets/"+esc(b)+"/policy", map[string]any{"policy": docString(in["PolicyDocument"])})
			if err != nil {
				return "", nil, err
			}
			return b, map[string]any{"bucket": b}, nil
		},
		Delete: func(x *xctx, r *Resource) error {
			_, err := x.Call("PUT", "/api/v1/s3/buckets/"+esc(r.native())+"/policy", map[string]any{"policy": ""})
			return err
		},
		Ref: func(v *attrView) string { return v.Stack + "-" + v.ID + "-policy" },
	}

	awsTypes["AWS::SQS::Queue"] = awsType{
		HC: "HC::SQS::Queue",
		Props: func(x *xctx, in map[string]any) (map[string]any, error) {
			fifo := bv(in, "FifoQueue")
			name := sv(in, "QueueName")
			if name == "" {
				name = x.GenName(75, false)
				if fifo {
					name += ".fifo"
				}
			}
			out := map[string]any{"name": name, "fifo": fifo || strings.HasSuffix(name, ".fifo")}
			setInt(out, "visibility_timeout", in, "VisibilityTimeout")
			setInt(out, "message_retention_seconds", in, "MessageRetentionPeriod")
			setInt(out, "delay_seconds", in, "DelaySeconds")
			setInt(out, "receive_wait_time_seconds", in, "ReceiveMessageWaitTimeSeconds")
			setInt(out, "max_message_size", in, "MaximumMessageSize")
			setInt(out, "kms_data_key_reuse_period_seconds", in, "KmsDataKeyReusePeriodSeconds")
			if has(in, "ContentBasedDeduplication") {
				out["content_based_deduplication"] = bv(in, "ContentBasedDeduplication")
			}
			if has(in, "DeduplicationScope") {
				out["deduplication_scope"] = sv(in, "DeduplicationScope")
			}
			if has(in, "FifoThroughputLimit") {
				out["fifo_throughput_limit"] = sv(in, "FifoThroughputLimit")
			}
			if has(in, "KmsMasterKeyId") {
				out["kms_master_key_id"] = sv(in, "KmsMasterKeyId")
			}
			if has(in, "SqsManagedSseEnabled") {
				out["sqs_managed_sse_enabled"] = bv(in, "SqsManagedSseEnabled")
			}
			if rp, ok := doc(in["RedrivePolicy"]).(map[string]any); ok {
				n, _ := iv(rp, "maxReceiveCount")
				out["redrive_policy"] = map[string]any{"dead_letter_queue": lastSeg(sv(rp, "deadLetterTargetArn")), "max_receive_count": n}
			}
			if has(in, "RedriveAllowPolicy") {
				out["redrive_allow_policy"] = docString(in["RedriveAllowPolicy"])
			}
			if t := tagMap(in["Tags"]); t != nil {
				out["tags"] = t
			}
			return out, nil
		},
		Ref: func(v *attrView) string {
			if v.Endpoint != "" {
				return v.Endpoint + "/" + v.Account + "/" + v.ID
			}
			return attrStr(v, "url")
		},
		Att: func(v *attrView, n string) (any, bool) {
			switch n {
			case "Arn":
				return core.ARN(v.Account, "sqs", v.ID), true
			case "QueueName":
				return v.ID, true
			case "QueueUrl":
				return v.Physical, true
			}
			return nil, false
		},
	}

	awsTypes["AWS::SQS::QueuePolicy"] = awsType{
		Create: func(x *xctx, in map[string]any) (string, map[string]any, error) {
			if err := req(in, "Queues", "PolicyDocument"); err != nil {
				return "", nil, err
			}
			var names []any
			for _, q := range lv(in, "Queues") {
				name := lastSeg(toStr(q))
				if _, err := x.Call("PATCH", "/api/v1/sqs/queues/"+esc(name), map[string]any{"policy": docString(in["PolicyDocument"])}); err != nil {
					return "", nil, err
				}
				names = append(names, name)
			}
			return x.Stack + "-" + x.Logical + "-" + randID(8), map[string]any{"queues": names}, nil
		},
		Delete: func(x *xctx, r *Resource) error {
			for _, q := range lv(r.Attributes, "queues") {
				if _, err := x.Call("PATCH", "/api/v1/sqs/queues/"+esc(toStr(q)), map[string]any{"policy": ""}); err != nil && !gone(err) {
					return err
				}
			}
			return nil
		},
	}

	awsTypes["AWS::SNS::Topic"] = awsType{
		HC: "HC::SNS::Topic",
		Props: func(x *xctx, in map[string]any) (map[string]any, error) {
			fifo := bv(in, "FifoTopic")
			name := sv(in, "TopicName")
			if name == "" {
				name = x.GenName(250, false)
				if fifo {
					name += ".fifo"
				}
			}
			out := map[string]any{"name": name}
			if has(in, "DisplayName") {
				out["display_name"] = sv(in, "DisplayName")
			}
			if bv(in, "ContentBasedDeduplication") {
				out["content_based_deduplication"] = true
			}
			if t := tagMap(in["Tags"]); t != nil {
				out["tags"] = t
			}
			return out, nil
		},
		Post: func(x *xctx, id string, in, attrs map[string]any) error {
			for _, s := range lv(in, "Subscription") {
				sm, _ := s.(map[string]any)
				if _, err := x.Call("POST", "/api/v1/sns/topics/"+esc(id)+"/subscriptions", map[string]any{"protocol": sv(sm, "Protocol"), "endpoint": sv(sm, "Endpoint")}); err != nil {
					return err
				}
			}
			return nil
		},
		Ref: func(v *attrView) string { return core.ARN(v.Account, "sns", v.ID) },
		Att: func(v *attrView, n string) (any, bool) {
			switch n {
			case "TopicArn":
				return core.ARN(v.Account, "sns", v.ID), true
			case "TopicName":
				return v.ID, true
			}
			return nil, false
		},
	}

	awsTypes["AWS::SNS::Subscription"] = awsType{
		Create: func(x *xctx, in map[string]any) (string, map[string]any, error) {
			if err := req(in, "TopicArn", "Protocol"); err != nil {
				return "", nil, err
			}
			body := map[string]any{"protocol": sv(in, "Protocol"), "endpoint": sv(in, "Endpoint")}
			if bv(in, "RawMessageDelivery") {
				body["raw_message_delivery"] = true
			}
			if has(in, "FilterPolicy") {
				body["filter_policy"] = doc(in["FilterPolicy"])
			}
			if has(in, "FilterPolicyScope") {
				body["filter_policy_scope"] = sv(in, "FilterPolicyScope")
			}
			if has(in, "RedrivePolicy") {
				body["redrive_policy"] = docString(in["RedrivePolicy"])
			}
			out, err := x.Call("POST", "/api/v1/sns/topics/"+esc(lastSeg(sv(in, "TopicArn")))+"/subscriptions", body)
			if err != nil {
				return "", nil, err
			}
			m, _ := out.(map[string]any)
			arn := sv(m, "arn")
			if arn == "" {
				arn = sv(m, "subscription_arn")
			}
			if arn == "" {
				return "", m, fmt.Errorf("SNS did not return the subscription ARN")
			}
			return arn, m, nil
		},
		Delete: func(x *xctx, r *Resource) error {
			_, err := x.Call("DELETE", "/api/v1/sns/subscriptions/"+url.PathEscape(r.native()), nil)
			return err
		},
		Att: func(v *attrView, n string) (any, bool) { return nil, false },
	}

	awsTypes["AWS::SNS::TopicPolicy"] = awsType{
		Create: func(x *xctx, in map[string]any) (string, map[string]any, error) {
			if err := req(in, "Topics", "PolicyDocument"); err != nil {
				return "", nil, err
			}
			var names []any
			for _, t := range lv(in, "Topics") {
				name := lastSeg(toStr(t))
				if _, err := x.Call("PATCH", "/api/v1/sns/topics/"+esc(name), map[string]any{"attributes": map[string]string{"Policy": docString(in["PolicyDocument"])}}); err != nil {
					return "", nil, err
				}
				names = append(names, name)
			}
			return x.Stack + "-" + x.Logical + "-" + randID(8), map[string]any{"topics": names}, nil
		},
		Delete: func(x *xctx, r *Resource) error {
			for _, t := range lv(r.Attributes, "topics") {
				if _, err := x.Call("PATCH", "/api/v1/sns/topics/"+esc(toStr(t)), map[string]any{"attributes": map[string]string{"Policy": ""}}); err != nil && !gone(err) {
					return err
				}
			}
			return nil
		},
	}

	awsTypes["AWS::SSM::Parameter"] = awsType{
		HC: "HC::SSM::Parameter",
		Props: func(x *xctx, in map[string]any) (map[string]any, error) {
			if err := req(in, "Type", "Value"); err != nil {
				return nil, err
			}
			name := sv(in, "Name")
			if name == "" {
				name = "CFN-" + x.Logical + "-" + randID(12)
			}
			out := map[string]any{"name": name, "type": sv(in, "Type"), "value": sv(in, "Value")}
			for k, n := range map[string]string{"Description": "description", "Tier": "tier", "AllowedPattern": "allowed_pattern", "DataType": "data_type"} {
				if has(in, k) {
					out[n] = sv(in, k)
				}
			}
			if t := tagMap(in["Tags"]); t != nil {
				out["tags"] = t
			}
			return out, nil
		},
		Att: func(v *attrView, n string) (any, bool) {
			switch n {
			case "Type":
				return v.Props["Type"], true
			case "Value":
				return v.Props["Value"], true
			}
			return nil, false
		},
	}

	awsTypes["AWS::Logs::LogGroup"] = awsType{
		HC: "HC::Logs::LogGroup",
		Props: func(x *xctx, in map[string]any) (map[string]any, error) {
			name := sv(in, "LogGroupName")
			if name == "" {
				name = x.GenName(512, false)
			}
			out := map[string]any{"name": name}
			setInt(out, "retention_days", in, "RetentionInDays")
			return out, nil
		},
		Att: arnAtt("", func(v *attrView) string {
			return "arn:aws:logs:" + core.Region + ":" + v.Account + ":log-group:" + v.ID + ":*"
		}, nil),
	}

	awsTypes["AWS::SecretsManager::Secret"] = awsType{
		HC: "HC::SecretsManager::Secret",
		Props: func(x *xctx, in map[string]any) (map[string]any, error) {
			name := sv(in, "Name")
			if name == "" {
				name = x.GenName(512, false)
			}
			out := map[string]any{"name": name}
			if has(in, "Description") {
				out["description"] = sv(in, "Description")
			}
			if has(in, "KmsKeyId") {
				out["kms_key_id"] = sv(in, "KmsKeyId")
			}
			switch {
			case has(in, "SecretString"):
				out["value"] = sv(in, "SecretString")
			case mv(in, "GenerateSecretString") != nil:
				v, err := generateSecret(mv(in, "GenerateSecretString"))
				if err != nil {
					return nil, err
				}
				out["value"] = v
			default:
				return nil, fmt.Errorf("SecretString or GenerateSecretString is required")
			}
			if t := tagMap(in["Tags"]); t != nil {
				out["tags"] = t
			}
			return out, nil
		},
		Ref: func(v *attrView) string {
			if a := attrStr(v, "arn"); a != "" {
				return a
			}
			return core.ARN(v.Account, "secretsmanager", "secret:"+v.ID)
		},
		Att: func(v *attrView, n string) (any, bool) {
			if n == "Id" {
				return v.Physical, true
			}
			return nil, false
		},
	}

	awsTypes["AWS::KMS::Key"] = awsType{
		HC: "HC::KMS::Key",
		Props: func(x *xctx, in map[string]any) (map[string]any, error) {
			out := map[string]any{}
			if has(in, "Description") {
				out["description"] = sv(in, "Description")
			}
			spec := sv(in, "KeySpec")
			if spec == "" {
				spec = sv(in, "CustomerMasterKeySpec")
			}
			if spec != "" {
				out["key_spec"] = spec
			}
			if has(in, "KeyUsage") {
				out["key_usage"] = sv(in, "KeyUsage")
			}
			if bv(in, "EnableKeyRotation") {
				out["rotation_enabled"] = true
			}
			if t := tagMap(in["Tags"]); t != nil {
				out["tags"] = t
			}
			return out, nil
		},
		Post: func(x *xctx, id string, in, attrs map[string]any) error {
			if has(in, "KeyPolicy") {
				_, err := x.Call("PUT", "/api/v1/kms/keys/"+esc(id)+"/policy", map[string]any{"policy": docString(in["KeyPolicy"])})
				return err
			}
			return nil
		},
		Att: func(v *attrView, n string) (any, bool) {
			switch n {
			case "Arn":
				if a := attrStr(v, "arn"); a != "" {
					return a, true
				}
				return core.ARN(v.Account, "kms", "key/"+v.ID), true
			case "KeyId":
				return v.ID, true
			}
			return nil, false
		},
	}

	awsTypes["AWS::KMS::Alias"] = awsType{
		Create: func(x *xctx, in map[string]any) (string, map[string]any, error) {
			if err := req(in, "AliasName", "TargetKeyId"); err != nil {
				return "", nil, err
			}
			name := sv(in, "AliasName")
			_, err := x.Call("POST", "/api/v1/kms/aliases", map[string]any{"name": name, "key_id": sv(in, "TargetKeyId")})
			return name, map[string]any{}, err
		},
		Delete: func(x *xctx, r *Resource) error {
			_, err := x.Call("DELETE", "/api/v1/kms/aliases/"+strings.TrimPrefix(r.native(), "/"), nil)
			return err
		},
	}

	awsTypes["AWS::DynamoDB::Table"] = awsType{
		HC: "HC::DynamoDB::Table",
		Props: func(x *xctx, in map[string]any) (map[string]any, error) {
			if err := req(in, "KeySchema"); err != nil {
				return nil, err
			}
			atype := map[string]string{}
			for _, a := range lv(in, "AttributeDefinitions") {
				am, _ := a.(map[string]any)
				atype[sv(am, "AttributeName")] = sv(am, "AttributeType")
			}
			keys := func(ks []any) (pk map[string]any, sk map[string]any, err error) {
				for _, k := range ks {
					km, _ := k.(map[string]any)
					n := sv(km, "AttributeName")
					t := atype[n]
					if t == "" {
						return nil, nil, fmt.Errorf("Property validation failure: [Attribute %s is used in KeySchema but not defined in AttributeDefinitions]", n)
					}
					def := map[string]any{"name": n, "type": t}
					if sv(km, "KeyType") == "RANGE" {
						sk = def
					} else {
						pk = def
					}
				}
				return
			}
			name := sv(in, "TableName")
			if name == "" {
				name = x.GenName(255, false)
			}
			pk, sk, err := keys(lv(in, "KeySchema"))
			if err != nil {
				return nil, err
			}
			out := map[string]any{"name": name, "partition_key": pk}
			if sk != nil {
				out["sort_key"] = sk
			}
			index := func(v any) (map[string]any, error) {
				im, _ := v.(map[string]any)
				ipk, isk, err := keys(lv(im, "KeySchema"))
				if err != nil {
					return nil, err
				}
				ix := map[string]any{"name": sv(im, "IndexName"), "partition_key": ipk}
				if isk != nil {
					ix["sort_key"] = isk
				}
				if p := mv(im, "Projection"); p != nil {
					pm := map[string]any{"type": sv(p, "ProjectionType")}
					if l := lv(p, "NonKeyAttributes"); l != nil {
						pm["non_key_attributes"] = l
					}
					ix["projection"] = pm
				}
				return ix, nil
			}
			for _, g := range lv(in, "GlobalSecondaryIndexes") {
				ix, err := index(g)
				if err != nil {
					return nil, err
				}
				out["global_secondary_indexes"] = append(anyList(out["global_secondary_indexes"]), ix)
			}
			for _, g := range lv(in, "LocalSecondaryIndexes") {
				ix, err := index(g)
				if err != nil {
					return nil, err
				}
				out["local_secondary_indexes"] = append(anyList(out["local_secondary_indexes"]), ix)
			}
			if s := mv(in, "StreamSpecification"); s != nil {
				out["stream_view_type"] = sv(s, "StreamViewType")
			}
			if t := mv(in, "TimeToLiveSpecification"); t != nil && bv(t, "Enabled") {
				out["ttl_attribute"] = sv(t, "AttributeName")
			}
			if bv(in, "DeletionProtectionEnabled") {
				out["deletion_protection"] = true
			}
			if p := mv(in, "PointInTimeRecoverySpecification"); p != nil && bv(p, "PointInTimeRecoveryEnabled") {
				out["point_in_time_recovery"] = true
			}
			if t := tagMap(in["Tags"]); t != nil {
				out["tags"] = t
			}
			return out, nil
		},
		Att: func(v *attrView, n string) (any, bool) {
			switch n {
			case "Arn":
				return core.ARN(v.Account, "dynamodb", "table/"+v.ID), true
			case "StreamArn":
				if a := attrStr(v, "stream_arn"); a != "" {
					return a, true
				}
				return nil, false
			}
			return nil, false
		},
	}

	awsTypes["AWS::Events::Rule"] = awsType{
		HC: "HC::Events::Rule",
		Props: func(x *xctx, in map[string]any) (map[string]any, error) {
			name := sv(in, "Name")
			if name == "" {
				name = x.GenName(64, false)
			}
			out := map[string]any{"name": name}
			if has(in, "Description") {
				out["description"] = sv(in, "Description")
			}
			if has(in, "ScheduleExpression") {
				out["schedule_expression"] = sv(in, "ScheduleExpression")
			}
			if has(in, "EventPattern") {
				out["event_pattern"] = doc(in["EventPattern"])
			}
			st := sv(in, "State")
			if st == "" {
				st = "ENABLED"
			}
			out["state"] = st
			if has(in, "RoleArn") {
				out["role_arn"] = sv(in, "RoleArn")
			}
			var targets []any
			for _, t := range lv(in, "Targets") {
				tm, _ := t.(map[string]any)
				tg := map[string]any{"id": sv(tm, "Id"), "arn": sv(tm, "Arn")}
				for k, n := range map[string]string{"Input": "input", "InputPath": "input_path", "RoleArn": "role_arn"} {
					if has(tm, k) {
						tg[n] = sv(tm, k)
					}
				}
				if d := mv(tm, "DeadLetterConfig"); d != nil {
					tg["dead_letter_arn"] = sv(d, "Arn")
				}
				if s := mv(tm, "SqsParameters"); s != nil {
					tg["message_group_id"] = sv(s, "MessageGroupId")
				}
				targets = append(targets, tg)
			}
			if targets != nil {
				out["targets"] = targets
			}
			return out, nil
		},
		Att: func(v *attrView, n string) (any, bool) {
			if n == "Arn" {
				if a := attrStr(v, "arn"); a != "" {
					return a, true
				}
				return core.ARN(v.Account, "events", "rule/"+v.ID), true
			}
			return nil, false
		},
	}

	awsTypes["AWS::StepFunctions::StateMachine"] = awsType{
		HC: "HC::StepFunctions::StateMachine",
		Props: func(x *xctx, in map[string]any) (map[string]any, error) {
			if err := req(in, "RoleArn"); err != nil {
				return nil, err
			}
			name := sv(in, "StateMachineName")
			if name == "" {
				name = x.GenName(80, false)
			}
			var def any
			switch {
			case has(in, "DefinitionString"):
				def = in["DefinitionString"]
			case has(in, "Definition"):
				def = in["Definition"]
			default:
				return nil, fmt.Errorf("Property validation failure: [DefinitionString or Definition is required]")
			}
			ds := docString(def)
			for k, v := range mv(in, "DefinitionSubstitutions") {
				ds = strings.ReplaceAll(ds, "${"+k+"}", toStr(v))
			}
			out := map[string]any{"name": name, "role_arn": sv(in, "RoleArn"), "definition": json.RawMessage(ds)}
			if !json.Valid([]byte(ds)) {
				return nil, fmt.Errorf("Invalid state machine definition: not valid JSON")
			}
			if has(in, "StateMachineType") {
				out["type"] = sv(in, "StateMachineType")
			}
			if t := tagMap(in["Tags"]); t != nil {
				out["tags"] = t
			}
			return out, nil
		},
		Ref: func(v *attrView) string {
			if a := attrStr(v, "arn"); a != "" {
				return a
			}
			return core.ARN(v.Account, "states", "stateMachine:"+v.ID)
		},
		Att: func(v *attrView, n string) (any, bool) {
			switch n {
			case "Arn":
				return v.Physical, true
			case "Name":
				return v.ID, true
			}
			return nil, false
		},
	}
}

func anyList(v any) []any {
	l, _ := v.([]any)
	return l
}

const punct = "!#$%&*()-_=+[]{}<>:?"

// generateSecret builds a Secrets Manager GenerateSecretString value.
func generateSecret(g map[string]any) (string, error) {
	n := 32
	if v, ok := iv(g, "PasswordLength"); ok {
		n = v
	}
	if n < 1 || n > 4096 {
		return "", fmt.Errorf("PasswordLength must be between 1 and 4096")
	}
	alphabet := "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	if bv(g, "ExcludeLowercase") {
		alphabet = strings.Map(func(r rune) rune {
			if r >= 'a' && r <= 'z' {
				return -1
			}
			return r
		}, alphabet)
	}
	if bv(g, "ExcludeUppercase") {
		alphabet = strings.Map(func(r rune) rune {
			if r >= 'A' && r <= 'Z' {
				return -1
			}
			return r
		}, alphabet)
	}
	if bv(g, "ExcludeNumbers") {
		alphabet = strings.Map(func(r rune) rune {
			if r >= '0' && r <= '9' {
				return -1
			}
			return r
		}, alphabet)
	}
	if !bv(g, "ExcludePunctuation") {
		alphabet += punct
	}
	if bv(g, "IncludeSpace") {
		alphabet += " "
	}
	excl := sv(g, "ExcludeCharacters")
	alphabet = strings.Map(func(r rune) rune {
		if strings.ContainsRune(excl, r) {
			return -1
		}
		return r
	}, alphabet)
	if alphabet == "" {
		return "", fmt.Errorf("GenerateSecretString excludes every character")
	}
	pw := make([]byte, n)
	rnd := core.RandHex(n * 2)
	for i := range pw {
		b, _ := strconv.ParseUint(rnd[i*2:i*2+2], 16, 8)
		pw[i] = alphabet[int(b)%len(alphabet)]
	}
	if key := sv(g, "GenerateStringKey"); key != "" {
		obj := map[string]any{}
		if t := sv(g, "SecretStringTemplate"); t != "" {
			if err := json.Unmarshal([]byte(t), &obj); err != nil {
				return "", fmt.Errorf("SecretStringTemplate is not valid JSON")
			}
		}
		obj[key] = string(pw)
		b, _ := json.Marshal(obj)
		return string(b), nil
	}
	return string(pw), nil
}
