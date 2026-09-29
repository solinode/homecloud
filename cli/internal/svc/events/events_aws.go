package events

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

// EventBridge over the AWS JSON 1.1 protocol (X-Amz-Target AWSEvents.*).

// RegisterAWS serves EventBridge ("events") and EventBridge Scheduler ("scheduler").
func (s *Service) RegisterAWS() {
	awsapi.Register(&awsapi.Service{
		Name: "events", JSONPrefix: "AWSEvents", JSONVersion: "1.1",
		ErrorCode: map[string]string{
			"ResourceNotFound":    "ResourceNotFoundException",
			"ValidationError":     "ValidationException",
			"InvalidEventPattern": "InvalidEventPatternException",
			"LimitExceeded":       "LimitExceededException",
			"AlreadyExists":       "ResourceAlreadyExistsException",
		},
		Ops: map[string]awsapi.Op{
			"PutEvents":             s.awsPutEvents,
			"PutRule":               s.awsPutRule,
			"DescribeRule":          s.awsDescribeRule,
			"ListRules":             s.awsListRules,
			"DeleteRule":            s.awsDeleteRule,
			"EnableRule":            s.awsEnableRule,
			"DisableRule":           s.awsDisableRule,
			"PutTargets":            s.awsPutTargets,
			"ListTargetsByRule":     s.awsListTargetsByRule,
			"RemoveTargets":         s.awsRemoveTargets,
			"ListRuleNamesByTarget": s.awsListRuleNamesByTarget,
			"CreateEventBus":        s.awsCreateEventBus,
			"DescribeEventBus":      s.awsDescribeEventBus,
			"ListEventBuses":        s.awsListEventBuses,
			"DeleteEventBus":        s.awsDeleteEventBus,
			"TestEventPattern":      s.awsTestEventPattern,
			"TagResource":           s.awsTagResource,
			"UntagResource":         s.awsUntagResource,
			"ListTagsForResource":   s.awsListTagsForResource,
		},
	})
	s.registerSchedulerAWS()
}

func evErr(code, format string, a ...any) error {
	return awsapi.Errorf(http.StatusBadRequest, code, format, a...)
}

// busName accepts an event bus name or ARN ("" is the default bus).
func busName(v string) string {
	if v == "" {
		return "default"
	}
	if i := strings.Index(v, ":event-bus/"); strings.HasPrefix(v, "arn:") && i >= 0 {
		return v[i+len(":event-bus/"):]
	}
	return v
}

type awsTag struct{ Key, Value string }

func tagMap(ts []awsTag) (core.Tags, error) {
	if len(ts) > 50 {
		return nil, evErr("ValidationException", "a resource can have at most 50 tags")
	}
	out := core.Tags{}
	for _, t := range ts {
		if t.Key == "" || len(t.Key) > 128 || len(t.Value) > 256 || strings.HasPrefix(strings.ToLower(t.Key), "aws:") {
			return nil, evErr("ValidationException", "invalid tag key %q", t.Key)
		}
		out[t.Key] = t.Value
	}
	return out, nil
}

// ---- events ----

