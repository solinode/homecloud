package lambda

import "testing"

func TestEscapeSegments(t *testing.T) {
	for in, want := range map[string]string{
		"/a/b":         "/a/b",
		"/a?x=1#frag":  "/a%3Fx=1%23frag",
		"/with space/": "/with%20space/",
		"x/%2F":        "x/%252F",
	} {
		if got := escapeSegments(in); got != want {
			t.Errorf("escapeSegments(%q) = %q, want %q", in, got, want)
		}
	}
}
