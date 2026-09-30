package server

import (
	"testing"

	"github.com/homecloudhq/homecloud/cli/internal/core"
)

// A target ARN of another account or region must not be treated as a local
// resource of the same name: IAM would authorize it as the foreign ARN while the
// delivery went to the local queue.
func TestTargetsRefuseForeignARNs(t *testing.T) {
	tg := &targets{account: "111122223333"}
	if _, _, ok := tg.parse(core.ARN("111122223333", "sqs", "q")); !ok {
		t.Fatal("a local ARN was refused")
	}
	if _, _, ok := tg.parse("arn:hc:sqs:local-1:111122223333:q"); !ok {
		t.Fatal("a legacy-form local ARN was refused")
	}
	for _, arn := range []string{
		"arn:aws:sqs:" + core.Region + ":999999999999:q",
		"arn:aws:sqs:eu-north-9:111122223333:q",
		"arn:aws:sqs::111122223333:q",
	} {
		if _, _, ok := tg.parse(arn); ok {
			t.Errorf("%s was accepted", arn)
		}
		if tg.exists(arn) {
			t.Errorf("%s exists", arn)
		}
	}
}
