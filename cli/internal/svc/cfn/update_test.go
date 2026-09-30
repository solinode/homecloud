package cfn_test

import (
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/svc/cfn"
)

var failDeletes, failUpdates atomic.Int32

func init() { cfn.RegisterTestTypes(&failDeletes, &failUpdates) }

// eventIndex is the position of the first event (they are listed newest first).
func eventIndex(evs []map[string]any, logical, status string) int {
	for i, ev := range evs {
		if str(ev, "LogicalResourceId") == logical && str(ev, "ResourceStatus") == status {
			return i
		}
	}
	return -1
}

func queueAttr(t *testing.T, e *env, url, attr string) string {
	t.Helper()
	a := e.AWSJSON(t, "sqs", "get-queue-attributes", "--queue-url", url, "--attribute-names", attr)["Attributes"].(map[string]any)
	s, _ := a[attr].(string)
	return s
}

func queueTemplate(visibility int, extra string) string {
	return fmt.Sprintf(`{"Resources":{
  "Jobs":{"Type":"AWS::SQS::Queue","Properties":{"QueueName":"upd-jobs","VisibilityTimeout":%d,"Tags":[{"Key":"v","Value":"%d"}]}},
  "Param":{"Type":"AWS::SSM::Parameter","Properties":{"Name":"/upd/p","Type":"String","Value":"v%d"}}%s},
"Outputs":{"Url":{"Value":{"Ref":"Jobs"}}}}`, visibility, visibility, visibility, extra)
}

func TestUpdateInPlace(t *testing.T) {
	e := newEnv(t, false)
	e.AWS(t, "cloudformation", "create-stack", "--stack-name", "inplace", "--template-body", queueTemplate(30, ""))
	st := e.waitFor(t, "inplace", "CREATE_COMPLETE")
	url := outputs(st)["Url"]
	res := func() map[string]string {
		out := map[string]string{}
		for _, r := range e.AWSJSON(t, "cloudformation", "list-stack-resources", "--stack-name", "inplace")["StackResourceSummaries"].([]any) {
			m := r.(map[string]any)
			out[str(m, "LogicalResourceId")] = str(m, "PhysicalResourceId")
		}
		return out
	}
	before := res()

	// The change set says what the update does to each resource.
	e.AWS(t, "cloudformation", "create-change-set", "--stack-name", "inplace", "--change-set-name", "peek", "--template-body", queueTemplate(90, ""))
	d := e.AWSJSON(t, "cloudformation", "describe-change-set", "--stack-name", "inplace", "--change-set-name", "peek")
	rep := map[string]string{}
	for _, c := range d["Changes"].([]any) {
		rc := c.(map[string]any)["ResourceChange"].(map[string]any)
		rep[str(rc, "LogicalResourceId")] = str(rc, "Replacement")
		if str(rc, "LogicalResourceId") == "Jobs" {
			det := rc["Details"].([]any)
			if len(det) != 2 || str(det[0].(map[string]any)["Target"].(map[string]any), "RequiresRecreation") != "Never" {
				t.Fatalf("change details: %v", det)
			}
		}
	}
	if rep["Jobs"] != "False" || rep["Param"] != "False" || len(rep) != 2 {
		t.Fatalf("replacement: %v", rep)
	}
	e.AWS(t, "cloudformation", "delete-change-set", "--stack-name", "inplace", "--change-set-name", "peek")

	e.AWS(t, "cloudformation", "update-stack", "--stack-name", "inplace", "--template-body", queueTemplate(90, ""))
	st = e.waitFor(t, "inplace", "UPDATE_COMPLETE")
	if outputs(st)["Url"] != url {
		t.Fatalf("the queue URL changed: %v", outputs(st))
	}
	if v := queueAttr(t, e, url, "VisibilityTimeout"); v != "90" {
		t.Fatalf("visibility timeout %q", v)
	}
	if v := e.AWSJSON(t, "ssm", "get-parameter", "--name", "/upd/p")["Parameter"].(map[string]any)["Value"]; v != "v90" {
		t.Fatalf("parameter %v", v)
	}
	if fmt.Sprint(res()) != fmt.Sprint(before) {
		t.Fatalf("physical IDs changed: %v -> %v", before, res())
	}
	evs := e.events(t, "inplace")
	if _, ok := hasEvent(evs, "Jobs", "DELETE_COMPLETE"); ok {
		t.Fatalf("the queue was deleted: %v", evs)
	}
	if _, ok := hasEvent(evs, "Jobs", "UPDATE_COMPLETE"); !ok {
		t.Fatalf("no UPDATE_COMPLETE for the queue: %v", evs)
	}
	tags := e.AWSJSON(t, "sqs", "list-queue-tags", "--queue-url", url)["Tags"].(map[string]any)
	if tags["v"] != "90" {
		t.Fatalf("queue tags: %v", tags)
	}
}

