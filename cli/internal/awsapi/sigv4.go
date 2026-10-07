package awsapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	algorithm       = "AWS4-HMAC-SHA256"
	amzDateFormat   = "20060102T150405Z"
	maxClockSkew    = 15 * time.Minute
	UnsignedPayload = "UNSIGNED-PAYLOAD"
)

// Signature is a parsed AWS Signature Version 4 (header or presigned URL).
type Signature struct {
	AccessKeyID   string
	Date          string // yyyymmdd from the credential scope
	Region        string
	Service       string
	SignedHeaders []string
	Signature     string
	AmzDate       time.Time
	SessionToken  string
	Presigned     bool
	Expires       time.Duration // presigned URLs only
	PayloadHash   string        // X-Amz-Content-Sha256, when sent
}

// Scope is the credential scope "date/region/service/aws4_request".
func (s *Signature) Scope() string {
	return s.Date + "/" + s.Region + "/" + s.Service + "/aws4_request"
}

// IsSigned reports whether r carries a SigV4 signature (header or query).
func IsSigned(r *http.Request) bool {
	return strings.HasPrefix(r.Header.Get("Authorization"), algorithm+" ") || r.URL.Query().Get("X-Amz-Algorithm") == algorithm
}

// ParseSignature extracts the signature fields from r without verifying them.
func ParseSignature(r *http.Request) (*Signature, error) {
	s := &Signature{PayloadHash: r.Header.Get("X-Amz-Content-Sha256")}
	var cred, dateStr string
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, algorithm+" ") {
		for _, part := range strings.Split(strings.TrimPrefix(h, algorithm+" "), ",") {
			k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
			switch k {
			case "Credential":
				cred = v
			case "SignedHeaders":
				s.SignedHeaders = strings.Split(v, ";")
			case "Signature":
				s.Signature = v
			}
		}
		dateStr = r.Header.Get("X-Amz-Date")
		if dateStr == "" {
			if d, err := http.ParseTime(r.Header.Get("Date")); err == nil {
				dateStr = d.UTC().Format(amzDateFormat)
			}
		}
		s.SessionToken = r.Header.Get("X-Amz-Security-Token")
	} else {
		q := r.URL.Query()
		if q.Get("X-Amz-Algorithm") != algorithm {
			return nil, Errorf(http.StatusBadRequest, "IncompleteSignature", "unsupported signing algorithm")
		}
		s.Presigned = true
		cred, dateStr, s.Signature = q.Get("X-Amz-Credential"), q.Get("X-Amz-Date"), q.Get("X-Amz-Signature")
		s.SignedHeaders = strings.Split(q.Get("X-Amz-SignedHeaders"), ";")
		s.SessionToken = q.Get("X-Amz-Security-Token")
		secs, err := strconv.Atoi(q.Get("X-Amz-Expires"))
		if err != nil || secs < 1 || secs > 7*24*3600 {
			return nil, Errorf(http.StatusBadRequest, "AuthorizationQueryParametersError", "X-Amz-Expires must be between 1 and 604800 seconds")
		}
		s.Expires = time.Duration(secs) * time.Second
		if s.PayloadHash == "" {
			s.PayloadHash = UnsignedPayload
		}
	}
	parts := strings.Split(cred, "/")
	if len(parts) != 5 || parts[4] != "aws4_request" || s.Signature == "" || len(s.SignedHeaders) == 0 {
		return nil, Errorf(http.StatusBadRequest, "IncompleteSignature", "the request signature is malformed")
	}
	// As AWS does, insist that the host (and the operation header) are signed, so a
	// captured signature can't be replayed against another host or operation.
	signed := map[string]bool{}
	for _, h := range s.SignedHeaders {
		signed[h] = true
	}
	if !signed["host"] || (r.Header.Get("X-Amz-Target") != "" && !signed["x-amz-target"]) {
		return nil, Errorf(http.StatusForbidden, "SignatureDoesNotMatch", "the host and x-amz-target headers must be signed")
	}
	s.AccessKeyID, s.Date, s.Region, s.Service = parts[0], parts[1], parts[2], parts[3]
	t, err := time.Parse(amzDateFormat, dateStr)
	if err != nil {
		return nil, Errorf(http.StatusBadRequest, "IncompleteSignature", "missing or malformed X-Amz-Date")
	}
	s.AmzDate = t
	if !strings.HasPrefix(dateStr, s.Date) {
		return nil, Errorf(http.StatusForbidden, "SignatureDoesNotMatch", "credential scope date does not match X-Amz-Date")
	}
	return s, nil
}

// checkTime rejects stale or future-dated requests and expired presigned URLs.
func (s *Signature) checkTime(now time.Time) error {
	if s.Presigned {
		if now.Before(s.AmzDate.Add(-maxClockSkew)) {
			return Errorf(http.StatusForbidden, "AccessDenied", "request is not valid yet")
		}
		if now.After(s.AmzDate.Add(s.Expires)) {
			return Errorf(http.StatusForbidden, "AccessDenied", "request has expired")
		}
		return nil
	}
	if d := now.Sub(s.AmzDate); d > maxClockSkew || d < -maxClockSkew {
		return Errorf(http.StatusForbidden, "RequestTimeTooSkewed", "the difference between the request time and the server's time is too large")
	}
	return nil
}

