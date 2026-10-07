package s3

import (
	"bytes"
	"crypto/md5"
	"encoding/base64"
	"encoding/xml"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
)

// Lifecycle configurations: MinIO runs expiration and transition rules but
// rejects AbortIncompleteMultipartUpload (a rule with only that action fails
// the whole document as malformed, and the action is dropped from mixed
// rules). HomeCloud keeps the document as it was put and returns it, and
// gives MinIO the rules it can run, without that action. Incomplete multipart
// uploads are therefore not aborted by age.

var abortElem = regexp.MustCompile(`(?s)<AbortIncompleteMultipartUpload>.*?</AbortIncompleteMultipartUpload>|<AbortIncompleteMultipartUpload\s*/>`)

// minioActions are the rule actions MinIO runs.
var minioActions = []string{"Expiration", "Transition", "NoncurrentVersionExpiration", "NoncurrentVersionTransition", "DelMarkerExpiration"}

type lifecycleDoc struct {
	XMLName xml.Name
	Rules   []struct {
		Inner string `xml:",innerxml"`
	} `xml:"Rule"`
}

// minioLifecycle returns the document MinIO is given (nil when no rule has
// an action MinIO runs) or an error for a document that is not valid.
func minioLifecycle(doc []byte) ([]byte, error) {
	var d lifecycleDoc
	if err := xml.Unmarshal(doc, &d); err != nil || d.XMLName.Local != "LifecycleConfiguration" {
		return nil, awsapi.Errorf(http.StatusBadRequest, "MalformedXML", "The XML you provided was not well-formed or did not validate against our published schema")
	}
	if len(d.Rules) == 0 || len(d.Rules) > 1000 {
		return nil, awsapi.Errorf(http.StatusBadRequest, "MalformedXML", "A lifecycle configuration needs 1 to 1000 rules")
	}
	var out strings.Builder
	n := 0
	for _, r := range d.Rules {
		var actions struct {
			Elems []xml.Name `xml:",any"`
		}
		inner := r.Inner
		if err := xml.Unmarshal([]byte("<Rule>"+inner+"</Rule>"), &actions); err != nil {
			return nil, awsapi.Errorf(http.StatusBadRequest, "MalformedXML", "The XML you provided was not well-formed or did not validate against our published schema")
		}
		has, abort := false, false
		for _, e := range actions.Elems {
			for _, a := range minioActions {
				if strings.HasPrefix(e.Local, a) {
					has = true
				}
			}
			abort = abort || e.Local == "AbortIncompleteMultipartUpload"
		}
		if !has && !abort {
			return nil, awsapi.Errorf(http.StatusBadRequest, "InvalidRequest", "At least one action needs to be specified in a rule")
		}
		if !has {
			continue
		}
		n++
		out.WriteString("<Rule>" + abortElem.ReplaceAllString(inner, "") + "</Rule>")
	}
	if n == 0 {
		return nil, nil
	}
	return []byte(`<LifecycleConfiguration xmlns="` + s3NS + `">` + out.String() + `</LifecycleConfiguration>`), nil
}

func (s *Service) awsPutLifecycle(a *s3req) error {
	if err := s.requireBucket(a); err != nil {
		return err
	}
	b, _, err := s.readBody(a, 512<<10)
	if err != nil {
		return err
	}
	b = bytes.TrimSpace(b)
	if i := bytes.Index(b, []byte("?>")); bytes.HasPrefix(b, []byte("<?xml")) && i > 0 {
		b = bytes.TrimSpace(b[i+2:])
	}
	forMinio, err := minioLifecycle(b)
	if err != nil {
		return err
	}
	method, body := http.MethodPut, forMinio
	if forMinio == nil {
		method = http.MethodDelete
	}
	resp, err := s.send(a, method, bytes.NewReader(body), int64(len(body)), awsapi.HashHex(body), func(h http.Header) {
		// The client's checksums cover the document it sent, not this one.
		for k := range h {
			if strings.HasPrefix(k, "X-Amz-Checksum-") || k == "Content-Md5" || k == "X-Amz-Sdk-Checksum-Algorithm" || k == "X-Amz-Trailer" ||
				k == "X-Amz-Decoded-Content-Length" {
				h.Del(k)
			}
		}
		dropEncoding(h, "aws-chunked")
		if body != nil {
			sum := md5.Sum(body) // MinIO requires it for lifecycle documents
			h.Set("Content-Type", "application/xml")
			h.Set("Content-Md5", base64.StdEncoding.EncodeToString(sum[:]))
		}
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return s.relay(a, resp)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	size := a.q.R.Header.Get(transitionSizeHeader)
	if err := s.updateMeta(a.bucket, func(m *bucketMeta) {
		m.setConfig("lifecycle", string(b))
		m.setConfig("transitionMinSize", size)
	}); err != nil {
		return err
	}
	a.q.W.Header().Set(transitionSizeHeader, transitionSize(size))
	a.writeRaw(http.StatusOK, nil)
	return nil
}

func transitionSize(v string) string {
	if v == "" {
		return "all_storage_classes_128K"
	}
	return v
}

// getLifecycle returns the stored document; buckets whose lifecycle was set
// before HomeCloud kept it are answered by MinIO (ok is false).
func (s *Service) awsGetLifecycle(a *s3req) (ok bool, err error) {
	m := s.meta(a.bucket)
	doc := m.Config["lifecycle"]
	if doc == "" {
		return false, nil
	}
	if err := s.requireBucket(a); err != nil {
		return true, err
	}
	a.q.W.Header().Set(transitionSizeHeader, transitionSize(m.Config["transitionMinSize"]))
	a.writeRaw(http.StatusOK, []byte(xml.Header+doc))
	return true, nil
}

func (s *Service) forgetLifecycle(bucket string) {
	_ = s.updateMeta(bucket, func(m *bucketMeta) { m.setConfig("lifecycle", "") })
}