func TestUpdateFailureRollsBack(t *testing.T) {
	e := newEnv(t, false)
	v1 := `{"Resources":{
  "Jobs":{"Type":"AWS::SQS::Queue","Properties":{"QueueName":"rb-jobs","VisibilityTimeout":30}},
  "Old":{"Type":"AWS::SNS::Topic","Properties":{"TopicName":"rb-old"}}},
"Outputs":{"Url":{"Value":{"Ref":"Jobs"}}}}`
	e.AWS(t, "cloudformation", "create-stack", "--stack-name", "rb", "--template-body", v1)
	url := outputs(e.waitFor(t, "rb", "CREATE_COMPLETE"))["Url"]

	// The update changes the queue, drops the topic, adds a topic and a resource that fails.
	v2 := `{"Resources":{
  "Jobs":{"Type":"AWS::SQS::Queue","Properties":{"QueueName":"rb-jobs","VisibilityTimeout":120}},
  "Added":{"Type":"AWS::SNS::Topic","Properties":{"TopicName":"rb-added"},"DependsOn":"Jobs"},
  "Bad":{"Type":"AWS::Test::Boom","DependsOn":["Jobs","Added"]}},
"Outputs":{"Url":{"Value":{"Ref":"Jobs"}}}}`
	e.AWS(t, "cloudformation", "update-stack", "--stack-name", "rb", "--template-body", v2)
	st := e.waitFor(t, "rb", "UPDATE_ROLLBACK_COMPLETE")
	if v := queueAttr(t, e, url, "VisibilityTimeout"); v != "30" {
		t.Fatalf("the queue was not restored: %q", v)
	}
	if o, err := e.AWSErr(t, "sns", "get-topic-attributes", "--topic-arn", "arn:aws:sns:us-east-1:"+e.Env.AccountID+":rb-added"); err == nil {
		t.Fatalf("the topic added by the update survived: %s", o)
	}
	if o, err := e.AWSErr(t, "sns", "get-topic-attributes", "--topic-arn", "arn:aws:sns:us-east-1:"+e.Env.AccountID+":rb-old"); err != nil {
		t.Fatalf("the topic the update dropped is gone: %s", o)
	}
	if tb := e.AWS(t, "cloudformation", "get-template", "--stack-name", "rb"); strings.Contains(tb, "rb-added") || !strings.Contains(tb, "rb-old") {
		t.Fatalf("template after rollback: %s", tb)
	}
	res := e.AWSJSON(t, "cloudformation", "list-stack-resources", "--stack-name", "rb")["StackResourceSummaries"].([]any)
	if len(res) != 2 {
		t.Fatalf("resources after rollback: %v", res)
	}
	if str(st, "LastUpdatedTime") == "" || outputs(st)["Url"] != url {
		t.Fatalf("stack: %v", st)
	}

	evs := e.events(t, "rb") // newest first
	for _, want := range [][2]string{{"rb", "UPDATE_IN_PROGRESS"}, {"Jobs", "UPDATE_COMPLETE"}, {"Added", "CREATE_COMPLETE"}, {"Bad", "CREATE_FAILED"},
		{"rb", "UPDATE_ROLLBACK_IN_PROGRESS"}, {"rb", "UPDATE_ROLLBACK_COMPLETE_CLEANUP_IN_PROGRESS"}, {"Added", "DELETE_COMPLETE"}, {"rb", "UPDATE_ROLLBACK_COMPLETE"}} {
		if eventIndex(evs, want[0], want[1]) < 0 {
			t.Fatalf("no %v event: %v", want, evs)
		}
	}
	if ev, _ := hasEvent(evs, "rb", "UPDATE_ROLLBACK_IN_PROGRESS"); !strings.Contains(str(ev, "ResourceStatusReason"), "failed to create: [Bad]") {
		t.Fatalf("rollback reason: %v", ev)
	}
	if _, ok := hasEvent(evs, "Old", "DELETE_IN_PROGRESS"); ok {
		t.Fatalf("the dropped topic was deleted although the update failed: %v", evs)
	}
	// The queue's properties are restored (an UPDATE_IN_PROGRESS after the failure), before the created resources go.
	if i, j := eventIndex(evs, "Jobs", "UPDATE_COMPLETE"), eventIndex(evs, "Added", "DELETE_COMPLETE"); i < j {
		t.Fatalf("the queue was restored after %d, want before %d", i, j)
	}
	// A rolled back stack can be updated again.
	e.AWS(t, "cloudformation", "update-stack", "--stack-name", "rb", "--template-body", strings.ReplaceAll(v1, `"VisibilityTimeout":30`, `"VisibilityTimeout":45`))
	e.waitFor(t, "rb", "UPDATE_COMPLETE")
}

