package cloudwatch

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

func TestLogFilters(t *testing.T) {
	h, s := newAWS(t)
	acct := h.Env.AccountID
	got := make(chan []byte, 4)
	s.Deliver = func(ctx context.Context, arn string, payload []byte) error { got <- payload; return nil }
	h.AWS(t, "logs", "create-log-group", "--log-group-name", "web")
	h.AWS(t, "logs", "create-log-stream", "--log-group-name", "web", "--log-stream-name", "s")
	h.AWS(t, "logs", "put-metric-filter", "--log-group-name", "web", "--filter-name", "errors", "--filter-pattern", `[ip, method, path, status = 5*, bytes]`,
		"--metric-transformations", `metricName=ServerErrors,metricNamespace=Web,metricValue=1,unit=Count`)
	h.AWS(t, "logs", "put-metric-filter", "--log-group-name", "web", "--filter-name", "bytes", "--filter-pattern", `[ip, method, path, status, bytes]`,
		"--metric-transformations", `[{"metricName":"Bytes","metricNamespace":"Web","metricValue":"$bytes","dimensions":{"Method":"$method"}}]`)
	h.AWS(t, "logs", "put-metric-filter", "--log-group-name", "web", "--filter-name", "latency", "--filter-pattern", `{ $.latency > 0 }`,
		"--metric-transformations", `metricName=Latency,metricNamespace=Web,metricValue=$.latency`)
	h.AWS(t, "logs", "put-subscription-filter", "--log-group-name", "web", "--filter-name", "to-fn", "--filter-pattern", "POST",
		"--destination-arn", "arn:aws:lambda:us-east-1:"+acct+":function:shipper")
	if out, err := h.AWSErr(t, "logs", "put-subscription-filter", "--log-group-name", "web", "--filter-name", "k", "--filter-pattern", "",
		"--destination-arn", "arn:aws:kinesis:us-east-1:"+acct+":stream/x"); err == nil || !strings.Contains(out, "InvalidParameterException") {
		t.Fatalf("kinesis destination: %v %s", err, out)
	}
	now := time.Now().UnixMilli()
	batch, _ := json.Marshal([]map[string]any{
		{"timestamp": now, "message": "10.0.0.1 GET /a 200 512"},
		{"timestamp": now, "message": "10.0.0.2 POST /b 503 100"},
		{"timestamp": now, "message": `{"latency": 42}`},
	})
	h.AWS(t, "logs", "put-log-events", "--log-group-name", "web", "--log-stream-name", "s", "--log-events", string(batch))

	stats := func(name string, dims ...string) float64 {
		args := []string{"cloudwatch", "get-metric-statistics", "--namespace", "Web", "--metric-name", name, "--statistics", "Sum",
			"--period", "60", "--start-time", time.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339), "--end-time", time.Now().Add(time.Minute).UTC().Format(time.RFC3339)}
		if len(dims) > 0 {
			args = append(args, "--dimensions", dims[0])
		}
		out := h.AWSJSON(t, args...)
		sum := 0.0
		for _, d := range out["Datapoints"].([]any) {
			sum += d.(map[string]any)["Sum"].(float64)
		}
		return sum
	}
	if n := stats("ServerErrors"); n != 1 {
		t.Errorf("ServerErrors = %v", n)
	}
	if n := stats("Bytes", "Name=Method,Value=GET"); n != 512 {
		t.Errorf("Bytes{GET} = %v", n)
	}
	if n := stats("Latency"); n != 42 {
		t.Errorf("Latency = %v", n)
	}
	select {
	case p := <-got:
		var body struct{ Awslogs struct{ Data string } }
		_ = json.Unmarshal(p, &body)
		raw, _ := base64.StdEncoding.DecodeString(body.Awslogs.Data)
		zr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		doc, _ := io.ReadAll(zr)
		var msg map[string]any
		_ = json.Unmarshal(doc, &msg)
		evs := msg["logEvents"].([]any)
		if msg["logGroup"] != "web" || msg["messageType"] != "DATA_MESSAGE" || len(evs) != 1 || !strings.Contains(evs[0].(map[string]any)["message"].(string), "POST") {
			t.Fatalf("subscription payload %s", doc)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no subscription delivery")
	}
	mf := h.AWSJSON(t, "logs", "describe-metric-filters", "--log-group-name", "web")
	if n := len(mf["metricFilters"].([]any)); n != 3 {
		t.Fatalf("metric filters %v", mf)
	}
	g := h.AWSJSON(t, "logs", "describe-log-groups", "--log-group-name-prefix", "web")["logGroups"].([]any)[0].(map[string]any)
	if g["metricFilterCount"].(float64) != 3 {
		t.Fatalf("metricFilterCount %v", g)
	}
	tm := h.AWSJSON(t, "logs", "test-metric-filter", "--filter-pattern", `[ip, method, path, status = 4*, bytes]`,
		"--log-event-messages", "1.1.1.1 GET / 404 10", "1.1.1.1 GET / 200 10")
	m := tm["matches"].([]any)
	if len(m) != 1 || m[0].(map[string]any)["extractedValues"].(map[string]any)["$status"] != "404" {
		t.Fatalf("test-metric-filter %v", tm)
	}
	h.AWS(t, "logs", "delete-metric-filter", "--log-group-name", "web", "--filter-name", "errors")
	h.AWS(t, "logs", "delete-subscription-filter", "--log-group-name", "web", "--filter-name", "to-fn")
	if sf := h.AWSJSON(t, "logs", "describe-subscription-filters", "--log-group-name", "web"); len(sf["subscriptionFilters"].([]any)) != 0 {
		t.Fatalf("subscription filters %v", sf)
	}
}
