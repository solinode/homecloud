package lambda

import (
	"testing"

	"github.com/homecloudhq/homecloud/cli/internal/svc/svctest"
)

// Function URLs and API Gateway endpoints follow the public URL (scheme and
// port included), and the API's TLS setting when there is none.
func TestGeneratedURLsFollowPublicURL(t *testing.T) {
	env := svctest.Env(t)
	env.Cfg.APIAddr, env.Cfg.PublicHost = "0.0.0.0:8080", "lab.example"
	s := &Service{env: env}
	if got := s.urlFor("fn"); got != "http://lab.example:8080/lambda-url/fn/" {
		t.Fatalf("default: %s", got)
	}
	env.Cfg.TLSCert = "cert.pem"
	if got := s.urlFor("fn"); got != "https://lab.example:8080/lambda-url/fn/" {
		t.Fatalf("built-in TLS must give https function URLs: %s", got)
	}
	env.Cfg.PublicURL = "https://cloud.example.com"
	if got := s.urlFor("fn"); got != "https://cloud.example.com/lambda-url/fn/" {
		t.Fatalf("public url: %s", got)
	}
	if got := s.apiEndpoint("abc123"); got != "https://cloud.example.com/apigw/abc123" {
		t.Fatalf("api endpoint: %s", got)
	}
	env.Cfg.TLSCert, env.Cfg.PublicURL = "", "http://plain.example:9000/hc"
	if got := s.apiEndpoint("abc123"); got != "http://plain.example:9000/hc/apigw/abc123" {
		t.Fatalf("path prefix: %s", got)
	}
}
