package cloudwatch

import (
	"math"
	"strings"
	"testing"
	"time"
)

// periodicHist is hours of a daily cycle sampled every 5 minutes, ending at end.
func periodicHist(end time.Time, days int) hist {
	var h hist
	for t := end.Add(-time.Duration(days) * 24 * time.Hour); t.Before(end); t = t.Add(5 * time.Minute) {
		h.t = append(h.t, t)
		h.v = append(h.v, cycle(t)+float64(t.Minute()/5%3)-1) // noise of -1, 0, +1
	}
	return h
}

func cycle(t time.Time) float64 {
	return 100 + 20*math.Sin(2*math.Pi*float64(t.Hour())/24)
}

func TestBandModel(t *testing.T) {
	end := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	h := periodicHist(end, 2)
	m := trainBand(h, nil, time.UTC)
	if m == nil || m.level != 1 {
		t.Fatalf("model %+v", m)
	}
	at := time.Date(2026, 9, 30, 18, 10, 0, 0, time.UTC)
	lo, hi := m.band(at, 2)
	want := cycle(at)
	if lo >= want || hi <= want || hi-lo > 4 || hi-lo < 0.5 {
		t.Fatalf("band [%v, %v] around %v", lo, hi, want)
	}
	if l1, h1 := m.band(at, 1); h1-l1 >= hi-lo {
		t.Fatalf("1 stddev band %v not narrower than 2 stddev band %v", h1-l1, hi-lo)
	}
	// Excluded ranges are ignored: a bad day marked as excluded does not move the band.
	bad := periodicHist(end, 2)
	for i, ts := range bad.t {
		if ts.After(end.Add(-30*time.Hour)) && ts.Before(end.Add(-24*time.Hour)) {
			bad.v[i] += 500
		}
	}
	excl := []timeRange{{end.Add(-30 * time.Hour), end.Add(-24 * time.Hour)}}
	polluted := trainBand(bad, nil, time.UTC)
	clean := trainBand(bad, excl, time.UTC)
	pl, ph := polluted.band(end.Add(-27*time.Hour), 2)
	cl, ch := clean.band(end.Add(-27*time.Hour), 2)
	if ph-pl < 100 || ch-cl > 4 {
		t.Fatalf("polluted width %v, excluded width %v", ph-pl, ch-cl)
	}
	// Too little history: no model.
	if trainBand(hist{t: h.t[:9], v: h.v[:9]}, nil, time.UTC) != nil {
		t.Fatal("model from 9 datapoints")
	}
	if trainBand(hist{t: h.t[:11], v: h.v[:11]}, nil, time.UTC) != nil {
		t.Fatal("model from under an hour of history")
	}
	// Under a day of history: one rolling window.
	r := trainBand(periodicHist(end, 1), nil, time.UTC)
	if r == nil || r.level != 0 {
		// exactly 24h minus one step is below the day threshold.
		t.Fatalf("rolling model %+v", r)
	}
	// Time zones shift the hour-of-day buckets.
	ny, _ := time.LoadLocation("America/New_York")
	if bucketKey(1, at, time.UTC) == bucketKey(1, at, ny) {
		t.Fatal("time zone ignored")
	}
}

