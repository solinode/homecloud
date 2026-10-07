package cloudwatch

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

// Composite alarms: an alarm whose state is a rule over other alarms' states,
// e.g. "ALARM(cpu) AND NOT OK(errors)". The rule supports ALARM(), OK() and
// INSUFFICIENT_DATA() with alarm names or ARNs, TRUE, FALSE, AND, OR, NOT and
// parentheses, and is evaluated with the metric alarms. Actions run on state
// changes as for metric alarms; an actions suppressor is stored and reported
// but does not suppress.

const cComposite = "cloudwatch_composite_alarms"

type CompositeAlarm struct {
	Name                    string    `json:"name"`
	ARN                     string    `json:"arn"`
	Description             string    `json:"description,omitempty"`
	Rule                    string    `json:"rule"`
	ActionsEnabled          *bool     `json:"actions_enabled,omitempty"`
	AlarmActions            []string  `json:"alarm_actions,omitempty"`
	OKActions               []string  `json:"ok_actions,omitempty"`
	InsufficientDataActions []string  `json:"insufficient_data_actions,omitempty"`
	Suppressor              string    `json:"suppressor,omitempty"`
	SuppressorWait          *int      `json:"suppressor_wait,omitempty"`
	SuppressorExtension     *int      `json:"suppressor_extension,omitempty"`
	State                   string    `json:"state"`
	StateReason             string    `json:"state_reason"`
	StateUpdatedAt          time.Time `json:"state_updated_at"`
	ConfigUpdatedAt         time.Time `json:"config_updated_at"`
	Tags                    core.Tags `json:"tags,omitempty"`
}

// ---- rule parsing ----

type ruleNode struct {
	op       string // AND, OR, NOT, STATE, CONST
	state    string // ALARM, OK, INSUFFICIENT_DATA (STATE)
	alarm    string // alarm name (STATE)
	value    bool   // CONST
	children []*ruleNode
}

type ruleParser struct {
	s   string
	pos int
}

func parseAlarmRule(s string) (*ruleNode, error) {
	if len(s) == 0 || len(s) > 10240 {
		return nil, fmt.Errorf("AlarmRule must be 1-10240 characters")
	}
	p := &ruleParser{s: s}
	n, err := p.or()
	if err != nil {
		return nil, err
	}
	p.space()
	if p.pos != len(p.s) {
		return nil, fmt.Errorf("unexpected %q at position %d", p.s[p.pos:], p.pos)
	}
	return n, nil
}

func (p *ruleParser) space() {
	for p.pos < len(p.s) && unicode.IsSpace(rune(p.s[p.pos])) {
		p.pos++
	}
}

func (p *ruleParser) keyword(k string) bool {
	p.space()
	if !strings.HasPrefix(strings.ToUpper(p.s[p.pos:]), k) {
		return false
	}
	end := p.pos + len(k)
	if end < len(p.s) && (unicode.IsLetter(rune(p.s[end])) || p.s[end] == '_') {
		return false
	}
	p.pos = end
	return true
}

func (p *ruleParser) or() (*ruleNode, error) {
	n, err := p.and()
	if err != nil {
		return nil, err
	}
	for p.keyword("OR") {
		r, err := p.and()
		if err != nil {
			return nil, err
		}
		n = &ruleNode{op: "OR", children: []*ruleNode{n, r}}
	}
	return n, nil
}

func (p *ruleParser) and() (*ruleNode, error) {
	n, err := p.unary()
	if err != nil {
		return nil, err
	}
	for p.keyword("AND") {
		r, err := p.unary()
		if err != nil {
			return nil, err
		}
		n = &ruleNode{op: "AND", children: []*ruleNode{n, r}}
	}
	return n, nil
}

