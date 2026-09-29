// Package sns implements pub/sub topics that fan messages out to SQS queues,
// Lambda functions and HTTP(S) endpoints, with subscription filter policies.
package sns

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/store"
	"github.com/homecloudhq/homecloud/cli/internal/svc"
	"github.com/homecloudhq/homecloud/cli/internal/svc/lambda"
	"github.com/homecloudhq/homecloud/cli/internal/svc/sqs"
)

const (
	cTopics = "sns_topics"
	cSubs   = "sns_subscriptions"
)

type Topic struct {
	Name        string    `json:"name"`
	ARN         string    `json:"arn"`
	DisplayName string    `json:"display_name"`
	FIFO        bool      `json:"fifo"`
	CreatedAt   time.Time `json:"created_at"`
	Tags        core.Tags `json:"tags,omitempty"`
	Published   int64     `json:"messages_published"`
}

type Subscription struct {
	ARN                string              `json:"arn"`
	TopicARN           string              `json:"topic_arn"`
	TopicName          string              `json:"topic_name"`
	Protocol           string              `json:"protocol"` // sqs | lambda | http | https
	Endpoint           string              `json:"endpoint"`
	RawMessageDelivery bool                `json:"raw_message_delivery"`
	FilterPolicy       map[string][]string `json:"filter_policy,omitempty"`
	Status             string              `json:"status"`
	Delivered          int64               `json:"delivered"`
	Failed             int64               `json:"failed"`
	LastError          string              `json:"last_error,omitempty"`
	LastDelivery       *time.Time          `json:"last_delivery,omitempty"`
	CreatedAt          time.Time           `json:"created_at"`
}

type Attribute struct {
	DataType    string `json:"data_type"`
	StringValue string `json:"string_value"`
}

type Service struct {
	env    *svc.Env
	sqs    *sqs.Service
	lambda *lambda.Service
	http   *http.Client
}

func New(env *svc.Env, q *sqs.Service, l *lambda.Service) *Service {
	return &Service{env: env, sqs: q, lambda: l, http: core.WebhookClient(15 * time.Second)}
}

