package elb

import (
	"reflect"
	"strings"
	"testing"
)

func TestPathList(t *testing.T) {
	got := Rule{Paths: []string{"/api/*", "/health", "/*.jpg"}}.pathList()
	want := []pathMatch{{"^~", "/api/"}, {"=", "/health"}, {"~", `^/.*\.jpg$`}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if got := (Rule{PathPrefix: "/x/"}).pathList(); got[0] != (pathMatch{"^~", "/x/"}) {
		t.Fatalf("native prefix: %v", got)
	}
}

func TestRedirectDirective(t *testing.T) {
	d := redirectDirective(RedirectConfig{Protocol: "HTTPS", Port: "443", Path: "/#{path}", StatusCode: "HTTP_301"}, LoadBalancer{PublicPorts: map[string]int{"443/tcp": 32775}})
	if !strings.Contains(d, `"https://$host:32775$uri`) || !strings.HasPrefix(d, "return 301") {
		t.Fatal(d)
	}
}

func TestARNRoundTrip(t *testing.T) {
	if n, ok := TargetGroupName("arn:aws:elasticloadbalancing:us-east-1:123456789012:targetgroup/web/0123456789abcdef"); !ok || n != "web" {
		t.Fatal(n, ok)
	}
	if _, ok := TargetGroupName("arn:aws:sqs:us-east-1:123456789012:web"); ok {
		t.Fatal("accepted a foreign ARN")
	}
}
