package sfn

import (
	"encoding/json"
	"strings"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
)

// awsHistory renders an execution's recorded events in the GetExecutionHistory
// shape. HomeCloud-only bookkeeping events (retries, waits, per-state
// failures) are left out and the remaining events renumbered.
func awsHistory(x *Execution, includeData bool) []map[string]any {
	out := []map[string]any{}
	text := func(v any) string {
		if s, ok := v.(string); ok {
			return s
		}
		b, _ := json.Marshal(v)
		return string(b)
	}
	details := func(e HistoryEvent) map[string]any {
		m, _ := e.Details.(map[string]any)
		if m == nil {
			m = map[string]any{}
		}
		return m
	}
	data := func(dst map[string]any, key string, v any) {
		if includeData {
			dst[key] = text(v)
			dst[key+"Details"] = map[string]any{"truncated": false}
		}
	}
	for _, e := range x.History {
		d := details(e)
		ev := map[string]any{"timestamp": awsapi.Time{Time: e.Timestamp}, "type": e.Type}
		legacy := false
		if rt, _ := d["resourceType"].(string); rt == "lambda" {
			if rn, _ := d["resourceName"].(string); strings.HasPrefix(rn, "arn:") {
				legacy = true
			}
		}
		res := map[string]any{"resourceType": d["resourceType"], "resource": d["resourceName"]}
		switch t := e.Type; {
		case t == "ExecutionStarted":
			det := map[string]any{}
			data(det, "input", d["input"])
			if r, _ := d["roleArn"].(string); r != "" {
				det["roleArn"] = r
			}
			ev["executionStartedEventDetails"] = det
		case t == "ExecutionSucceeded":
			det := map[string]any{}
			data(det, "output", d["output"])
			ev["executionSucceededEventDetails"] = det
		case t == "ExecutionFailed", t == "ExecutionAborted", t == "ExecutionTimedOut":
			key := "execution" + strings.TrimPrefix(t, "Execution") + "EventDetails"
			ev[key] = map[string]any{"error": d["error"], "cause": d["cause"]}
		case strings.HasSuffix(t, "StateEntered"):
			det := map[string]any{"name": e.State}
			data(det, "input", d["input"])
			ev["stateEnteredEventDetails"] = det
		case strings.HasSuffix(t, "StateExited"):
			det := map[string]any{"name": e.State}
			data(det, "output", d["output"])
			ev["stateExitedEventDetails"] = det
		case t == "TaskScheduled":
			if legacy {
				ev["type"] = "LambdaFunctionScheduled"
				det := map[string]any{"resource": d["resourceName"]}
				if includeData {
					det["input"], det["inputDetails"] = d["parameters"], map[string]any{"truncated": false}
				}
				if n, _ := d["timeout"].(float64); n > 0 {
					det["timeoutInSeconds"] = n
				}
				ev["lambdaFunctionScheduledEventDetails"] = det
				break
			}
			det := map[string]any{"resourceType": d["resourceType"], "resource": d["resourceName"], "region": x.region(), "parameters": d["parameters"]}
			if n, _ := d["timeout"].(float64); n > 0 {
				det["timeoutInSeconds"] = n
			}
			if n, _ := d["heartbeat"].(float64); n > 0 {
				det["heartbeatInSeconds"] = n
			}
			ev["taskScheduledEventDetails"] = det
		case t == "TaskStarted":
			if legacy {
				ev["type"] = "LambdaFunctionStarted"
				break
			}
			ev["taskStartedEventDetails"] = res
		case t == "TaskSucceeded":
			if legacy {
				ev["type"] = "LambdaFunctionSucceeded"
				det := map[string]any{}
				data(det, "output", d["output"])
				ev["lambdaFunctionSucceededEventDetails"] = det
				break
			}
			data(res, "output", d["output"])
			ev["taskSucceededEventDetails"] = res
		case t == "TaskFailed", t == "TaskTimedOut":
			if legacy {
				ev["type"] = "LambdaFunction" + strings.TrimPrefix(t, "Task")
				ev["lambdaFunction"+strings.TrimPrefix(t, "Task")+"EventDetails"] = map[string]any{"error": d["error"], "cause": d["cause"]}
				break
			}
			res["error"], res["cause"] = d["error"], d["cause"]
			ev["task"+strings.TrimPrefix(t, "Task")+"EventDetails"] = res
		case t == "MapStateStarted":
			ev["mapStateStartedEventDetails"] = map[string]any{"length": d["length"]}
		case strings.HasPrefix(t, "MapIteration"):
			ev[lowerFirst(t)+"EventDetails"] = map[string]any{"name": d["name"], "index": d["index"]}
		case t == "ParallelStateStarted", t == "ParallelStateSucceeded", t == "ParallelStateFailed",
			t == "MapStateSucceeded", t == "MapStateFailed":
		default:
			continue // TaskRetrying, WaitStateWaiting, <Type>StateFailed
		}
		ev["id"] = len(out) + 1
		ev["previousEventId"] = len(out)
		out = append(out, ev)
	}
	return out
}

// region is the execution's region (from its ARN).
func (x *Execution) region() string {
	parts := strings.SplitN(x.ARN, ":", 5)
	if len(parts) == 5 {
		return parts[3]
	}
	return ""
}
