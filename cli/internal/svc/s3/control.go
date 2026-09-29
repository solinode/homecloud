package s3

import (
	"encoding/xml"
	"net/http"
	"sort"
	"strings"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
	"github.com/homecloudhq/homecloud/cli/internal/core"
)

// S3 Control (signing name "s3", paths under /v20180820/, host prefixed with
// the account ID). HomeCloud implements the resource tagging operations, which
// the Terraform AWS provider uses for bucket tags.

const s3ControlNS = "http://awss3control.amazonaws.com/doc/2018-08-20/"

func isControl(r *http.Request) bool { return strings.HasPrefix(r.URL.Path, "/v20180820/") }

func (s *Service) serveControl(a *s3req) error {
	q := a.q
	if acct := q.R.Header.Get("X-Amz-Account-Id"); acct != "" && acct != q.Account {
		return awsapi.Errorf(http.StatusForbidden, "AccessDenied", "Access Denied")
	}
	arn, ok := strings.CutPrefix(q.R.URL.Path, "/v20180820/tags/")
	if !ok {
		return errNotImpl("S3 Control " + q.R.Method + " " + q.R.URL.Path)
	}
	bucket, isBucket := strings.CutPrefix(arn, "arn:aws:s3:::")
	if !isBucket || validBucket(bucket) != nil {
		return awsapi.Errorf(http.StatusBadRequest, "InvalidRequest", "HomeCloud supports tagging general purpose buckets (arn:aws:s3:::name) only")
	}
	a.bucket = bucket
	var op s3op
	switch q.R.Method {
	case http.MethodGet:
		op = s3op{"ListTagsForResource", "s3:ListTagsForResource", false}
	case http.MethodPost:
		op = s3op{"TagResource", "s3:TagResource", false}
	case http.MethodDelete:
		op = s3op{"UntagResource", "s3:UntagResource", false}
	default:
		return awsapi.Errorf(http.StatusMethodNotAllowed, "MethodNotAllowed", "the specified method is not allowed against this resource")
	}
	a.op, q.Op = op, op.name
	if err := s.authorize(a, op.action, a.bucketARN()); err != nil {
		return err
	}
	if err := s.requireBucket(a); err != nil {
		return err
	}
	switch op.name {
	case "ListTagsForResource":
		m := s.meta(bucket)
		keys := make([]string, 0, len(m.Tags))
		for k := range m.Tags {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := struct {
			XMLName xml.Name `xml:"http://awss3control.amazonaws.com/doc/2018-08-20/ ListTagsForResourceResult"`
			Tags    []xmlTag `xml:"Tags>Tag"`
		}{}
		for _, k := range keys {
			out.Tags = append(out.Tags, xmlTag{k, m.Tags[k]})
		}
		return a.writeXML(http.StatusOK, out)
	case "TagResource":
		b, _, err := s.readBody(a, 64<<10)
		if err != nil {
			return err
		}
		var in struct {
			Tags []xmlTag `xml:"Tags>Tag"`
		}
		if err := xml.Unmarshal(b, &in); err != nil {
			return awsapi.Errorf(http.StatusBadRequest, "MalformedXML", "the XML you provided was not well-formed")
		}
		if err := s.updateMeta(bucket, func(m *bucketMeta) {
			if m.Tags == nil {
				m.Tags = core.Tags{}
			}
			for _, t := range in.Tags {
				m.Tags[t.Key] = t.Value
			}
		}); err != nil {
			return err
		}
		return a.noContent()
	default: // UntagResource
		keys := a.query["tagKeys"]
		if err := s.updateMeta(bucket, func(m *bucketMeta) {
			for _, k := range keys {
				delete(m.Tags, k)
			}
		}); err != nil {
			return err
		}
		return a.noContent()
	}
}