func (p *ruleParser) unary() (*ruleNode, error) {
	if p.keyword("NOT") {
		n, err := p.unary()
		if err != nil {
			return nil, err
		}
		return &ruleNode{op: "NOT", children: []*ruleNode{n}}, nil
	}
	p.space()
	if p.pos < len(p.s) && p.s[p.pos] == '(' {
		p.pos++
		n, err := p.or()
		if err != nil {
			return nil, err
		}
		p.space()
		if p.pos >= len(p.s) || p.s[p.pos] != ')' {
			return nil, fmt.Errorf("missing ) at position %d", p.pos)
		}
		p.pos++
		return n, nil
	}
	for _, c := range []string{"TRUE", "FALSE"} {
		if p.keyword(c) {
			return &ruleNode{op: "CONST", value: c == "TRUE"}, nil
		}
	}
	for _, st := range []string{"INSUFFICIENT_DATA", "ALARM", "OK"} {
		if !p.keyword(st) {
			continue
		}
		p.space()
		if p.pos >= len(p.s) || p.s[p.pos] != '(' {
			return nil, fmt.Errorf("%s must be followed by (alarm name)", st)
		}
		p.pos++
		p.space()
		var name string
		if p.pos < len(p.s) && p.s[p.pos] == '"' {
			end := strings.IndexByte(p.s[p.pos+1:], '"')
			if end < 0 {
				return nil, fmt.Errorf("unterminated quoted alarm name")
			}
			name = p.s[p.pos+1 : p.pos+1+end]
			p.pos += end + 2
		} else {
			end := strings.IndexByte(p.s[p.pos:], ')')
			if end < 0 {
				return nil, fmt.Errorf("missing ) after %s(", st)
			}
			name = strings.TrimSpace(p.s[p.pos : p.pos+end])
			p.pos += end
		}
		p.space()
		if p.pos >= len(p.s) || p.s[p.pos] != ')' || name == "" {
			return nil, fmt.Errorf("%s needs one alarm name", st)
		}
		p.pos++
		if i := strings.Index(name, ":alarm:"); strings.HasPrefix(name, "arn:") && i > 0 {
			name = name[i+len(":alarm:"):]
		}
		return &ruleNode{op: "STATE", state: st, alarm: name}, nil
	}
	return nil, fmt.Errorf("expected ALARM(), OK(), INSUFFICIENT_DATA(), TRUE, FALSE, NOT or ( at position %d", p.pos)
}

func (n *ruleNode) alarms(out []string) []string {
	if n.op == "STATE" && !slices.Contains(out, n.alarm) {
		out = append(out, n.alarm)
	}
	for _, c := range n.children {
		out = c.alarms(out)
	}
	return out
}

func (n *ruleNode) eval(states map[string]string) bool {
	switch n.op {
	case "CONST":
		return n.value
	case "STATE":
		return states[n.alarm] == n.state
	case "NOT":
		return !n.children[0].eval(states)
	case "AND":
		return n.children[0].eval(states) && n.children[1].eval(states)
	}
	return n.children[0].eval(states) || n.children[1].eval(states)
}

// ---- evaluation ----

func (s *Service) alarmStates() map[string]string {
	m := map[string]string{}
	for _, a := range store.List[Alarm](s.env.Store, cAlarms) {
		m[a.Name] = a.State
	}
	for _, c := range store.List[CompositeAlarm](s.env.Store, cComposite) {
		m[c.Name] = c.State
	}
	return m
}

// evaluateComposites updates composite alarms; composites of composites
// settle within a few passes.
func (s *Service) evaluateComposites() {
	for range 5 {
		changed := false
		states := s.alarmStates()
		for _, c := range store.List[CompositeAlarm](s.env.Store, cComposite) {
			n, err := parseAlarmRule(c.Rule)
			if err != nil {
				continue
			}
			state := "OK"
			if n.eval(states) {
				state = "ALARM"
			}
			if state != c.State {
				s.setCompositeState(c, state, fmt.Sprintf("The alarm rule evaluated to %s", strings.ToUpper(strconv.FormatBool(state == "ALARM"))))
				changed = true
			}
		}
		if !changed {
			return
		}
	}
}