func TestUpdateRemovesResourcesAfterSuccess(t *testing.T) {
	e := newEnv(t, false)
	v1 := `{"Resources":{"Keep":{"Type":"AWS::SQS::Queue","Properties":{"QueueName":"rm-keep"}},"Gone":{"Type":"AWS::SNS::Topic","Properties":{"TopicName":"rm-gone"}}}}`
	v2 := `{"Resources":{"Keep":{"Type":"AWS::SQS::Queue","Properties":{"QueueName":"rm-keep","VisibilityTimeout":50}}}}`
	e.AWS(t, "cloudformation", "create-stack", "--stack-name", "rm", "--template-body", v1)
	e.waitFor(t, "rm", "CREATE_COMPLETE")
	e.AWS(t, "cloudformation", "update-stack", "--stack-name", "rm", "--template-body", v2)
	e.waitFor(t, "rm", "UPDATE_COMPLETE")
	evs := e.events(t, "rm")
	up, cl, del, done := eventIndex(evs, "Keep", "UPDATE_COMPLETE"), eventIndex(evs, "rm", "UPDATE_COMPLETE_CLEANUP_IN_PROGRESS"), eventIndex(evs, "Gone", "DELETE_COMPLETE"), eventIndex(evs, "rm", "UPDATE_COMPLETE")
	if up < 0 || cl < 0 || del < 0 || done < 0 || !(done < del && del < cl && cl < up) {
		t.Fatalf("order (newest first) update %d, cleanup %d, delete %d, complete %d: %v", up, cl, del, done, evs)
	}
	if o, err := e.AWSErr(t, "sns", "get-topic-attributes", "--topic-arn", "arn:aws:sns:us-east-1:"+e.Env.AccountID+":rm-gone"); err == nil {
		t.Fatalf("the dropped topic survived: %s", o)
	}
}

