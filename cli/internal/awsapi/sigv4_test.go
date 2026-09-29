package awsapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Vectors from the AWS Signature Version 4 test suite.
func TestSigV4Vectors(t *testing.T) {
	const secret = "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"
	now, _ := time.Parse(amzDateFormat, "20150830T123600Z")
	cases := []struct{ target, sig string }{
		{"/", "5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31"},
		{"/?Param2=value2&Param1=value1", "b97d918cfa904a5beff61c982a1b6f458b799221646efd99d3219ec94cdf2500"},
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodGet, c.target, nil)
		r.Host = "example.amazonaws.com"
		r.Header.Set("X-Amz-Date", "20150830T123600Z")
		r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, SignedHeaders=host;x-amz-date, Signature="+c.sig)
		s, err := ParseSignature(r)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Verify(r, secret, HashHex(nil), now); err != nil {
			t.Errorf("%s: %v\n%s", c.target, err, CanonicalRequest(r, s, HashHex(nil)))
		}
		if err := s.Verify(r, "wrong", HashHex(nil), now); err == nil {
			t.Errorf("%s: wrong secret accepted", c.target)
		}
		if err := s.Verify(r, secret, HashHex(nil), now.Add(time.Hour)); err == nil {
			t.Errorf("%s: stale request accepted", c.target)
		}
	}
}