func uuid() string {
	h := core.RandHex(32)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

func nameFromARN(arn string) string { return arn[strings.LastIndex(arn, ":")+1:] }

// Publish fans a message out to every matching subscription and returns its ID.
func (s *Service) Publish(topic, subject, message string, attrs map[string]Attribute) (string, error) {
	topic = nameFromARN(topic)
	t, err := store.Get[Topic](s.env.Store, cTopics, topic)
	if err != nil {
		return "", core.NotFound("topic", topic)
	}
	if message == "" {
		return "", core.BadRequest("message must not be empty")
	}
	if len(message) > 256<<10 {
		return "", core.BadRequest("message exceeds 256 KB")
	}
	id := uuid()
	now := time.Now().UTC()
	envAttrs := map[string]map[string]string{}
	for k, a := range attrs {
		envAttrs[k] = map[string]string{"Type": a.DataType, "Value": a.StringValue}
	}
	envelope := map[string]any{
		"Type": "Notification", "MessageId": id, "TopicArn": t.ARN, "Subject": subject, "Message": message,
		"Timestamp": now.Format("2006-01-02T15:04:05.000Z"), "SignatureVersion": "1", "MessageAttributes": envAttrs,
	}
	_, _ = store.Update(s.env.Store, cTopics, topic, func(x *Topic) error { x.Published++; return nil })
	for _, sub := range store.List[Subscription](s.env.Store, cSubs) {
		if sub.TopicARN != t.ARN || !matches(sub.FilterPolicy, attrs) {
			continue
		}
		go s.deliver(sub, envelope, message, attrs)
	}
	return id, nil
}

// matches applies a filter policy: every key must be present with one of the allowed values.
func matches(policy map[string][]string, attrs map[string]Attribute) bool {
	for k, allowed := range policy {
		a, ok := attrs[k]
		if !ok || !slices.Contains(allowed, a.StringValue) {
			return false
		}
	}
	return true
}

func (s *Service) deliver(sub Subscription, envelope map[string]any, message string, attrs map[string]Attribute) {
	env, _ := json.Marshal(envelope)
	var err error
	switch sub.Protocol {
	case "sqs":
		body := string(env)
		qa := map[string]sqs.MessageAttribute{}
		if sub.RawMessageDelivery {
			body = message
			for k, a := range attrs {
				qa[k] = sqs.MessageAttribute{DataType: a.DataType, StringValue: a.StringValue}
			}
		}
		in := sqs.SendInput{Body: body, MessageAttributes: qa}
		if q := sqs.NameFromARN(sub.Endpoint); strings.HasSuffix(q, ".fifo") {
			in.GroupID, in.DedupID = "sns", fmt.Sprint(envelope["MessageId"])
		}
		_, err = s.sqs.Send(sqs.NameFromARN(sub.Endpoint), in)
	case "lambda":
		rec := map[string]any{"EventSource": "aws:sns", "EventVersion": "1.0", "EventSubscriptionArn": sub.ARN, "Sns": envelope}
		payload, _ := json.Marshal(map[string]any{"Records": []any{rec}})
		var res *lambda.InvokeResult
		res, err = s.lambda.Invoke(context.Background(), nameFromARN(sub.Endpoint), payload)
		if err == nil && res.FunctionError != "" {
			err = fmt.Errorf("function error: %s", string(res.Payload))
		}
	case "http", "https":
		body := env
		if sub.RawMessageDelivery {
			body = []byte(message)
		}
		for attempt := 0; attempt < 3; attempt++ {
			if attempt > 0 {
				time.Sleep(time.Duration(attempt*attempt) * time.Second)
			}
			err = s.post(sub, body, envelope)
			if err == nil {
				break
			}
		}
	}
	_, _ = store.Update(s.env.Store, cSubs, sub.ARN, func(x *Subscription) error {
		n := core.Now()
		x.LastDelivery = &n
		if err != nil {
			x.Failed++
			x.LastError = err.Error()
		} else {
			x.Delivered++
		}
		return nil
	})
	if err != nil {
		log.Printf("sns: deliver to %s %s: %v", sub.Protocol, sub.Endpoint, err)
	}
}

func (s *Service) post(sub Subscription, body []byte, envelope map[string]any) error {
	req, err := http.NewRequest(http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "text/plain; charset=UTF-8")
	req.Header.Set("x-amz-sns-message-type", "Notification")
	req.Header.Set("x-amz-sns-message-id", fmt.Sprint(envelope["MessageId"]))
	req.Header.Set("x-amz-sns-topic-arn", sub.TopicARN)
	req.Header.Set("x-amz-sns-subscription-arn", sub.ARN)
	resp, err := s.http.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("endpoint returned %s", resp.Status)
	}
	return nil
}

// Notify delivers an alarm-style notification to a topic ARN or an http(s) URL.
func (s *Service) Notify(target, subject, message string) {
	if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
		if err := core.CheckWebhookURL(target); err != nil {
			log.Printf("notify %s: %v", target, err)
			return
		}
		body, _ := json.Marshal(map[string]string{"subject": subject, "message": message})
		resp, err := s.http.Post(target, "application/json", bytes.NewReader(body))
		if err != nil {
			log.Printf("notify %s: %v", target, err)
			return
		}
		resp.Body.Close()
		return
	}
	if _, err := s.Publish(target, subject, message, nil); err != nil {
		log.Printf("notify %s: %v", target, err)
	}
}

// ---- routes ----

func (s *Service) Routes(r *httpx.Router) {
	res := httpx.Res("arn:hc:sns:local-1:{account}:{name}")
	r.Handle("GET /api/v1/sns/topics", "sns:ListTopics", s.listTopics)
	r.Handle("POST /api/v1/sns/topics", "sns:CreateTopic", s.createTopic)
	r.Handle("GET /api/v1/sns/topics/{name}", "sns:GetTopicAttributes", s.getTopic, res)
	r.Handle("PATCH /api/v1/sns/topics/{name}", "sns:SetTopicAttributes", s.updateTopic, res)
	r.Handle("DELETE /api/v1/sns/topics/{name}", "sns:DeleteTopic", s.deleteTopic, res)
	r.Handle("POST /api/v1/sns/topics/{name}/publish", "sns:Publish", s.publish, res)
	r.Handle("GET /api/v1/sns/subscriptions", "sns:ListSubscriptions", s.listSubs)
	r.Handle("POST /api/v1/sns/topics/{name}/subscriptions", "sns:Subscribe", s.subscribe, res)
	r.Handle("PATCH /api/v1/sns/subscriptions/{arn}", "sns:SetSubscriptionAttributes", s.updateSub, httpx.Deferred())
	r.Handle("DELETE /api/v1/sns/subscriptions/{arn}", "sns:Unsubscribe", s.unsubscribe, httpx.Deferred())
}