func TestUpdateReplacementCreatesFirst(t *testing.T) {
	e := newEnv(t, false)
	// No QueueName: CloudFormation names the queue, so a replacement can exist beside the old one.
	tmpl := func(fifo string) string {
		return `{"Resources":{"Q":{"Type":"AWS::SQS::Queue","Properties":{` + fifo + `}},
  "Param":{"Type":"AWS::SSM::Parameter","Properties":{"Name":"/repl/q","Type":"String","Value":{"Ref":"Q"}}}},
"Outputs":{"Url":{"Value":{"Ref":"Q"}}}}`
	}
	e.AWS(t, "cloudformation", "create-stack", "--stack-name", "repl", "--template-body", tmpl(`"VisibilityTimeout":30`))
	first := outputs(e.waitFor(t, "repl", "CREATE_COMPLETE"))["Url"]

	e.AWS(t, "cloudformation", "create-change-set", "--stack-name", "repl", "--change-set-name", "peek", "--template-body", tmpl(`"FifoQueue":true`))
	d := e.AWSJSON(t, "cloudformation", "describe-change-set", "--stack-name", "repl", "--change-set-name", "peek")
	rep := map[string]string{}
	for _, c := range d["Changes"].([]any) {
		rc := c.(map[string]any)["ResourceChange"].(map[string]any)
		rep[str(rc, "LogicalResourceId")] = str(rc, "Replacement")
	}
	if rep["Q"] != "True" {
		t.Fatalf("change set replacement: %v", rep)
	}

	e.AWS(t, "cloudformation", "update-stack", "--stack-name", "repl", "--template-body", tmpl(`"FifoQueue":true`))
	second := outputs(e.waitFor(t, "repl", "UPDATE_COMPLETE"))["Url"]
	if second == first || !strings.HasSuffix(second, ".fifo") {
		t.Fatalf("the queue was not replaced: %s -> %s", first, second)
	}
	if o, err := e.AWSErr(t, "sqs", "get-queue-attributes", "--queue-url", first, "--attribute-names", "All"); err == nil {
		t.Fatalf("the old queue survived: %s", o)
	}
	if v := e.AWSJSON(t, "ssm", "get-parameter", "--name", "/repl/q")["Parameter"].(map[string]any)["Value"]; v != second {
		t.Fatalf("the parameter follows the new queue: %v", v)
	}
	evs := e.events(t, "repl")
	created, cleanup, deleted := eventIndex(evs, "Q", "UPDATE_COMPLETE"), eventIndex(evs, "repl", "UPDATE_COMPLETE_CLEANUP_IN_PROGRESS"), eventIndex(evs, "Q", "DELETE_COMPLETE")
	if created < 0 || cleanup < 0 || deleted < 0 || !(deleted < cleanup && cleanup < created) {
		t.Fatalf("new queue %d, cleanup %d, old queue deleted %d (newest first): %v", created, cleanup, deleted, evs)
	}
	if ev, _ := hasEvent(evs, "Q", "DELETE_COMPLETE"); str(ev, "PhysicalResourceId") != first {
		t.Fatalf("the deleted physical ID is the old one: %v", ev)
	}
	if ev, ok := hasEvent(evs, "Q", "UPDATE_IN_PROGRESS"); !ok || !strings.Contains(str(ev, "ResourceStatusReason"), "creation of a new physical resource") {
		t.Fatalf("replacement reason: %v", ev)
	}

	// A failure after a replacement puts the old resource back and removes the new one.
	bad := `{"Resources":{"Q":{"Type":"AWS::SQS::Queue","Properties":{"VisibilityTimeout":30}},
  "Param":{"Type":"AWS::SSM::Parameter","Properties":{"Name":"/repl/q","Type":"String","Value":{"Ref":"Q"}}},
  "Bad":{"Type":"AWS::Test::Boom","DependsOn":"Param"}},"Outputs":{"Url":{"Value":{"Ref":"Q"}}}}`
	e.AWS(t, "cloudformation", "update-stack", "--stack-name", "repl", "--template-body", bad)
	st := e.waitFor(t, "repl", "UPDATE_ROLLBACK_COMPLETE")
	if outputs(st)["Url"] != second {
		t.Fatalf("after rollback the stack is back on %s, got %v", second, outputs(st))
	}
	if v := e.AWSJSON(t, "ssm", "get-parameter", "--name", "/repl/q")["Parameter"].(map[string]any)["Value"]; v != second {
		t.Fatalf("the parameter was not restored: %v", v)
	}
	if o, err := e.AWSErr(t, "sqs", "get-queue-attributes", "--queue-url", second, "--attribute-names", "All"); err != nil {
		t.Fatalf("the original queue is gone: %s", o)
	}
	qs := e.AWSJSON(t, "sqs", "list-queues")["QueueUrls"].([]any)
	if len(qs) != 1 {
		t.Fatalf("queues after rollback: %v", qs)
	}
}

