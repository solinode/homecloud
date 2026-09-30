package awsapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/httpx"
)

type noCreds struct{}

func (noCreds) SigningSecret(string, string) (string, *httpx.Principal, error) {
	return "", nil, Errorf(http.StatusForbidden, "InvalidClientTokenId", "The security token included in the request is invalid.")
}

// tripBody fails the test when the server reads it.
type tripBody struct{ t *testing.T }

func (b tripBody) Read([]byte) (int, error) {
	b.t.Error("the body of an unauthenticated request was read")
	return 0, errors.New("read")
}
func (tripBody) Close() error { return nil }

const testSvc = "regsvc"

func init() {
	Register(&Service{Name: testSvc, JSONPrefix: "RegSvc", Ops: map[string]Op{"Ping": func(*Req) (any, error) { return nil, nil }},
		PublicOps: map[string]bool{"Open": true}})
}

func signedReq(t *testing.T, signed string, body *strings.Reader) *http.Request {
	now := time.Now().UTC().Format(amzDateFormat)
	r := httptest.NewRequest(http.MethodPost, "/", body)
	r.Header.Set("X-Amz-Date", now)
	r.Header.Set("X-Amz-Target", "RegSvc.Ping")
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=AKIAUNKNOWN/"+now[:8]+"/us-east-1/"+testSvc+"/aws4_request, SignedHeaders="+signed+", Signature=00")
	return r
}

// A request signed with an unknown access key must be refused before its body
// is buffered: otherwise anyone can make the server allocate up to maxBody bytes
// per connection.
func TestBodyNotReadBeforeAuthentication(t *testing.T) {
	h := &Handler{Creds: noCreds{}}
	r := signedReq(t, "host;x-amz-date;x-amz-target", strings.NewReader(""))
	r.Body = tripBody{t}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
}

func TestUnsignedBodyIsCapped(t *testing.T) {
	h := &Handler{Creds: noCreds{}}
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("a", maxPublicBody+10)))
	r.Header.Set("X-Amz-Target", "RegSvc.Open")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
}

func TestHostAndTargetMustBeSigned(t *testing.T) {
	for _, signed := range []string{"x-amz-date", "host;x-amz-date"} {
		if _, err := ParseSignature(signedReq(t, signed, strings.NewReader(""))); err == nil {
			t.Errorf("SignedHeaders=%s accepted", signed)
		}
	}
	if _, err := ParseSignature(signedReq(t, "host;x-amz-date;x-amz-target", strings.NewReader(""))); err != nil {
		t.Error(err)
	}
}
