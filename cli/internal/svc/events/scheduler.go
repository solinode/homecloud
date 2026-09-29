package events

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // schedule time zones work without the host's zoneinfo

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

// EventBridge Scheduler over restJson1 (signing name "scheduler"): one-time
// at(), rate() and cron() schedules in any time zone, with flexible windows,
// delivering to Lambda, SQS, SNS and Step Functions targets.

const (
	cSchedules      = "scheduler_schedules"
	cScheduleGroups = "scheduler_groups"
)

type SchedTarget struct {
	Arn              string
	RoleArn          string
	Input            string                           `json:",omitempty"`
	RetryPolicy      *SchedRetry                      `json:",omitempty"`
	DeadLetterConfig *struct{ Arn string }            `json:",omitempty"`
	SqsParameters    *struct{ MessageGroupId string } `json:",omitempty"`
}

type SchedRetry struct {
	MaximumEventAgeInSeconds *int `json:",omitempty"`
	MaximumRetryAttempts     *int `json:",omitempty"`
}

type FlexWindow struct {
	Mode                   string
	MaximumWindowInMinutes int `json:",omitempty"`
}

// ScheduleDef is a stored schedule (fields named as in the Scheduler API).
type ScheduleDef struct {
	Name                       string
	GroupName                  string
	Arn                        string
	Description                string `json:",omitempty"`
	ScheduleExpression         string
	ScheduleExpressionTimezone string       `json:",omitempty"`
	StartDate                  *awsapi.Time `json:",omitempty"`
	EndDate                    *awsapi.Time `json:",omitempty"`
	State                      string
	FlexibleTimeWindow         FlexWindow
	Target                     SchedTarget
	ActionAfterCompletion      string `json:",omitempty"`
	KmsKeyArn                  string `json:",omitempty"`
	CreationDate               awsapi.Time
	LastModificationDate       awsapi.Time
	LastFired                  *time.Time `json:"-"`
}

// storedSchedule persists LastFired, which the API shape leaves out.
type storedSchedule struct {
	ScheduleDef
	Fired *time.Time `json:"last_fired,omitempty"`
}

type ScheduleGroup struct {
	Name                 string
	Arn                  string
	State                string
	CreationDate         awsapi.Time
	LastModificationDate awsapi.Time
}

type storedGroup struct {
	ScheduleGroup
	TagsStored core.Tags `json:"tags,omitempty"`
}

func (s *Service) registerSchedulerAWS() {
	awsapi.Register(&awsapi.Service{Name: "scheduler", REST: s.schedulerREST})
}

func schedKey(group, name string) string { return group + "/" + name }

func (s *Service) scheduleARN(group, name string) string {
	return s.env.ARN("scheduler", "schedule/"+group+"/"+name)
}

func (s *Service) groupARN(name string) string { return s.env.ARN("scheduler", "schedule-group/"+name) }

func schedErr(status int, code, format string, a ...any) error {
	return awsapi.Errorf(status, code, format, a...)
}

func validationErr(format string, a ...any) error {
	return schedErr(http.StatusBadRequest, "ValidationException", format, a...)
}

var schedNameRe = regexp.MustCompile(`^[0-9a-zA-Z\-_.]{1,64}$`)

func (s *Service) groupExists(name string) bool {
	return name == "default" || store.Has(s.env.Store, cScheduleGroups, name)
}

