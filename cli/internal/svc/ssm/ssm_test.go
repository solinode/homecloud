package ssm

import "testing"

func TestNameRule(t *testing.T) {
	for _, ok := range []string{"/app/db/url", "plain", "/a-b_c.d"} {
		if !nameRe.MatchString(ok) {
			t.Errorf("%s should be valid", ok)
		}
	}
	for _, bad := range []string{"", "has space", "bad!"} {
		if nameRe.MatchString(bad) {
			t.Errorf("%q should be invalid", bad)
		}
	}
}
