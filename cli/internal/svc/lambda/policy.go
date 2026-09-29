package lambda

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
)

// PolicyStatement is one statement of a function's resource-based policy.
type PolicyStatement struct {
	Sid       string                       `json:"Sid"`
	Effect    string                       `json:"Effect"`
	Principal any                          `json:"Principal"`
	Action    string                       `json:"Action"`
	Resource  string                       `json:"Resource"`
	Condition map[string]map[string]string `json:"Condition,omitempty"`
}

// permissionInput is AddPermission's request.
type permissionInput struct {
	StatementId         string `json:"StatementId"`
	Action              string `json:"Action"`
	Principal           string `json:"Principal"`
	SourceArn           string `json:"SourceArn"`
	SourceAccount       string `json:"SourceAccount"`
	EventSourceToken    string `json:"EventSourceToken"`
	RevisionId          string `json:"RevisionId"`
	PrincipalOrgID      string `json:"PrincipalOrgID"`
	FunctionUrlAuthType string `json:"FunctionUrlAuthType"`
}

var (
	sidRe     = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,100}$`)
	accountRe = regexp.MustCompile(`^[0-9]{12}$`)
)

// principalOf renders AddPermission's Principal as a policy principal.
func principalOf(p string) any {
	switch {
	case p == "*":
		return "*"
	case accountRe.MatchString(p):
		return map[string]string{"AWS": core.ARN(p, "iam", "root")}
	case strings.HasPrefix(p, "arn:"):
		return map[string]string{"AWS": p}
	default:
		return map[string]string{"Service": p}
	}
}

func (s *Service) addPermission(name, qual string, in permissionInput) (PolicyStatement, error) {
	f, err := s.getLatest(name)
	if err != nil {
		return PolicyStatement{}, err
	}
	if qual != "" {
		if _, _, err := s.resolve(name, qual); err != nil {
			return PolicyStatement{}, err
		}
	}
	if qual == latest {
		qual = ""
	}
	if !sidRe.MatchString(in.StatementId) {
		return PolicyStatement{}, core.BadRequest("StatementId must be 1-100 letters, digits, '-', '_' or '.'")
	}
	if !strings.HasPrefix(in.Action, "lambda:") {
		return PolicyStatement{}, core.BadRequest("Action must start with lambda: (e.g. lambda:InvokeFunction)")
	}
	if in.Principal == "" {
		return PolicyStatement{}, core.BadRequest("Principal is required")
	}
	if in.RevisionId != "" && in.RevisionId != f.RevisionID {
		return PolicyStatement{}, core.Errf(http.StatusPreconditionFailed, "PreconditionFailed", "The Revision Id provided does not match the latest Revision Id.")
	}
	st := PolicyStatement{Sid: in.StatementId, Effect: "Allow", Principal: principalOf(in.Principal), Action: in.Action, Resource: f.qualifiedARN(qual)}
	cond := map[string]map[string]string{}
	add := func(op, key, v string) {
		if v == "" {
			return
		}
		if cond[op] == nil {
			cond[op] = map[string]string{}
		}
		cond[op][key] = v
	}
	add("ArnLike", "AWS:SourceArn", in.SourceArn)
	add("StringEquals", "AWS:SourceAccount", in.SourceAccount)
	add("StringEquals", "aws:PrincipalOrgID", in.PrincipalOrgID)
	add("StringEquals", "lambda:EventSourceToken", in.EventSourceToken)
	add("StringEquals", "lambda:FunctionUrlAuthType", in.FunctionUrlAuthType)
	if len(cond) > 0 {
		st.Condition = cond
	}
	_, err = s.modify(name, func(f *Function) error {
		for _, x := range f.Policy[qual] {
			if x.Sid == in.StatementId {
				return core.Conflict("The statement id (%s) provided already exists. Please provide a new statement id, or remove the existing statement.", in.StatementId)
			}
		}
		if f.Policy == nil {
			f.Policy = map[string][]PolicyStatement{}
		}
		f.Policy[qual] = append(f.Policy[qual], st)
		return nil
	})
	return st, err
}

func (s *Service) removePermission(name, qual, sid, revision string) error {
	if qual == latest {
		qual = ""
	}
	_, err := s.modify(name, func(f *Function) error {
		if revision != "" && revision != f.RevisionID {
			return core.Errf(http.StatusPreconditionFailed, "PreconditionFailed", "The Revision Id provided does not match the latest Revision Id.")
		}
		for i, x := range f.Policy[qual] {
			if x.Sid == sid {
				f.Policy[qual] = append(f.Policy[qual][:i], f.Policy[qual][i+1:]...)
				if len(f.Policy[qual]) == 0 {
					delete(f.Policy, qual)
				}
				return nil
			}
		}
		return core.Errf(http.StatusNotFound, "ResourceNotFound", "Statement %s is not found in resource policy.", sid)
	})
	return err
}

// policyDoc returns a function's resource-based policy as a JSON document.
func (s *Service) policyDoc(name, qual string) (string, string, error) {
	f, err := s.getLatest(name)
	if err != nil {
		return "", "", err
	}
	if qual == latest {
		qual = ""
	}
	sts := f.Policy[qual]
	if len(sts) == 0 {
		return "", "", core.Errf(http.StatusNotFound, "ResourceNotFound", "The resource you requested does not exist.")
	}
	b, _ := json.Marshal(map[string]any{"Version": "2012-10-17", "Id": "default", "Statement": sts})
	return string(b), f.RevisionID, nil
}

// policyAllows reports whether a function's resource policy grants a caller an
// action (for callers whose identity policies don't).
func (s *Service) policyAllows(name, qual string, p *httpx.Principal, action string) bool {
	if p == nil {
		return false
	}
	f, err := s.getLatest(name)
	if err != nil {
		return false
	}
	if qual == latest {
		qual = ""
	}
	for _, st := range f.Policy[qual] {
		if st.Effect != "Allow" || len(st.Condition) > 0 || (st.Action != action && st.Action != "lambda:*") {
			continue
		}
		switch pr := st.Principal.(type) {
		case string:
			if pr == "*" {
				return true
			}
		case map[string]any:
			if a, _ := pr["AWS"].(string); a == p.ARN {
				return true
			}
		case map[string]string:
			if pr["AWS"] == p.ARN {
				return true
			}
		}
	}
	return false
}
