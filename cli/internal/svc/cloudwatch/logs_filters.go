package cloudwatch

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"log"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

// Metric filters turn matching log events into CloudWatch metrics;
// subscription filters send matching events to a Lambda function in the
// CloudWatch Logs subscription format ({"awslogs": {"data": base64(gzip(json))}}).

const cLogFilters = "logs_filters"

type MetricTransformation struct {
	MetricName      string            `json:"metricName"`
	MetricNamespace string            `json:"metricNamespace"`
	MetricValue     string            `json:"metricValue"`
	DefaultValue    *float64          `json:"defaultValue,omitempty"`
	Dimensions      map[string]string `json:"dimensions,omitempty"`
	Unit            string            `json:"unit,omitempty"`
}

type MetricFilter struct {
	FilterName            string                 `json:"filterName"`
	FilterPattern         string                 `json:"filterPattern"`
	MetricTransformations []MetricTransformation `json:"metricTransformations"`
	CreationTime          int64                  `json:"creationTime"`
	LogGroupName          string                 `json:"logGroupName"`
}

type SubscriptionFilter struct {
	FilterName     string `json:"filterName"`
	LogGroupName   string `json:"logGroupName"`
	FilterPattern  string `json:"filterPattern"`
	DestinationArn string `json:"destinationArn"`
	RoleArn        string `json:"roleArn,omitempty"`
	Distribution   string `json:"distribution"`
	CreationTime   int64  `json:"creationTime"`
}

type groupFilters struct {
	Metric       []MetricFilter       `json:"metric,omitempty"`
	Subscription []SubscriptionFilter `json:"subscription,omitempty"`
}

func (s *Service) filters(group string) groupFilters {
	f, _ := store.Get[groupFilters](s.env.Store, cLogFilters, group)
	return f
}

var (
	patternCache   = map[string]FieldMatcher{}
	patternCacheMu sync.Mutex
)

func compiledPattern(p string) (FieldMatcher, error) {
	patternCacheMu.Lock()
	defer patternCacheMu.Unlock()
	if m, ok := patternCache[p]; ok {
		return m, nil
	}
	m, err := ParseFilterPatternFields(p)
	if err != nil {
		return nil, err
	}
	if len(patternCache) > 1000 {
		patternCache = map[string]FieldMatcher{}
	}
	patternCache[p] = m
	return m, nil
}

// fieldValue resolves a metric value or dimension: a number, "$.json.path"
// in JSON events, or "$name" (a space-delimited pattern field).
func fieldValue(ref, msg string, caps map[string]string) (string, bool) {
	switch {
	case strings.HasPrefix(ref, "$."):
		sel, err := parseSelector(ref)
		if err != nil {
			return "", false
		}
		var doc any
		if json.Unmarshal([]byte(strings.TrimSpace(msg)), &doc) != nil {
			return "", false
		}
		v, ok := sel(doc)
		if !ok || v == nil {
			return "", false
		}
		if s, isStr := v.(string); isStr {
			return s, true
		}
		b, _ := json.Marshal(v)
		return string(b), true
	case strings.HasPrefix(ref, "$"):
		v, ok := caps[ref]
		return v, ok
	}
	return ref, true
}