func (s *Service) awsPutEvents(q *awsapi.Req) (any, error) {
	var in struct {
		Entries []struct {
			Time         *awsapi.Time
			Source       string
			Resources    []string
			DetailType   string
			Detail       string
			EventBusName string
			TraceHeader  string
		}
		EndpointId string
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if len(in.Entries) == 0 || len(in.Entries) > 10 {
		return nil, evErr("ValidationException", "1 validation error detected: Value at 'entries' failed to satisfy constraint: Member must have length between 1 and 10")
	}
	size := 0
	buses := map[string]bool{}
	for _, e := range in.Entries {
		size += len(e.Source) + len(e.DetailType) + len(e.Detail)
		for _, r := range e.Resources {
			size += len(r)
		}
		buses[busName(e.EventBusName)] = true
	}
	if size > 256*1024 {
		return nil, evErr("ValidationException", "Total size of the entries in the request is over the limit (256 KB).")
	}
	names := make([]string, 0, len(buses))
	for b := range buses {
		names = append(names, b)
	}
	sort.Strings(names)
	for _, b := range names {
		if err := q.Authorize("events:PutEvents", s.busARN(b)); err != nil {
			return nil, err
		}
	}
	type result struct {
		EventId      string `json:",omitempty"`
		ErrorCode    string `json:",omitempty"`
		ErrorMessage string `json:",omitempty"`
	}
	out := struct {
		FailedEntryCount int
		Entries          []result
	}{Entries: []result{}}
	fail := func(code, msg string) {
		out.FailedEntryCount++
		out.Entries = append(out.Entries, result{ErrorCode: code, ErrorMessage: msg})
	}
	for _, e := range in.Entries {
		bus := busName(e.EventBusName)
		var detail map[string]any
		switch {
		case e.Source == "":
			fail("InvalidArgument", "Parameter Source is not valid. Reason: Source is a required argument.")
		case e.DetailType == "":
			fail("InvalidArgument", "Parameter DetailType is not valid. Reason: DetailType is a required argument.")
		case e.Detail == "":
			fail("InvalidArgument", "Parameter Detail is not valid. Reason: Detail is a required argument.")
		case json.Unmarshal([]byte(e.Detail), &detail) != nil:
			fail("MalformedDetail", "Detail is malformed.")
		case strings.HasPrefix(e.Source, "aws."):
			fail("NotAuthorizedForSourceException", "Not authorized for the source.")
		case !s.busExists(bus):
			fail("InternalException", "Event bus "+bus+" does not exist.")
		default:
			ent := entry{Source: e.Source, DetailType: e.DetailType, Detail: json.RawMessage(e.Detail), Resources: e.Resources, Bus: bus}
			if e.Time != nil {
				ent.Time = e.Time.Time
			}
			id, _ := s.Publish(context.Background(), ent)
			out.Entries = append(out.Entries, result{EventId: id})
		}
	}
	return out, nil
}

// ---- rules ----

type awsRule struct {
	Name               string
	Arn                string
	EventPattern       string `json:",omitempty"`
	ScheduleExpression string `json:",omitempty"`
	State              string
	Description        string `json:",omitempty"`
	RoleArn            string `json:",omitempty"`
	EventBusName       string
	CreatedBy          string `json:",omitempty"`
}

func (s *Service) toAWSRule(r Rule) awsRule {
	bus := r.EventBus
	if bus == "" {
		bus = "default"
	}
	return awsRule{Name: r.Name, Arn: r.ARN, EventPattern: string(r.EventPattern), ScheduleExpression: r.ScheduleExpression, State: r.State,
		Description: r.Description, RoleArn: r.RoleARN, EventBusName: bus, CreatedBy: s.env.AccountID}
}

func (s *Service) getRule(bus, name string) (Rule, error) {
	r, err := store.Get[Rule](s.env.Store, cRules, ruleKey(bus, name))
	if err != nil {
		return r, ruleNotFound(bus, name)
	}
	return r, nil
}

func (s *Service) awsPutRule(q *awsapi.Req) (any, error) {
	var in struct {
		Name, ScheduleExpression, EventPattern, State, Description, RoleArn, EventBusName string
		Tags                                                                              []awsTag
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	bus := busName(in.EventBusName)
	if err := q.Authorize("events:PutRule", s.ruleARN(bus, in.Name)); err != nil {
		return nil, err
	}
	tags, err := tagMap(in.Tags)
	if err != nil {
		return nil, err
	}
	if in.RoleArn != "" {
		if err := q.Check("iam:PassRole", in.RoleArn); err != nil {
			return nil, err
		}
	}
	r, err := s.PutRule(RuleInput{Name: in.Name, Bus: bus, Description: in.Description, ScheduleExpression: in.ScheduleExpression,
		State: in.State, RoleARN: in.RoleArn, EventPattern: json.RawMessage(in.EventPattern), Tags: tags})
	if err != nil {
		return nil, err
	}
	return map[string]string{"RuleArn": r.ARN}, nil
}

func (s *Service) awsDescribeRule(q *awsapi.Req) (any, error) {
	var in struct{ Name, EventBusName string }
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	bus := busName(in.EventBusName)
	if err := q.Authorize("events:DescribeRule", s.ruleARN(bus, in.Name)); err != nil {
		return nil, err
	}
	r, err := s.getRule(bus, in.Name)
	if err != nil {
		return nil, err
	}
	return s.toAWSRule(r), nil
}

func (s *Service) busRules(bus string) []Rule {
	var out []Rule
	for _, r := range store.List[Rule](s.env.Store, cRules) {
		rb := r.EventBus
		if rb == "" {
			rb = "default"
		}
		if rb == bus {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func page(token string, limit, max int) (int, int, error) {
	start := 0
	if token != "" {
		var err error
		if start, err = strconv.Atoi(token); err != nil || start < 0 {
			return 0, 0, evErr("ValidationException", "The NextToken is not valid.")
		}
	}
	if limit <= 0 || limit > max {
		limit = max
	}
	return start, limit, nil
}

func (s *Service) awsListRules(q *awsapi.Req) (any, error) {
	var in struct {
		NamePrefix, EventBusName, NextToken string
		Limit                               int
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	bus := busName(in.EventBusName)
	if err := q.Authorize("events:ListRules", s.ruleARN(bus, "*")); err != nil {
		return nil, err
	}
	if !s.busExists(bus) {
		return nil, evErr("ResourceNotFoundException", "Event bus %s does not exist.", bus)
	}
	start, limit, err := page(in.NextToken, in.Limit, 100)
	if err != nil {
		return nil, err
	}
	out := struct {
		Rules     []awsRule
		NextToken string `json:",omitempty"`
	}{Rules: []awsRule{}}
	n := 0
	for _, r := range s.busRules(bus) {
		if !strings.HasPrefix(r.Name, in.NamePrefix) {
			continue
		}
		n++
		if n <= start {
			continue
		}
		if len(out.Rules) == limit {
			out.NextToken = strconv.Itoa(start + limit)
			break
		}
		out.Rules = append(out.Rules, s.toAWSRule(r))
	}
	return out, nil
}

func (s *Service) awsDeleteRule(q *awsapi.Req) (any, error) {
	var in struct {
		Name, EventBusName string
		Force              bool
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	bus := busName(in.EventBusName)
	if err := q.Authorize("events:DeleteRule", s.ruleARN(bus, in.Name)); err != nil {
		return nil, err
	}
	r, err := store.Get[Rule](s.env.Store, cRules, ruleKey(bus, in.Name))
	if err != nil {
		return nil, nil // deleting a missing rule succeeds, as in EventBridge
	}
	if len(r.Targets) > 0 {
		return nil, evErr("ValidationException", "Rule can't be deleted since it has targets.")
	}
	return nil, store.Delete(s.env.Store, cRules, ruleKey(bus, in.Name))
}

func (s *Service) awsSetState(q *awsapi.Req, action, state string) (any, error) {
	var in struct{ Name, EventBusName string }
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	bus := busName(in.EventBusName)
	if err := q.Authorize(action, s.ruleARN(bus, in.Name)); err != nil {
		return nil, err
	}
	_, err := s.SetState(bus, in.Name, state)
	return nil, err
}

func (s *Service) awsEnableRule(q *awsapi.Req) (any, error) {
	return s.awsSetState(q, "events:EnableRule", "ENABLED")
}

func (s *Service) awsDisableRule(q *awsapi.Req) (any, error) {
	return s.awsSetState(q, "events:DisableRule", "DISABLED")
}

// ---- targets ----

type awsTarget struct {
	Id               string
	Arn              string
	RoleArn          string `json:",omitempty"`
	Input            string `json:",omitempty"`
	InputPath        string `json:",omitempty"`
	InputTransformer *struct {
		InputPathsMap map[string]string `json:",omitempty"`
		InputTemplate string
	} `json:",omitempty"`
	SqsParameters    *struct{ MessageGroupId string } `json:",omitempty"`
	DeadLetterConfig *struct{ Arn string }            `json:",omitempty"`
	RetryPolicy      *struct {
		MaximumRetryAttempts     *int `json:",omitempty"`
		MaximumEventAgeInSeconds *int `json:",omitempty"`
	} `json:",omitempty"`
	// Parameters of target types HomeCloud cannot deliver to; their presence is rejected.
	EcsParameters        json.RawMessage `json:",omitempty"`
	KinesisParameters    json.RawMessage `json:",omitempty"`
	HttpParameters       json.RawMessage `json:",omitempty"`
	BatchParameters      json.RawMessage `json:",omitempty"`
	RunCommandParameters json.RawMessage `json:",omitempty"`
}

func fromAWSTarget(a awsTarget) Target {
	t := Target{ID: a.Id, ARN: a.Arn, RoleARN: a.RoleArn, Input: a.Input, InputPath: a.InputPath}
	if a.InputTransformer != nil {
		t.InputTransformer = &InputTransformer{InputPathsMap: a.InputTransformer.InputPathsMap, InputTemplate: a.InputTransformer.InputTemplate}
	}
	if a.SqsParameters != nil {
		t.MessageGroupID = a.SqsParameters.MessageGroupId
	}
	if a.DeadLetterConfig != nil {
		t.DeadLetterARN = a.DeadLetterConfig.Arn
	}
	if a.RetryPolicy != nil {
		t.RetryPolicy = &RetryPolicy{MaximumRetryAttempts: a.RetryPolicy.MaximumRetryAttempts, MaximumEventAgeInSeconds: a.RetryPolicy.MaximumEventAgeInSeconds}
	}
	return t
}

func toAWSTarget(t Target) awsTarget {
	a := awsTarget{Id: t.ID, Arn: t.ARN, RoleArn: t.RoleARN, Input: t.Input, InputPath: t.InputPath}
	if it := t.InputTransformer; it != nil {
		a.InputTransformer = &struct {
			InputPathsMap map[string]string `json:",omitempty"`
			InputTemplate string
		}{it.InputPathsMap, it.InputTemplate}
	}
	if t.MessageGroupID != "" {
		a.SqsParameters = &struct{ MessageGroupId string }{t.MessageGroupID}
	}
	if t.DeadLetterARN != "" {
		a.DeadLetterConfig = &struct{ Arn string }{t.DeadLetterARN}
	}
	if rp := t.RetryPolicy; rp != nil {
		a.RetryPolicy = &struct {
			MaximumRetryAttempts     *int `json:",omitempty"`
			MaximumEventAgeInSeconds *int `json:",omitempty"`
		}{rp.MaximumRetryAttempts, rp.MaximumEventAgeInSeconds}
	}
	return a
}

type failedTarget struct{ TargetId, ErrorCode, ErrorMessage string }

func (s *Service) awsPutTargets(q *awsapi.Req) (any, error) {
	var in struct {
		Rule, EventBusName string
		Targets            []awsTarget
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	bus := busName(in.EventBusName)
	if len(in.Targets) == 0 || len(in.Targets) > 10 {
		return nil, evErr("ValidationException", "Targets must contain 1-10 items")
	}
	out := struct {
		FailedEntryCount int
		FailedEntries    []failedTarget
	}{FailedEntries: []failedTarget{}}
	var ok []Target
	for _, a := range in.Targets {
		if a.EcsParameters != nil || a.KinesisParameters != nil || a.HttpParameters != nil || a.BatchParameters != nil || a.RunCommandParameters != nil {
			out.FailedEntries = append(out.FailedEntries, failedTarget{a.Id, "ValidationException", "HomeCloud delivers only to Lambda, SQS, SNS and Step Functions targets"})
			continue
		}
		t := fromAWSTarget(a)
		if err := ValidateTarget(t); err != nil {
			out.FailedEntries = append(out.FailedEntries, failedTarget{a.Id, "ValidationException", err.(*core.Error).Message})
			continue
		}
		// Delivering to a target (and its dead-letter queue) needs the caller's permission.
		if err := q.Check(core.TargetAction(t.ARN), t.ARN); err != nil {
			return nil, err
		}
		if t.DeadLetterARN != "" {
			if err := q.Check("sqs:SendMessage", t.DeadLetterARN); err != nil {
				return nil, err
			}
		}
		if t.RoleARN != "" {
			if err := q.Check("iam:PassRole", t.RoleARN); err != nil {
				return nil, err
			}
		}
		ok = append(ok, t)
	}
	if err := q.Authorize("events:PutTargets", s.ruleARN(bus, in.Rule)); err != nil {
		return nil, err
	}
	if len(ok) > 0 {
		if _, err := s.PutTargets(bus, in.Rule, ok); err != nil {
			return nil, err
		}
	} else if _, err := s.getRule(bus, in.Rule); err != nil {
		return nil, err
	}
	out.FailedEntryCount = len(out.FailedEntries)
	return out, nil
}

func (s *Service) awsListTargetsByRule(q *awsapi.Req) (any, error) {
	var in struct {
		Rule, EventBusName, NextToken string
		Limit                         int
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	bus := busName(in.EventBusName)
	if err := q.Authorize("events:ListTargetsByRule", s.ruleARN(bus, in.Rule)); err != nil {
		return nil, err
	}
	r, err := s.getRule(bus, in.Rule)
	if err != nil {
		return nil, err
	}
	start, limit, err := page(in.NextToken, in.Limit, 100)
	if err != nil {
		return nil, err
	}
	out := struct {
		Targets   []awsTarget
		NextToken string `json:",omitempty"`
	}{Targets: []awsTarget{}}
	for i, t := range r.Targets {
		if i < start {
			continue
		}
		if len(out.Targets) == limit {
			out.NextToken = strconv.Itoa(start + limit)
			break
		}
		out.Targets = append(out.Targets, toAWSTarget(t))
	}
	return out, nil
}

func (s *Service) awsRemoveTargets(q *awsapi.Req) (any, error) {
	var in struct {
		Rule, EventBusName string
		Ids                []string
		Force              bool
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	bus := busName(in.EventBusName)
	if err := q.Authorize("events:RemoveTargets", s.ruleARN(bus, in.Rule)); err != nil {
		return nil, err
	}
	if len(in.Ids) == 0 {
		return nil, evErr("ValidationException", "Ids must not be empty")
	}
	_, err := store.Update(s.env.Store, cRules, ruleKey(bus, in.Rule), func(r *Rule) error {
		keep := r.Targets[:0]
		for _, t := range r.Targets {
			drop := false
			for _, id := range in.Ids {
				drop = drop || t.ID == id
			}
			if !drop {
				keep = append(keep, t)
			}
		}
		r.Targets = keep
		return nil
	})
	if err == store.ErrNotFound {
		return nil, ruleNotFound(bus, in.Rule)
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"FailedEntryCount": 0, "FailedEntries": []failedTarget{}}, nil
}

func (s *Service) awsListRuleNamesByTarget(q *awsapi.Req) (any, error) {
	var in struct {
		TargetArn, EventBusName, NextToken string
		Limit                              int
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	bus := busName(in.EventBusName)
	if err := q.Authorize("events:ListRuleNamesByTarget", s.ruleARN(bus, "*")); err != nil {
		return nil, err
	}
	start, limit, err := page(in.NextToken, in.Limit, 100)
	if err != nil {
		return nil, err
	}
	out := struct {
		RuleNames []string
		NextToken string `json:",omitempty"`
	}{RuleNames: []string{}}
	n := 0
	target := core.CanonicalARN(in.TargetArn)
	for _, r := range s.busRules(bus) {
		match := false
		for _, t := range r.Targets {
			match = match || core.CanonicalARN(t.ARN) == target
		}
		if !match {
			continue
		}
		n++
		if n <= start {
			continue
		}
		if len(out.RuleNames) == limit {
			out.NextToken = strconv.Itoa(start + limit)
			break
		}
		out.RuleNames = append(out.RuleNames, r.Name)
	}
	return out, nil
}

// ---- event buses ----

var busNameRe = regexp.MustCompile(`^[/\.\-_A-Za-z0-9]{1,256}$`)

type awsBus struct {
	Name             string
	Arn              string
	Description      string                `json:",omitempty"`
	DeadLetterConfig *struct{ Arn string } `json:",omitempty"`
	CreationTime     *awsapi.Time          `json:",omitempty"`
	LastModifiedTime *awsapi.Time          `json:",omitempty"`
}

func (s *Service) bus(name string) (Bus, bool) {
	if name == "default" {
		return Bus{Name: "default", ARN: s.busARN("default")}, true
	}
	b, err := store.Get[Bus](s.env.Store, cBuses, name)
	return b, err == nil
}

func toAWSBus(b Bus) awsBus {
	out := awsBus{Name: b.Name, Arn: b.ARN, Description: b.Description, CreationTime: awsapi.T(b.CreatedAt), LastModifiedTime: awsapi.T(b.UpdatedAt)}
	if b.DeadLetterARN != "" {
		out.DeadLetterConfig = &struct{ Arn string }{b.DeadLetterARN}
	}
	return out
}

func (s *Service) awsCreateEventBus(q *awsapi.Req) (any, error) {
	var in struct {
		Name, EventSourceName, Description, KmsKeyIdentifier string
		DeadLetterConfig                                     *struct{ Arn string }
		Tags                                                 []awsTag
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("events:CreateEventBus", s.busARN(in.Name)); err != nil {
		return nil, err
	}
	if !busNameRe.MatchString(in.Name) || in.Name == "default" || strings.HasPrefix(in.Name, "aws.") {
		return nil, evErr("ValidationException", "Event bus name %q is not valid (1-256 of letters, digits, / . - _; not default or aws.*).", in.Name)
	}
	if in.EventSourceName != "" {
		return nil, evErr("ValidationException", "partner event sources are not supported")
	}
	tags, err := tagMap(in.Tags)
	if err != nil {
		return nil, err
	}
	b := Bus{Name: in.Name, ARN: s.busARN(in.Name), Description: in.Description, CreatedAt: core.Now(), UpdatedAt: core.Now(), Tags: tags}
	if in.DeadLetterConfig != nil {
		if err := q.Check("sqs:SendMessage", in.DeadLetterConfig.Arn); err != nil {
			return nil, err
		}
		b.DeadLetterARN = in.DeadLetterConfig.Arn
	}
	if store.Has(s.env.Store, cBuses, in.Name) {
		return nil, evErr("ResourceAlreadyExistsException", "Event bus %s already exists.", in.Name)
	}
	if err := store.Put(s.env.Store, cBuses, b.Name, b); err != nil {
		return nil, err
	}
	out := map[string]any{"EventBusArn": b.ARN}
	if b.Description != "" {
		out["Description"] = b.Description
	}
	return out, nil
}

func (s *Service) awsDescribeEventBus(q *awsapi.Req) (any, error) {
	var in struct{ Name string }
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	name := busName(in.Name)
	if err := q.Authorize("events:DescribeEventBus", s.busARN(name)); err != nil {
		return nil, err
	}
	b, ok := s.bus(name)
	if !ok {
		return nil, evErr("ResourceNotFoundException", "Event bus %s does not exist.", name)
	}
	return toAWSBus(b), nil
}

func (s *Service) awsListEventBuses(q *awsapi.Req) (any, error) {
	var in struct {
		NamePrefix, NextToken string
		Limit                 int
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("events:ListEventBuses", s.busARN("*")); err != nil {
		return nil, err
	}
	start, limit, err := page(in.NextToken, in.Limit, 100)
	if err != nil {
		return nil, err
	}
	all := append([]Bus{{Name: "default", ARN: s.busARN("default")}}, store.List[Bus](s.env.Store, cBuses)...)
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	out := struct {
		EventBuses []awsBus
		NextToken  string `json:",omitempty"`
	}{EventBuses: []awsBus{}}
	n := 0
	for _, b := range all {
		if !strings.HasPrefix(b.Name, in.NamePrefix) {
			continue
		}
		n++
		if n <= start {
			continue
		}
		if len(out.EventBuses) == limit {
			out.NextToken = strconv.Itoa(start + limit)
			break
		}
		out.EventBuses = append(out.EventBuses, toAWSBus(b))
	}
	return out, nil
}

func (s *Service) awsDeleteEventBus(q *awsapi.Req) (any, error) {
	var in struct{ Name string }
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	name := busName(in.Name)
	if err := q.Authorize("events:DeleteEventBus", s.busARN(name)); err != nil {
		return nil, err
	}
	if name == "default" {
		return nil, evErr("ValidationException", "Cannot delete event bus default.")
	}
	if !store.Has(s.env.Store, cBuses, name) {
		return nil, nil // as in EventBridge, deleting a missing bus succeeds
	}
	for _, r := range s.busRules(name) {
		_ = store.Delete(s.env.Store, cRules, ruleKey(name, r.Name))
	}
	return nil, store.Delete(s.env.Store, cBuses, name)
}

// ---- test pattern ----

func (s *Service) awsTestEventPattern(q *awsapi.Req) (any, error) {
	var in struct{ EventPattern, Event string }
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("events:TestEventPattern", "*"); err != nil {
		return nil, err
	}
	p, err := CompilePattern([]byte(in.EventPattern))
	if err != nil {
		return nil, evErr("InvalidEventPatternException", "%v", err)
	}
	var ev map[string]any
	dec := json.NewDecoder(strings.NewReader(in.Event))
	dec.UseNumber()
	if dec.Decode(&ev) != nil {
		return nil, evErr("ValidationException", "Parameter Event is not valid.")
	}
	for _, f := range []string{"id", "account", "source", "time", "region", "detail-type"} {
		if _, ok := ev[f]; !ok {
			return nil, evErr("ValidationException", "Parameter Event is not valid. Reason: Provided Event is missing required field %s.", f)
		}
	}
	return map[string]bool{"Result": p.Match(ev)}, nil
}

// ---- tags ----

// taggable resolves a rule or event bus ARN.
func (s *Service) taggable(arn string) (coll, key string, err error) {
	switch {
	case strings.HasPrefix(arn, s.busARN("")):
		name := strings.TrimPrefix(arn, s.busARN(""))
		if store.Has(s.env.Store, cBuses, name) {
			return cBuses, name, nil
		}
	case strings.HasPrefix(arn, s.env.ARN("events", "rule/")):
		rest := strings.TrimPrefix(arn, s.env.ARN("events", "rule/"))
		bus, name := "default", rest
		if i := strings.LastIndexByte(rest, '/'); i >= 0 {
			bus, name = rest[:i], rest[i+1:]
		}
		if store.Has(s.env.Store, cRules, ruleKey(bus, name)) {
			return cRules, ruleKey(bus, name), nil
		}
	default:
		return "", "", evErr("ValidationException", "ResourceARN must be a rule or event bus ARN in this account")
	}
	return "", "", evErr("ResourceNotFoundException", "Resource %s does not exist.", arn)
}

func (s *Service) retag(coll, key string, fn func(core.Tags)) error {
	var err error
	if coll == cBuses {
		_, err = store.Update(s.env.Store, cBuses, key, func(b *Bus) error {
			if b.Tags == nil {
				b.Tags = core.Tags{}
			}
			fn(b.Tags)
			b.UpdatedAt = core.Now()
			return nil
		})
	} else {
		_, err = store.Update(s.env.Store, cRules, key, func(r *Rule) error {
			if r.Tags == nil {
				r.Tags = core.Tags{}
			}
			fn(r.Tags)
			return nil
		})
	}
	return err
}

func (s *Service) awsTagResource(q *awsapi.Req) (any, error) {
	var in struct {
		ResourceARN string
		Tags        []awsTag
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("events:TagResource", in.ResourceARN); err != nil {
		return nil, err
	}
	tags, err := tagMap(in.Tags)
	if err != nil {
		return nil, err
	}
	coll, key, err := s.taggable(in.ResourceARN)
	if err != nil {
		return nil, err
	}
	return nil, s.retag(coll, key, func(t core.Tags) {
		for k, v := range tags {
			t[k] = v
		}
	})
}

func (s *Service) awsUntagResource(q *awsapi.Req) (any, error) {
	var in struct {
		ResourceARN string
		TagKeys     []string
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("events:UntagResource", in.ResourceARN); err != nil {
		return nil, err
	}
	coll, key, err := s.taggable(in.ResourceARN)
	if err != nil {
		return nil, err
	}
	return nil, s.retag(coll, key, func(t core.Tags) {
		for _, k := range in.TagKeys {
			delete(t, k)
		}
	})
}

func (s *Service) awsListTagsForResource(q *awsapi.Req) (any, error) {
	var in struct{ ResourceARN string }
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("events:ListTagsForResource", in.ResourceARN); err != nil {
		return nil, err
	}
	coll, key, err := s.taggable(in.ResourceARN)
	if err != nil {
		return nil, err
	}
	var tags core.Tags
	if coll == cBuses {
		b, _ := store.Get[Bus](s.env.Store, cBuses, key)
		tags = b.Tags
	} else {
		r, _ := store.Get[Rule](s.env.Store, cRules, key)
		tags = r.Tags
	}
	out := []awsTag{}
	for k, v := range tags {
		out = append(out, awsTag{k, v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return map[string]any{"Tags": out}, nil
}
