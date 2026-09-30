package lambda

import (
	"net/http"

	"github.com/homecloudhq/homecloud/cli/internal/httpx"
)

// permissionRoutes is the native form of AddPermission and RemovePermission,
// which CloudFormation's AWS::Lambda::Permission uses.
func (s *Service) permissionRoutes(r *httpx.Router) {
	res := httpx.Res("arn:aws:lambda:{region}:{account}:function:{name}")
	r.Handle("POST /api/v1/lambda/functions/{name}/permissions", "lambda:AddPermission", s.addPermissionRoute, res)
	r.Handle("DELETE /api/v1/lambda/functions/{name}/permissions/{sid}", "lambda:RemovePermission", s.removePermissionRoute, res)
}

func (s *Service) addPermissionRoute(c *httpx.Ctx) (any, error) {
	var in struct {
		StatementID         string `json:"statement_id"`
		Action              string `json:"action"`
		Principal           string `json:"principal"`
		SourceARN           string `json:"source_arn"`
		SourceAccount       string `json:"source_account"`
		EventSourceToken    string `json:"event_source_token"`
		PrincipalOrgID      string `json:"principal_org_id"`
		FunctionURLAuthType string `json:"function_url_auth_type"`
		RevisionID          string `json:"revision_id"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	st, err := s.addPermission(c.Param("name"), c.Query("qualifier"), permissionInput{StatementId: in.StatementID, Action: in.Action,
		Principal: in.Principal, SourceArn: in.SourceARN, SourceAccount: in.SourceAccount, EventSourceToken: in.EventSourceToken,
		RevisionId: in.RevisionID, PrincipalOrgID: in.PrincipalOrgID, FunctionUrlAuthType: in.FunctionURLAuthType})
	if err != nil {
		return nil, err
	}
	return c.JSON(http.StatusCreated, st)
}

func (s *Service) removePermissionRoute(c *httpx.Ctx) (any, error) {
	return nil, s.removePermission(c.Param("name"), c.Query("qualifier"), c.Param("sid"), c.Query("revision_id"))
}