func TestFillLinear(t *testing.T) {
	_, svc := newAWS(t)
	base := time.Now().UTC().Truncate(time.Hour).Add(-time.Hour)
	// Datapoints at minutes 1, 4 and 5; minute 0 and 2-3 are missing.
	for _, m := range []int{1, 4, 5} {
		svc.Put("F", "x", nil, "None", float64(m*10), base.Add(time.Duration(m)*time.Minute))
	}
	q := func(expr string) []MetricDataQuery {
		return []MetricDataQuery{{Id: "m", MetricStat: &MetricStat{Metric: Metric{Namespace: "F", MetricName: "x"}, Period: 60, Stat: "Sum"}, ReturnData: new(bool)},
			{Id: "f", Expression: expr}}
	}
	res, _, err := svc.evalMetricQueries(q("FILL(m, LINEAR)"), base, base.Add(7*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	f := res["f"].series
	got := map[int]float64{}
	for i, ts := range f.t {
		got[int(ts.Sub(base).Minutes())] = f.v[i]
	}
	want := map[int]float64{1: 10, 2: 20, 3: 30, 4: 40, 5: 50}
	if len(got) != len(want) {
		t.Fatalf("linear fill %v", got)
	}
	for k, v := range want {
		if math.Abs(got[k]-v) > 1e-9 {
			t.Fatalf("linear fill %v", got)
		}
	}
	res, _, err = svc.evalMetricQueries(q("FILL(m, REPEAT)"), base, base.Add(7*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	r := res["f"].series
	if len(r.v) != 6 || r.v[1] != 10 || r.v[2] != 10 || r.v[5] != 50 {
		t.Fatalf("repeat fill %v", r.v)
	}
	if _, _, err := svc.evalMetricQueries(q("FILL(m)"), base, base.Add(time.Minute)); err == nil || !strings.Contains(err.Error(), "FILL needs") {
		t.Fatalf("FILL(m): %v", err)
	}
}

// seedPeriodic stores two days of the daily cycle at 5-minute resolution.
func seedPeriodic(s *Service) {
	end := time.Now().UTC().Truncate(5 * time.Minute).Add(-5 * time.Minute)
	for _, p := range periodicHist(end, 2).t {
		s.Put("Anom", "Load", map[string]string{"Host": "a"}, "None", cycle(p)+float64(p.Minute()/5%3)-1, p)
	}
}

func TestAnomalyDetectionCLI(t *testing.T) {
	h, s := newAWS(t)
	seedPeriodic(s)
	q := `[{"Id":"m1","MetricStat":{"Metric":{"Namespace":"Anom","MetricName":"Load","Dimensions":[{"Name":"Host","Value":"a"}]},"Period":300,"Stat":"Average"},"ReturnData":true},
	       {"Id":"ad1","Expression":"ANOMALY_DETECTION_BAND(m1, 2)","Label":"Load band"}]`
	get := func() []any {
		now := time.Now().UTC()
		md := h.AWSJSON(t, "cloudwatch", "get-metric-data", "--metric-data-queries", q, "--scan-by", "TimestampAscending",
			"--start-time", now.Add(-3*time.Hour).Format(time.RFC3339), "--end-time", now.Format(time.RFC3339))
		return md["MetricDataResults"].([]any)
	}
	// No detector yet: the band is empty.
	res := get()
	if len(res) != 3 || len(res[1].(map[string]any)["Values"].([]any)) != 0 {
		t.Fatalf("band without a detector: %v", res)
	}
	det := "Namespace=Anom,MetricName=Load,Stat=Average,Dimensions=[{Name=Host,Value=a}]"
	excl := time.Now().UTC().Add(-40 * time.Hour)
	h.AWS(t, "cloudwatch", "put-anomaly-detector", "--single-metric-anomaly-detector", det, "--configuration",
		`{"MetricTimezone":"Europe/Berlin","ExcludedTimeRanges":[{"StartTime":"`+excl.Format(time.RFC3339)+`","EndTime":"`+excl.Add(time.Hour).Format(time.RFC3339)+`"}]}`)
	if out, err := h.AWSErr(t, "cloudwatch", "put-anomaly-detector", "--single-metric-anomaly-detector", det, "--configuration", `{"MetricTimezone":"Nowhere/Land"}`); err == nil || !strings.Contains(out, "MetricTimezone") {
		t.Fatalf("bad time zone: %v %s", err, out)
	}
	d := h.AWSJSON(t, "cloudwatch", "describe-anomaly-detectors", "--namespace", "Anom")
	ds := d["AnomalyDetectors"].([]any)
	if len(ds) != 1 {
		t.Fatalf("describe %v", d)
	}
	d0 := ds[0].(map[string]any)
	cfg := d0["Configuration"].(map[string]any)
	if d0["StateValue"] != "TRAINED" || cfg["MetricTimezone"] != "Europe/Berlin" || len(cfg["ExcludedTimeRanges"].([]any)) != 1 || d0["MetricName"] != "Load" {
		t.Fatalf("detector %v", d0)
	}
	if d := h.AWSJSON(t, "cloudwatch", "describe-anomaly-detectors", "--anomaly-detector-types", "METRIC_MATH"); len(d["AnomalyDetectors"].([]any)) != 0 {
		t.Fatalf("type filter %v", d)
	}
	res = get()
	if len(res) != 3 {
		t.Fatalf("results %v", res)
	}
	up, lo := res[1].(map[string]any), res[2].(map[string]any)
	if up["Id"] != "ad1" || lo["Id"] != "ad1" || up["Label"] != "Load band (Upper)" || lo["Label"] != "Load band (Lower)" {
		t.Fatalf("band results %v %v", up, lo)
	}
	uv, lv := up["Values"].([]any), lo["Values"].([]any)
	if len(uv) < 30 || len(uv) != len(lv) {
		t.Fatalf("band sizes %d %d", len(uv), len(lv))
	}
	for i := range uv {
		if uv[i].(float64) < lv[i].(float64) {
			t.Fatalf("upper below lower at %d", i)
		}
	}
	if out, err := h.AWSErr(t, "cloudwatch", "get-metric-data", "--metric-data-queries", `[{"Id":"a","Expression":"ANOMALY_DETECTION_BAND(zz, 2)"}]`,
		"--start-time", time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), "--end-time", time.Now().UTC().Format(time.RFC3339)); err == nil {
		t.Fatalf("unknown band source accepted: %s", out)
	}

	// An anomaly alarm: OK on a normal value, ALARM on a spike.
	alarmQ := `[{"Id":"m1","MetricStat":{"Metric":{"Namespace":"Anom","MetricName":"Load","Dimensions":[{"Name":"Host","Value":"a"}]},"Period":300,"Stat":"Average"},"ReturnData":true},
	            {"Id":"ad1","Expression":"ANOMALY_DETECTION_BAND(m1, 2)","Label":"Load (expected)","ReturnData":true}]`
	if out, err := h.AWSErr(t, "cloudwatch", "put-metric-alarm", "--alarm-name", "bad-op", "--metrics", alarmQ, "--threshold-metric-id", "ad1",
		"--evaluation-periods", "1", "--comparison-operator", "GreaterThanThreshold"); err == nil {
		t.Fatalf("plain operator on an anomaly alarm accepted: %s", out)
	}
	h.AWS(t, "cloudwatch", "put-metric-alarm", "--alarm-name", "anomaly", "--metrics", alarmQ, "--threshold-metric-id", "ad1",
		"--evaluation-periods", "1", "--comparison-operator", "LessThanLowerOrGreaterThanUpperThreshold", "--treat-missing-data", "missing")
	a := h.AWSJSON(t, "cloudwatch", "describe-alarms", "--alarm-names", "anomaly")["MetricAlarms"].([]any)[0].(map[string]any)
	if a["ThresholdMetricId"] != "ad1" || a["Threshold"] != nil || len(a["Metrics"].([]any)) != 2 {
		t.Fatalf("alarm %v", a)
	}
	state := func() string {
		s.evaluateAlarms()
		return h.AWSJSON(t, "cloudwatch", "describe-alarms", "--alarm-names", "anomaly")["MetricAlarms"].([]any)[0].(map[string]any)["StateValue"].(string)
	}
	put := func(v float64) {
		s.Put("Anom", "Load", map[string]string{"Host": "a"}, "None", v, time.Now().UTC())
	}
	put(cycle(time.Now().UTC()))
	if st := state(); st != "OK" {
		t.Fatalf("normal value: %s", st)
	}
	put(5000)
	if st := state(); st != "ALARM" {
		t.Fatalf("spike: %s", st)
	}
	// Terraform and older SDKs use awsQuery with the same fields.
	out := h.Python(t, `
import urllib.request, os
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
print(call({"Action": "PutMetricAlarm", "Version": "2010-08-01", "AlarmName": "tf-anomaly", "EvaluationPeriods": "2", "DatapointsToAlarm": "2",
    "ComparisonOperator": "GreaterThanUpperThreshold", "ThresholdMetricId": "ad1", "TreatMissingData": "breaching",
    "Metrics.member.1.Id": "m1", "Metrics.member.1.ReturnData": "true", "Metrics.member.1.MetricStat.Period": "300", "Metrics.member.1.MetricStat.Stat": "Average",
    "Metrics.member.1.MetricStat.Metric.Namespace": "Anom", "Metrics.member.1.MetricStat.Metric.MetricName": "Cpu",
    "Metrics.member.2.Id": "ad1", "Metrics.member.2.Expression": "ANOMALY_DETECTION_BAND(m1, 3)", "Metrics.member.2.Label": "band", "Metrics.member.2.ReturnData": "true"}))
print(call({"Action": "DescribeAlarms", "Version": "2010-08-01", "AlarmNames.member.1": "tf-anomaly"}))
print(call({"Action": "DescribeAnomalyDetectors", "Version": "2010-08-01", "Namespace": "Anom", "MetricName": "Cpu"}))
`)
	for _, want := range []string{"<PutMetricAlarmResponse", "<ThresholdMetricId>ad1</ThresholdMetricId>", "<ComparisonOperator>GreaterThanUpperThreshold</ComparisonOperator>",
		"<MetricName>Cpu</MetricName>", "<StateValue>PENDING_TRAINING</StateValue>"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "<Threshold>") {
		t.Errorf("Threshold present on an anomaly alarm:\n%s", out)
	}
	// The alarm created a detector for Cpu; delete it, and deleting again fails.
	cpu := "Namespace=Anom,MetricName=Cpu,Stat=Average"
	h.AWS(t, "cloudwatch", "delete-anomaly-detector", "--single-metric-anomaly-detector", cpu)
	if out, err := h.AWSErr(t, "cloudwatch", "delete-anomaly-detector", "--single-metric-anomaly-detector", cpu); err == nil || !strings.Contains(out, "ResourceNotFound") {
		t.Fatalf("delete missing detector: %v %s", err, out)
	}
}

func TestAnomalyMathDetector(t *testing.T) {
	h, s := newAWS(t)
	seedPeriodic(s)
	qs := `{"MetricDataQueries":[{"Id":"m1","MetricStat":{"Metric":{"Namespace":"Anom","MetricName":"Load","Dimensions":[{"Name":"Host","Value":"a"}]},"Period":300,"Stat":"Average"},"ReturnData":false},
	        {"Id":"e1","Expression":"m1 * 2","ReturnData":true}]}`
	h.AWS(t, "cloudwatch", "put-anomaly-detector", "--metric-math-anomaly-detector", qs)
	d := h.AWSJSON(t, "cloudwatch", "describe-anomaly-detectors", "--anomaly-detector-types", "METRIC_MATH")["AnomalyDetectors"].([]any)
	if len(d) != 1 || d[0].(map[string]any)["StateValue"] != "TRAINED" || d[0].(map[string]any)["MetricMathAnomalyDetector"] == nil {
		t.Fatalf("math detector %v", d)
	}
	q := `[{"Id":"m1","MetricStat":{"Metric":{"Namespace":"Anom","MetricName":"Load","Dimensions":[{"Name":"Host","Value":"a"}]},"Period":300,"Stat":"Average"},"ReturnData":false},
	       {"Id":"e1","Expression":"m1 * 2","ReturnData":false},{"Id":"b","Expression":"ANOMALY_DETECTION_BAND(e1)"}]`
	now := time.Now().UTC()
	md := h.AWSJSON(t, "cloudwatch", "get-metric-data", "--metric-data-queries", q,
		"--start-time", now.Add(-2*time.Hour).Format(time.RFC3339), "--end-time", now.Format(time.RFC3339))["MetricDataResults"].([]any)
	if len(md) != 2 || len(md[0].(map[string]any)["Values"].([]any)) < 10 {
		t.Fatalf("math band %v", md)
	}
	h.AWS(t, "cloudwatch", "delete-anomaly-detector", "--metric-math-anomaly-detector", qs)
	if d := h.AWSJSON(t, "cloudwatch", "describe-anomaly-detectors"); len(d["AnomalyDetectors"].([]any)) != 0 {
		t.Fatalf("after delete %v", d)
	}
}
