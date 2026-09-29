package cloudwatch

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi/awstest"
)

func newAWS(t *testing.T) (*awstest.Harness, *Service) {
	t.Helper()
	h := awstest.New(t)
	s, err := New(h.Env)
	if err != nil {
		t.Fatal(err)
	}
	s.RegisterAWS()
	s.Routes(h.Router)
	return h, s
}

// lockedBuffer collects a process's output while the test reads it.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func msNow(d time.Duration) string { return strconv.FormatInt(time.Now().Add(d).UnixMilli(), 10) }

func TestLogsAWSCLI(t *testing.T) {
	h, _ := newAWS(t)
	h.AWS(t, "logs", "create-log-group", "--log-group-name", "/app/web", "--tags", "team=core")
	if out, err := h.AWSErr(t, "logs", "create-log-group", "--log-group-name", "/app/web"); err == nil || !strings.Contains(out, "ResourceAlreadyExistsException") {
		t.Fatalf("duplicate group: %v %s", err, out)
	}
	h.AWS(t, "logs", "put-retention-policy", "--log-group-name", "/app/web", "--retention-in-days", "7")
	if out, err := h.AWSErr(t, "logs", "put-retention-policy", "--log-group-name", "/app/web", "--retention-in-days", "8"); err == nil || !strings.Contains(out, "InvalidParameterException") {
		t.Fatalf("bad retention: %v %s", err, out)
	}
	gs := h.AWSJSON(t, "logs", "describe-log-groups", "--log-group-name-prefix", "/app")
	g := gs["logGroups"].([]any)[0].(map[string]any)
	if g["logGroupName"] != "/app/web" || g["retentionInDays"].(float64) != 7 || !strings.HasSuffix(g["arn"].(string), ":log-group:/app/web:*") {
		t.Fatalf("group %v", g)
	}
	h.AWS(t, "logs", "create-log-stream", "--log-group-name", "/app/web", "--log-stream-name", "a")
	h.AWS(t, "logs", "create-log-stream", "--log-group-name", "/app/web", "--log-stream-name", "b")
	if out, err := h.AWSErr(t, "logs", "put-log-events", "--log-group-name", "/app/web", "--log-stream-name", "nope",
		"--log-events", "timestamp="+msNow(0)+",message=x"); err == nil || !strings.Contains(out, "ResourceNotFoundException") {
		t.Fatalf("missing stream: %v %s", err, out)
	}
	batch, _ := json.Marshal([]map[string]any{
		{"timestamp": time.Now().Add(-3 * time.Second).UnixMilli(), "message": "INFO started"},
		{"timestamp": time.Now().Add(-2 * time.Second).UnixMilli(), "message": `{"level":"ERROR","latency":250}`},
		{"timestamp": time.Now().Add(-time.Second).UnixMilli(), "message": "ERROR disk full"}})
	put := h.AWSJSON(t, "logs", "put-log-events", "--log-group-name", "/app/web", "--log-stream-name", "a", "--log-events", string(batch))
	if put["nextSequenceToken"] == nil || put["rejectedLogEventsInfo"] != nil {
		t.Fatalf("put: %v", put)
	}
	time.Sleep(20 * time.Millisecond)
	batch, _ = json.Marshal([]map[string]any{{"timestamp": time.Now().UnixMilli(), "message": `{"level":"INFO","latency":20}`}})
	h.AWS(t, "logs", "put-log-events", "--log-group-name", "/app/web", "--log-stream-name", "b", "--log-events", string(batch))

	ss := h.AWSJSON(t, "logs", "describe-log-streams", "--log-group-name", "/app/web", "--order-by", "LastEventTime", "--descending")
	streams := ss["logStreams"].([]any)
	if len(streams) != 2 || streams[0].(map[string]any)["logStreamName"] != "b" || streams[1].(map[string]any)["firstEventTimestamp"] == nil {
		t.Fatalf("streams %v", streams)
	}

	ev := h.AWSJSON(t, "logs", "get-log-events", "--log-group-name", "/app/web", "--log-stream-name", "a", "--start-from-head", "--limit", "2")
	if es := ev["events"].([]any); len(es) != 2 || es[0].(map[string]any)["message"] != "INFO started" {
		t.Fatalf("get-log-events %v", ev)
	}
	fwd := ev["nextForwardToken"].(string)
	ev = h.AWSJSON(t, "logs", "get-log-events", "--log-group-name", "/app/web", "--log-stream-name", "a", "--next-token", fwd)
	if es := ev["events"].([]any); len(es) != 1 || es[0].(map[string]any)["message"] != "ERROR disk full" {
		t.Fatalf("forward page %v", ev)
	}
	ev = h.AWSJSON(t, "logs", "get-log-events", "--log-group-name", "/app/web", "--log-stream-name", "a", "--next-token", ev["nextForwardToken"].(string))
	if es := ev["events"].([]any); len(es) != 0 {
		t.Fatalf("end of stream %v", ev)
	}

	for pattern, want := range map[string]int{
		"ERROR":                 2,
		`"disk full"`:           1,
		"?started ?disk":        2,
		"ERROR -disk":           1,
		`{ $.level = "ERROR" }`: 1,
		`{ $.latency > 100 }`:   1,
		`{ $.level = "INFO" || $.latency >= 250 }`: 2,
		`%dis[k]%`: 1,
		"":         4,
	} {
		f := h.AWSJSON(t, "logs", "filter-log-events", "--log-group-name", "/app/web", "--filter-pattern", pattern)
		if n := len(f["events"].([]any)); n != want {
			t.Errorf("pattern %q: %d events, want %d: %v", pattern, n, want, f["events"])
		}
	}
	// Pagination through the CLI's paginator (page size 1).
	f := h.AWSJSON(t, "logs", "filter-log-events", "--log-group-name", "/app/web", "--page-size", "1")
	if n := len(f["events"].([]any)); n != 4 {
		t.Fatalf("paginated filter returned %d events", n)
	}

	out := h.AWS(t, "logs", "tail", "/app/web", "--since", "10m", "--format", "short")
	if !strings.Contains(out, "disk full") || !strings.Contains(out, "INFO started") {
		t.Fatalf("tail: %s", out)
	}

	tags := h.AWSJSON(t, "logs", "list-tags-for-resource", "--resource-arn", "arn:aws:logs:us-east-1:"+h.Env.AccountID+":log-group:/app/web")
	if tags["tags"].(map[string]any)["team"] != "core" {
		t.Fatalf("tags %v", tags)
	}
	h.AWS(t, "logs", "tag-resource", "--resource-arn", "arn:aws:logs:us-east-1:"+h.Env.AccountID+":log-group:/app/web", "--tags", "env=dev")
	h.AWS(t, "logs", "untag-log-group", "--log-group-name", "/app/web", "--tags", "team")
	tags = h.AWSJSON(t, "logs", "list-tags-log-group", "--log-group-name", "/app/web")
	if tg := tags["tags"].(map[string]any); tg["env"] != "dev" || tg["team"] != nil {
		t.Fatalf("tags after update %v", tags)
	}

	start := strconv.FormatInt(time.Now().Add(-time.Hour).Unix(), 10)
	end := strconv.FormatInt(time.Now().Add(time.Minute).Unix(), 10)
	qid := h.AWSJSON(t, "logs", "start-query", "--log-group-name", "/app/web", "--start-time", start, "--end-time", end,
		"--query-string", `fields @timestamp, @message | filter ispresent(level) | stats count(*) as n, avg(latency) as lat by level | sort n desc`)["queryId"].(string)
	res := h.AWSJSON(t, "logs", "get-query-results", "--query-id", qid)
	if res["status"] != "Complete" || len(res["results"].([]any)) != 2 {
		t.Fatalf("query results %v", res)
	}
	if _, err := h.AWSErr(t, "logs", "start-query", "--log-group-name", "/app/web", "--start-time", start, "--end-time", end, "--query-string", "bogus x"); err == nil {
		t.Fatal("malformed query accepted")
	}

	h.AWS(t, "logs", "delete-log-stream", "--log-group-name", "/app/web", "--log-stream-name", "b")
	h.AWS(t, "logs", "delete-retention-policy", "--log-group-name", "/app/web")
	h.AWS(t, "logs", "delete-log-group", "--log-group-name", "/app/web")
	if out, err := h.AWSErr(t, "logs", "delete-log-group", "--log-group-name", "/app/web"); err == nil || !strings.Contains(out, "ResourceNotFoundException") {
		t.Fatalf("delete missing group: %v %s", err, out)
	}
}

