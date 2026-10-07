package cloudwatch

import (
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

// Logs Insights saved queries (query definitions): PutQueryDefinition,
// DescribeQueryDefinitions and DeleteQueryDefinition. They are stored for
// tools and consoles to list; running one is an ordinary StartQuery.

const cQueryDefs = "logs_query_definitions"

type queryDef struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	QueryString   string    `json:"query_string"`
	LogGroupNames []string  `json:"log_group_names,omitempty"`
	Language      string    `json:"query_language,omitempty"`
	ClientToken   string    `json:"client_token,omitempty"`
	ModifiedAt    time.Time `json:"modified_at"`
}

func newUUID() string {
	h := core.RandHex(32)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

func (s *Service) awsPutQueryDefinition(q *awsapi.Req) (any, error) {
	var in struct {
		Name              string   `json:"name"`
		QueryDefinitionId string   `json:"queryDefinitionId"`
		LogGroupNames     []string `json:"logGroupNames"`
		QueryString       string   `json:"queryString"`
		QueryLanguage     string   `json:"queryLanguage"`
		ClientToken       string   `json:"clientToken"`
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("logs:PutQueryDefinition", "*"); err != nil {
		return nil, err
	}
	if in.Name == "" || len(in.Name) > 255 {
		return nil, logsErr("InvalidParameterException", "name must be 1-255 characters")
	}
	if in.QueryString == "" || len(in.QueryString) > 10000 {
		return nil, logsErr("InvalidParameterException", "queryString must be 1-10000 characters")
	}
	switch in.QueryLanguage {
	case "":
		in.QueryLanguage = "CWLI"
	case "CWLI", "SQL", "PPL":
	default:
		return nil, logsErr("InvalidParameterException", "queryLanguage must be CWLI, SQL or PPL")
	}
	d := queryDef{ID: in.QueryDefinitionId, Name: in.Name, QueryString: in.QueryString, LogGroupNames: in.LogGroupNames,
		Language: in.QueryLanguage, ClientToken: in.ClientToken, ModifiedAt: core.Now()}
	if d.ID != "" {
		if !store.Has(s.env.Store, cQueryDefs, d.ID) {
			return nil, logsErr("ResourceNotFoundException", "Query definition %s does not exist.", d.ID)
		}
	} else {
		if in.ClientToken != "" {
			for _, x := range store.List[queryDef](s.env.Store, cQueryDefs) {
				if x.ClientToken == in.ClientToken {
					return map[string]any{"queryDefinitionId": x.ID}, nil
				}
			}
		}
		d.ID = newUUID()
	}
	if err := store.Put(s.env.Store, cQueryDefs, d.ID, d); err != nil {
		return nil, err
	}
	return map[string]any{"queryDefinitionId": d.ID}, nil
}

func (s *Service) awsDescribeQueryDefinitions(q *awsapi.Req) (any, error) {
	var in struct {
		Prefix        string `json:"queryDefinitionNamePrefix"`
		QueryLanguage string `json:"queryLanguage"`
		MaxResults    int    `json:"maxResults"`
		NextToken     string `json:"nextToken"`
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("logs:DescribeQueryDefinitions", "*"); err != nil {
		return nil, err
	}
	all := store.List[queryDef](s.env.Store, cQueryDefs)
	slices.SortFunc(all, func(a, b queryDef) int { return strings.Compare(a.Name+a.ID, b.Name+b.ID) })
	var out []map[string]any
	for _, d := range all {
		if !strings.HasPrefix(d.Name, in.Prefix) || (in.QueryLanguage != "" && in.QueryLanguage != d.Language) {
			continue
		}
		if in.NextToken != "" && d.Name+d.ID <= in.NextToken {
			continue
		}
		m := map[string]any{"queryDefinitionId": d.ID, "name": d.Name, "queryString": d.QueryString,
			"lastModified": d.ModifiedAt.UnixMilli(), "queryLanguage": d.Language}
		if len(d.LogGroupNames) > 0 {
			m["logGroupNames"] = d.LogGroupNames
		}
		out = append(out, m)
		if in.MaxResults > 0 && len(out) == in.MaxResults {
			return map[string]any{"queryDefinitions": out, "nextToken": d.Name + d.ID}, nil
		}
	}
	if out == nil {
		out = []map[string]any{}
	}
	return map[string]any{"queryDefinitions": out}, nil
}

func (s *Service) awsDeleteQueryDefinition(q *awsapi.Req) (any, error) {
	var in struct {
		ID string `json:"queryDefinitionId"`
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("logs:DeleteQueryDefinition", "*"); err != nil {
		return nil, err
	}
	if !store.Has(s.env.Store, cQueryDefs, in.ID) {
		return nil, awsapi.Errorf(http.StatusBadRequest, "ResourceNotFoundException", "Query definition %s does not exist.", in.ID)
	}
	if err := store.Delete(s.env.Store, cQueryDefs, in.ID); err != nil {
		return nil, err
	}
	return map[string]any{"success": true}, nil
}