// applyFilters runs a group's metric and subscription filters over newly stored events.
func (s *Service) applyFilters(group, stream string, events []LogEvent) {
	if len(events) == 0 {
		return
	}
	f := s.filters(group)
	for _, mf := range f.Metric {
		m, err := compiledPattern(mf.FilterPattern)
		if err != nil {
			continue
		}
		for _, e := range events {
			ok, caps := m(e.Message)
			for _, t := range mf.MetricTransformations {
				if !ok {
					if t.DefaultValue != nil {
						s.Put(t.MetricNamespace, t.MetricName, nil, t.Unit, *t.DefaultValue, e.Timestamp)
					}
					continue
				}
				raw, found := fieldValue(t.MetricValue, e.Message, caps)
				v, err := strconv.ParseFloat(raw, 64)
				if !found || err != nil {
					continue
				}
				var dims map[string]string
				if len(t.Dimensions) > 0 {
					dims = map[string]string{}
					for k, ref := range t.Dimensions {
						if dv, ok := fieldValue(ref, e.Message, caps); ok {
							dims[k] = dv
						}
					}
				}
				s.Put(t.MetricNamespace, t.MetricName, dims, t.Unit, v, e.Timestamp)
			}
		}
	}
	for _, sf := range f.Subscription {
		m, err := compiledPattern(sf.FilterPattern)
		if err != nil || s.Deliver == nil {
			continue
		}
		var matched []map[string]any
		for _, e := range events {
			if ok, _ := m(e.Message); ok {
				matched = append(matched, map[string]any{"id": e.ID, "timestamp": e.Timestamp.UnixMilli(), "message": e.Message})
			}
		}
		if len(matched) == 0 {
			continue
		}
		doc, _ := json.Marshal(map[string]any{"messageType": "DATA_MESSAGE", "owner": s.env.AccountID, "logGroup": group, "logStream": stream,
			"subscriptionFilters": []string{sf.FilterName}, "logEvents": matched})
		var gz bytes.Buffer
		w := gzip.NewWriter(&gz)
		_, _ = w.Write(doc)
		_ = w.Close()
		payload, _ := json.Marshal(map[string]any{"awslogs": map[string]string{"data": base64.StdEncoding.EncodeToString(gz.Bytes())}})
		go func(dest string) {
			defer core.Recover("logs subscription " + group)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			if err := s.Deliver(ctx, dest, payload); err != nil {
				log.Printf("logs: subscription filter %s on %s -> %s: %v", sf.FilterName, group, dest, err)
			}
		}(sf.DestinationArn)
	}
}

var filterNameRe = regexp.MustCompile(`^[^:*]{1,512}$`)

func (s *Service) updateFilters(group string, fn func(f *groupFilters) error) error {
	if _, err := s.storedGroup(group); err != nil {
		return err
	}
	f := s.filters(group)
	if err := fn(&f); err != nil {
		return err
	}
	if len(f.Metric) == 0 && len(f.Subscription) == 0 {
		_ = store.Delete(s.env.Store, cLogFilters, group)
		return nil
	}
	return store.Put(s.env.Store, cLogFilters, group, f)
}

// ---- AWS operations ----

type putMetricFilterIn struct {
	LogGroupName          string                 `json:"logGroupName"`
	FilterName            string                 `json:"filterName"`
	FilterPattern         string                 `json:"filterPattern"`
	MetricTransformations []MetricTransformation `json:"metricTransformations"`
}

func (s *Service) awsPutMetricFilter(q *awsapi.Req) (any, error) {
	var in putMetricFilterIn
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	return s.putMetricFilter(q, in)
}

func (s *Service) putMetricFilter(q authorizer, in putMetricFilterIn) (any, error) {
	if err := s.authGroup(q, "logs:PutMetricFilter", in.LogGroupName); err != nil {
		return nil, err
	}
	if !filterNameRe.MatchString(in.FilterName) {
		return nil, logsErr("InvalidParameterException", "filterName must be 1-512 characters without : or *")
	}
	if _, err := ParseFilterPattern(in.FilterPattern); err != nil {
		return nil, logsErr("InvalidParameterException", "Invalid metric filter pattern: %v", err)
	}
	if len(in.MetricTransformations) != 1 {
		return nil, logsErr("InvalidParameterException", "metricTransformations must contain exactly one transformation")
	}
	for _, t := range in.MetricTransformations {
		if t.MetricName == "" || t.MetricNamespace == "" || t.MetricValue == "" {
			return nil, logsErr("InvalidParameterException", "metricName, metricNamespace and metricValue are required")
		}
		if strings.HasPrefix(t.MetricNamespace, "AWS/") || strings.HasPrefix(t.MetricNamespace, "HC/") {
			return nil, logsErr("InvalidParameterException", "the AWS/ and HC/ namespaces are reserved")
		}
		if !strings.HasPrefix(t.MetricValue, "$") {
			if _, err := strconv.ParseFloat(t.MetricValue, 64); err != nil {
				return nil, logsErr("InvalidParameterException", "metricValue must be a number, $.json.field or $field")
			}
		}
		if t.Unit != "" && !units[t.Unit] {
			return nil, logsErr("InvalidParameterException", "invalid unit %s", t.Unit)
		}
		if t.DefaultValue != nil && len(t.Dimensions) > 0 {
			return nil, logsErr("InvalidParameterException", "a metric filter with dimensions cannot have a defaultValue")
		}
		if len(t.Dimensions) > 3 {
			return nil, logsErr("InvalidParameterException", "at most 3 dimensions")
		}
	}
	return nil, s.updateFilters(in.LogGroupName, func(f *groupFilters) error {
		mf := MetricFilter{FilterName: in.FilterName, FilterPattern: in.FilterPattern, MetricTransformations: in.MetricTransformations,
			CreationTime: time.Now().UnixMilli(), LogGroupName: in.LogGroupName}
		for i := range f.Metric {
			if f.Metric[i].FilterName == in.FilterName {
				mf.CreationTime = f.Metric[i].CreationTime
				f.Metric[i] = mf
				return nil
			}
		}
		if len(f.Metric) >= 100 {
			return logsErr("LimitExceededException", "a log group has at most 100 metric filters")
		}
		f.Metric = append(f.Metric, mf)
		return nil
	})
}

