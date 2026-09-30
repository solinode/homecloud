package cfn

import (
	"strings"

	"github.com/homecloudhq/homecloud/cli/internal/core"
)

func init() {
	awsTypes["AWS::ECR::Repository"] = awsType{
		HC: "HC::ECR::Repository",
		Props: func(x *xctx, in map[string]any) (map[string]any, error) {
			name := sv(in, "RepositoryName")
			if name == "" {
				name = strings.ToLower(x.GenName(256, true))
			}
			out := map[string]any{"name": name}
			if sv(in, "ImageTagMutability") == "IMMUTABLE" {
				out["tag_mutable"] = false
			}
			if t := tagMap(in["Tags"]); t != nil {
				out["tags"] = t
			}
			return out, nil
		},
		Att: func(v *attrView, n string) (any, bool) {
			switch n {
			case "Arn":
				return firstNonEmpty(attrStr(v, "arn"), core.ARN(v.Account, "ecr", "repository/"+v.ID)), true
			case "RepositoryUri":
				return attrStr(v, "uri"), attrStr(v, "uri") != ""
			}
			return nil, false
		},
	}

	awsTypes["AWS::CloudWatch::Alarm"] = awsType{
		HC: "HC::CloudWatch::Alarm",
		Props: func(x *xctx, in map[string]any) (map[string]any, error) {
			if err := req(in, "ComparisonOperator", "EvaluationPeriods"); err != nil {
				return nil, err
			}
			name := sv(in, "AlarmName")
			if name == "" {
				name = x.GenName(255, false)
			}
			out := map[string]any{"name": name, "comparison_operator": sv(in, "ComparisonOperator")}
			for k, n := range map[string]string{"AlarmDescription": "description", "Namespace": "namespace", "MetricName": "metric", "Statistic": "statistic",
				"ExtendedStatistic": "extended_statistic", "Unit": "unit", "TreatMissingData": "treat_missing_data"} {
				if has(in, k) {
					out[n] = sv(in, k)
				}
			}
			setInt(out, "period", in, "Period")
			setInt(out, "evaluation_periods", in, "EvaluationPeriods")
			setInt(out, "datapoints_to_alarm", in, "DatapointsToAlarm")
			if f, ok := num(in["Threshold"]); ok {
				out["threshold"] = f
			}
			if has(in, "ActionsEnabled") {
				out["actions_enabled"] = bv(in, "ActionsEnabled")
			}
			if ds := lv(in, "Dimensions"); len(ds) > 0 {
				dm := map[string]string{}
				for _, d := range ds {
					m, _ := d.(map[string]any)
					dm[sv(m, "Name")] = sv(m, "Value")
				}
				out["dimensions"] = dm
			}
			if ms := lv(in, "Metrics"); len(ms) > 0 {
				out["metrics"] = ms
			}
			for k, n := range map[string]string{"AlarmActions": "alarm_actions", "OKActions": "ok_actions", "InsufficientDataActions": "insufficient_data_actions"} {
				if l := lv(in, k); len(l) > 0 {
					out[n] = strs(l)
				}
			}
			return out, nil
		},
		Att: func(v *attrView, n string) (any, bool) {
			if n == "Arn" {
				return firstNonEmpty(attrStr(v, "arn"), core.ARN(v.Account, "cloudwatch", "alarm:"+v.ID)), true
			}
			return nil, false
		},
	}
}
