package trail_test

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi/awstest"
	"github.com/homecloudhq/homecloud/cli/internal/svc/trail"
)

func newTrail(t *testing.T) (*awstest.Harness, *trail.Service) {
	t.Helper()
	h := awstest.New(t)
	tr, err := trail.New(h.Env)
	if err != nil {
		t.Fatal(err)
	}
	tr.RegisterAWS()
	h.AuditHook = tr.Record
	return h, tr
}

func events(t *testing.T, h *awstest.Harness, args ...string) []map[string]any {
	t.Helper()
	out := h.AWSJSON(t, append([]string{"cloudtrail", "lookup-events"}, args...)...)
	var evs []map[string]any
	for _, e := range out["Events"].([]any) {
		evs = append(evs, e.(map[string]any))
	}
	return evs
}

func TestLookupEvents(t *testing.T) {
	h, _ := newTrail(t)
	// AWS API calls made through the wire protocol are recorded.
	h.AWS(t, "iam", "create-user", "--user-name", "alice")
	h.AWS(t, "iam", "list-users")

	evs := events(t, h, "--lookup-attributes", "AttributeKey=EventName,AttributeValue=CreateUser")
	if len(evs) != 1 {
		t.Fatalf("CreateUser events: %v", evs)
	}
	e := evs[0]
	if e["EventSource"] != "iam.amazonaws.com" || e["Username"] == "" || e["ReadOnly"] != "false" || e["AccessKeyId"] != h.AccessKeyID {
		t.Fatalf("event %v", e)
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(e["CloudTrailEvent"].(string)), &rec); err != nil {
		t.Fatal(err)
	}
	ident := rec["userIdentity"].(map[string]any)
	if rec["eventName"] != "CreateUser" || rec["awsRegion"] != "us-east-1" || rec["eventType"] != "AwsApiCall" || rec["requestID"] == "" ||
		ident["accessKeyId"] != h.AccessKeyID || ident["accountId"] != h.Env.AccountID || rec["eventID"] != e["EventId"] {
		t.Fatalf("record %v", rec)
	}
	res := e["Resources"].([]any)[0].(map[string]any)
	if res["ResourceType"] != "AWS::IAM::User" || !strings.HasSuffix(res["ResourceName"].(string), ":user/alice") {
		t.Fatalf("resources %v", e["Resources"])
	}

	if got := events(t, h, "--lookup-attributes", "AttributeKey=ResourceName,AttributeValue=alice"); len(got) != 1 {
		t.Fatalf("by resource name: %v", got)
	}
	if got := events(t, h, "--lookup-attributes", "AttributeKey=ResourceType,AttributeValue=AWS::IAM::User"); len(got) != 1 {
		t.Fatalf("by resource type: %v", got)
	}
	if got := events(t, h, "--lookup-attributes", "AttributeKey=EventSource,AttributeValue=iam.amazonaws.com"); len(got) < 2 {
		t.Fatalf("by source: %v", got)
	}
	if got := events(t, h, "--lookup-attributes", "AttributeKey=AccessKeyId,AttributeValue="+h.AccessKeyID); len(got) < 2 {
		t.Fatalf("by access key: %v", got)
	}
	for _, ro := range events(t, h, "--lookup-attributes", "AttributeKey=ReadOnly,AttributeValue=true") {
		if ro["ReadOnly"] != "true" {
			t.Fatalf("read-only filter: %v", ro)
		}
	}
	if got := events(t, h, "--lookup-attributes", "AttributeKey=EventName,AttributeValue=ListUsers"); len(got) != 1 || got[0]["ReadOnly"] != "true" {
		t.Fatalf("ListUsers: %v", got)
	}
	user := e["Username"].(string)
	if got := events(t, h, "--lookup-attributes", "AttributeKey=Username,AttributeValue="+user); len(got) < 2 {
		t.Fatalf("by user: %v", got)
	}
	if got := events(t, h, "--lookup-attributes", "AttributeKey=Username,AttributeValue=nobody"); len(got) != 0 {
		t.Fatalf("unknown user: %v", got)
	}
	// Time ranges.
	if got := events(t, h, "--start-time", "2000-01-01", "--end-time", "2000-01-02"); len(got) != 0 {
		t.Fatalf("old range: %v", got)
	}
	if got := events(t, h, "--start-time", "2000-01-01"); len(got) < 3 {
		t.Fatalf("since 2000: %v", got)
	}
	// Errors.
	if o, err := h.AWSErr(t, "cloudtrail", "lookup-events", "--lookup-attributes", "AttributeKey=Nope,AttributeValue=x"); err == nil || !strings.Contains(o, "InvalidLookupAttributes") {
		t.Fatalf("bad key: %v %s", err, o)
	}
	if o, err := h.AWSErr(t, "cloudtrail", "lookup-events", "--start-time", "2030-01-02", "--end-time", "2030-01-01"); err == nil || !strings.Contains(o, "InvalidTimeRange") {
		t.Fatalf("bad range: %v %s", err, o)
	}
	if o, err := h.AWSErr(t, "cloudtrail", "lookup-events", "--next-token", "bogus"); err == nil || !strings.Contains(o, "InvalidNextToken") {
		t.Fatalf("bad token: %v %s", err, o)
	}

	// Failed calls carry the error code.
	h.AWSErr(t, "iam", "get-user", "--user-name", "ghost")
	got := events(t, h, "--lookup-attributes", "AttributeKey=EventName,AttributeValue=GetUser")
	if len(got) == 0 || !strings.Contains(got[0]["CloudTrailEvent"].(string), `"errorCode":"`) {
		t.Fatalf("failed call: %v", got)
	}
}