func (s *Service) awsDescribeMetricFilters(q *awsapi.Req) (any, error) {
	var in struct {
		LogGroupName     string `json:"logGroupName"`
		FilterNamePrefix string `json:"filterNamePrefix"`
		MetricName       string `json:"metricName"`
		MetricNamespace  string `json:"metricNamespace"`
		Limit            int    `json:"limit"`
		NextToken        string `json:"nextToken"`
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	res := s.groupARN("*")
	if in.LogGroupName != "" {
		res = s.groupARN(in.LogGroupName)
	}
	if err := q.Authorize("logs:DescribeMetricFilters", res); err != nil {
		return nil, err
	}
	var all []MetricFilter
	if in.LogGroupName != "" {
		if _, err := s.storedGroup(in.LogGroupName); err != nil {
			return nil, err
		}
		all = s.filters(in.LogGroupName).Metric
	} else {
		for _, f := range store.List[groupFilters](s.env.Store, cLogFilters) {
			all = append(all, f.Metric...)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].FilterName < all[j].FilterName })
	out := []MetricFilter{}
	for _, mf := range all {
		if !strings.HasPrefix(mf.FilterName, in.FilterNamePrefix) {
			continue
		}
		if in.MetricName != "" && (len(mf.MetricTransformations) == 0 || mf.MetricTransformations[0].MetricName != in.MetricName ||
			mf.MetricTransformations[0].MetricNamespace != in.MetricNamespace) {
			continue
		}
		out = append(out, mf)
	}
	return map[string]any{"metricFilters": out}, nil
}

type deleteFilterIn struct {
	LogGroupName string `json:"logGroupName"`
	FilterName   string `json:"filterName"`
}

func (s *Service) awsDeleteMetricFilter(q *awsapi.Req) (any, error) {
	var in deleteFilterIn
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	return s.deleteMetricFilter(q, in)
}

func (s *Service) deleteMetricFilter(q authorizer, in deleteFilterIn) (any, error) {
	if err := s.authGroup(q, "logs:DeleteMetricFilter", in.LogGroupName); err != nil {
		return nil, err
	}
	return nil, s.updateFilters(in.LogGroupName, func(f *groupFilters) error {
		for i, mf := range f.Metric {
			if mf.FilterName == in.FilterName {
				f.Metric = append(f.Metric[:i], f.Metric[i+1:]...)
				return nil
			}
		}
		return logsErr("ResourceNotFoundException", "The specified filter does not exist.")
	})
}