func TestUpdateCustomNameReplacement(t *testing.T) {
	e := newEnv(t, false)
	v1 := `{"Resources":{"Q":{"Type":"AWS::SQS::Queue","Properties":{"QueueName":"named.fifo"}},"P":{"Type":"AWS::SSM::Parameter","Properties":{"Name":"/named/p","Type":"String","Value":"a"}}}}`
	e.AWS(t, "cloudformation", "create-stack", "--stack-name", "named", "--template-body", v1)
	e.waitFor(t, "named", "CREATE_COMPLETE")
	// FifoQueue needs a replacement and the name would stay the same.
	v2 := `{"Resources":{"Q":{"Type":"AWS::SQS::Queue","Properties":{"QueueName":"named.fifo","FifoQueue":true}},"P":{"Type":"AWS::SSM::Parameter","Properties":{"Name":"/named/p","Type":"String","Value":"b"}}}}`
	e.AWS(t, "cloudformation", "update-stack", "--stack-name", "named", "--template-body", v2)
	e.waitFor(t, "named", "UPDATE_ROLLBACK_COMPLETE")
	evs := e.events(t, "named")
	ev, ok := hasEvent(evs, "Q", "UPDATE_FAILED")
	if !ok || !strings.Contains(str(ev, "ResourceStatusReason"), "CloudFormation cannot update a stack when a custom-named resource requires replacing. Rename named.fifo and update the stack again.") {
		t.Fatalf("custom name event: %v", ev)
	}
	if ev, _ := hasEvent(evs, "named", "UPDATE_ROLLBACK_IN_PROGRESS"); !strings.Contains(str(ev, "ResourceStatusReason"), "failed to update: [Q]") {
		t.Fatalf("stack reason: %v", ev)
	}
	e.AWS(t, "sqs", "get-queue-url", "--queue-name", "named.fifo")
	// The parameter is as before, whether or not it was changed before the queue failed.
	if v := e.AWSJSON(t, "ssm", "get-parameter", "--name", "/named/p")["Parameter"].(map[string]any)["Value"]; v != "a" {
		t.Fatalf("parameter %v", v)
	}

	// Renaming the resource in the same update is fine: the new name does not collide.
	v3 := `{"Resources":{"Q":{"Type":"AWS::SQS::Queue","Properties":{"QueueName":"renamed.fifo","FifoQueue":true}},"P":{"Type":"AWS::SSM::Parameter","Properties":{"Name":"/named/p","Type":"String","Value":"a"}}}}`
	e.AWS(t, "cloudformation", "update-stack", "--stack-name", "named", "--template-body", v3)
	e.waitFor(t, "named", "UPDATE_COMPLETE")
	e.AWS(t, "sqs", "get-queue-url", "--queue-name", "renamed.fifo")
	if o, err := e.AWSErr(t, "sqs", "get-queue-url", "--queue-name", "named.fifo"); err == nil {
		t.Fatalf("the old queue survived: %s", o)
	}
}

func TestUpdateDisableRollback(t *testing.T) {
	e := newEnv(t, false)
	v1 := `{"Resources":{"Q":{"Type":"AWS::SQS::Queue","Properties":{"QueueName":"dr-q","VisibilityTimeout":30}}}}`
	v2 := `{"Resources":{"Q":{"Type":"AWS::SQS::Queue","Properties":{"QueueName":"dr-q","VisibilityTimeout":60}},
  "T":{"Type":"AWS::SNS::Topic","Properties":{"TopicName":"dr-t"}},"Bad":{"Type":"AWS::Test::Boom","DependsOn":["T","Q"]}}}`
	e.AWS(t, "cloudformation", "create-stack", "--stack-name", "dr", "--template-body", v1)
	e.waitFor(t, "dr", "CREATE_COMPLETE")
	e.AWS(t, "cloudformation", "update-stack", "--stack-name", "dr", "--template-body", v2, "--disable-rollback")
	st := e.waitFor(t, "dr", "UPDATE_FAILED")
	if !strings.Contains(str(st, "StackStatusReason"), "failed to create: [Bad]") || st["DisableRollback"] != true {
		t.Fatalf("stack: %v", st)
	}
	e.AWS(t, "sns", "get-topic-attributes", "--topic-arn", "arn:aws:sns:us-east-1:"+e.Env.AccountID+":dr-t")
	q := e.AWSJSON(t, "sqs", "get-queue-url", "--queue-name", "dr-q")
	if v := queueAttr(t, e, str(q, "QueueUrl"), "VisibilityTimeout"); v != "60" {
		t.Fatalf("with rollback disabled the queue keeps the update: %q", v)
	}
	if _, ok := hasEvent(e.events(t, "dr"), "dr", "UPDATE_ROLLBACK_IN_PROGRESS"); ok {
		t.Fatal("rolled back although rollback was disabled")
	}
	// The stack can be updated again from UPDATE_FAILED; the leftovers of the failure are cleaned up.
	e.AWS(t, "cloudformation", "update-stack", "--stack-name", "dr", "--template-body", v1)
	e.waitFor(t, "dr", "UPDATE_COMPLETE")
	if o, err := e.AWSErr(t, "sns", "get-topic-attributes", "--topic-arn", "arn:aws:sns:us-east-1:"+e.Env.AccountID+":dr-t"); err == nil {
		t.Fatalf("the topic of the failed update survived the next update: %s", o)
	}
}

