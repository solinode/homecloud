package cognito

import "github.com/homecloudhq/homecloud/cli/internal/awsapi"

func (s *Service) awsForgotPassword(q *awsapi.Req) (any, error) {
	in := map[string]any{}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	p, cl, err := s.clientByID(str(in, "ClientId"))
	if err != nil {
		return nil, err
	}
	name := str(in, "Username")
	if name == "" {
		return nil, invalid("Missing required parameter Username")
	}
	if _, err := s.secretHash(cl, name, str(in, "SecretHash")); err != nil {
		return nil, err
	}
	u, err := s.forgotPassword(p.ID, name)
	if err != nil {
		return nil, err
	}
	return map[string]any{"CodeDeliveryDetails": codeDelivery(u)}, nil
}

func (s *Service) awsConfirmForgotPassword(q *awsapi.Req) (any, error) {
	in := map[string]any{}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	p, cl, err := s.clientByID(str(in, "ClientId"))
	if err != nil {
		return nil, err
	}
	name := str(in, "Username")
	if _, err := s.secretHash(cl, name, str(in, "SecretHash")); err != nil {
		return nil, err
	}
	if name == "" || str(in, "ConfirmationCode") == "" || str(in, "Password") == "" {
		return nil, invalid("Missing required parameter Username, ConfirmationCode or Password")
	}
	return nil, s.confirmForgotPassword(p.ID, name, str(in, "ConfirmationCode"), str(in, "Password"))
}

func (s *Service) awsAdminResetPassword(q *awsapi.Req) (any, error) {
	in := map[string]any{}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	id := str(in, "UserPoolId")
	if err := s.authz(q, "AdminResetUserPassword", id); err != nil {
		return nil, err
	}
	if _, err := s.pool(id); err != nil {
		return nil, err
	}
	return nil, s.adminResetPassword(id, str(in, "Username"))
}
