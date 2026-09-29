package server

import (
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/svc/ec2"
	"github.com/homecloudhq/homecloud/cli/internal/svc/iam"
)

// ec2Roles gives EC2 instance profiles and their role's credentials (IAM).
type ec2Roles struct{ iam *iam.Service }

func (r ec2Roles) InstanceProfile(ref string) (arn, id, role string, err error) {
	p, err := r.iam.GetInstanceProfile(ref)
	if err != nil {
		return "", "", "", err
	}
	if rl, err := r.iam.InstanceProfileRole(ref); err == nil {
		role = rl.ARN
	}
	return p.ARN, p.ID, role, nil
}

func (r ec2Roles) InstanceCredentials(role, session string, ttl time.Duration) (ec2.Credentials, error) {
	c, err := r.iam.AssumeRoleForService(role, "ec2.amazonaws.com", session, ttl)
	if err != nil {
		return ec2.Credentials{}, err
	}
	return ec2.Credentials{AccessKeyID: c.AccessKeyID, SecretAccessKey: c.SecretAccessKey, SessionToken: c.SessionToken, Expiration: c.Expiration}, nil
}
