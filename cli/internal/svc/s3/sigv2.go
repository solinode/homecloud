package s3

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
)

// AWS Signature Version 2 (HMAC-SHA1). boto3's generate_presigned_url still
// produces SigV2 query-string URLs for S3 unless signature_version is set, and
// some older tools sign requests with it, so the S3 endpoint accepts both
// forms (Authorization: AWS key:sig, and ?AWSAccessKeyId=&Signature=&Expires=).

// v2SubResources are the query parameters included in the signed resource.
var v2SubResources = map[string]bool{
	"accelerate": true, "acl": true, "cors": true, "defaultObjectAcl": true, "location": true, "logging": true,
	"partNumber": true, "policy": true, "requestPayment": true, "torrent": true, "versioning": true, "versionId": true,
	"versions": true, "website": true, "uploads": true, "uploadId": true, "response-content-type": true,
	"response-content-language": true, "response-expires": true, "response-cache-control": true,
	"response-content-disposition": true, "response-content-encoding": true, "delete": true, "lifecycle": true,
	"tagging": true, "restore": true, "storageClass": true, "notification": true, "replication": true,
	"analytics": true, "metrics": true, "inventory": true, "select": true, "select-type": true, "object-lock": true,
}

// v2QueryParams are removed before forwarding a SigV2 presigned request.
var v2QueryParams = []string{"AWSAccessKeyId", "Signature", "Expires"}

func isV2Header(r *http.Request) bool {
	return strings.HasPrefix(r.Header.Get("Authorization"), "AWS ")
}

func isV2Query(q url.Values) bool {
	return q.Get("AWSAccessKeyId") != "" && q.Get("Signature") != "" && q.Get("Expires") != ""
}

// sigV2 authenticates a SigV2-signed request, setting the principal. It
// reports false when the request carries no SigV2 signature.
func (s *Service) sigV2(a *s3req) (bool, error) {
	r := a.q.R
	var akid, sig, date, token string
	switch {
	case isV2Header(r):
		cred := strings.TrimPrefix(r.Header.Get("Authorization"), "AWS ")
		i := strings.LastIndexByte(cred, ':')
		if i <= 0 {
			return true, awsapi.Errorf(http.StatusBadRequest, "InvalidArgument", "malformed Authorization header")
		}
		akid, sig = cred[:i], cred[i+1:]
		token = r.Header.Get("X-Amz-Security-Token")
		t, err := http.ParseTime(r.Header.Get("X-Amz-Date"))
		if err != nil {
			t, err = http.ParseTime(r.Header.Get("Date"))
			date = r.Header.Get("Date")
		}
		if err != nil {
			return true, awsapi.Errorf(http.StatusForbidden, "AccessDenied", "AWS authentication requires a valid Date or x-amz-date header")
		}
		if d := time.Since(t); d > 15*time.Minute || d < -15*time.Minute {
			return true, awsapi.Errorf(http.StatusForbidden, "RequestTimeTooSkewed", "the difference between the request time and the server's time is too large")
		}
	case isV2Query(a.query):
		akid, sig, date = a.query.Get("AWSAccessKeyId"), a.query.Get("Signature"), a.query.Get("Expires")
		token = a.query.Get("x-amz-security-token")
		exp, err := strconv.ParseInt(date, 10, 64)
		if err != nil {
			return true, awsapi.Errorf(http.StatusForbidden, "AccessDenied", "invalid Expires")
		}
		if time.Now().Unix() > exp {
			return true, awsapi.Errorf(http.StatusForbidden, "AccessDenied", "Request has expired")
		}
		if exp-time.Now().Unix() > 7*24*3600 {
			return true, awsapi.Errorf(http.StatusBadRequest, "AuthorizationQueryParametersError", "presigned URLs may be valid for at most 7 days")
		}
	default:
		return false, nil
	}
	if a.q.Creds == nil {
		return true, awsapi.Errorf(http.StatusForbidden, "AccessDenied", "signature version 2 is not available")
	}
	secret, p, err := a.q.Creds.SigningSecret(akid, token)
	if err != nil {
		return true, err
	}
	m := hmac.New(sha1.New, []byte(secret))
	m.Write([]byte(s.v2StringToSign(a, date)))
	want := base64.StdEncoding.EncodeToString(m.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(want), []byte(sig)) != 1 {
		return true, awsapi.Errorf(http.StatusForbidden, "SignatureDoesNotMatch",
			"The request signature we calculated does not match the signature you provided. Check your key and signing method.")
	}
	a.q.P = p
	return true, nil
}

func (s *Service) v2StringToSign(a *s3req, date string) string {
	r := a.q.R
	q := a.query
	first := func(qk, hk string) string {
		if v := q.Get(qk); v != "" {
			return v
		}
		return r.Header.Get(hk)
	}
	var b strings.Builder
	b.WriteString(r.Method + "\n")
	b.WriteString(strings.TrimSpace(first("content-md5", "Content-Md5")) + "\n")
	b.WriteString(strings.TrimSpace(first("content-type", "Content-Type")) + "\n")
	b.WriteString(date + "\n")
	amz := map[string][]string{}
	for k, vv := range r.Header {
		if lk := strings.ToLower(k); strings.HasPrefix(lk, "x-amz-") && lk != "x-amz-date" || lk == "x-amz-date" && isV2Header(r) {
			for _, v := range vv {
				amz[lk] = append(amz[lk], strings.TrimSpace(v))
			}
		}
	}
	for k, vv := range q {
		if lk := strings.ToLower(k); strings.HasPrefix(lk, "x-amz-") && !strings.HasPrefix(k, "X-Amz-") {
			amz[lk] = append(amz[lk], vv...)
		}
	}
	keys := make([]string, 0, len(amz))
	for k := range amz {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b.WriteString(k + ":" + strings.Join(amz[k], ",") + "\n")
	}
	// Resource: the path as sent, always including the bucket.
	p := a.q.R.RequestURI
	if i := strings.IndexByte(p, '?'); i >= 0 {
		p = p[:i]
	}
	if u, err := url.Parse(p); err == nil && u.IsAbs() {
		p = u.EscapedPath()
	}
	if b0, _ := s.vhostBucket(r.Host); b0 != "" {
		p = "/" + b0 + p
	}
	b.WriteString(p)
	type kv struct{ k, v string }
	var subs []kv
	for _, part := range strings.Split(r.URL.RawQuery, "&") {
		k, v, hasV := strings.Cut(part, "=")
		if !v2SubResources[k] {
			continue
		}
		if hasV {
			if dv, err := url.QueryUnescape(v); err == nil {
				v = dv
			}
			subs = append(subs, kv{k, "=" + v})
		} else {
			subs = append(subs, kv{k, ""})
		}
	}
	sort.SliceStable(subs, func(i, j int) bool { return subs[i].k < subs[j].k })
	for i, sr := range subs {
		if i == 0 {
			b.WriteByte('?')
		} else {
			b.WriteByte('&')
		}
		b.WriteString(sr.k + sr.v)
	}
	return b.String()
}