func (s *Service) awsTestMetricFilter(q *awsapi.Req) (any, error) {
	var in struct {
		FilterPattern    string   `json:"filterPattern"`
		LogEventMessages []string `json:"logEventMessages"`
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("logs:TestMetricFilter", "*"); err != nil {
		return nil, err
	}
	if len(in.LogEventMessages) == 0 || len(in.LogEventMessages) > 50 {
		return nil, logsErr("InvalidParameterException", "logEventMessages must contain 1-50 messages")
	}
	m, err := ParseFilterPatternFields(in.FilterPattern)
	if err != nil {
		return nil, logsErr("InvalidParameterException", "Invalid metric filter pattern: %v", err)
	}
	matches := []map[string]any{}
	for i, msg := range in.LogEventMessages {
		if ok, caps := m(msg); ok {
			matches = append(matches, map[string]any{"eventNumber": i + 1, "eventMessage": msg, "extractedValues": caps})
		}
	}
	return map[string]any{"matches": matches}, nil
}

type putSubscriptionFilterIn struct {
	LogGroupName   string `json:"logGroupName"`
	FilterName     string `json:"filterName"`
	FilterPattern  string `json:"filterPattern"`
	DestinationArn string `json:"destinationArn"`
	RoleArn        string `json:"roleArn"`
	Distribution   string `json:"distribution"`
}

func (s *Service) awsPutSubscriptionFilter(q *awsapi.Req) (any, error) {
	var in putSubscriptionFilterIn
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	return s.putSubscriptionFilter(q, in)
}

func (s *Service) putSubscriptionFilter(q authorizer, in putSubscriptionFilterIn) (any, error) {
	if !strings.HasPrefix(core.CanonicalARN(in.DestinationArn), "arn:"+core.Partition+":lambda:") || !strings.Contains(in.DestinationArn, ":function:") {
		return nil, logsErr("InvalidParameterException", "HomeCloud subscription filters deliver to Lambda functions (destinationArn must be a function ARN)")
	}
	// Delivering to the function needs the caller's permission for it.
	if err := q.Check("lambda:InvokeFunction", in.DestinationArn); err != nil {
		return nil, err
	}
	if err := s.authGroup(q, "logs:PutSubscriptionFilter", in.LogGroupName); err != nil {
		return nil, err
	}
	if !filterNameRe.MatchString(in.FilterName) {
		return nil, logsErr("InvalidParameterException", "filterName must be 1-512 characters without : or *")
	}
	if _, err := ParseFilterPattern(in.FilterPattern); err != nil {
		return nil, logsErr("InvalidParameterException", "Invalid subscription filter pattern: %v", err)
	}
	if in.Distribution == "" {
		in.Distribution = "ByLogStream"
	}
	return nil, s.updateFilters(in.LogGroupName, func(f *groupFilters) error {
		sf := SubscriptionFilter{FilterName: in.FilterName, LogGroupName: in.LogGroupName, FilterPattern: in.FilterPattern,
			DestinationArn: in.DestinationArn, RoleArn: in.RoleArn, Distribution: in.Distribution, CreationTime: time.Now().UnixMilli()}
		for i := range f.Subscription {
			if f.Subscription[i].FilterName == in.FilterName {
				sf.CreationTime = f.Subscription[i].CreationTime
				f.Subscription[i] = sf
				return nil
			}
		}
		if len(f.Subscription) >= 2 {
			return logsErr("LimitExceededException", "Resource limit exceeded: a log group has at most 2 subscription filters.")
		}
		f.Subscription = append(f.Subscription, sf)
		return nil
	})
}

func (s *Service) awsDescribeSubscriptionFilters(q *awsapi.Req) (any, error) {
	var in struct {
		LogGroupName     string `json:"logGroupName"`
		FilterNamePrefix string `json:"filterNamePrefix"`
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := s.authGroup(q, "logs:DescribeSubscriptionFilters", in.LogGroupName); err != nil {
		return nil, err
	}
	if _, err := s.storedGroup(in.LogGroupName); err != nil {
		return nil, err
	}
	out := []SubscriptionFilter{}
	for _, sf := range s.filters(in.LogGroupName).Subscription {
		if strings.HasPrefix(sf.FilterName, in.FilterNamePrefix) {
			out = append(out, sf)
		}
	}
	return map[string]any{"subscriptionFilters": out}, nil
}

func (s *Service) awsDeleteSubscriptionFilter(q *awsapi.Req) (any, error) {
	var in deleteFilterIn
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	return s.deleteSubscriptionFilter(q, in)
}

func (s *Service) deleteSubscriptionFilter(q authorizer, in deleteFilterIn) (any, error) {
	if err := s.authGroup(q, "logs:DeleteSubscriptionFilter", in.LogGroupName); err != nil {
		return nil, err
	}
	return nil, s.updateFilters(in.LogGroupName, func(f *groupFilters) error {
		for i, sf := range f.Subscription {
			if sf.FilterName == in.FilterName {
				f.Subscription = append(f.Subscription[:i], f.Subscription[i+1:]...)
				return nil
			}
		}
		return logsErr("ResourceNotFoundException", "The specified subscription filter does not exist.")
	})
}
