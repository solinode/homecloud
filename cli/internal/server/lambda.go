package server

import (
	"context"
	"encoding/json"
	"io"
	"regexp"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/svc/dynamodb"
	"github.com/homecloudhq/homecloud/cli/internal/svc/ecr"
	"github.com/homecloudhq/homecloud/cli/internal/svc/events"
	"github.com/homecloudhq/homecloud/cli/internal/svc/iam"
	"github.com/homecloudhq/homecloud/cli/internal/svc/lambda"
	"github.com/homecloudhq/homecloud/cli/internal/svc/s3"
	"github.com/minio/minio-go/v7"
)

// lambdaRoles lets Lambda validate execution roles and assume them.
type lambdaRoles struct{ iam *iam.Service }

const lambdaPrincipal = "lambda.amazonaws.com"

func (r lambdaRoles) LambdaRole(ref string) (string, error) {
	role, err := r.iam.ServiceRole(ref, lambdaPrincipal)
	return role.ARN, err
}

func (r lambdaRoles) LambdaCredentials(ref, session string, ttl time.Duration) (lambda.Credentials, error) {
	c, err := r.iam.AssumeRoleForService(ref, lambdaPrincipal, session, ttl)
	return lambda.Credentials{AccessKeyID: c.AccessKeyID, SecretAccessKey: c.SecretAccessKey, SessionToken: c.SessionToken, Expiration: c.Expiration}, err
}

// ecrURI matches AWS-style ECR image URIs (<account>.dkr.ecr.<region>.amazonaws.com/<repo>...).
var ecrURI = regexp.MustCompile(`^[0-9]{12}\.dkr\.ecr\.[a-z0-9-]+\.amazonaws\.com/`)

// wireLambda connects Lambda to IAM (execution roles), S3 (code packages),
// ECR (container images) and the delivery targets of asynchronous invocations.
func wireLambda(l *lambda.Service, im *iam.Service, tg *targets, ev *events.Service, s3s *s3.Service, reg *ecr.Service) {
	l.Roles = lambdaRoles{im}
	l.Deliver, l.TargetExists = tg.deliver, tg.exists
	l.PutEvent = func(ctx context.Context, bus, source, detailType string, resources []string, detail []byte) error {
		ev.PublishEvent(ctx, source, detailType, resources, json.RawMessage(detail))
		return nil
	}
	l.GetObject = func(ctx context.Context, bucket, key, version string) ([]byte, error) {
		cl, err := s3s.Client()
		if err != nil {
			return nil, err
		}
		obj, err := cl.GetObject(ctx, bucket, key, minio.GetObjectOptions{VersionID: version})
		if err != nil {
			return nil, err
		}
		defer obj.Close()
		return io.ReadAll(io.LimitReader(obj, 51<<20))
	}
	l.ResolveImage = func(uri string) string {
		if loc := ecrURI.FindStringIndex(uri); loc != nil {
			return reg.Host() + "/" + uri[loc[1]:]
		}
		return uri
	}
	l.RegisterAWS()
}

// ddbStreams adapts DynamoDB stream records to the generic records Lambda
// event source mappings consume.
type ddbStreams struct{ d *dynamodb.Service }

func (a ddbStreams) ReadStream(arn, checkpoint string, limit int) ([]map[string]any, string, error) {
	recs, next, err := a.d.ReadStream(arn, checkpoint, limit)
	if err != nil {
		return nil, next, err
	}
	out := make([]map[string]any, 0, len(recs))
	for _, r := range recs {
		b, err := json.Marshal(r)
		if err != nil {
			return nil, checkpoint, err
		}
		m := map[string]any{}
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, checkpoint, err
		}
		out = append(out, m)
	}
	return out, next, nil
}