func TestLookupEventsPagination(t *testing.T) {
	h, _ := newTrail(t)
	for i := 0; i < 7; i++ {
		h.AWS(t, "iam", "create-user", "--user-name", "u"+string(rune('a'+i)))
	}
	seen := map[string]bool{}
	token := ""
	pages := 0
	for {
		args := []string{"cloudtrail", "lookup-events", "--max-results", "3", "--lookup-attributes", "AttributeKey=EventName,AttributeValue=CreateUser"}
		if token != "" {
			args = append(args, "--next-token", token)
		}
		out := h.AWSJSON(t, args...)
		for _, e := range out["Events"].([]any) {
			id := e.(map[string]any)["EventId"].(string)
			if seen[id] {
				t.Fatalf("duplicate %s", id)
			}
			seen[id] = true
		}
		pages++
		if out["NextToken"] == nil {
			break
		}
		token = out["NextToken"].(string)
		if pages > 10 {
			t.Fatal("too many pages")
		}
	}
	if len(seen) != 7 || pages != 3 {
		t.Fatalf("saw %d events in %d pages", len(seen), pages)
	}
}

func TestTrailLifecycleAndDelivery(t *testing.T) {
	h, tr := newTrail(t)
	var mu sync.Mutex
	files := map[string][]byte{}
	tr.BucketExists = func(b string) (bool, error) { return b == "logs", nil }
	tr.Deliver = func(bucket, key string, body []byte) error {
		mu.Lock()
		defer mu.Unlock()
		files[bucket+"/"+key] = body
		return nil
	}

	if o, err := h.AWSErr(t, "cloudtrail", "create-trail", "--name", "audit", "--s3-bucket-name", "missing"); err == nil || !strings.Contains(o, "S3BucketDoesNotExist") {
		t.Fatalf("missing bucket: %v %s", err, o)
	}
	if o, err := h.AWSErr(t, "cloudtrail", "create-trail", "--name", "a", "--s3-bucket-name", "logs"); err == nil || !strings.Contains(o, "InvalidTrailName") {
		t.Fatalf("short name: %v %s", err, o)
	}
	c := h.AWSJSON(t, "cloudtrail", "create-trail", "--name", "audit", "--s3-bucket-name", "logs", "--s3-key-prefix", "pfx",
		"--is-multi-region-trail", "--enable-log-file-validation", "--tags-list", "Key=env,Value=qa")
	arn := c["TrailARN"].(string)
	if c["Name"] != "audit" || c["S3BucketName"] != "logs" || c["S3KeyPrefix"] != "pfx" || c["IsMultiRegionTrail"] != true ||
		!strings.HasSuffix(arn, ":trail/audit") {
		t.Fatalf("create %v", c)
	}
	if o, err := h.AWSErr(t, "cloudtrail", "create-trail", "--name", "audit", "--s3-bucket-name", "logs"); err == nil || !strings.Contains(o, "TrailAlreadyExists") {
		t.Fatalf("duplicate: %v %s", err, o)
	}
	if st := h.AWSJSON(t, "cloudtrail", "get-trail-status", "--name", "audit"); st["IsLogging"] != false {
		t.Fatalf("new trail logging: %v", st)
	}
	if d := h.AWSJSON(t, "cloudtrail", "describe-trails"); len(d["trailList"].([]any)) != 1 {
		t.Fatalf("describe %v", d)
	}
	if g := h.AWSJSON(t, "cloudtrail", "get-trail", "--name", arn)["Trail"].(map[string]any); g["Name"] != "audit" {
		t.Fatalf("get %v", g)
	}
	if l := h.AWSJSON(t, "cloudtrail", "list-trails"); len(l["Trails"].([]any)) != 1 {
		t.Fatalf("list %v", l)
	}
	u := h.AWSJSON(t, "cloudtrail", "update-trail", "--name", "audit", "--s3-key-prefix", "p2", "--no-is-multi-region-trail")
	if u["S3KeyPrefix"] != "p2" || u["IsMultiRegionTrail"] != false {
		t.Fatalf("update %v", u)
	}
	h.AWS(t, "cloudtrail", "add-tags", "--resource-id", arn, "--tags-list", "Key=team,Value=core")
	h.AWS(t, "cloudtrail", "remove-tags", "--resource-id", arn, "--tags-list", "Key=env")
	tags := h.AWSJSON(t, "cloudtrail", "list-tags", "--resource-id-list", arn)["ResourceTagList"].([]any)[0].(map[string]any)["TagsList"].([]any)
	if len(tags) != 1 || tags[0].(map[string]any)["Key"] != "team" {
		t.Fatalf("tags %v", tags)
	}
	if es := h.AWSJSON(t, "cloudtrail", "get-event-selectors", "--trail-name", "audit"); len(es["EventSelectors"].([]any)) != 1 {
		t.Fatalf("event selectors %v", es)
	}
	if o, err := h.AWSErr(t, "cloudtrail", "get-insight-selectors", "--trail-name", "audit"); err == nil || !strings.Contains(o, "InsightNotEnabled") {
		t.Fatalf("insights: %v %s", err, o)
	}

	// Events before StartLogging are not delivered; events while logging are, on stop.
	h.AWS(t, "iam", "create-user", "--user-name", "before")
	h.AWS(t, "cloudtrail", "start-logging", "--name", "audit")
	if st := h.AWSJSON(t, "cloudtrail", "get-trail-status", "--name", "audit"); st["IsLogging"] != true || st["StartLoggingTime"] == nil {
		t.Fatalf("status %v", st)
	}
	h.AWS(t, "iam", "create-user", "--user-name", "during")
	h.AWS(t, "cloudtrail", "stop-logging", "--name", "audit")
	st := h.AWSJSON(t, "cloudtrail", "get-trail-status", "--name", "audit")
	if st["IsLogging"] != false || st["StopLoggingTime"] == nil || st["LatestDeliveryTime"] == nil {
		t.Fatalf("stopped status %v", st)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(files) != 1 {
		t.Fatalf("delivered %d files", len(files))
	}
	for key, body := range files {
		want := "logs/p2/AWSLogs/" + h.Env.AccountID + "/CloudTrail/us-east-1/"
		if !strings.HasPrefix(key, want) || !strings.HasSuffix(key, ".json.gz") || !strings.Contains(key, "_CloudTrail_us-east-1_") {
			t.Fatalf("key %s", key)
		}
		zr, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(zr)
		var f struct{ Records []map[string]any }
		if err := json.Unmarshal(raw, &f); err != nil {
			t.Fatal(err)
		}
		var names []string
		users := 0
		for _, r := range f.Records {
			names = append(names, r["eventName"].(string))
			if r["eventName"] == "CreateUser" {
				users++
			}
		}
		if users != 1 {
			t.Fatalf("want only the CreateUser made while logging, got %d in %v", users, names)
		}
	}

	h.AWS(t, "cloudtrail", "delete-trail", "--name", "audit")
	if o, err := h.AWSErr(t, "cloudtrail", "get-trail", "--name", "audit"); err == nil || !strings.Contains(o, "TrailNotFound") {
		t.Fatalf("deleted: %v %s", err, o)
	}
}

func TestBoto3(t *testing.T) {
	h, _ := newTrail(t)
	h.Python(t, `
iam = boto3.client("iam"); ct = boto3.client("cloudtrail")
iam.create_user(UserName="bob")
r = ct.lookup_events(LookupAttributes=[{"AttributeKey": "EventName", "AttributeValue": "CreateUser"}], MaxResults=5)
assert len(r["Events"]) == 1, r
ev = json.loads(r["Events"][0]["CloudTrailEvent"])
assert ev["eventSource"] == "iam.amazonaws.com" and ev["userIdentity"]["type"] in ("Root", "IAMUser"), ev
t = ct.create_trail(Name="py-trail", S3BucketName="b")
assert t["TrailARN"].endswith("trail/py-trail")
ct.start_logging(Name="py-trail")
assert ct.get_trail_status(Name="py-trail")["IsLogging"]
ct.stop_logging(Name="py-trail")
ct.delete_trail(Name="py-trail")
try:
    ct.get_trail(Name="py-trail"); raise SystemExit("expected error")
except ct.exceptions.TrailNotFoundException:
    pass
print("ok")
`)
}

func TestIAM(t *testing.T) {
	h, _ := newTrail(t)
	h.AWS(t, "cloudtrail", "create-trail", "--name", "audit", "--s3-bucket-name", "logs")
	akid, secret := h.User(t, "ro", "ReadOnlyAccess")
	for _, args := range [][]string{
		{"cloudtrail", "lookup-events"},
		{"cloudtrail", "describe-trails"},
		{"cloudtrail", "get-trail-status", "--name", "audit"},
		{"cloudtrail", "list-tags", "--resource-id-list", "arn:aws:cloudtrail:us-east-1:" + h.Env.AccountID + ":trail/audit"},
	} {
		if o, err := h.AWSAs(t, akid, secret, "", args...); err != nil {
			t.Fatalf("%v as read-only: %v %s", args, err, o)
		}
	}
	for _, args := range [][]string{
		{"cloudtrail", "create-trail", "--name", "x1x", "--s3-bucket-name", "logs"},
		{"cloudtrail", "start-logging", "--name", "audit"},
		{"cloudtrail", "stop-logging", "--name", "audit"},
		{"cloudtrail", "update-trail", "--name", "audit", "--s3-key-prefix", "z"},
		{"cloudtrail", "delete-trail", "--name", "audit"},
		{"cloudtrail", "add-tags", "--resource-id", "arn:aws:cloudtrail:us-east-1:" + h.Env.AccountID + ":trail/audit", "--tags-list", "Key=a,Value=b"},
	} {
		if o, err := h.AWSAs(t, akid, secret, "", args...); err == nil || !strings.Contains(o, "AccessDenied") {
			t.Fatalf("%v as read-only: %v %s", args, err, o)
		}
	}
	// A user without cloudtrail access cannot look events up, and the denial is logged.
	akid2, secret2 := h.User(t, "nobody")
	if o, err := h.AWSAs(t, akid2, secret2, "", "cloudtrail", "lookup-events"); err == nil || !strings.Contains(o, "AccessDenied") {
		t.Fatalf("no policy: %v %s", err, o)
	}
	got := events(t, h, "--lookup-attributes", "AttributeKey=Username,AttributeValue=nobody")
	if len(got) == 0 || !strings.Contains(got[0]["CloudTrailEvent"].(string), `"errorCode":"AccessDenied`) {
		t.Fatalf("denial not recorded: %v", got)
	}
}
