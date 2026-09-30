package cloudwatch

import (
	"encoding/json"
	"testing"
	"time"
)

func TestNativeLogs(t *testing.T) {
	h, _ := newAWS(t)
	h.AWS(t, "logs", "create-log-group", "--log-group-name", "web")
	h.AWS(t, "logs", "create-log-stream", "--log-group-name", "web", "--log-stream-name", "s")
	now := time.Now().UnixMilli()
	batch, _ := json.Marshal([]map[string]any{{"timestamp": now, "message": "hello"}, {"timestamp": now, "message": "world"}})
	h.AWS(t, "logs", "put-log-events", "--log-group-name", "web", "--log-stream-name", "s", "--log-events", string(batch))

	h.Native(t, "PUT", "/api/v1/logs/groups/web/metric-filters/f1", map[string]any{"filterPattern": "hello",
		"metricTransformations": []map[string]any{{"metricName": "Hello", "metricNamespace": "Qa", "metricValue": "1"}}})
	var mf []map[string]any
	_ = json.Unmarshal(h.Native(t, "GET", "/api/v1/logs/groups/web/metric-filters", nil), &mf)
	if len(mf) != 1 {
		t.Fatalf("metric filters %v", mf)
	}
	h.Native(t, "DELETE", "/api/v1/logs/groups/web/metric-filters/f1", nil)

	var st map[string]string
	_ = json.Unmarshal(h.Native(t, "POST", "/api/v1/logs/insights/queries", map[string]any{"logGroupNames": []string{"web"},
		"startTime": time.Now().Add(-time.Hour).Unix(), "endTime": time.Now().Add(time.Hour).Unix(), "queryString": "fields @message | limit 5"}), &st)
	var res struct {
		Status  string
		Results [][]map[string]string
	}
	_ = json.Unmarshal(h.Native(t, "GET", "/api/v1/logs/insights/queries/"+st["queryId"], nil), &res)
	if res.Status != "Complete" || len(res.Results) != 2 {
		t.Fatalf("query %+v", res)
	}
	var hist []map[string]any
	h.AWS(t, "cloudwatch", "put-metric-alarm", "--alarm-name", "a1", "--metric-name", "M", "--namespace", "Qa", "--statistic", "Sum",
		"--period", "60", "--evaluation-periods", "1", "--threshold", "1", "--comparison-operator", "GreaterThanThreshold")
	_ = json.Unmarshal(h.Native(t, "GET", "/api/v1/cloudwatch/alarms/a1/history", nil), &hist)
	if len(hist) == 0 {
		t.Fatal("no alarm history")
	}
}