// topicParam rejects topic path parameters that are not plain names; Publish
// would otherwise resolve "x:prod" to "prod" after authorizing "x:prod".
func topicParam(c *httpx.Ctx) (string, error) {
	n := c.Param("name")
	if strings.Contains(n, ":") {
		return "", core.BadRequest("use the topic name, not an ARN, in the path")
	}
	return n, nil
}

func (s *Service) topicView(t Topic) map[string]any {
	n := 0
	for _, sub := range store.List[Subscription](s.env.Store, cSubs) {
		if sub.TopicARN == t.ARN {
			n++
		}
	}
	return map[string]any{"name": t.Name, "arn": t.ARN, "display_name": t.DisplayName, "fifo": t.FIFO, "created_at": t.CreatedAt,
		"tags": t.Tags, "messages_published": t.Published, "subscriptions": n}
}

func (s *Service) listTopics(c *httpx.Ctx) (any, error) {
	out := []map[string]any{}
	for _, t := range store.List[Topic](s.env.Store, cTopics) {
		out = append(out, s.topicView(t))
	}
	return out, nil
}

var nameRe = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,256}$`)

func (s *Service) createTopic(c *httpx.Ctx) (any, error) {
	var in struct {
		Name        string    `json:"name"`
		DisplayName string    `json:"display_name"`
		Tags        core.Tags `json:"tags"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	fifo := strings.HasSuffix(in.Name, ".fifo")
	if !nameRe.MatchString(strings.TrimSuffix(in.Name, ".fifo")) || strings.Contains(in.Name, ":") {
		return nil, core.BadRequest("topic names are 1-256 letters, digits, hyphens or underscores")
	}
	if t, err := store.Get[Topic](s.env.Store, cTopics, in.Name); err == nil {
		return s.topicView(t), nil // CreateTopic is idempotent
	}
	t := Topic{Name: in.Name, ARN: s.env.ARN("sns", in.Name), DisplayName: in.DisplayName, FIFO: fifo, CreatedAt: core.Now(), Tags: in.Tags}
	return s.topicView(t), store.Put(s.env.Store, cTopics, t.Name, t)
}

func (s *Service) getTopic(c *httpx.Ctx) (any, error) {
	t, err := store.Get[Topic](s.env.Store, cTopics, c.Param("name"))
	if err != nil {
		return nil, core.NotFound("topic", c.Param("name"))
	}
	v := s.topicView(t)
	subs := []Subscription{}
	for _, sub := range store.List[Subscription](s.env.Store, cSubs) {
		if sub.TopicARN == t.ARN {
			subs = append(subs, sub)
		}
	}
	v["subscription_list"] = subs
	return v, nil
}

