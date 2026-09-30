package cfn_test

import (
	"strings"
	"testing"
)

func TestAlarmType(t *testing.T) {
	e := newEnv(t, false)
	tf := write(t, "alarm.yaml", `
Resources:
  Topic:
    Type: AWS::SNS::Topic
    Properties: {TopicName: alarm-topic}
  Alarm:
    Type: AWS::CloudWatch::Alarm
    Properties:
      AlarmName: high-cpu
      AlarmDescription: cpu is high
      Namespace: AWS/EC2
      MetricName: CPUUtilization
      Dimensions: [{Name: InstanceId, Value: i-123}]
      Statistic: Average
      Period: 300
      EvaluationPeriods: 2
      Threshold: 80.5
      ComparisonOperator: GreaterThanThreshold
      TreatMissingData: notBreaching
      AlarmActions: [!Ref Topic]
Outputs:
  AlarmArn: {Value: !GetAtt Alarm.Arn}
  AlarmName: {Value: !Ref Alarm}
`)
	e.AWS(t, "cloudformation", "create-stack", "--stack-name", "alarms", "--template-body", "file://"+tf)
	st := e.waitFor(t, "alarms", "CREATE_COMPLETE")
	out := outputs(st)
	if out["AlarmName"] != "high-cpu" || !strings.HasSuffix(out["AlarmArn"], ":alarm:high-cpu") {
		t.Fatalf("outputs %v", out)
	}
	al := e.AWSJSON(t, "cloudwatch", "describe-alarms", "--alarm-names", "high-cpu")["MetricAlarms"].([]any)
	if len(al) != 1 {
		t.Fatalf("alarms: %v", al)
	}
	a := al[0].(map[string]any)
	if a["Threshold"] != 80.5 || a["EvaluationPeriods"] != float64(2) || a["ComparisonOperator"] != "GreaterThanThreshold" || len(a["AlarmActions"].([]any)) != 1 || a["TreatMissingData"] != "notBreaching" {
		t.Fatalf("alarm: %v", a)
	}
	e.AWS(t, "cloudformation", "delete-stack", "--stack-name", "alarms")
	e.waitGone(t, "alarms")
	if al := e.AWSJSON(t, "cloudwatch", "describe-alarms", "--alarm-names", "high-cpu")["MetricAlarms"].([]any); len(al) != 0 {
		t.Fatalf("alarm survived: %v", al)
	}
}
