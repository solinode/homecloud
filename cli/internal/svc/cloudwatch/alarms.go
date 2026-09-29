package cloudwatch

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

const cAlarms = "cloudwatch_alarms"

var alarmName = regexp.MustCompile(`^[^/\x00-\x1f]{1,255}$`)

type Alarm struct {
	Name               string            `json:"name"`
	ARN                string            `json:"arn"`
	Description        string            `json:"description"`
	Namespace          string            `json:"namespace"`
	Metric             string            `json:"metric"`
	Dimensions         map[string]string `json:"dimensions"`
	Statistic          string            `json:"statistic"`
	Period             int               `json:"period"` // seconds
	EvaluationPeriods  int               `json:"evaluation_periods"`
	Threshold          float64           `json:"threshold"`
	ComparisonOperator string            `json:"comparison_operator"`
	// Actions are SNS topic ARNs or http(s) webhook URLs notified on state change.
	AlarmActions   []string  `json:"alarm_actions"`
	OKActions      []string  `json:"ok_actions"`
	State          string    `json:"state"`
	StateReason    string    `json:"state_reason"`
	StateUpdatedAt time.Time `json:"state_updated_at"`
	CreatedAt      time.Time `json:"created_at"`
}

var operators = map[string]func(a, b float64) bool{
	"GreaterThanThreshold":          func(a, b float64) bool { return a > b },
	"GreaterThanOrEqualToThreshold": func(a, b float64) bool { return a >= b },
	"LessThanThreshold":             func(a, b float64) bool { return a < b },
	"LessThanOrEqualToThreshold":    func(a, b float64) bool { return a <= b },
}

func (s *Service) evaluateAlarms() {
	for _, a := range store.List[Alarm](s.env.Store, cAlarms) {
		period := time.Duration(a.Period) * time.Second
		end := time.Now()
		dps, _ := s.Statistics(a.Namespace, a.Metric, a.Dimensions, end.Add(-period*time.Duration(a.EvaluationPeriods)), end, period)
		state, reason := "INSUFFICIENT_DATA", "not enough datapoints"
		if len(dps) >= a.EvaluationPeriods {
			recent := dps[len(dps)-a.EvaluationPeriods:]
			breaching := 0
			for _, d := range recent {
				if operators[a.ComparisonOperator](d.Stat(a.Statistic), a.Threshold) {
					breaching++
				}
			}
			last := recent[len(recent)-1].Stat(a.Statistic)
			if breaching == a.EvaluationPeriods {
				state = "ALARM"
				reason = fmt.Sprintf("%d of %d datapoints breached %s %v (last %.2f)", breaching, a.EvaluationPeriods, a.ComparisonOperator, a.Threshold, last)
			} else {
				state = "OK"
				reason = fmt.Sprintf("%d of %d datapoints breached the threshold (last %.2f)", breaching, a.EvaluationPeriods, last)
			}
		}
		if state == a.State {
			continue
		}
		prev := a.State
		_, _ = store.Update(s.env.Store, cAlarms, a.Name, func(x *Alarm) error {
			x.State, x.StateReason, x.StateUpdatedAt = state, reason, core.Now()
			return nil
		})
		if s.Notify == nil {
			continue
		}
		targets := a.OKActions
		if state == "ALARM" {
			targets = a.AlarmActions
		}
		for _, t := range targets {
			subject := fmt.Sprintf("ALARM %q changed from %s to %s", a.Name, prev, state)
			go s.Notify(t, subject, subject+"\n\nReason: "+reason)
		}
	}
}

func (s *Service) alarmRoutes(r *httpx.Router) {
	r.Handle("GET /api/v1/cloudwatch/alarms", "cloudwatch:DescribeAlarms", s.listAlarms)
	r.Handle("GET /api/v1/cloudwatch/alarms/{name}", "cloudwatch:DescribeAlarms", s.getAlarm, httpx.Res("arn:hc:cloudwatch:local-1:{account}:alarm:{name}"))
	r.Handle("PUT /api/v1/cloudwatch/alarms/{name}", "cloudwatch:PutMetricAlarm", s.putAlarm, httpx.Res("arn:hc:cloudwatch:local-1:{account}:alarm:{name}"))
	r.Handle("DELETE /api/v1/cloudwatch/alarms/{name}", "cloudwatch:DeleteAlarms", s.deleteAlarm, httpx.Res("arn:hc:cloudwatch:local-1:{account}:alarm:{name}"))
}

func (s *Service) listAlarms(c *httpx.Ctx) (any, error) {
	return store.List[Alarm](s.env.Store, cAlarms), nil
}

func (s *Service) getAlarm(c *httpx.Ctx) (any, error) {
	a, err := store.Get[Alarm](s.env.Store, cAlarms, c.Param("name"))
	if err != nil {
		return nil, core.NotFound("alarm", c.Param("name"))
	}
	return a, nil
}

func (s *Service) putAlarm(c *httpx.Ctx) (any, error) {
	var a Alarm
	if err := c.Bind(&a); err != nil {
		return nil, err
	}
	a.Name = c.Param("name")
	if !alarmName.MatchString(a.Name) {
		return nil, core.BadRequest("alarm names are 1-255 characters without control characters or slashes")
	}
	for _, t := range append(append([]string{}, a.AlarmActions...), a.OKActions...) {
		switch {
		case strings.HasPrefix(t, "arn:hc:sns:"):
			if err := c.Authorize("sns:Publish", t); err != nil {
				return nil, err
			}
		case strings.HasPrefix(t, "http://") || strings.HasPrefix(t, "https://"):
			if err := core.CheckWebhookURL(t); err != nil {
				return nil, err
			}
		default:
			return nil, core.BadRequest("action %q must be an SNS topic ARN or an http(s) URL", t)
		}
	}
	if math.IsNaN(a.Threshold) || math.IsInf(a.Threshold, 0) {
		return nil, core.BadRequest("threshold must be a number")
	}
	if a.Namespace == "" || a.Metric == "" {
		return nil, core.BadRequest("namespace and metric are required")
	}
	if _, ok := operators[a.ComparisonOperator]; !ok {
		return nil, core.BadRequest("comparison_operator must be one of GreaterThanThreshold, GreaterThanOrEqualToThreshold, LessThanThreshold, LessThanOrEqualToThreshold")
	}
	if a.Period < 30 {
		a.Period = 60
	}
	if a.EvaluationPeriods < 1 {
		a.EvaluationPeriods = 1
	}
	if a.Statistic == "" {
		a.Statistic = "Average"
	}
	a.ARN = s.env.ARN("cloudwatch", "alarm:"+a.Name)
	a.State, a.StateReason, a.StateUpdatedAt, a.CreatedAt = "INSUFFICIENT_DATA", "alarm created", core.Now(), core.Now()
	if old, err := store.Get[Alarm](s.env.Store, cAlarms, a.Name); err == nil {
		a.CreatedAt = old.CreatedAt
	}
	if a.AlarmActions == nil {
		a.AlarmActions = []string{}
	}
	if a.OKActions == nil {
		a.OKActions = []string{}
	}
	return a, store.Put(s.env.Store, cAlarms, a.Name, a)
}

func (s *Service) deleteAlarm(c *httpx.Ctx) (any, error) {
	if err := store.Delete(s.env.Store, cAlarms, c.Param("name")); err != nil {
		return nil, core.NotFound("alarm", c.Param("name"))
	}
	return nil, nil
}