// schedulerREST routes Scheduler requests by method and path.
func (s *Service) schedulerREST(q *awsapi.Req) {
	path := q.R.URL.EscapedPath()
	seg := strings.Split(strings.Trim(path, "/"), "/")
	unesc := func(v string) string { u, _ := url.PathUnescape(v); return u }
	var out any
	var err error
	switch {
	case len(seg) == 1 && seg[0] == "schedules" && q.R.Method == http.MethodGet:
		q.Op = "ListSchedules"
		out, err = s.listSchedules(q)
	case len(seg) == 2 && seg[0] == "schedules":
		name := unesc(seg[1])
		switch q.R.Method {
		case http.MethodPost:
			q.Op = "CreateSchedule"
			out, err = s.putSchedule(q, name, true)
		case http.MethodPut:
			q.Op = "UpdateSchedule"
			out, err = s.putSchedule(q, name, false)
		case http.MethodGet:
			q.Op = "GetSchedule"
			out, err = s.getSchedule(q, name)
		case http.MethodDelete:
			q.Op = "DeleteSchedule"
			out, err = s.deleteSchedule(q, name)
		}
	case len(seg) == 1 && seg[0] == "schedule-groups" && q.R.Method == http.MethodGet:
		q.Op = "ListScheduleGroups"
		out, err = s.listScheduleGroups(q)
	case len(seg) == 2 && seg[0] == "schedule-groups":
		name := unesc(seg[1])
		switch q.R.Method {
		case http.MethodPost:
			q.Op = "CreateScheduleGroup"
			out, err = s.createScheduleGroup(q, name)
		case http.MethodGet:
			q.Op = "GetScheduleGroup"
			out, err = s.getScheduleGroup(q, name)
		case http.MethodDelete:
			q.Op = "DeleteScheduleGroup"
			out, err = s.deleteScheduleGroup(q, name)
		}
	case len(seg) >= 2 && seg[0] == "tags":
		arn := unesc(strings.Join(seg[1:], "/"))
		switch q.R.Method {
		case http.MethodGet:
			q.Op = "ListTagsForResource"
			out, err = s.schedTags(q, arn, "scheduler:ListTagsForResource", nil, nil)
		case http.MethodPost:
			q.Op = "TagResource"
			var in struct{ Tags []struct{ Key, Value string } }
			if err = q.Bind(&in); err == nil {
				add := map[string]string{}
				for _, t := range in.Tags {
					add[t.Key] = t.Value
				}
				out, err = s.schedTags(q, arn, "scheduler:TagResource", add, nil)
			}
		case http.MethodDelete:
			q.Op = "UntagResource"
			out, err = s.schedTags(q, arn, "scheduler:UntagResource", nil, q.R.URL.Query()["TagKeys"])
		}
	}
	if q.Op == "" {
		q.Fail(schedErr(http.StatusNotFound, "UnknownOperationException", "no Scheduler operation for %s %s", q.R.Method, path))
		return
	}
	if err != nil {
		q.Fail(err)
		return
	}
	q.WriteJSON(http.StatusOK, out)
}

// ---- schedules ----

func (s *Service) putSchedule(q *awsapi.Req, name string, create bool) (any, error) {
	var in ScheduleDef
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if in.GroupName == "" {
		in.GroupName = "default"
	}
	arn := s.scheduleARN(in.GroupName, name)
	action := "scheduler:UpdateSchedule"
	if create {
		action = "scheduler:CreateSchedule"
	}
	if err := q.Authorize(action, arn); err != nil {
		return nil, err
	}
	if !schedNameRe.MatchString(name) {
		return nil, validationErr("schedule names are 1-64 letters, digits, - _ or .")
	}
	if !s.groupExists(in.GroupName) {
		return nil, schedErr(http.StatusNotFound, "ResourceNotFoundException", "Schedule group %s does not exist.", in.GroupName)
	}
	loc := time.UTC
	if in.ScheduleExpressionTimezone != "" {
		var err error
		if loc, err = time.LoadLocation(in.ScheduleExpressionTimezone); err != nil {
			return nil, validationErr("Invalid time zone: %s", in.ScheduleExpressionTimezone)
		}
	}
	if _, err := ParseScheduleIn(in.ScheduleExpression, loc); err != nil {
		return nil, validationErr("Invalid Schedule Expression %s: %v", in.ScheduleExpression, err)
	}
	switch in.FlexibleTimeWindow.Mode {
	case "OFF":
		in.FlexibleTimeWindow.MaximumWindowInMinutes = 0
	case "FLEXIBLE":
		if w := in.FlexibleTimeWindow.MaximumWindowInMinutes; w < 1 || w > 1440 {
			return nil, validationErr("MaximumWindowInMinutes must be 1-1440 for a FLEXIBLE window")
		}
	default:
		return nil, validationErr("FlexibleTimeWindow.Mode must be OFF or FLEXIBLE")
	}
	switch in.State {
	case "":
		in.State = "ENABLED"
	case "ENABLED", "DISABLED":
	default:
		return nil, validationErr("State must be ENABLED or DISABLED")
	}
	switch in.ActionAfterCompletion {
	case "", "NONE", "DELETE":
	default:
		return nil, validationErr("ActionAfterCompletion must be NONE or DELETE")
	}
	if in.StartDate != nil && in.EndDate != nil && !in.StartDate.Before(in.EndDate.Time) {
		return nil, validationErr("StartDate must be before EndDate")
	}
	t := in.Target
	if t.Arn == "" || t.RoleArn == "" {
		return nil, validationErr("Target.Arn and Target.RoleArn are required")
	}
	target := Target{ID: "scheduler", ARN: t.Arn, Input: t.Input, RoleARN: t.RoleArn}
	if t.RetryPolicy != nil {
		target.RetryPolicy = &RetryPolicy{MaximumRetryAttempts: t.RetryPolicy.MaximumRetryAttempts, MaximumEventAgeInSeconds: t.RetryPolicy.MaximumEventAgeInSeconds}
	}
	if t.DeadLetterConfig != nil {
		target.DeadLetterARN = t.DeadLetterConfig.Arn
	}
	if t.Input != "" && !json.Valid([]byte(t.Input)) {
		// Scheduler passes any text; HomeCloud targets receive it as a JSON string.
		b, _ := json.Marshal(t.Input)
		target.Input = string(b)
	}
	if err := ValidateTarget(target); err != nil {
		return nil, validationErr("%s", err.(*core.Error).Message)
	}
	// Delivering to the target needs the caller's permission for it.
	if err := q.Check(core.TargetAction(t.Arn), t.Arn); err != nil {
		return nil, err
	}
	if target.DeadLetterARN != "" {
		if err := q.Check("sqs:SendMessage", target.DeadLetterARN); err != nil {
			return nil, err
		}
	}
	key := schedKey(in.GroupName, name)
	old, err := store.Get[storedSchedule](s.env.Store, cSchedules, key)
	exists := err == nil
	if create && exists {
		return nil, schedErr(http.StatusConflict, "ConflictException", "Schedule %s already exists.", name)
	}
	if !create && !exists {
		return nil, schedErr(http.StatusNotFound, "ResourceNotFoundException", "Schedule %s does not exist.", name)
	}
	now := awsapi.Time{Time: core.Now()}
	in.Name, in.Arn, in.CreationDate, in.LastModificationDate = name, arn, now, now
	st := storedSchedule{ScheduleDef: in}
	if exists {
		st.CreationDate = old.CreationDate
		if old.ScheduleExpression == in.ScheduleExpression && old.ScheduleExpressionTimezone == in.ScheduleExpressionTimezone {
			st.Fired = old.Fired
		}
	}
	if err := store.Put(s.env.Store, cSchedules, key, st); err != nil {
		return nil, err
	}
	return map[string]string{"ScheduleArn": arn}, nil
}