func TestLogsTailFollow(t *testing.T) {
	h, _ := newAWS(t)
	h.AWS(t, "logs", "create-log-group", "--log-group-name", "follow")
	h.AWS(t, "logs", "create-log-stream", "--log-group-name", "follow", "--log-stream-name", "s")
	h.AWS(t, "logs", "put-log-events", "--log-group-name", "follow", "--log-stream-name", "s", "--log-events", "timestamp="+msNow(0)+",message=first line")
	cmd := exec.Command("aws", "logs", "tail", "follow", "--follow", "--format", "short")
	cmd.Env = append(h.Environ(h.AccessKeyID, h.SecretKey, ""), "PYTHONUNBUFFERED=1")
	out := &lockedBuffer{}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		t.Skip("aws CLI not available:", err)
	}
	defer cmd.Process.Kill()
	time.Sleep(2 * time.Second)
	h.AWS(t, "logs", "put-log-events", "--log-group-name", "follow", "--log-stream-name", "s", "--log-events", "timestamp="+msNow(0)+",message=second line")
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(out.String(), "second line") {
		time.Sleep(250 * time.Millisecond)
	}
	// Interrupt rather than kill: the CLI may block-buffer stdout when it isn't a
	// terminal (the bundled AWS CLI ignores PYTHONUNBUFFERED) and flushes on exit.
	_ = cmd.Process.Signal(os.Interrupt)
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		<-done
	}
	if s := out.String(); !strings.Contains(s, "first line") || !strings.Contains(s, "second line") || strings.Count(s, "first line") != 1 {
		t.Fatalf("tail --follow output:\n%s", s)
	}
}

