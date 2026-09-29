package sfn

import (
	"encoding/json"
	"testing"
	"time"
)

func TestNativeStateMachines(t *testing.T) {
	h := newSFN(t)
	h.Native(t, "POST", "/api/v1/sfn/state-machines", map[string]any{"name": "native", "definition": map[string]any{"StartAt": "P",
		"States": map[string]any{"P": map[string]any{"Type": "Pass", "Parameters": map[string]any{"v.$": "States.MathAdd($.a, 1)"}, "End": true}}}})
	var x map[string]any
	_ = json.Unmarshal(h.Native(t, "POST", "/api/v1/sfn/state-machines/native/executions", map[string]any{"input": map[string]any{"a": 1}}), &x)
	for i := 0; i < 100; i++ {
		var e map[string]any
		_ = json.Unmarshal(h.Native(t, "GET", "/api/v1/sfn/executions/"+x["id"].(string), nil), &e)
		if e["status"] == "SUCCEEDED" {
			if mustJSON(e["output"]) != `{"v":2}` {
				t.Fatalf("native output %v", e["output"])
			}
			d := h.AWSJSON(t, "stepfunctions", "describe-execution", "--execution-arn", x["arn"].(string))
			if d["output"] != `{"v":2}` {
				t.Fatalf("aws view %v", d)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("native execution did not finish")
}
