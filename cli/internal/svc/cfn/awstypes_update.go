package cfn

import (
	"fmt"
	"reflect"
	"slices"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
)

// In-place updates. A type lists the properties it can change without a
// replacement (Mutable) and how (Update); a change to any other property
// replaces the resource, which needs a generated name or a different one.
// The service behind a type decides what "in place" means, so properties the
// native API cannot change are not listed.

// differs reports whether a property has another value in the two property sets.
func differs(old, in map[string]any, keys ...string) bool {
	for _, k := range keys {
		if !reflect.DeepEqual(normalize(old[k]), normalize(in[k])) {
			return true
		}
	}
	return false
}

// patchBody is what a PATCH or PUT needs to move a resource from the old
// translated properties to the new: changed values, and for dropped keys the
// value in resets (a dropped property returns to its default).
func patchBody(np, op map[string]any, skip []string, resets map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range np {
		if !slices.Contains(skip, k) && !reflect.DeepEqual(normalize(v), normalize(op[k])) {
			out[k] = v
		}
	}
	for k := range op {
		if _, kept := np[k]; !kept && !slices.Contains(skip, k) {
			if r, ok := resets[k]; ok {
				out[k] = r
			}
		}
	}
	return out
}

// translate runs a type's property translation for a resource that exists (its name is kept).
func translate(x *xctx, typ string, r *Resource, in map[string]any) (map[string]any, error) {
	np, err := awsTypes[typ].Props(x, in)
	if err != nil {
		return nil, err
	}
	if _, ok := np["name"]; ok {
		np["name"] = r.native()
	}
	return np, nil
}

func tagsOrEmpty(v any) core.Tags {
	if t := tagMap(v); t != nil {
		return t
	}
	return core.Tags{}
}

// configure adds an Update to a type.
func configure(typ string, name string, mutable []string, update func(x *xctx, r *Resource, old, in map[string]any) (map[string]any, error)) {
	a, ok := awsTypes[typ]
	if !ok {
		panic("cfn: no type " + typ)
	}
	a.Name, a.Mutable, a.Update = name, mutable, update
	awsTypes[typ] = a
}