func (s *Service) updateTopic(c *httpx.Ctx) (any, error) {
	var in struct {
		DisplayName *string   `json:"display_name"`
		Tags        core.Tags `json:"tags"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	t, err := store.Update(s.env.Store, cTopics, c.Param("name"), func(t *Topic) error {
		if in.DisplayName != nil {
			t.DisplayName = *in.DisplayName
		}
		if in.Tags != nil {
			t.Tags = in.Tags
		}
		return nil
	})
	if err == store.ErrNotFound {
		return nil, core.NotFound("topic", c.Param("name"))
	}
	return s.topicView(t), err
}

func (s *Service) deleteTopic(c *httpx.Ctx) (any, error) {
	t, err := store.Get[Topic](s.env.Store, cTopics, c.Param("name"))
	if err != nil {
		return nil, core.NotFound("topic", c.Param("name"))
	}
	for _, sub := range store.List[Subscription](s.env.Store, cSubs) {
		if sub.TopicARN == t.ARN {
			_ = store.Delete(s.env.Store, cSubs, sub.ARN)
		}
	}
	return nil, store.Delete(s.env.Store, cTopics, t.Name)
}

func (s *Service) publish(c *httpx.Ctx) (any, error) {
	var in struct {
		Subject           string               `json:"subject"`
		Message           string               `json:"message"`
		MessageAttributes map[string]Attribute `json:"message_attributes"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	name, err := topicParam(c)
	if err != nil {
		return nil, err
	}
	id, err := s.Publish(name, in.Subject, in.Message, in.MessageAttributes)
	if err != nil {
		return nil, err
	}
	return map[string]string{"message_id": id}, nil
}

func (s *Service) listSubs(c *httpx.Ctx) (any, error) {
	out := []Subscription{}
	for _, sub := range store.List[Subscription](s.env.Store, cSubs) {
		if t := c.Query("topic"); t == "" || sub.TopicName == t {
			out = append(out, sub)
		}
	}
	return out, nil
}

func (s *Service) subscribe(c *httpx.Ctx) (any, error) {
	var in struct {
		Protocol           string              `json:"protocol"`
		Endpoint           string              `json:"endpoint"`
		RawMessageDelivery bool                `json:"raw_message_delivery"`
		FilterPolicy       map[string][]string `json:"filter_policy"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if _, err := topicParam(c); err != nil {
		return nil, err
	}
	t, err := store.Get[Topic](s.env.Store, cTopics, c.Param("name"))
	if err != nil {
		return nil, core.NotFound("topic", c.Param("name"))
	}
	switch in.Protocol {
	case "sqs":
		arn, ok := s.sqs.QueueARN(sqs.NameFromARN(in.Endpoint))
		if !ok {
			return nil, core.NotFound("queue", in.Endpoint)
		}
		if err := c.Authorize("sqs:SendMessage", arn); err != nil {
			return nil, err
		}
		in.Endpoint = arn
	case "lambda":
		name := nameFromARN(in.Endpoint)
		if !s.lambda.Exists(name) {
			return nil, core.NotFound("function", name)
		}
		in.Endpoint = s.env.ARN("lambda", "function:"+name)
		if err := c.Authorize("lambda:InvokeFunction", in.Endpoint); err != nil {
			return nil, err
		}
	case "http", "https":
		if !strings.HasPrefix(in.Endpoint, in.Protocol+"://") {
			return nil, core.BadRequest("endpoint must be a %s:// URL", in.Protocol)
		}
		if err := core.CheckWebhookURL(in.Endpoint); err != nil {
			return nil, err
		}
	default:
		return nil, core.BadRequest("protocol must be sqs, lambda, http or https")
	}
	for _, sub := range store.List[Subscription](s.env.Store, cSubs) {
		if sub.TopicARN == t.ARN && sub.Protocol == in.Protocol && sub.Endpoint == in.Endpoint {
			return sub, nil
		}
	}
	sub := Subscription{ARN: t.ARN + ":" + uuid(), TopicARN: t.ARN, TopicName: t.Name, Protocol: in.Protocol, Endpoint: in.Endpoint,
		RawMessageDelivery: in.RawMessageDelivery, FilterPolicy: in.FilterPolicy, Status: "Confirmed", CreatedAt: core.Now()}
	return sub, store.Put(s.env.Store, cSubs, sub.ARN, sub)
}

func (s *Service) updateSub(c *httpx.Ctx) (any, error) {
	var in struct {
		RawMessageDelivery *bool                `json:"raw_message_delivery"`
		FilterPolicy       *map[string][]string `json:"filter_policy"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if err := s.authorizeSub(c, "sns:SetSubscriptionAttributes"); err != nil {
		return nil, err
	}
	sub, err := store.Update(s.env.Store, cSubs, c.Param("arn"), func(x *Subscription) error {
		if in.RawMessageDelivery != nil {
			x.RawMessageDelivery = *in.RawMessageDelivery
		}
		if in.FilterPolicy != nil {
			x.FilterPolicy = *in.FilterPolicy
		}
		return nil
	})
	if err == store.ErrNotFound {
		return nil, core.NotFound("subscription", c.Param("arn"))
	}
	return sub, err
}

// authorizeSub checks action against the subscription's topic.
func (s *Service) authorizeSub(c *httpx.Ctx, action string) error {
	sub, err := store.Get[Subscription](s.env.Store, cSubs, c.Param("arn"))
	if err != nil {
		return core.NotFound("subscription", c.Param("arn"))
	}
	return c.Authorize(action, sub.TopicARN)
}

func (s *Service) unsubscribe(c *httpx.Ctx) (any, error) {
	if err := s.authorizeSub(c, "sns:Unsubscribe"); err != nil {
		return nil, err
	}
	if err := store.Delete(s.env.Store, cSubs, c.Param("arn")); err != nil {
		return nil, core.NotFound("subscription", c.Param("arn"))
	}
	return nil, nil
}

// TopicExists reports whether a topic exists.
func (s *Service) TopicExists(name string) bool {
	return store.Has(s.env.Store, cTopics, nameFromARN(name))
}
