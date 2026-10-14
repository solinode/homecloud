package dynamodb

import (
	"slices"
	"sort"
	"strconv"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
	"github.com/homecloudhq/homecloud/cli/internal/store"
	bolt "go.etcd.io/bbolt"
)

// CloudWatch Contributor Insights for DynamoDB: UpdateContributorInsights,
// DescribeContributorInsights and ListContributorInsights. HomeCloud stores
// and reports the setting per table and per global secondary index; it
// creates no Contributor Insights rules and publishes no metrics.

// ContributorInsights is the stored setting of a table or one of its indexes.
type ContributorInsights struct {
	Status    string    `json:"status"`         // ENABLED | DISABLED
	Mode      string    `json:"mode,omitempty"` // ACCESSED_AND_THROTTLED_KEYS | THROTTLED_KEYS
	UpdatedAt time.Time `json:"updated_at"`
}

func (t *Table) hasGSI(name string) bool {
	return slices.ContainsFunc(t.Indexes, func(ix Index) bool { return ix.Name == name })
}

// insights returns the stored setting of the table (index "") or an index.
func (t *Table) insights(index string) (ContributorInsights, bool) {
	ci, ok := t.ContributorInsights[index]
	if ok && index != "" && !t.hasGSI(index) {
		return ContributorInsights{}, false // the index was deleted
	}
	return ci, ok
}

func (s *Service) insightsResource(table, index string) string {
	if index != "" {
		return s.tableARN(table) + "/index/" + index
	}
	return s.tableARN(table)
}

func indexNotFound(table, index string) error {
	return errf("ResourceNotFoundException", "Requested resource not found: Index: %s not found for table %s", index, table)
}

func (s *Service) awsUpdateContributorInsights(q *awsapi.Req) (any, error) {
	var in struct {
		TableName, IndexName, ContributorInsightsAction, ContributorInsightsMode string
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("dynamodb:UpdateContributorInsights", s.insightsResource(in.TableName, in.IndexName)); err != nil {
		return nil, err
	}
	status := map[string]string{"ENABLE": "ENABLED", "DISABLE": "DISABLED"}[in.ContributorInsightsAction]
	if status == "" {
		return nil, validation("1 validation error detected: Value '%s' at 'contributorInsightsAction' failed to satisfy constraint: Member must satisfy enum value set: [ENABLE, DISABLE]", in.ContributorInsightsAction)
	}
	switch in.ContributorInsightsMode {
	case "", "ACCESSED_AND_THROTTLED_KEYS", "THROTTLED_KEYS":
	default:
		return nil, validation("1 validation error detected: Value '%s' at 'contributorInsightsMode' failed to satisfy constraint: Member must satisfy enum value set: [ACCESSED_AND_THROTTLED_KEYS, THROTTLED_KEYS]", in.ContributorInsightsMode)
	}
	var ci ContributorInsights
	_, err := s.alterTable(in.TableName, func(t *Table, _ *bolt.Tx) error {
		if in.IndexName != "" && !t.hasGSI(in.IndexName) {
			return indexNotFound(in.TableName, in.IndexName)
		}
		if t.ContributorInsights == nil {
			t.ContributorInsights = map[string]ContributorInsights{}
		}
		ci, _ = t.insights(in.IndexName)
		ci.Status, ci.UpdatedAt = status, s.now()
		if in.ContributorInsightsMode != "" {
			ci.Mode = in.ContributorInsightsMode
		} else if ci.Mode == "" && status == "ENABLED" {
			ci.Mode = "ACCESSED_AND_THROTTLED_KEYS"
		}
		t.ContributorInsights[in.IndexName] = ci
		return nil
	})
	if err != nil {
		return nil, err
	}
	res := map[string]any{"TableName": in.TableName, "ContributorInsightsStatus": ci.Status}
	if in.IndexName != "" {
		res["IndexName"] = in.IndexName
	}
	if ci.Mode != "" {
		res["ContributorInsightsMode"] = ci.Mode
	}
	return res, nil
}

func (s *Service) awsDescribeContributorInsights(q *awsapi.Req) (any, error) {
	var in struct{ TableName, IndexName string }
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("dynamodb:DescribeContributorInsights", s.insightsResource(in.TableName, in.IndexName)); err != nil {
		return nil, err
	}
	t, err := s.getTable(in.TableName)
	if err != nil {
		return nil, err
	}
	if in.IndexName != "" && !t.hasGSI(in.IndexName) {
		return nil, indexNotFound(in.TableName, in.IndexName)
	}
	res := map[string]any{"TableName": in.TableName, "ContributorInsightsStatus": "DISABLED"}
	if in.IndexName != "" {
		res["IndexName"] = in.IndexName
	}
	if ci, ok := t.insights(in.IndexName); ok {
		res["ContributorInsightsStatus"] = ci.Status
		res["LastUpdateDateTime"] = awsapi.Epoch(ci.UpdatedAt)
		res["ContributorInsightsRuleList"] = []string{}
		if ci.Mode != "" {
			res["ContributorInsightsMode"] = ci.Mode
		}
	}
	return res, nil
}

func (s *Service) awsListContributorInsights(q *awsapi.Req) (any, error) {
	var in struct {
		TableName, NextToken string
		MaxResults           *int
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	resource := s.tableARN("*")
	if in.TableName != "" {
		resource = s.tableARN(in.TableName)
	}
	if err := q.Authorize("dynamodb:ListContributorInsights", resource); err != nil {
		return nil, err
	}
	limit := 100
	if in.MaxResults != nil {
		if *in.MaxResults < 0 || *in.MaxResults > 100 {
			return nil, validation("1 validation error detected: Value '%d' at 'maxResults' failed to satisfy constraint: Member must have value less than or equal to 100", *in.MaxResults)
		}
		if *in.MaxResults > 0 {
			limit = *in.MaxResults
		}
	}
	start := 0
	if in.NextToken != "" {
		n, err := strconv.Atoi(in.NextToken)
		if err != nil || n < 0 {
			return nil, validation("Invalid NextToken")
		}
		start = n
	}
	var tables []Table
	if in.TableName != "" {
		t, err := s.getTable(in.TableName)
		if err != nil {
			return nil, err
		}
		tables = []Table{*t}
	} else {
		tables = store.List[Table](s.env.Store, cTables)
		sort.Slice(tables, func(i, j int) bool { return tables[i].Name < tables[j].Name })
	}
	all := []map[string]any{}
	for i := range tables {
		t := &tables[i]
		indexes := make([]string, 0, len(t.ContributorInsights))
		for ix := range t.ContributorInsights {
			indexes = append(indexes, ix)
		}
		sort.Strings(indexes) // the table ("") first
		for _, ix := range indexes {
			ci, ok := t.insights(ix)
			if !ok {
				continue
			}
			m := map[string]any{"TableName": t.Name, "ContributorInsightsStatus": ci.Status}
			if ix != "" {
				m["IndexName"] = ix
			}
			if ci.Mode != "" {
				m["ContributorInsightsMode"] = ci.Mode
			}
			all = append(all, m)
		}
	}
	res := map[string]any{}
	if start > len(all) {
		start = len(all)
	}
	page := all[start:]
	if len(page) > limit {
		page = page[:limit]
		res["NextToken"] = strconv.Itoa(start + limit)
	}
	res["ContributorInsightsSummaries"] = page
	return res, nil
}