func (s *Service) setCompositeState(c CompositeAlarm, state, reason string) {
	prev := c.State
	updated, err := store.Update(s.env.Store, cComposite, c.Name, func(x *CompositeAlarm) error {
		x.State, x.StateReason, x.StateUpdatedAt = state, reason, core.Now()
		return nil
	})
	if err != nil {
		return
	}
	s.addHistory(c.Name, "StateUpdate", fmt.Sprintf("Alarm updated from %s to %s", prev, state), map[string]any{
		"version": "1.0", "oldState": map[string]string{"stateValue": prev}, "newState": map[string]string{"stateValue": state, "stateReason": reason}})
	if s.Notify == nil || (updated.ActionsEnabled != nil && !*updated.ActionsEnabled) {
		return
	}
	targets := updated.OKActions
	switch state {
	case "ALARM":
		targets = updated.AlarmActions
	case "INSUFFICIENT_DATA":
		targets = updated.InsufficientDataActions
	}
	b, _ := json.Marshal(map[string]any{"AlarmName": updated.Name, "AlarmDescription": updated.Description, "AWSAccountId": s.env.AccountID,
		"NewStateValue": state, "NewStateReason": reason, "OldStateValue": prev, "AlarmRule": updated.Rule, "AlarmArn": updated.ARN,
		"StateChangeTime": updated.StateUpdatedAt.Format("2006-01-02T15:04:05.000+0000"), "Region": s.env.Cfg.Region})
	for _, t := range targets {
		go s.Notify(t, fmt.Sprintf("%s: %q in %s", state, updated.Name, s.env.Cfg.Region), string(b))
	}
}

// ---- AWS API ----