func (s *Service) getSchedule(q *awsapi.Req, name string) (any, error) {
	group := q.R.URL.Query().Get("groupName")
	if group == "" {
		group = "default"
	}
	if err := q.Authorize("scheduler:GetSchedule", s.scheduleARN(group, name)); err != nil {
		return nil, err
	}
	st, err := store.Get[storedSchedule](s.env.Store, cSchedules, schedKey(group, name))
	if err != nil {
		return nil, schedErr(http.StatusNotFound, "ResourceNotFoundException", "Schedule %s does not exist.", name)
	}
	return st.ScheduleDef, nil
}

func (s *Service) deleteSchedule(q *awsapi.Req, name string) (any, error) {
	group := q.R.URL.Query().Get("groupName")
	if group == "" {
		group = "default"
	}
	if err := q.Authorize("scheduler:DeleteSchedule", s.scheduleARN(group, name)); err != nil {
		return nil, err
	}
	if err := store.Delete(s.env.Store, cSchedules, schedKey(group, name)); err != nil {
		return nil, schedErr(http.StatusNotFound, "ResourceNotFoundException", "Schedule %s does not exist.", name)
	}
	return struct{}{}, nil
}

func pageStart(token string) (int, error) {
	if token == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(token)
	if err != nil || n < 0 {
		return 0, validationErr("invalid NextToken")
	}
	return n, nil
}

func (s *Service) listSchedules(q *awsapi.Req) (any, error) {
	qs := q.R.URL.Query()
	if err := q.Authorize("scheduler:ListSchedules", s.scheduleARN("*", "*")); err != nil {
		return nil, err
	}
	start, err := pageStart(qs.Get("NextToken"))
	if err != nil {
		return nil, err
	}
	limit, _ := strconv.Atoi(qs.Get("MaxResults"))
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	all := store.List[storedSchedule](s.env.Store, cSchedules)
	sort.Slice(all, func(i, j int) bool {
		return schedKey(all[i].GroupName, all[i].Name) < schedKey(all[j].GroupName, all[j].Name)
	})
	type summary struct {
		Arn                  string
		CreationDate         awsapi.Time
		GroupName            string
		LastModificationDate awsapi.Time
		Name                 string
		State                string
		Target               struct{ Arn string }
	}
	out := struct {
		Schedules []summary
		NextToken string `json:",omitempty"`
	}{Schedules: []summary{}}
	n := 0
	for _, sc := range all {
		if (qs.Get("ScheduleGroup") != "" && sc.GroupName != qs.Get("ScheduleGroup")) || (qs.Get("State") != "" && sc.State != qs.Get("State")) ||
			!strings.HasPrefix(sc.Name, qs.Get("NamePrefix")) {
			continue
		}
		n++
		if n <= start {
			continue
		}
		if len(out.Schedules) == limit {
			out.NextToken = strconv.Itoa(start + limit)
			break
		}
		sm := summary{Arn: sc.Arn, CreationDate: sc.CreationDate, GroupName: sc.GroupName, LastModificationDate: sc.LastModificationDate, Name: sc.Name, State: sc.State}
		sm.Target.Arn = sc.Target.Arn
		out.Schedules = append(out.Schedules, sm)
	}
	return out, nil
}