func TestUpdateDisableRollbackKeepsReplacedResourceUntilNextUpdate(t *testing.T) {
	e := newEnv(t, false)
	v1 := `{"Resources":{"Q":{"Type":"AWS::SQS::Queue","Properties":{"VisibilityTimeout":30}}}}`
	v2 := `{"Resources":{"Q":{"Type":"AWS::SQS::Queue","Properties":{"FifoQueue":true}},"Bad":{"Type":"AWS::Test::Boom","DependsOn":"Q"}}}`
	queues := func() int { l, _ := e.AWSJSON(t, "sqs", "list-queues")["QueueUrls"].([]any); return len(l) }
	e.AWS(t, "cloudformation", "create-stack", "--stack-name", "left", "--template-body", v1)
	e.waitFor(t, "left", "CREATE_COMPLETE")
	e.AWS(t, "cloudformation", "update-stack", "--stack-name", "left", "--template-body", v2, "--disable-rollback")
	e.waitFor(t, "left", "UPDATE_FAILED")
	if n := queues(); n != 2 {
		t.Fatalf("the old queue stays until the next update, got %d queues", n)
	}
	e.AWS(t, "cloudformation", "update-stack", "--stack-name", "left", "--template-body", v1)
	e.waitFor(t, "left", "UPDATE_COMPLETE")
	if n := queues(); n != 1 {
		t.Fatalf("the next successful update removes what the failed one left, got %d queues", n)
	}
	e.AWS(t, "cloudformation", "update-stack", "--stack-name", "left", "--template-body", v2, "--disable-rollback")
	e.waitFor(t, "left", "UPDATE_FAILED")
	e.AWS(t, "cloudformation", "delete-stack", "--stack-name", "left")
	e.waitGone(t, "left")
	if n := queues(); n != 0 {
		t.Fatalf("deleting the stack must remove the leftovers too, got %d queues", n)
	}
}

func TestContinueUpdateRollback(t *testing.T) {
	e := newEnv(t, false)
	v1 := `{"Resources":{"Q":{"Type":"AWS::SQS::Queue","Properties":{"QueueName":"cur-q","VisibilityTimeout":30}}}}`
	v2 := `{"Resources":{"Q":{"Type":"AWS::SQS::Queue","Properties":{"QueueName":"cur-q","VisibilityTimeout":60}},
  "F":{"Type":"AWS::Test::Flaky"},"Bad":{"Type":"AWS::Test::Boom","DependsOn":["F","Q"]}}}`
	e.AWS(t, "cloudformation", "create-stack", "--stack-name", "cur", "--template-body", v1)
	e.waitFor(t, "cur", "CREATE_COMPLETE")

	failDeletes.Store(1)
	e.AWS(t, "cloudformation", "update-stack", "--stack-name", "cur", "--template-body", v2)
	st := e.waitFor(t, "cur", "UPDATE_ROLLBACK_FAILED")
	if !strings.Contains(str(st, "StackStatusReason"), "failed to delete: [F]") {
		t.Fatalf("reason: %v", st["StackStatusReason"])
	}
	if o, err := e.AWSErr(t, "cloudformation", "update-stack", "--stack-name", "cur", "--template-body", v1); err == nil || !strings.Contains(o, "UPDATE_ROLLBACK_FAILED") {
		t.Fatalf("update of a stack whose rollback failed: %v %s", err, o)
	}
	if o, err := e.AWSErr(t, "cloudformation", "continue-update-rollback", "--stack-name", "nope"); err == nil {
		t.Fatalf("unknown stack: %s", o)
	}
	e.AWS(t, "cloudformation", "continue-update-rollback", "--stack-name", "cur")
	e.waitFor(t, "cur", "UPDATE_ROLLBACK_COMPLETE")
	if v := queueAttr(t, e, str(e.AWSJSON(t, "sqs", "get-queue-url", "--queue-name", "cur-q"), "QueueUrl"), "VisibilityTimeout"); v != "30" {
		t.Fatalf("queue after continued rollback: %q", v)
	}
	if res := e.AWSJSON(t, "cloudformation", "list-stack-resources", "--stack-name", "cur")["StackResourceSummaries"].([]any); len(res) != 1 {
		t.Fatalf("resources: %v", res)
	}
	if o, err := e.AWSErr(t, "cloudformation", "continue-update-rollback", "--stack-name", "cur"); err == nil || !strings.Contains(o, "UPDATE_ROLLBACK_COMPLETE") {
		t.Fatalf("continue on a finished rollback: %v %s", err, o)
	}

	// ResourcesToSkip gives up on a resource that cannot be removed.
	failDeletes.Store(100)
	e.AWS(t, "cloudformation", "update-stack", "--stack-name", "cur", "--template-body", v2)
	e.waitFor(t, "cur", "UPDATE_ROLLBACK_FAILED")
	e.AWS(t, "cloudformation", "continue-update-rollback", "--stack-name", "cur", "--resources-to-skip", "F")
	e.waitFor(t, "cur", "UPDATE_ROLLBACK_COMPLETE")
	failDeletes.Store(0)
	if res := e.AWSJSON(t, "cloudformation", "list-stack-resources", "--stack-name", "cur")["StackResourceSummaries"].([]any); len(res) != 1 {
		t.Fatalf("resources after skip: %v", res)
	}
}