func (s *Service) awsPutCompositeAlarm(q *awsapi.Req) (any, error) {
	var in struct {
		AlarmName                        string
		AlarmDescription                 string
		AlarmRule                        string
		ActionsEnabled                   *bool
		AlarmActions                     []string
		OKActions                        []string
		InsufficientDataActions          []string
		ActionsSuppressor                string
		ActionsSuppressorWaitPeriod      *int
		ActionsSuppressorExtensionPeriod *int
		Tags                             []awsTag
	}
	if err := q.Decode(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("cloudwatch:PutCompositeAlarm", s.alarmARN(in.AlarmName)); err != nil {
		return nil, err
	}
	if !alarmName.MatchString(in.AlarmName) {
		return nil, invalid("AlarmName must be 1-255 characters")
	}
	if store.Has(s.env.Store, cAlarms, in.AlarmName) {
		return nil, invalid("A metric alarm named %s already exists", in.AlarmName)
	}
	n, err := parseAlarmRule(in.AlarmRule)
	if err != nil {
		return nil, invalid("Invalid AlarmRule: %v", err)
	}
	states := s.alarmStates()
	for _, a := range n.alarms(nil) {
		if a == in.AlarmName {
			return nil, invalid("A composite alarm cannot refer to itself")
		}
		if _, ok := states[a]; !ok {
			return nil, invalid("AlarmRule refers to %s, which does not exist", a)
		}
	}
	if (in.ActionsSuppressorWaitPeriod != nil || in.ActionsSuppressorExtensionPeriod != nil) && in.ActionsSuppressor == "" {
		return nil, invalid("ActionsSuppressorWaitPeriod and ActionsSuppressorExtensionPeriod need ActionsSuppressor")
	}
	now := core.Now()
	c, err := store.Get[CompositeAlarm](s.env.Store, cComposite, in.AlarmName)
	if err != nil {
		c = CompositeAlarm{Name: in.AlarmName, ARN: s.alarmARN(in.AlarmName), State: "INSUFFICIENT_DATA",
			StateReason: "Unchecked: Initial alarm creation", StateUpdatedAt: now}
		if len(in.Tags) > 0 {
			c.Tags = core.Tags{}
			for _, t := range in.Tags {
				c.Tags[t.Key] = t.Value
			}
		}
	}
	c.Description, c.Rule, c.ActionsEnabled = in.AlarmDescription, in.AlarmRule, in.ActionsEnabled
	c.AlarmActions, c.OKActions, c.InsufficientDataActions = in.AlarmActions, in.OKActions, in.InsufficientDataActions
	c.Suppressor, c.SuppressorWait, c.SuppressorExtension = in.ActionsSuppressor, in.ActionsSuppressorWaitPeriod, in.ActionsSuppressorExtensionPeriod
	c.ConfigUpdatedAt = now
	if err := store.Put(s.env.Store, cComposite, c.Name, c); err != nil {
		return nil, err
	}
	s.evaluateComposites()
	return nil, nil
}

type awsCompositeAlarm struct {
	AlarmName                          string
	AlarmArn                           string
	AlarmDescription                   string `json:",omitempty"`
	AlarmRule                          string
	ActionsEnabled                     bool
	AlarmActions                       []string
	OKActions                          []string
	InsufficientDataActions            []string
	StateValue                         string
	StateReason                        string
	StateUpdatedTimestamp              *awsapi.Time `json:",omitempty"`
	StateTransitionedTimestamp         *awsapi.Time `json:",omitempty"`
	AlarmConfigurationUpdatedTimestamp *awsapi.Time `json:",omitempty"`
	ActionsSuppressor                  string       `json:",omitempty"`
	ActionsSuppressorWaitPeriod        *int         `json:",omitempty"`
	ActionsSuppressorExtensionPeriod   *int         `json:",omitempty"`
}

func toAWSComposite(c CompositeAlarm) awsCompositeAlarm {
	return awsCompositeAlarm{AlarmName: c.Name, AlarmArn: c.ARN, AlarmDescription: c.Description, AlarmRule: c.Rule,
		ActionsEnabled: c.ActionsEnabled == nil || *c.ActionsEnabled, AlarmActions: nonNil(c.AlarmActions), OKActions: nonNil(c.OKActions),
		InsufficientDataActions: nonNil(c.InsufficientDataActions), StateValue: c.State, StateReason: c.StateReason,
		StateUpdatedTimestamp: awsapi.T(c.StateUpdatedAt), StateTransitionedTimestamp: awsapi.T(c.StateUpdatedAt),
		AlarmConfigurationUpdatedTimestamp: awsapi.T(c.ConfigUpdatedAt), ActionsSuppressor: c.Suppressor,
		ActionsSuppressorWaitPeriod: c.SuppressorWait, ActionsSuppressorExtensionPeriod: c.SuppressorExtension}
}

// compositesFor lists the composite alarms DescribeAlarms returns.
func (s *Service) compositesFor(names []string, prefix, state, action, after string) []awsCompositeAlarm {
	cs := store.List[CompositeAlarm](s.env.Store, cComposite)
	sort.Slice(cs, func(i, j int) bool { return cs[i].Name < cs[j].Name })
	out := []awsCompositeAlarm{}
	for _, c := range cs {
		switch {
		case after != "" && c.Name <= after,
			len(names) > 0 && !slices.Contains(names, c.Name),
			prefix != "" && !strings.HasPrefix(c.Name, prefix),
			state != "" && c.State != state,
			action != "" && !slices.ContainsFunc(slices.Concat(c.AlarmActions, c.OKActions, c.InsufficientDataActions),
				func(x string) bool { return strings.HasPrefix(x, action) }):
			continue
		}
		out = append(out, toAWSComposite(c))
	}
	return out
}

// deleteComposite removes a composite alarm; ok is false when there is none.
func (s *Service) deleteComposite(name string) bool {
	if store.Delete(s.env.Store, cComposite, name) != nil {
		return false
	}
	_ = store.Delete(s.env.Store, cAlarmHistory, name)
	return true
}
