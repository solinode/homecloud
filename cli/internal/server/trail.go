package server

import (
	"bytes"
	"context"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/svc/s3"
	"github.com/homecloudhq/homecloud/cli/internal/svc/trail"
	"github.com/minio/minio-go/v7"
)

// wireTrail lets CloudTrail check and write to its delivery buckets in S3.
func wireTrail(t *trail.Service, s3s *s3.Service) {
	t.BucketExists = func(bucket string) (bool, error) {
		cl, err := s3s.Client()
		if err != nil {
			return false, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return cl.BucketExists(ctx, bucket)
	}
	t.Deliver = func(bucket, key string, body []byte) error {
		cl, err := s3s.Client()
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, err = cl.PutObject(ctx, bucket, key, bytes.NewReader(body), int64(len(body)),
			minio.PutObjectOptions{ContentType: "application/x-gzip", ContentEncoding: "gzip"})
		return err
	}
}