func TestUpdateRollbackRestoresFailedModification(t *testing.T) {
	e := newEnv(t, false)
	mk := func(v, timeout string) string {
		return `{"Resources":{"Q":{"Type":"AWS::SQS::Queue","Properties":{"QueueName":"rfm-q","VisibilityTimeout":` + timeout + `}},
  "S":{"Type":"AWS::Test::Sticky","Properties":{"Value":"` + v + `"},"DependsOn":"Q"}}}`
	}
	e.AWS(t, "cloudformation", "create-stack", "--stack-name", "rfm", "--template-body", mk("a", "30"))
	e.waitFor(t, "rfm", "CREATE_COMPLETE")
	failUpdates.Store(1) // the update of S fails; the rollback's own update of S then succeeds
	e.AWS(t, "cloudformation", "update-stack", "--stack-name", "rfm", "--template-body", mk("b", "77"))
	e.waitFor(t, "rfm", "UPDATE_ROLLBACK_COMPLETE")
	if v := queueAttr(t, e, str(e.AWSJSON(t, "sqs", "get-queue-url", "--queue-name", "rfm-q"), "QueueUrl"), "VisibilityTimeout"); v != "30" {
		t.Fatalf("the queue was not restored: %q", v)
	}
	evs := e.events(t, "rfm")
	if ev, ok := hasEvent(evs, "S", "UPDATE_FAILED"); !ok || !strings.Contains(str(ev, "ResourceStatusReason"), "sticky update failed") {
		t.Fatalf("S update failure: %v", evs)
	}
	if ev, _ := hasEvent(evs, "rfm", "UPDATE_ROLLBACK_IN_PROGRESS"); !strings.Contains(str(ev, "ResourceStatusReason"), "failed to update: [S]") {
		t.Fatalf("rollback reason: %v", ev)
	}
	// If restoring a resource fails, the rollback stops in UPDATE_ROLLBACK_FAILED.
	failUpdates.Store(2)
	e.AWS(t, "cloudformation", "update-stack", "--stack-name", "rfm", "--template-body", mk("c", "88"))
	st := e.waitFor(t, "rfm", "UPDATE_ROLLBACK_FAILED")
	if !strings.Contains(str(st, "StackStatusReason"), "failed to update: [S]") {
		t.Fatalf("reason: %v", st["StackStatusReason"])
	}
	e.AWS(t, "cloudformation", "continue-update-rollback", "--stack-name", "rfm")
	e.waitFor(t, "rfm", "UPDATE_ROLLBACK_COMPLETE")
	if v := queueAttr(t, e, str(e.AWSJSON(t, "sqs", "get-queue-url", "--queue-name", "rfm-q"), "QueueUrl"), "VisibilityTimeout"); v != "30" {
		t.Fatalf("the queue after the continued rollback: %q", v)
	}
}