func TestLogsBoto3(t *testing.T) {
	h, _ := newAWS(t)
	out := h.Python(t, `
import time
logs = boto3.client("logs")
logs.create_log_group(logGroupName="g1")
logs.create_log_stream(logGroupName="g1", logStreamName="s1")
now = int(time.time() * 1000)
r = logs.put_log_events(logGroupName="g1", logStreamName="s1", logEvents=[{"timestamp": now - 20*86400*1000, "message": "too old"}])
print("old", json.dumps(r["rejectedLogEventsInfo"], sort_keys=True))
r = logs.put_log_events(logGroupName="g1", logStreamName="s1", logEvents=[
    {"timestamp": now, "message": "ok 1"},
    {"timestamp": now + 1, "message": "ok 2"},
    {"timestamp": now + 5*3600*1000, "message": "too new"},
])
print("rejected", json.dumps(r["rejectedLogEventsInfo"], sort_keys=True))
try:
    logs.put_log_events(logGroupName="g1", logStreamName="s1", logEvents=[{"timestamp": now - 30*3600*1000, "message": "a"}, {"timestamp": now, "message": "b"}])
except botocore.exceptions.ClientError as e:
    print("span", e.response["Error"]["Code"])
try:
    logs.put_log_events(logGroupName="g1", logStreamName="s1", logEvents=[{"timestamp": now, "message": "b"}, {"timestamp": now - 1000, "message": "a"}])
except botocore.exceptions.ClientError as e:
    print("order", e.response["Error"]["Code"])
for i in range(3):
    logs.create_log_group(logGroupName="pg%d" % i)
pages = list(logs.get_paginator("describe_log_groups").paginate(logGroupNamePrefix="pg", PaginationConfig={"PageSize": 1}))
print("pages", len(pages), sum(len(p["logGroups"]) for p in pages))
ev = logs.filter_log_events(logGroupName="g1")["events"]
print("events", [e["message"] for e in ev], all("eventId" in e and "ingestionTime" in e for e in ev))
try:
    logs.get_log_events(logGroupName="nope", logStreamName="x")
except logs.exceptions.ResourceNotFoundException as e:
    print("missing", e.response["Error"]["Code"])
`)
	for _, want := range []string{`old {"tooOldLogEventEndIndex": 0}`, `rejected {"tooNewLogEventStartIndex": 2}`, "span InvalidParameterException", "order InvalidParameterException",
		"pages 3 3", "events ['ok 1', 'ok 2'] True", "missing ResourceNotFoundException"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestLogsPermissions(t *testing.T) {
	h, _ := newAWS(t)
	h.AWS(t, "logs", "create-log-group", "--log-group-name", "mine")
	h.AWS(t, "logs", "create-log-group", "--log-group-name", "theirs")
	h.Native(t, "POST", "/api/v1/iam/policies", map[string]any{"name": "logs-mine", "document": map[string]any{"Version": "2012-10-17",
		"Statement": []any{map[string]any{"Effect": "Allow", "Action": []string{"logs:CreateLogStream", "logs:PutLogEvents", "logs:GetLogEvents", "logs:FilterLogEvents"},
			"Resource": "arn:aws:logs:*:*:log-group:mine:*"}}}})
	akid, secret := h.User(t, "writer", "logs-mine")
	if out, err := h.AWSAs(t, akid, secret, "", "logs", "create-log-stream", "--log-group-name", "mine", "--log-stream-name", "s"); err != nil {
		t.Fatalf("allowed create-log-stream: %v %s", err, out)
	}
	if out, err := h.AWSAs(t, akid, secret, "", "logs", "put-log-events", "--log-group-name", "mine", "--log-stream-name", "s",
		"--log-events", "timestamp="+msNow(0)+",message=hi"); err != nil {
		t.Fatalf("allowed put: %v %s", err, out)
	}
	if out, err := h.AWSAs(t, akid, secret, "", "logs", "filter-log-events", "--log-group-name", "theirs"); err == nil || !strings.Contains(out, "AccessDenied") {
		t.Fatalf("denied filter: %v %s", err, out)
	}
	if out, err := h.AWSAs(t, akid, secret, "", "logs", "delete-log-group", "--log-group-name", "mine"); err == nil || !strings.Contains(out, "AccessDenied") {
		t.Fatalf("denied delete: %v %s", err, out)
	}
}

func TestCloudWatchAWSCLI(t *testing.T) {
	h, s := newAWS(t)
	now := time.Now().UTC()
	ts := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339) }
	h.AWS(t, "cloudwatch", "put-metric-data", "--namespace", "App", "--metric-data",
		`[{"MetricName":"Latency","Dimensions":[{"Name":"Service","Value":"api"}],"Value":100,"Unit":"Milliseconds","Timestamp":"`+ts(-2*time.Minute)+`"},
		  {"MetricName":"Latency","Dimensions":[{"Name":"Service","Value":"api"}],"Value":300,"Unit":"Milliseconds","Timestamp":"`+ts(-2*time.Minute)+`"},
		  {"MetricName":"Latency","Dimensions":[{"Name":"Service","Value":"api"}],"StatisticValues":{"SampleCount":2,"Sum":1000,"Minimum":400,"Maximum":600},"Unit":"Milliseconds","Timestamp":"`+ts(-time.Minute)+`"},
		  {"MetricName":"Requests","Dimensions":[{"Name":"Service","Value":"api"}],"Values":[1,5],"Counts":[3,1],"Timestamp":"`+ts(-time.Minute)+`"}]`)
	if out, err := h.AWSErr(t, "cloudwatch", "put-metric-data", "--namespace", "AWS/EC2", "--metric-name", "x", "--value", "1"); err == nil || !strings.Contains(out, "InvalidParameterValue") {
		t.Fatalf("reserved namespace: %v %s", err, out)
	}
	if out, err := h.AWSErr(t, "cloudwatch", "put-metric-data", "--namespace", "App", "--metric-data", `[{"MetricName":"x"}]`); err == nil || !strings.Contains(out, "InvalidParameterCombination") {
		t.Fatalf("missing value: %v %s", err, out)
	}
	st := h.AWSJSON(t, "cloudwatch", "get-metric-statistics", "--namespace", "App", "--metric-name", "Latency", "--dimensions", "Name=Service,Value=api",
		"--start-time", ts(-10*time.Minute), "--end-time", ts(time.Minute), "--period", "60", "--statistics", "Average", "Maximum", "SampleCount", "--extended-statistics", "p50")
	dps := st["Datapoints"].([]any)
	if len(dps) != 2 {
		t.Fatalf("datapoints %v", st)
	}
	var total float64
	for _, d := range dps {
		m := d.(map[string]any)
		total += m["SampleCount"].(float64)
		if m["Unit"] != "Milliseconds" || m["ExtendedStatistics"].(map[string]any)["p50"] == nil {
			t.Fatalf("datapoint %v", m)
		}
	}
	if total != 4 {
		t.Fatalf("sample count %v", total)
	}

	lm := h.AWSJSON(t, "cloudwatch", "list-metrics", "--namespace", "App", "--dimensions", "Name=Service")
	if n := len(lm["Metrics"].([]any)); n != 2 {
		t.Fatalf("list-metrics %v", lm)
	}

	q := `[{"Id":"lat","MetricStat":{"Metric":{"Namespace":"App","MetricName":"Latency","Dimensions":[{"Name":"Service","Value":"api"}]},"Period":60,"Stat":"Sum"},"ReturnData":false},
	       {"Id":"req","MetricStat":{"Metric":{"Namespace":"App","MetricName":"Requests","Dimensions":[{"Name":"Service","Value":"api"}]},"Period":60,"Stat":"SampleCount"},"ReturnData":false},
	       {"Id":"avg","Expression":"lat / 4","Label":"quarter"},
	       {"Id":"total","Expression":"SUM(METRICS())"}]`
	md := h.AWSJSON(t, "cloudwatch", "get-metric-data", "--metric-data-queries", q, "--start-time", ts(-10*time.Minute), "--end-time", ts(time.Minute), "--scan-by", "TimestampAscending")
	res := md["MetricDataResults"].([]any)
	if len(res) != 2 {
		t.Fatalf("get-metric-data %v", md)
	}
	r0 := res[0].(map[string]any)
	if r0["Label"] != "quarter" || len(r0["Values"].([]any)) != 2 || r0["Values"].([]any)[0].(float64) != 100 || r0["Values"].([]any)[1].(float64) != 250 {
		t.Fatalf("expression result %v", r0)
	}
	if out, err := h.AWSErr(t, "cloudwatch", "get-metric-data", "--metric-data-queries", `[{"Id":"e","Expression":"e + 1"}]`,
		"--start-time", ts(-time.Hour), "--end-time", ts(0)); err == nil {
		t.Fatalf("self-referencing expression accepted: %s", out)
	}

	// Alarms.
	h.AWS(t, "cloudwatch", "put-metric-alarm", "--alarm-name", "high-latency", "--namespace", "App", "--metric-name", "Latency",
		"--dimensions", "Name=Service,Value=api", "--statistic", "Maximum", "--period", "60", "--evaluation-periods", "1",
		"--threshold", "500", "--comparison-operator", "GreaterThanThreshold", "--tags", "Key=team,Value=core", "--treat-missing-data", "notBreaching")
	if out, err := h.AWSErr(t, "cloudwatch", "put-metric-alarm", "--alarm-name", "bad", "--namespace", "App", "--metric-name", "Latency",
		"--statistic", "Average", "--period", "45", "--evaluation-periods", "1", "--threshold", "1", "--comparison-operator", "GreaterThanThreshold"); err == nil {
		t.Fatalf("bad period accepted: %s", out)
	}
	s.evaluateAlarms()
	al := h.AWSJSON(t, "cloudwatch", "describe-alarms", "--alarm-names", "high-latency")
	a := al["MetricAlarms"].([]any)[0].(map[string]any)
	if a["StateValue"] != "ALARM" || a["ActionsEnabled"] != true || a["Dimensions"].([]any)[0].(map[string]any)["Value"] != "api" {
		t.Fatalf("alarm %v", a)
	}
	h.AWS(t, "cloudwatch", "set-alarm-state", "--alarm-name", "high-latency", "--state-value", "OK", "--state-reason", "testing")
	h.AWS(t, "cloudwatch", "disable-alarm-actions", "--alarm-names", "high-latency")
	al = h.AWSJSON(t, "cloudwatch", "describe-alarms", "--state-value", "OK")
	if a := al["MetricAlarms"].([]any); len(a) != 1 || a[0].(map[string]any)["ActionsEnabled"] != false {
		t.Fatalf("describe by state %v", al)
	}
	hist := h.AWSJSON(t, "cloudwatch", "describe-alarm-history", "--alarm-name", "high-latency", "--history-item-type", "StateUpdate")
	if n := len(hist["AlarmHistoryItems"].([]any)); n != 2 {
		t.Fatalf("history %v", hist)
	}
	arn := a["AlarmArn"].(string)
	tags := h.AWSJSON(t, "cloudwatch", "list-tags-for-resource", "--resource-arn", arn)
	if tg := tags["Tags"].([]any); len(tg) != 1 || tg[0].(map[string]any)["Value"] != "core" {
		t.Fatalf("tags %v", tags)
	}
	h.AWS(t, "cloudwatch", "untag-resource", "--resource-arn", arn, "--tag-keys", "team")
	if out, err := h.AWSErr(t, "cloudwatch", "delete-alarms", "--alarm-names", "high-latency", "nope"); err == nil || !strings.Contains(out, "ResourceNotFound") {
		t.Fatalf("delete missing alarm: %v %s", err, out)
	}
	h.AWS(t, "cloudwatch", "delete-alarms", "--alarm-names", "high-latency")

	// Dashboards.
	h.AWS(t, "cloudwatch", "put-dashboard", "--dashboard-name", "main", "--dashboard-body", `{"widgets":[]}`)
	if out, err := h.AWSErr(t, "cloudwatch", "put-dashboard", "--dashboard-name", "bad", "--dashboard-body", `{"nope":1}`); err == nil || !strings.Contains(out, "InvalidParameterInput") {
		t.Fatalf("bad dashboard: %v %s", err, out)
	}
	d := h.AWSJSON(t, "cloudwatch", "get-dashboard", "--dashboard-name", "main")
	if d["DashboardBody"] != `{"widgets":[]}` || !strings.HasSuffix(d["DashboardArn"].(string), ":dashboard/main") {
		t.Fatalf("dashboard %v", d)
	}
	if l := h.AWSJSON(t, "cloudwatch", "list-dashboards"); len(l["DashboardEntries"].([]any)) != 1 {
		t.Fatalf("list-dashboards %v", l)
	}
	h.AWS(t, "cloudwatch", "delete-dashboards", "--dashboard-names", "main")
	if out, err := h.AWSErr(t, "cloudwatch", "get-dashboard", "--dashboard-name", "main"); err == nil || !strings.Contains(out, "ResourceNotFound") {
		t.Fatalf("deleted dashboard: %v %s", err, out)
	}
}