func init() {
	// Types without in-place updates still know the property that names them,
	// so a replacement that would collide is reported as CloudFormation does.
	for typ, name := range map[string]string{"AWS::KMS::Alias": "AliasName", "AWS::StepFunctions::StateMachine": "StateMachineName", "AWS::IAM::User": "UserName",
		"AWS::IAM::Group": "GroupName", "AWS::IAM::ManagedPolicy": "ManagedPolicyName", "AWS::IAM::InstanceProfile": "InstanceProfileName",
		"AWS::ElasticLoadBalancingV2::TargetGroup": "Name", "AWS::ElasticLoadBalancingV2::LoadBalancer": "Name", "AWS::ECR::Repository": "RepositoryName",
		"AWS::RDS::DBInstance": "DBInstanceIdentifier", "AWS::RDS::DBSubnetGroup": "DBSubnetGroupName", "AWS::Lambda::Alias": "Name"} {
		if a, ok := awsTypes[typ]; ok {
			a.Name = name
			awsTypes[typ] = a
		}
	}

	// ---- Route 53 ----
	// A record set is its name and type in a zone: changing those is a replacement
	// (of a different record), anything else rewrites the record.
	configure("AWS::Route53::RecordSet", "", []string{"TTL", "ResourceRecords", "AliasTarget", "Comment"},
		func(x *xctx, r *Resource, old, in map[string]any) (map[string]any, error) {
			rec, err := infraRecord(in)
			if err != nil {
				return nil, err
			}
			zone := sv(r.Attributes, "zone")
			if err := infraChange(x, zone, "UPSERT", rec); err != nil {
				return nil, err
			}
			return map[string]any{"zone": zone, "record": rec}, nil
		})
	configure("AWS::Route53::RecordSetGroup", "", []string{"RecordSets", "Comment"},
		func(x *xctx, r *Resource, old, in map[string]any) (map[string]any, error) {
			var recs []any
			keep := map[string]bool{}
			for _, s := range lv(in, "RecordSets") {
				sm, _ := s.(map[string]any)
				rec, err := infraRecord(sm)
				if err != nil {
					return nil, err
				}
				recs = append(recs, rec)
				keep[sv(rec, "name")+"|"+sv(rec, "type")] = true
			}
			zone := sv(r.Attributes, "zone")
			if err := infraChange(x, zone, "UPSERT", recs...); err != nil {
				return nil, err
			}
			for _, o := range lv(r.Attributes, "records") {
				om, _ := o.(map[string]any)
				if !keep[sv(om, "name")+"|"+sv(om, "type")] {
					if err := infraChange(x, zone, "DELETE", o); err != nil && !gone(err) {
						return nil, err
					}
				}
			}
			return map[string]any{"zone": zone, "records": recs}, nil
		})

	// ---- SQS ----
	configure("AWS::SQS::Queue", "QueueName", []string{"VisibilityTimeout", "MessageRetentionPeriod", "DelaySeconds", "ReceiveMessageWaitTimeSeconds",
		"MaximumMessageSize", "KmsMasterKeyId", "KmsDataKeyReusePeriodSeconds", "SqsManagedSseEnabled", "ContentBasedDeduplication", "DeduplicationScope",
		"FifoThroughputLimit", "RedrivePolicy", "RedriveAllowPolicy", "Tags"},
		func(x *xctx, r *Resource, old, in map[string]any) (map[string]any, error) {
			np, err := translate(x, "AWS::SQS::Queue", r, in)
			if err != nil {
				return nil, err
			}
			op, err := translate(x, "AWS::SQS::Queue", r, old)
			if err != nil {
				return nil, err
			}
			body := patchBody(np, op, []string{"name", "fifo"}, map[string]any{"visibility_timeout": 30, "message_retention_seconds": 345600, "delay_seconds": 0,
				"receive_wait_time_seconds": 0, "max_message_size": 262144, "kms_data_key_reuse_period_seconds": 300, "content_based_deduplication": false,
				"deduplication_scope": "queue", "fifo_throughput_limit": "perQueue", "redrive_policy": map[string]any{"dead_letter_queue": "", "max_receive_count": 0},
				"redrive_allow_policy": "", "kms_master_key_id": "", "tags": core.Tags{}})
			if len(body) == 0 {
				return nil, nil
			}
			_, err = x.Call("PATCH", "/api/v1/sqs/queues/"+esc(r.native()), body)
			return nil, err
		})

	// Policies are set on the queues and topics themselves, so a replacement
	// (which creates first) would be undone by removing the old one.
	policyOn := func(kind, field string) func(x *xctx, r *Resource, old, in map[string]any) (map[string]any, error) {
		return func(x *xctx, r *Resource, old, in map[string]any) (map[string]any, error) {
			if err := req(in, field, "PolicyDocument"); err != nil {
				return nil, err
			}
			set := func(name, policy string) error {
				body := any(map[string]any{"policy": policy})
				if kind == "topics" {
					body = map[string]any{"attributes": map[string]string{"Policy": policy}}
				}
				_, err := x.Call("PATCH", "/api/v1/"+map[string]string{"queues": "sqs", "topics": "sns"}[kind]+"/"+kind+"/"+esc(name), body)
				return err
			}
			var names []any
			keep := map[string]bool{}
			for _, q := range lv(in, field) {
				name := lastSeg(toStr(q))
				keep[name] = true
				if err := set(name, docString(in["PolicyDocument"])); err != nil {
					return nil, err
				}
				names = append(names, name)
			}
			for _, q := range lv(r.Attributes, kind) {
				if !keep[toStr(q)] {
					if err := set(toStr(q), ""); err != nil && !gone(err) {
						return nil, err
					}
				}
			}
			return map[string]any{kind: names}, nil
		}
	}
	configure("AWS::SQS::QueuePolicy", "", []string{"Queues", "PolicyDocument"}, policyOn("queues", "Queues"))
	configure("AWS::SNS::TopicPolicy", "", []string{"Topics", "PolicyDocument"}, policyOn("topics", "Topics"))

	// ---- SNS ----
	configure("AWS::SNS::Topic", "TopicName", []string{"DisplayName", "ContentBasedDeduplication", "Tags"},
		func(x *xctx, r *Resource, old, in map[string]any) (map[string]any, error) {
			body := map[string]any{}
			if differs(old, in, "DisplayName") {
				body["display_name"] = sv(in, "DisplayName")
			}
			if differs(old, in, "ContentBasedDeduplication") {
				body["attributes"] = map[string]string{"ContentBasedDeduplication": fmt.Sprint(bv(in, "ContentBasedDeduplication"))}
			}
			if differs(old, in, "Tags") {
				body["tags"] = tagsOrEmpty(in["Tags"])
			}
			if len(body) == 0 {
				return nil, nil
			}
			_, err := x.Call("PATCH", "/api/v1/sns/topics/"+esc(r.native()), body)
			return nil, err
		})

	// ---- SSM ----
	configure("AWS::SSM::Parameter", "Name", []string{"Type", "Value", "Description", "Tier", "AllowedPattern", "DataType"},
		func(x *xctx, r *Resource, old, in map[string]any) (map[string]any, error) {
			np, err := translate(x, "AWS::SSM::Parameter", r, in)
			if err != nil {
				return nil, err
			}
			np["overwrite"] = true
			if _, ok := np["description"]; !ok {
				np["description"] = ""
			}
			_, err = x.Call("PUT", "/api/v1/ssm/parameter", np)
			return nil, err
		})

	// ---- Logs ----
	configure("AWS::Logs::LogGroup", "LogGroupName", []string{"RetentionInDays", "Tags"},
		func(x *xctx, r *Resource, old, in map[string]any) (map[string]any, error) {
			if !differs(old, in, "RetentionInDays") {
				return nil, nil
			}
			days, _ := iv(in, "RetentionInDays")
			_, err := x.Call("PUT", "/api/v1/logs/groups/"+esc(r.native())+"/retention", map[string]any{"retention_days": days})
			return nil, err
		})

	// ---- Secrets Manager ----
	configure("AWS::SecretsManager::Secret", "Name", []string{"SecretString", "GenerateSecretString", "Description", "KmsKeyId", "Tags"},
		func(x *xctx, r *Resource, old, in map[string]any) (map[string]any, error) {
			name := "/api/v1/secrets/" + esc(r.native())
			body := map[string]any{}
			if differs(old, in, "Description") {
				body["description"] = sv(in, "Description")
			}
			if differs(old, in, "KmsKeyId") {
				body["kms_key_id"] = sv(in, "KmsKeyId")
			}
			if differs(old, in, "Tags") {
				body["tags"] = tagsOrEmpty(in["Tags"])
			}
			// A generated secret keeps its value until the recipe changes.
			if differs(old, in, "SecretString", "GenerateSecretString") {
				np, err := translate(x, "AWS::SecretsManager::Secret", r, in)
				if err != nil {
					return nil, err
				}
				if _, err := x.Call("PUT", name+"/value", map[string]any{"value": np["value"]}); err != nil {
					return nil, err
				}
			}
			if len(body) == 0 {
				return nil, nil
			}
			_, err := x.Call("PATCH", name, body)
			return nil, err
		})

	// ---- CloudWatch ----
	alarmProps := []string{"AlarmDescription", "ComparisonOperator", "EvaluationPeriods", "Namespace", "MetricName", "Statistic", "ExtendedStatistic", "Unit",
		"TreatMissingData", "Period", "DatapointsToAlarm", "Threshold", "ActionsEnabled", "Dimensions", "Metrics", "AlarmActions", "OKActions", "InsufficientDataActions", "Tags"}
	configure("AWS::CloudWatch::Alarm", "AlarmName", alarmProps, func(x *xctx, r *Resource, old, in map[string]any) (map[string]any, error) {
		np, err := translate(x, "AWS::CloudWatch::Alarm", r, in)
		if err != nil {
			return nil, err
		}
		_, err = x.Call("PUT", "/api/v1/cloudwatch/alarms/"+esc(r.native()), np)
		return nil, err
	})

	// ---- EventBridge ----
	configure("AWS::Events::Rule", "Name", []string{"Description", "ScheduleExpression", "EventPattern", "State", "RoleArn", "Targets", "Tags"},
		func(x *xctx, r *Resource, old, in map[string]any) (map[string]any, error) {
			np, err := translate(x, "AWS::Events::Rule", r, in)
			if err != nil {
				return nil, err
			}
			_, err = x.Call("PUT", "/api/v1/events/rules/"+esc(r.native()), np)
			return nil, err
		})

	// ---- S3 ----
	configure("AWS::S3::Bucket", "BucketName", []string{"VersioningConfiguration", "Tags", "WebsiteConfiguration"},
		func(x *xctx, r *Resource, old, in map[string]any) (map[string]any, error) {
			b := "/api/v1/s3/buckets/" + esc(r.native())
			if differs(old, in, "VersioningConfiguration") {
				on := sv(mv(in, "VersioningConfiguration"), "Status") == "Enabled"
				if _, err := x.Call("PUT", b+"/versioning", map[string]any{"enabled": on}); err != nil {
					return nil, err
				}
			}
			if differs(old, in, "Tags") {
				if _, err := x.Call("PUT", b+"/tags", map[string]any{"tags": tagsOrEmpty(in["Tags"])}); err != nil {
					return nil, err
				}
			}
			if differs(old, in, "WebsiteConfiguration") {
				body := map[string]any{"enabled": false}
				if w := mv(in, "WebsiteConfiguration"); w != nil {
					body = map[string]any{"enabled": true, "index_document": sv(w, "IndexDocument"), "error_document": sv(w, "ErrorDocument")}
				}
				if _, err := x.Call("PUT", b+"/website", body); err != nil {
					return nil, err
				}
			}
			return nil, nil
		})

	// ---- IAM ----
	configure("AWS::IAM::Role", "RoleName", []string{"AssumeRolePolicyDocument", "Description", "MaxSessionDuration", "ManagedPolicyArns", "Policies", "Tags"},
		func(x *xctx, r *Resource, old, in map[string]any) (map[string]any, error) {
			base := "/api/v1/iam/roles/" + esc(r.native())
			if differs(old, in, "AssumeRolePolicyDocument") {
				if _, err := x.Call("PUT", base+"/trust-policy", doc(in["AssumeRolePolicyDocument"])); err != nil {
					return nil, err
				}
			}
			body := map[string]any{}
			if differs(old, in, "Description") {
				body["description"] = sv(in, "Description")
			}
			if differs(old, in, "MaxSessionDuration") {
				n, ok := iv(in, "MaxSessionDuration")
				if !ok {
					n = 3600
				}
				body["max_session_duration"] = n
			}
			if differs(old, in, "Tags") {
				body["tags"] = tagsOrEmpty(in["Tags"])
			}
			if len(body) > 0 {
				if _, err := x.Call("PATCH", base, body); err != nil {
					return nil, err
				}
			}
			oldARNs, newARNs := strs(lv(old, "ManagedPolicyArns")), strs(lv(in, "ManagedPolicyArns"))
			for _, a := range newARNs {
				if !slices.Contains(oldARNs, a) {
					if _, err := x.Call("POST", base+"/policies", map[string]any{"policy": a}); err != nil {
						return nil, err
					}
				}
			}
			for _, a := range oldARNs {
				if !slices.Contains(newARNs, a) {
					if _, err := x.Call("DELETE", base+"/policies/"+esc(a), nil); err != nil && !gone(err) {
						return nil, err
					}
				}
			}
			oldPol := map[string]any{}
			for _, p := range lv(old, "Policies") {
				pm, _ := p.(map[string]any)
				oldPol[sv(pm, "PolicyName")] = pm["PolicyDocument"]
			}
			for _, p := range lv(in, "Policies") {
				pm, _ := p.(map[string]any)
				n := sv(pm, "PolicyName")
				prev, existed := oldPol[n]
				delete(oldPol, n)
				if existed && reflect.DeepEqual(normalize(doc(prev)), normalize(doc(pm["PolicyDocument"]))) {
					continue
				}
				if _, err := x.Call("PUT", base+"/inline-policies/"+esc(n), doc(pm["PolicyDocument"])); err != nil {
					return nil, err
				}
			}
			for n := range oldPol {
				if _, err := x.Call("DELETE", base+"/inline-policies/"+esc(n), nil); err != nil && !gone(err) {
					return nil, err
				}
			}
			return nil, nil
		})

	// An inline policy lives on its roles, users and groups; the name is the
	// policy's identity there, so a replacement would undo itself.
	configure("AWS::IAM::Policy", "", []string{"PolicyName", "PolicyDocument", "Roles", "Users", "Groups"},
		func(x *xctx, r *Resource, old, in map[string]any) (map[string]any, error) {
			if err := req(in, "PolicyName", "PolicyDocument"); err != nil {
				return nil, err
			}
			pn := sv(in, "PolicyName")
			attrs := map[string]any{"policy_name": pn}
			for field, kind := range map[string]string{"Roles": "roles", "Users": "users", "Groups": "groups"} {
				attrs[kind] = []any{}
				keep := map[string]bool{}
				for _, target := range lv(in, field) {
					t := toStr(target)
					keep[t] = true
					if _, err := x.Call("PUT", "/api/v1/iam/"+kind+"/"+esc(t)+"/inline-policies/"+esc(pn), doc(in["PolicyDocument"])); err != nil {
						return nil, err
					}
					attrs[kind] = append(attrs[kind].([]any), t)
				}
				oldName := sv(r.Attributes, "policy_name")
				for _, target := range lv(r.Attributes, kind) {
					if t := toStr(target); (!keep[t] || oldName != pn) && oldName != "" {
						if _, err := x.Call("DELETE", "/api/v1/iam/"+kind+"/"+esc(t)+"/inline-policies/"+esc(oldName), nil); err != nil && !gone(err) {
							return nil, err
						}
					}
				}
			}
			return attrs, nil
		})

	// ---- DynamoDB ----
	// Capacity is not metered by HomeCloud, so billing settings are accepted and have nothing to change.
	configure("AWS::DynamoDB::Table", "TableName", []string{"TimeToLiveSpecification", "StreamSpecification", "Tags", "BillingMode", "ProvisionedThroughput"},
		func(x *xctx, r *Resource, old, in map[string]any) (map[string]any, error) {
			body := map[string]any{}
			if differs(old, in, "TimeToLiveSpecification") {
				attr := ""
				if t := mv(in, "TimeToLiveSpecification"); t != nil && bv(t, "Enabled") {
					attr = sv(t, "AttributeName")
				}
				body["ttl_attribute"] = attr
			}
			if differs(old, in, "StreamSpecification") {
				view := ""
				if s := mv(in, "StreamSpecification"); s != nil {
					view = sv(s, "StreamViewType")
				}
				body["stream_view_type"] = view
			}
			if differs(old, in, "Tags") {
				body["tags"] = tagsOrEmpty(in["Tags"])
			}
			if len(body) == 0 {
				return nil, nil
			}
			_, err := x.Call("PATCH", "/api/v1/dynamodb/tables/"+esc(r.native()), body)
			return nil, err
		})

	// ---- Lambda ----
	configure("AWS::Lambda::Function", "FunctionName", []string{"Code", "Runtime", "Handler", "Description", "Timeout", "MemorySize", "Environment", "Role",
		"Layers", "Architectures", "Tags", "VpcConfig", "DeadLetterConfig", "ImageConfig", "ReservedConcurrentExecutions"},
		func(x *xctx, r *Resource, old, in map[string]any) (map[string]any, error) {
			np, err := translate(x, "AWS::Lambda::Function", r, in)
			if err != nil {
				return nil, err
			}
			name := fnPath(r.native())
			if differs(old, in, "Code") || (has(mv(in, "Code"), "ZipFile") && differs(old, in, "Runtime", "Handler")) {
				body := map[string]any{}
				switch {
				case has(np, "image_uri"):
					body["image_uri"] = np["image_uri"]
				default:
					if c, ok := np["code"].(map[string]any); ok {
						body = c
					}
				}
				if _, err := x.Call("PUT", name+"/code", body); err != nil {
					return nil, err
				}
			}
			if differs(old, in, "Runtime", "Handler", "Description", "Timeout", "MemorySize", "Environment", "Role", "Layers", "Architectures", "Tags", "VpcConfig",
				"DeadLetterConfig", "ImageConfig") {
				cfg := map[string]any{}
				for _, k := range []string{"runtime", "handler", "description", "timeout_seconds", "memory_mb", "environment", "role", "layers", "architectures", "tags",
					"subnet_id", "security_group_ids", "dead_letter_target", "image_config"} {
					if v, ok := np[k]; ok {
						cfg[k] = v
					}
				}
				// what the template dropped goes back to the default
				for k, reset := range map[string]map[string]any{"Description": {"description": ""}, "Timeout": {"timeout_seconds": 3}, "MemorySize": {"memory_mb": 128},
					"Environment": {"environment": map[string]string{}}, "Layers": {"layers": []string{}}, "Tags": {"tags": core.Tags{}},
					"VpcConfig": {"subnet_id": "", "security_group_ids": []string{}}, "DeadLetterConfig": {"dead_letter_target": ""}} {
					if has(old, k) && !has(in, k) {
						for rk, rv := range reset {
							cfg[rk] = rv
						}
					}
				}
				if _, err := x.Call("PATCH", name, cfg); err != nil {
					return nil, err
				}
			}
			if differs(old, in, "ReservedConcurrentExecutions") {
				if n, ok := iv(in, "ReservedConcurrentExecutions"); ok {
					_, err = x.Call("PUT", name+"/concurrency", map[string]any{"reserved_concurrent_executions": n})
				} else {
					_, err = x.Call("DELETE", name+"/concurrency", nil)
				}
				if err != nil {
					return nil, err
				}
			}
			return nil, nil
		})

	// ---- ECS ----
	configure("AWS::ECS::Service", "ServiceName", []string{"DesiredCount", "TaskDefinition"},
		func(x *xctx, r *Resource, old, in map[string]any) (map[string]any, error) {
			name := r.native()
			body := map[string]any{"desired_count": infraInt(in, "DesiredCount", 1), "task_definition": infraTDKey(sv(in, "TaskDefinition"))}
			if _, err := x.Call("PATCH", "/api/v1/ecs/services/"+esc(name), body); err != nil {
				return nil, err
			}
			deadline := time.Now().Add(5 * time.Minute)
			for {
				cur, err := x.Call("GET", "/api/v1/ecs/services/"+esc(name), nil)
				if err != nil {
					return nil, err
				}
				m, _ := cur.(map[string]any)
				want, _ := iv(m, "desired_count")
				got, _ := iv(m, "running_count")
				if got >= want && sv(m, "task_definition") == infraTDKey(sv(in, "TaskDefinition")) {
					return m, nil
				}
				if time.Now().After(deadline) {
					return nil, fmt.Errorf("service %s did not reach a steady state (%d of %d tasks running)", name, got, want)
				}
				select {
				case <-x.ctx.Done():
					return nil, x.ctx.Err()
				case <-time.After(time.Second):
				}
			}
		})

	// ---- Security groups ----
	// Tags and egress rules are not stored by the native model (see awstypes_ec2.go).
	configure("AWS::EC2::SecurityGroup", "GroupName", []string{"SecurityGroupIngress", "SecurityGroupEgress", "Tags"},
		func(x *xctx, r *Resource, old, in map[string]any) (map[string]any, error) {
			if !differs(old, in, "SecurityGroupIngress") {
				return nil, nil
			}
			want := []map[string]any{}
			for _, e := range lv(in, "SecurityGroupIngress") {
				if m, ok := e.(map[string]any); ok {
					if rule, ok := ingressRule(m); ok {
						want = append(want, rule)
					}
				}
			}
			key := func(m map[string]any) string {
				return fmt.Sprint(sv(m, "protocol"), "|", sv(m, "cidr"), "|", sv(m, "from_port"), "|", sv(m, "to_port"), "|", sv(m, "description"))
			}
			base := "/api/v1/vpc/security-groups/" + esc(r.native())
			cur, err := x.Call("GET", base, nil)
			if err != nil {
				return nil, err
			}
			cm, _ := cur.(map[string]any)
			have := map[string]string{} // rule key -> rule id
			for _, e := range lv(cm, "ingress") {
				if m, ok := e.(map[string]any); ok {
					have[key(m)] = sv(m, "id")
				}
			}
			wanted := map[string]bool{}
			for _, rule := range want {
				wanted[key(rule)] = true
				if _, ok := have[key(rule)]; !ok {
					if _, err := x.Call("POST", base+"/ingress", rule); err != nil {
						return nil, err
					}
				}
			}
			// Only rules this resource made are removed: SecurityGroupIngress resources add their own.
			mine := map[string]bool{}
			for _, e := range lv(old, "SecurityGroupIngress") {
				if m, ok := e.(map[string]any); ok {
					if rule, ok := ingressRule(m); ok {
						mine[key(rule)] = true
					}
				}
			}
			for k, id := range have {
				if mine[k] && !wanted[k] {
					if _, err := x.Call("DELETE", base+"/ingress/"+esc(id), nil); err != nil && !gone(err) {
						return nil, err
					}
				}
			}
			return nil, nil
		})
}