// SigningKey derives the SigV4 signing key for secret and the signature's scope.
func (s *Signature) SigningKey(secret string) []byte {
	k := hmacSHA256([]byte("AWS4"+secret), s.Date)
	k = hmacSHA256(k, s.Region)
	k = hmacSHA256(k, s.Service)
	return hmacSHA256(k, "aws4_request")
}

// Verify checks the signature against secret. payloadHash is the hex SHA-256 of
// the body (or UNSIGNED-PAYLOAD / a STREAMING-* marker, as the client declared).
func (s *Signature) Verify(r *http.Request, secret, payloadHash string, now time.Time) error {
	if err := s.checkTime(now); err != nil {
		return err
	}
	creq := CanonicalRequest(r, s, payloadHash)
	sts := StringToSign(s, creq)
	want := hex.EncodeToString(hmacSHA256(s.SigningKey(secret), sts))
	if subtle.ConstantTimeCompare([]byte(want), []byte(s.Signature)) != 1 {
		return Errorf(http.StatusForbidden, "SignatureDoesNotMatch",
			"The request signature we calculated does not match the signature you provided. Check your key and signing method.")
	}
	return nil
}

// StringToSign builds the SigV4 string to sign for a canonical request.
func StringToSign(s *Signature, canonicalRequest string) string {
	h := sha256.Sum256([]byte(canonicalRequest))
	return algorithm + "\n" + s.AmzDate.Format(amzDateFormat) + "\n" + s.Scope() + "\n" + hex.EncodeToString(h[:])
}

// CanonicalRequest builds the SigV4 canonical request for r.
func CanonicalRequest(r *http.Request, s *Signature, payloadHash string) string {
	var b strings.Builder
	b.WriteString(r.Method)
	b.WriteByte('\n')
	b.WriteString(canonicalURI(r, s.Service))
	b.WriteByte('\n')
	b.WriteString(canonicalQuery(r, s.Presigned))
	b.WriteByte('\n')
	for _, h := range s.SignedHeaders {
		b.WriteString(h)
		b.WriteByte(':')
		b.WriteString(headerValue(r, h))
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	b.WriteString(strings.Join(s.SignedHeaders, ";"))
	b.WriteByte('\n')
	b.WriteString(payloadHash)
	return b.String()
}

// rawPath is the path exactly as the client sent it.
func rawPath(r *http.Request) string {
	p := r.RequestURI
	if i := strings.IndexByte(p, '?'); i >= 0 {
		p = p[:i]
	}
	if strings.HasPrefix(p, "http://") || strings.HasPrefix(p, "https://") { // absolute-form request target
		if u, err := url.Parse(p); err == nil {
			p = u.EscapedPath()
		}
	}
	if p == "" {
		p = "/"
	}
	return p
}

func canonicalURI(r *http.Request, service string) string {
	p := rawPath(r)
	if service == "s3" {
		return p // S3 signs the path as sent, without normalization or double encoding
	}
	// Other services sign the URI-encoded path once more (each segment encoded twice).
	segs := strings.Split(p, "/")
	for i, seg := range segs {
		segs[i] = Escape(seg)
	}
	return strings.Join(segs, "/")
}

func canonicalQuery(r *http.Request, presigned bool) string {
	raw := r.URL.RawQuery
	if raw == "" {
		return ""
	}
	type kv struct{ k, v string }
	var pairs []kv
	for _, part := range strings.Split(raw, "&") {
		if part == "" {
			continue
		}
		k, v, _ := strings.Cut(part, "=")
		dk, err1 := url.QueryUnescape(k)
		dv, err2 := url.QueryUnescape(v)
		if err1 != nil {
			dk = k
		}
		if err2 != nil {
			dv = v
		}
		if presigned && dk == "X-Amz-Signature" {
			continue
		}
		pairs = append(pairs, kv{Escape(dk), Escape(dv)})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].k != pairs[j].k {
			return pairs[i].k < pairs[j].k
		}
		return pairs[i].v < pairs[j].v
	})
	out := make([]string, len(pairs))
	for i, p := range pairs {
		out[i] = p.k + "=" + p.v
	}
	return strings.Join(out, "&")
}

func headerValue(r *http.Request, name string) string {
	var vals []string
	switch name {
	case "host":
		vals = []string{r.Host}
	case "content-length":
		vals = r.Header.Values("Content-Length")
		if len(vals) == 0 && r.ContentLength >= 0 {
			vals = []string{strconv.FormatInt(r.ContentLength, 10)}
		}
	default:
		vals = r.Header.Values(name)
	}
	for i, v := range vals {
		vals[i] = strings.Join(strings.Fields(v), " ")
	}
	return strings.Join(vals, ",")
}

// Escape percent-encodes s as SigV4 requires: everything except A-Z a-z 0-9 - _ . ~.
func Escape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if ('A' <= c && c <= 'Z') || ('a' <= c && c <= 'z') || ('0' <= c && c <= '9') || c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

// HashHex returns the hex SHA-256 of b.
func HashHex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