// TestCloudWatchQuery drives the awsQuery protocol (older SDKs, Terraform) with signed form requests.
func TestCloudWatchQuery(t *testing.T) {
	h, _ := newAWS(t)
	out := h.Python(t, `
import urllib.request, os, datetime
from botocore.auth import SigV4Auth
from botocore.awsrequest import AWSRequest
creds = boto3.Session().get_credentials()
def call(params):
    body = "&".join("%s=%s" % (k, urllib.parse.quote(str(v), safe="")) for k, v in params.items())
    req = AWSRequest(method="POST", url=os.environ["AWS_ENDPOINT_URL"] + "/", data=body, headers={"Content-Type": "application/x-www-form-urlencoded; charset=utf-8"})
    SigV4Auth(creds, "monitoring", "us-east-1").add_auth(req)
    r = urllib.request.Request(req.url, data=body.encode(), headers=dict(req.headers), method="POST")
    try:
        return urllib.request.urlopen(r).read().decode()
    except urllib.error.HTTPError as e:
        return e.read().decode()
now = datetime.datetime.utcnow()
print(call({"Action": "PutMetricData", "Version": "2010-08-01", "Namespace": "Q", "MetricData.member.1.MetricName": "m",
    "MetricData.member.1.Value": "42", "MetricData.member.1.Dimensions.member.1.Name": "d", "MetricData.member.1.Dimensions.member.1.Value": "v",
    "MetricData.member.1.Timestamp": now.strftime("%Y-%m-%dT%H:%M:%SZ")}))
print(call({"Action": "GetMetricStatistics", "Version": "2010-08-01", "Namespace": "Q", "MetricName": "m",
    "Dimensions.member.1.Name": "d", "Dimensions.member.1.Value": "v", "Period": "60", "Statistics.member.1": "Sum",
    "StartTime": (now - datetime.timedelta(minutes=5)).strftime("%Y-%m-%dT%H:%M:%SZ"), "EndTime": (now + datetime.timedelta(minutes=1)).strftime("%Y-%m-%dT%H:%M:%SZ")}))
print(call({"Action": "PutMetricAlarm", "Version": "2010-08-01", "AlarmName": "qa", "Namespace": "Q", "MetricName": "m", "Statistic": "Sum",
    "Period": "60", "EvaluationPeriods": "1", "Threshold": "10", "ComparisonOperator": "GreaterThanThreshold"}))
print(call({"Action": "DescribeAlarms", "Version": "2010-08-01", "AlarmNames.member.1": "qa"}))
print(call({"Action": "DescribeAlarms", "Version": "2010-08-01", "MaxRecords": "x"}))
`)
	for _, want := range []string{"<PutMetricDataResponse", "<Sum>42</Sum>", "<Unit>None</Unit>", "<PutMetricAlarmResponse",
		"<MetricAlarms><member><AlarmName>qa</AlarmName>", "<Threshold>10</Threshold>", "<Code>InvalidParameterValue</Code>"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestCloudWatchBoto3(t *testing.T) {
	h, _ := newAWS(t)
	out := h.Python(t, `
import datetime
cw = boto3.client("cloudwatch")
now = datetime.datetime.now(datetime.timezone.utc)
# Keep all points inside one 5-minute period so they aggregate into one value.
base = now.replace(minute=now.minute - now.minute % 5, second=30, microsecond=0)
if base > now:
    base -= datetime.timedelta(minutes=5)
cw.put_metric_data(Namespace="B", MetricData=[{"MetricName": "hits", "Value": v, "Timestamp": base + datetime.timedelta(seconds=5*i), "StorageResolution": 1} for i, v in enumerate([1, 2, 3])])
r = cw.get_metric_data(MetricDataQueries=[{"Id": "h", "MetricStat": {"Metric": {"Namespace": "B", "MetricName": "hits"}, "Period": 300, "Stat": "Sum"}},
    {"Id": "twice", "Expression": "h * 2"}], StartTime=now - datetime.timedelta(minutes=30), EndTime=now + datetime.timedelta(minutes=5))
print("gmd", [(x["Id"], [float(v) for v in x["Values"]]) for x in r["MetricDataResults"]])
try:
    cw.describe_alarm_history(AlarmName="x", MaxRecords=5)
    cw.get_dashboard(DashboardName="none")
except botocore.exceptions.ClientError as e:
    print("dashboard", e.response["Error"]["Code"])
p = cw.get_paginator("describe_alarms")
print("pages", sum(len(x["MetricAlarms"]) for x in p.paginate()))
`)
	for _, want := range []string{"gmd [('h', [6.0]), ('twice', [12.0])]", "dashboard ResourceNotFound", "pages 0"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	_ = json.Valid
}