// ---- groups ----

func (s *Service) createScheduleGroup(q *awsapi.Req, name string) (any, error) {
	var in struct {
		Tags []struct{ Key, Value string }
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("scheduler:CreateScheduleGroup", s.groupARN(name)); err != nil {
		return nil, err
	}
	if !schedNameRe.MatchString(name) || name == "default" {
		return nil, validationErr("invalid schedule group name %q", name)
	}
	if store.Has(s.env.Store, cScheduleGroups, name) {
		return nil, schedErr(http.StatusConflict, "ConflictException", "Schedule group %s already exists.", name)
	}
	now := awsapi.Time{Time: core.Now()}
	g := storedGroup{ScheduleGroup: ScheduleGroup{Name: name, Arn: s.groupARN(name), State: "ACTIVE", CreationDate: now, LastModificationDate: now}, TagsStored: core.Tags{}}
	for _, t := range in.Tags {
		g.TagsStored[t.Key] = t.Value
	}
	if err := store.Put(s.env.Store, cScheduleGroups, name, g); err != nil {
		return nil, err
	}
	return map[string]string{"ScheduleGroupArn": g.Arn}, nil
}

func (s *Service) defaultGroup() storedGroup {
	return storedGroup{ScheduleGroup: ScheduleGroup{Name: "default", Arn: s.groupARN("default"), State: "ACTIVE"}}
}

func (s *Service) getScheduleGroup(q *awsapi.Req, name string) (any, error) {
	if err := q.Authorize("scheduler:GetScheduleGroup", s.groupARN(name)); err != nil {
		return nil, err
	}
	if name == "default" {
		return s.defaultGroup().ScheduleGroup, nil
	}
	g, err := store.Get[storedGroup](s.env.Store, cScheduleGroups, name)
	if err != nil {
		return nil, schedErr(http.StatusNotFound, "ResourceNotFoundException", "Schedule group %s does not exist.", name)
	}
	return g.ScheduleGroup, nil
}

func (s *Service) deleteScheduleGroup(q *awsapi.Req, name string) (any, error) {
	if err := q.Authorize("scheduler:DeleteScheduleGroup", s.groupARN(name)); err != nil {
		return nil, err
	}
	if name == "default" {
		return nil, validationErr("the default schedule group cannot be deleted")
	}
	if err := store.Delete(s.env.Store, cScheduleGroups, name); err != nil {
		return nil, schedErr(http.StatusNotFound, "ResourceNotFoundException", "Schedule group %s does not exist.", name)
	}
	// Deleting a group deletes its schedules.
	s.env.Store.Retain(cSchedules, func(id string, _ json.RawMessage) bool { return !strings.HasPrefix(id, name+"/") })
	return struct{}{}, nil
}

func (s *Service) listScheduleGroups(q *awsapi.Req) (any, error) {
	qs := q.R.URL.Query()
	if err := q.Authorize("scheduler:ListScheduleGroups", s.groupARN("*")); err != nil {
		return nil, err
	}
	gs := append([]storedGroup{s.defaultGroup()}, store.List[storedGroup](s.env.Store, cScheduleGroups)...)
	sort.Slice(gs, func(i, j int) bool { return gs[i].Name < gs[j].Name })
	out := []ScheduleGroup{}
	for _, g := range gs {
		if strings.HasPrefix(g.Name, qs.Get("NamePrefix")) {
			out = append(out, g.ScheduleGroup)
		}
	}
	return map[string]any{"ScheduleGroups": out}, nil
}

func (s *Service) schedTags(q *awsapi.Req, arn, action string, add map[string]string, remove []string) (any, error) {
	if err := q.Authorize(action, arn); err != nil {
		return nil, err
	}
	name, ok := strings.CutPrefix(arn, s.groupARN(""))
	if !ok || name == "default" {
		return nil, schedErr(http.StatusNotFound, "ResourceNotFoundException", "Resource %s does not exist (only schedule groups have tags).", arn)
	}
	g, err := store.Update(s.env.Store, cScheduleGroups, name, func(g *storedGroup) error {
		if g.TagsStored == nil {
			g.TagsStored = core.Tags{}
		}
		for k, v := range add {
			g.TagsStored[k] = v
		}
		for _, k := range remove {
			delete(g.TagsStored, k)
		}
		return nil
	})
	if err != nil {
		return nil, schedErr(http.StatusNotFound, "ResourceNotFoundException", "Resource %s does not exist.", arn)
	}
	if add != nil || remove != nil {
		return struct{}{}, nil
	}
	tags := []map[string]string{}
	for k, v := range g.TagsStored {
		tags = append(tags, map[string]string{"Key": k, "Value": v})
	}
	sort.Slice(tags, func(i, j int) bool { return tags[i]["Key"] < tags[j]["Key"] })
	return map[string]any{"Tags": tags}, nil
}

// ---- firing ----

// tickSchedules fires the schedules due at now.
func (s *Service) tickSchedules(ctx context.Context, now time.Time) {
	for _, sc := range store.List[storedSchedule](s.env.Store, cSchedules) {
		if sc.State != "ENABLED" {
			continue
		}
		key := schedKey(sc.GroupName, sc.Name)
		if sc.EndDate != nil && now.After(sc.EndDate.Time) {
			if sc.ActionAfterCompletion == "DELETE" {
				_ = store.Delete(s.env.Store, cSchedules, key)
			}
			continue
		}
		if sc.StartDate != nil && now.Before(sc.StartDate.Time) {
			continue
		}
		loc := time.UTC
		if sc.ScheduleExpressionTimezone != "" {
			if l, err := time.LoadLocation(sc.ScheduleExpressionTimezone); err == nil {
				loc = l
			}
		}
		sch, err := ParseScheduleIn(sc.ScheduleExpression, loc)
		if err != nil {
			continue
		}
		last := time.Time{}
		if sc.Fired != nil {
			last = *sc.Fired
		} else if _, isRate := sch.(rate); isRate {
			last = sc.CreationDate.Time
			if sc.StartDate != nil && sc.StartDate.After(last) {
				last = sc.StartDate.Time
			}
		}
		if !sch.Due(now, last) {
			continue
		}
		fired := now.UTC().Truncate(time.Second)
		_, oneTime := sch.(at)
		_, _ = store.Update(s.env.Store, cSchedules, key, func(x *storedSchedule) error { x.Fired = &fired; return nil })
		if oneTime && sc.ActionAfterCompletion == "DELETE" {
			_ = store.Delete(s.env.Store, cSchedules, key)
		}
		delay := time.Duration(0)
		if sc.FlexibleTimeWindow.Mode == "FLEXIBLE" && sc.FlexibleTimeWindow.MaximumWindowInMinutes > 0 {
			delay = time.Duration(rand.Int64N(int64(time.Duration(sc.FlexibleTimeWindow.MaximumWindowInMinutes) * time.Minute)))
		}
		go s.fireSchedule(ctx, sc.ScheduleDef, delay)
	}
}

func (s *Service) fireSchedule(ctx context.Context, sc ScheduleDef, delay time.Duration) {
	defer core.Recover("schedule " + sc.Name)
	if delay > 0 {
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
	t := Target{ID: "scheduler", ARN: sc.Target.Arn, Input: sc.Target.Input}
	if sc.Target.RetryPolicy != nil {
		t.RetryPolicy = &RetryPolicy{MaximumRetryAttempts: sc.Target.RetryPolicy.MaximumRetryAttempts, MaximumEventAgeInSeconds: sc.Target.RetryPolicy.MaximumEventAgeInSeconds}
	}
	if sc.Target.DeadLetterConfig != nil {
		t.DeadLetterARN = sc.Target.DeadLetterConfig.Arn
	}
	payload := []byte(t.Input)
	if len(payload) == 0 {
		payload = []byte("{}")
	} else if !json.Valid(payload) {
		payload, _ = json.Marshal(t.Input)
	}
	_ = s.deliver(ctx, Rule{Name: "schedule " + sc.GroupName + "/" + sc.Name, ARN: sc.Arn}, t, payload, time.Now())
}
