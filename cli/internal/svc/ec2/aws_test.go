package ec2

import (
	"testing"

	"github.com/homecloudhq/homecloud/cli/internal/core"
)

func TestFilters(t *testing.T) {
	a := attrs{}.set("name", "ubuntu/images/hvm-ssd/ubuntu-jammy-22.04-amd64-server").set("key-name").tags(core.Tags{"env": "qa"})
	cases := []struct {
		fs   []filter
		want bool
	}{
		{[]filter{{"name", []string{"ubuntu/images/*jammy*"}}}, true},
		{[]filter{{"name", []string{"debian-*"}}}, false},
		{[]filter{{"tag:env", []string{"qa", "prod"}}}, true},
		{[]filter{{"tag:team", []string{"*"}}}, false},
		{[]filter{{"key-name", []string{"*"}}}, false},
		{[]filter{{"unknown-filter", []string{"x"}}}, true},
	}
	for i, c := range cases {
		if got := match(c.fs, a); got != c.want {
			t.Errorf("case %d: match = %v, want %v", i, got, c.want)
		}
	}
	if azID("us-east-1b") != "use1-az2" {
		t.Errorf("azID = %s", azID("us-east-1b"))
	}
}
