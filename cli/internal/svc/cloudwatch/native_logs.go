package cloudwatch

import (
	"errors"
	"github.com/homecloudhq/homecloud/cli/internal/store"
	"slices"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
)

// Native (console) routes for Logs Insights, metric and subscription filters
// and alarm history. They share their logic with the AWS handlers.

// nat converts an AWS API error to a native one.
func nat[T any](v T, err error) (T, error) {
	var ae *awsapi.Error
	if errors.As(err, &ae) {
		st := ae.Status
		if ae.Code == "ResourceNotFoundException" {
			st = 404
		}
		return v, core.Errf(st, ae.Code, "%s", ae.Message)
	}
	return v, err
}

func (s *Service) nativeLogRoutes(r *httpx.Router) {
	res := httpx.Res("arn:aws:logs:{region}:{account}:log-group:{group}")
	r.Handle("POST /api/v1/logs/insights/queries", "logs:StartQuery", s.nStartQuery, httpx.Deferred())
	r.Handle("GET /api/v1/logs/insights/queries/{id}", "logs:GetQueryResults", s.nQueryResults, httpx.Deferred())
	r.Handle("GET /api/v1/logs/groups/{group}/metric-filters", "logs:DescribeMetricFilters", s.nListMetricFilters, res)
	r.Handle("PUT /api/v1/logs/groups/{group}/metric-filters/{name}", "logs:PutMetricFilter", s.nPutMetricFilter, res)
	r.Handle("DELETE /api/v1/logs/groups/{group}/metric-filters/{name}", "logs:DeleteMetricFilter", s.nDeleteMetricFilter, res)
	r.Handle("GET /api/v1/logs/groups/{group}/subscription-filters", "logs:DescribeSubscriptionFilters", s.nListSubFilters, res)
	r.Handle("PUT /api/v1/logs/groups/{group}/subscription-filters/{name}", "logs:PutSubscriptionFilter", s.nPutSubFilter, res)
	r.Handle("DELETE /api/v1/logs/groups/{group}/subscription-filters/{name}", "logs:DeleteSubscriptionFilter", s.nDeleteSubFilter, res)
	r.Handle("GET /api/v1/cloudwatch/alarms/{name}/history", "cloudwatch:DescribeAlarmHistory", s.nAlarmHistory,
		httpx.Res("arn:aws:cloudwatch:{region}:{account}:alarm:{name}"))
}

func (s *Service) nStartQuery(c *httpx.Ctx) (any, error) {
	var in startQueryIn
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	return nat(s.startQuery(c, in))
}

func (s *Service) nQueryResults(c *httpx.Ctx) (any, error) {
	return nat(s.queryResults(c, c.Param("id")))
}

func (s *Service) nListMetricFilters(c *httpx.Ctx) (any, error) {
	if _, err := nat(s.storedGroup(c.Param("group"))); err != nil {
		return nil, err
	}
	out := slices.Clone(s.filters(c.Param("group")).Metric)
	if out == nil {
		out = []MetricFilter{}
	}
	return out, nil
}

func (s *Service) nPutMetricFilter(c *httpx.Ctx) (any, error) {
	var in putMetricFilterIn
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	in.LogGroupName, in.FilterName = c.Param("group"), c.Param("name")
	return nat(s.putMetricFilter(c, in))
}

func (s *Service) nDeleteMetricFilter(c *httpx.Ctx) (any, error) {
	return nat(s.deleteMetricFilter(c, deleteFilterIn{LogGroupName: c.Param("group"), FilterName: c.Param("name")}))
}

func (s *Service) nListSubFilters(c *httpx.Ctx) (any, error) {
	if _, err := nat(s.storedGroup(c.Param("group"))); err != nil {
		return nil, err
	}
	out := slices.Clone(s.filters(c.Param("group")).Subscription)
	if out == nil {
		out = []SubscriptionFilter{}
	}
	return out, nil
}

func (s *Service) nPutSubFilter(c *httpx.Ctx) (any, error) {
	var in putSubscriptionFilterIn
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	in.LogGroupName, in.FilterName = c.Param("group"), c.Param("name")
	return nat(s.putSubscriptionFilter(c, in))
}

func (s *Service) nDeleteSubFilter(c *httpx.Ctx) (any, error) {
	return nat(s.deleteSubscriptionFilter(c, deleteFilterIn{LogGroupName: c.Param("group"), FilterName: c.Param("name")}))
}

func (s *Service) nAlarmHistory(c *httpx.Ctx) (any, error) {
	if !store.Has(s.env.Store, cAlarms, c.Param("name")) {
		return nil, core.NotFound("alarm", c.Param("name"))
	}
	out := slices.Clone(s.history(c.Param("name")))
	slices.SortStableFunc(out, func(a, b AlarmHistoryItem) int { return b.Timestamp.Compare(a.Timestamp) })
	if out == nil {
		out = []AlarmHistoryItem{}
	}
	return out, nil
}
