package iam

import (
	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
)

const stsNS = "https://sts.amazonaws.com/doc/2011-06-15/"

// RegisterAWS serves IAM and STS over the AWS Query protocol.
func (s *Service) RegisterAWS() {
	s.registerIAM()
	awsapi.Register(&awsapi.Service{
		Name: "sts", XMLNS: stsNS,
		ErrorCode: map[string]string{"ResourceNotFound": "NoSuchEntity", "ValidationError": "ValidationError", "AccessDenied": "AccessDenied"},
		Ops: map[string]awsapi.Op{
			"GetCallerIdentity": s.awsGetCallerIdentity,
			"AssumeRole":        s.awsAssumeRole,
			"GetSessionToken":   s.awsGetSessionToken,
		},
	})
}

func (s *Service) awsGetCallerIdentity(q *awsapi.Req) (any, error) {
	q.Authorize("sts:GetCallerIdentity", "*")
	id := q.P.UserName
	if u, err := store_getUser(s, q.P.UserName); err == nil {
		id = u.ID
	}
	if q.P.RoleName != "" {
		if r, err := s.GetRole(q.P.RoleName); err == nil {
			id = r.ID + ":" + q.P.SessionName
		}
	}
	return map[string]any{"Arn": q.P.ARN, "UserId": id, "Account": q.P.AccountID}, nil
}

func credsXML(c Credentials) map[string]any {
	return map[string]any{"AccessKeyId": c.AccessKeyID, "SecretAccessKey": c.SecretAccessKey, "SessionToken": c.SessionToken, "Expiration": c.Expiration}
}

func (s *Service) awsAssumeRole(q *awsapi.Req) (any, error) {
	arn := q.Param("RoleArn")
	q.Authorize("sts:AssumeRole", arn)
	c, err := s.AssumeRole(q.P, arn, q.Param("RoleSessionName"), q.ParamInt("DurationSeconds", 0), q.Param("ExternalId"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"Credentials": credsXML(c), "AssumedRoleUser": map[string]any{"Arn": c.AssumedRoleARN, "AssumedRoleId": c.AssumedRoleID}}, nil
}

func (s *Service) awsGetSessionToken(q *awsapi.Req) (any, error) {
	q.Authorize("sts:GetSessionToken", "*")
	c, err := s.SessionToken(q.P, q.ParamInt("DurationSeconds", 0))
	if err != nil {
		return nil, err
	}
	return map[string]any{"Credentials": credsXML(c)}, nil
}