func TestDeployFailureRollsBack(t *testing.T) {
	e := newEnv(t, false)
	good := write(t, "good.yaml", "Resources:\n  Q:\n    Type: AWS::SQS::Queue\n    Properties: {QueueName: dep-rb, VisibilityTimeout: 30}\n")
	bad := write(t, "bad.yaml", "Resources:\n  Q:\n    Type: AWS::SQS::Queue\n    Properties: {QueueName: dep-rb, VisibilityTimeout: 99}\n  Boom:\n    Type: AWS::Test::Boom\n    DependsOn: Q\n")
	if o, err := e.AWSAs(t, e.AccessKeyID, e.SecretKey, "", "cloudformation", "deploy", "--template-file", good, "--stack-name", "deprb"); err != nil {
		t.Fatalf("first deploy: %v\n%s", err, o)
	}
	time.Sleep(1100 * time.Millisecond) // the CLI names change sets by the second
	o, err := e.AWSAs(t, e.AccessKeyID, e.SecretKey, "", "cloudformation", "deploy", "--template-file", bad, "--stack-name", "deprb")
	if err == nil || !strings.Contains(o, "Failed to create/update the stack") {
		t.Fatalf("a failing deploy must fail: %v\n%s", err, o)
	}
	e.waitFor(t, "deprb", "UPDATE_ROLLBACK_COMPLETE")
	url := str(e.AWSJSON(t, "sqs", "get-queue-url", "--queue-name", "dep-rb"), "QueueUrl")
	if v := queueAttr(t, e, url, "VisibilityTimeout"); v != "30" {
		t.Fatalf("after the failed deploy the queue has %q", v)
	}
	// A rolled back stack deploys again.
	time.Sleep(1100 * time.Millisecond) // the CLI names change sets by the second
	fixed := write(t, "fixed.yaml", "Resources:\n  Q:\n    Type: AWS::SQS::Queue\n    Properties: {QueueName: dep-rb, VisibilityTimeout: 45}\n")
	if o, err := e.AWSAs(t, e.AccessKeyID, e.SecretKey, "", "cloudformation", "deploy", "--template-file", fixed, "--stack-name", "deprb"); err != nil {
		t.Fatalf("deploy after the rollback: %v\n%s", err, o)
	}
	if v := queueAttr(t, e, url, "VisibilityTimeout"); v != "45" {
		t.Fatalf("queue after the second deploy: %q", v)
	}
}

func TestBoto3UpdateRollbackWaiters(t *testing.T) {
	e := newEnv(t, false)
	good := `{"Resources":{"Q":{"Type":"AWS::SQS::Queue","Properties":{"QueueName":"wrb","VisibilityTimeout":30}}}}`
	bad := `{"Resources":{"Q":{"Type":"AWS::SQS::Queue","Properties":{"QueueName":"wrb","VisibilityTimeout":31}},"B":{"Type":"AWS::Test::Boom","DependsOn":"Q"}}}`
	script := fmt.Sprintf(`
cf = boto3.client("cloudformation")
cf.create_stack(StackName="wrb", TemplateBody=%q)
cf.get_waiter("stack_create_complete").wait(StackName="wrb", WaiterConfig={"Delay": 1, "MaxAttempts": 60})
cf.update_stack(StackName="wrb", TemplateBody=%q.replace("30", "40"))
cf.get_waiter("stack_update_complete").wait(StackName="wrb", WaiterConfig={"Delay": 1, "MaxAttempts": 60})
cf.update_stack(StackName="wrb", TemplateBody=%q)
try:
    cf.get_waiter("stack_update_complete").wait(StackName="wrb", WaiterConfig={"Delay": 1, "MaxAttempts": 60})
    raise SystemExit("the update waiter must fail when the update rolls back")
except botocore.exceptions.WaiterError as err:
    assert "UPDATE_ROLLBACK_COMPLETE" in str(err), err
cf.get_waiter("stack_rollback_complete").wait(StackName="wrb", WaiterConfig={"Delay": 1, "MaxAttempts": 60})
st = cf.describe_stacks(StackName="wrb")["Stacks"][0]
assert st["StackStatus"] == "UPDATE_ROLLBACK_COMPLETE", st
events = cf.describe_stack_events(StackName="wrb")["StackEvents"]
statuses = [e["ResourceStatus"] for e in events if e["LogicalResourceId"] == "wrb"]
for want in ["UPDATE_IN_PROGRESS", "UPDATE_COMPLETE", "UPDATE_ROLLBACK_IN_PROGRESS", "UPDATE_ROLLBACK_COMPLETE_CLEANUP_IN_PROGRESS", "UPDATE_ROLLBACK_COMPLETE"]:
    assert want in statuses, (want, statuses)
q = boto3.client("sqs")
url = q.get_queue_url(QueueName="wrb")["QueueUrl"]
assert q.get_queue_attributes(QueueUrl=url, AttributeNames=["VisibilityTimeout"])["Attributes"]["VisibilityTimeout"] == "40"
print("ok")
`, good, good, bad)
	if out := e.Python(t, script); !strings.Contains(out, "ok") {
		t.Fatal(out)
	}
}
