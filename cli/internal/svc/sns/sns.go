// Package sns implements pub/sub topics that fan messages out to SQS queues,
// Lambda functions, HTTP(S) endpoints and email addresses, with subscription
// filter policies, raw delivery, FIFO topics and dead-letter queues.
//
// The native API is in this file; aws.go serves the same topics over the AWS
// SNS protocol (awsQuery).
package sns

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
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

	maxMessageBytes = 256 << 10
	dedupWindow     = 5 * time.Minute

	statusConfirmed = "Confirmed"
	statusPending   = "PendingConfirmation"
)

type Topic struct {
	Name        string    `json:"name"`
	ARN         string    `json:"arn"`
	DisplayName string    `json:"display_name"`
	FIFO        bool      `json:"fifo"`
	CreatedAt   time.Time `json:"created_at"`
	Tags        core.Tags `json:"tags,omitempty"`
	Published   int64     `json:"messages_published"`
	// Attributes holds the other AWS topic attributes (Policy, DeliveryPolicy,
	// ContentBasedDeduplication, SignatureVersion, ...), stored verbatim.
	Attributes map[string]string `json:"attributes,omitempty"`
	// SubsDeleted counts unsubscribes (SubscriptionsDeleted).
	SubsDeleted int64 `json:"subscriptions_deleted,omitempty"`
}

func (t Topic) attr(name string) string { return t.Attributes[name] }

type Subscription struct {
	ARN                string          `json:"arn"`
	TopicARN           string          `json:"topic_arn"`
	TopicName          string          `json:"topic_name"`
	Protocol           string          `json:"protocol"` // sqs | lambda | http | https | email | email-json | sms
	Endpoint           string          `json:"endpoint"`
	RawMessageDelivery bool            `json:"raw_message_delivery"`
	FilterPolicy       json.RawMessage `json:"filter_policy,omitempty"`
	// FilterPolicyScope is MessageAttributes (default) or MessageBody.
	FilterPolicyScope   string     `json:"filter_policy_scope,omitempty"`
	RedrivePolicy       string     `json:"redrive_policy,omitempty"` // {"deadLetterTargetArn": "<queue ARN>"}
	DeliveryPolicy      string     `json:"delivery_policy,omitempty"`
	SubscriptionRoleARN string     `json:"subscription_role_arn,omitempty"`
	Owner               string     `json:"owner,omitempty"`
	Principal           string     `json:"principal,omitempty"` // who subscribed
	Status              string     `json:"status"`              // Confirmed | PendingConfirmation
	Token               string     `json:"confirmation_token,omitempty"`
	AuthOnUnsubscribe   bool       `json:"authenticate_on_unsubscribe,omitempty"`
	ConfirmedByAuth     bool       `json:"confirmation_was_authenticated,omitempty"`
	BaseURL             string     `json:"base_url,omitempty"` // endpoint for SubscribeURL/UnsubscribeURL
	Delivered           int64      `json:"delivered"`
	Failed              int64      `json:"failed"`
	LastError           string     `json:"last_error,omitempty"`
	LastDelivery        *time.Time `json:"last_delivery,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
}

func (s Subscription) scope() string {
	if s.FilterPolicyScope == "" {
		return scopeAttributes
	}
	return s.FilterPolicyScope
}

// public hides the confirmation token.
func (s Subscription) public() Subscription { s.Token = ""; return s }

// Attribute is a message attribute: DataType String, String.Array, Number or Binary.
type Attribute struct {
	DataType    string `json:"data_type"`
	StringValue string `json:"string_value"`
	BinaryValue []byte `json:"binary_value,omitempty"`
}

// Invoker runs Lambda functions (implemented by the Lambda service).
type Invoker interface {
	Invoke(ctx context.Context, name string, payload []byte) (*lambda.InvokeResult, error)
	Exists(name string) bool
}

type Service struct {
	env    *svc.Env
	sqs    *sqs.Service
	lambda Invoker
	// HTTP delivers to http(s) subscriptions; CheckURL vets their endpoints.
	// Both refuse loopback and link-local addresses by default.
	HTTP     *http.Client
	CheckURL func(string) error
	cfgMu    sync.RWMutex // guards HTTP and CheckURL once the service is serving

	mu    sync.Mutex
	dedup map[string]map[string]dedupEntry // FIFO topic -> dedup ID
	seq   map[string]int64

	signOnce sync.Once
	sign     *signer
}

type dedupEntry struct {
	at  time.Time
	id  string
	seq string
}

// New creates the service. l may be nil (Lambda subscriptions then fail).
func New(env *svc.Env, q *sqs.Service, l Invoker) *Service {
	s := &Service{env: env, sqs: q, HTTP: core.WebhookClient(15 * time.Second), CheckURL: core.CheckWebhookURL,
		dedup: map[string]map[string]dedupEntry{}, seq: map[string]int64{}}
	if l != nil {
		if lp, ok := l.(*lambda.Service); !ok || lp != nil {
			s.lambda = l
		}
	}
	return s
}

func uuid() string {
	h := core.RandHex(32)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

func nameFromARN(arn string) string { return arn[strings.LastIndex(arn, ":")+1:] }

// ---- errors (SNS codes; the native API reports the same codes) ----

func errInvalid(format string, a ...any) error {
	return core.Errf(http.StatusBadRequest, "InvalidParameter", "Invalid parameter: "+format, a...)
}

func errValue(format string, a ...any) error {
	return core.Errf(http.StatusBadRequest, "ParameterValueInvalid", format, a...)
}

func errNoTopic() error {
	return core.Errf(http.StatusNotFound, "NotFound", "Topic does not exist")
}

func errNoSub() error {
	return core.Errf(http.StatusNotFound, "NotFound", "Subscription does not exist")
}

func (s *Service) getTopic(name string) (Topic, error) {
	t, err := store.Get[Topic](s.env.Store, cTopics, nameFromARN(name))
	if err != nil {
		return t, errNoTopic()
	}
	return t, nil
}

// defaultBaseURL is the API endpoint used in links when no request tells us
// how clients reach the server.
func (s *Service) defaultBaseURL() string {
	_, port, _ := strings.Cut(s.env.Cfg.APIAddr, ":")
	return fmt.Sprintf("http://%s:%s", s.env.Cfg.PublicHost, port)
}

// BaseURL returns the endpoint a client used for r (scheme://host).
func BaseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if p := r.Header.Get("X-Forwarded-Proto"); p == "http" || p == "https" {
		scheme = p
	}
	return scheme + "://" + r.Host
}

// ---- topics ----

var (
	nameRe     = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,256}$`)
	feedbackRe = regexp.MustCompile(`^(Application|Firehose|HTTP|Lambda|SQS)(SuccessFeedbackRoleArn|SuccessFeedbackSampleRate|FailureFeedbackRoleArn)$`)
)

// setTopicAttribute validates and applies one attribute.
func setTopicAttribute(t *Topic, name, value string, creating bool) error {
	isJSON := func() error {
		if value == "" {
			return nil
		}
		var v map[string]any
		if json.Unmarshal([]byte(value), &v) != nil {
			return errInvalid("Attributes Reason: %s: failed to parse JSON", name)
		}
		return nil
	}
	fifoOnly := func() error {
		if !t.FIFO {
			return errInvalid("Attributes Reason: %s is only valid for FIFO topics", name)
		}
		return nil
	}
	var err error
	switch {
	case name == "DisplayName":
		if len(value) > 100 {
			return errInvalid("DisplayName Reason: must be at most 100 characters")
		}
		t.DisplayName = value
		return nil
	case name == "FifoTopic":
		if !creating {
			return errInvalid("AttributeName Reason: FifoTopic cannot be changed")
		}
		return nil // applied by createTopic
	case name == "Policy", name == "DeliveryPolicy", name == "DataProtectionPolicy":
		err = isJSON()
	case name == "ArchivePolicy":
		if err = fifoOnly(); err == nil {
			err = isJSON()
		}
	case name == "ContentBasedDeduplication":
		if err = fifoOnly(); err == nil && value != "true" && value != "false" {
			err = errInvalid("Attributes Reason: ContentBasedDeduplication must be true or false")
		}
	case name == "FifoThroughputScope":
		if err = fifoOnly(); err == nil && value != "Topic" && value != "MessageGroup" {
			err = errInvalid("Attributes Reason: FifoThroughputScope must be Topic or MessageGroup")
		}
	case name == "SignatureVersion":
		if value != "1" && value != "2" {
			err = errInvalid("Attributes Reason: SignatureVersion must be 1 or 2")
		}
	case name == "TracingConfig":
		if value != "PassThrough" && value != "Active" {
			err = errInvalid("Attributes Reason: TracingConfig must be PassThrough or Active")
		}
	case name == "KmsMasterKeyId", feedbackRe.MatchString(name):
	default:
		return errInvalid("AttributeName")
	}
	if err != nil {
		return err
	}
	if t.Attributes == nil {
		t.Attributes = map[string]string{}
	}
	if value == "" {
		delete(t.Attributes, name)
	} else {
		t.Attributes[name] = value
	}
	return nil
}

// createTopic creates a topic or returns the existing one when the requested
// attributes match it.
func (s *Service) createTopic(name string, attrs map[string]string, tags core.Tags) (Topic, error) {
	fifo := strings.HasSuffix(name, ".fifo")
	if !nameRe.MatchString(strings.TrimSuffix(name, ".fifo")) {
		return Topic{}, errInvalid("Topic Name")
	}
	if v, ok := attrs["FifoTopic"]; ok {
		if (v == "true") != fifo {
			return Topic{}, errInvalid("Fifo Topic names must end with .fifo and must be made up of only uppercase and lowercase ASCII letters, numbers, underscores, and hyphens, and must be between 1 and 256 characters long.")
		}
	}
	t := Topic{Name: name, ARN: s.env.ARN("sns", name), FIFO: fifo, CreatedAt: core.Now(), Tags: tags}
	if existing, err := store.Get[Topic](s.env.Store, cTopics, name); err == nil {
		cp := existing
		cp.Attributes = map[string]string{}
		for k, v := range existing.Attributes {
			cp.Attributes[k] = v
		}
		for k, v := range attrs {
			if err := setTopicAttribute(&cp, k, v, true); err != nil {
				return Topic{}, err
			}
		}
		b1, _ := json.Marshal(cp)
		b2, _ := json.Marshal(existing)
		if !bytes.Equal(b1, b2) {
			return Topic{}, errInvalid("Attributes Reason: Topic already exists with different attributes")
		}
		return existing, nil // CreateTopic is idempotent
	}
	for k, v := range attrs {
		if err := setTopicAttribute(&t, k, v, true); err != nil {
			return Topic{}, err
		}
	}
	if len(tags) > 50 {
		return Topic{}, core.Errf(http.StatusBadRequest, "TagLimitExceeded", "Could not complete request: tag quota of per resource exceeded")
	}
	return t, store.Put(s.env.Store, cTopics, t.Name, t)
}

// deleteTopic deletes a topic and its subscriptions (no error if missing).
func (s *Service) deleteTopic(name string) error {
	t, err := s.getTopic(name)
	if err != nil {
		return nil
	}
	for _, sub := range s.subsOf(t.ARN) {
		_ = store.Delete(s.env.Store, cSubs, sub.ARN)
	}
	s.mu.Lock()
	delete(s.dedup, t.ARN)
	s.mu.Unlock()
	return store.Delete(s.env.Store, cTopics, t.Name)
}

func (s *Service) subsOf(topicARN string) []Subscription {
	var out []Subscription
	for _, sub := range store.List[Subscription](s.env.Store, cSubs) {
		if sub.TopicARN == topicARN {
			out = append(out, sub)
		}
	}
	return out
}

// ---- subscriptions ----

type subscribeInput struct {
	Protocol, Endpoint string
	Attributes         map[string]string
	BaseURL            string
	Principal          string
}

var phoneRe = regexp.MustCompile(`^\+?[0-9]{5,15}$`)

// setSubAttribute validates and applies one subscription attribute.
func (s *Service) setSubAttribute(sub *Subscription, name, value string) error {
	switch name {
	case "RawMessageDelivery":
		if value != "true" && value != "false" {
			return errInvalid("Attributes Reason: RawMessageDelivery: Invalid value [%s]. Must be true or false.", value)
		}
		switch sub.Protocol {
		case "sqs", "http", "https", "firehose":
		default:
			if value == "true" {
				return errInvalid("Attributes Reason: Delivery protocol [%s] does not support raw message delivery.", sub.Protocol)
			}
		}
		sub.RawMessageDelivery = value == "true"
	case "FilterPolicyScope":
		if value != scopeAttributes && value != scopeBody {
			return errInvalid("Attributes Reason: FilterPolicyScope: Invalid value [%s]. Please use either MessageBody or MessageAttributes", value)
		}
		if _, err := parsePolicy(sub.FilterPolicy, value); err != nil {
			return errInvalid("Filter Policy Scope Reason: %v", err)
		}
		sub.FilterPolicyScope = value
	case "FilterPolicy":
		p, err := parsePolicy([]byte(value), sub.scope())
		if err != nil {
			return errInvalid("Filter Policy: %v", err)
		}
		if p == nil {
			sub.FilterPolicy = nil
		} else {
			var b bytes.Buffer
			_ = json.Compact(&b, []byte(value))
			sub.FilterPolicy = b.Bytes()
		}
	case "RedrivePolicy":
		if value == "" {
			sub.RedrivePolicy = ""
			return nil
		}
		var rp struct {
			DeadLetterTargetArn string `json:"deadLetterTargetArn"`
		}
		if json.Unmarshal([]byte(value), &rp) != nil || !strings.HasPrefix(core.CanonicalARN(rp.DeadLetterTargetArn), "arn:"+core.Partition+":sqs:") {
			return errInvalid("RedrivePolicy: deadLetterTargetArn must be an Amazon SQS queue ARN")
		}
		if _, ok := s.sqs.QueueARN(sqs.NameFromARN(rp.DeadLetterTargetArn)); !ok {
			return errInvalid("RedrivePolicy: dead-letter queue %s does not exist", rp.DeadLetterTargetArn)
		}
		sub.RedrivePolicy = value
	case "DeliveryPolicy":
		if value != "" {
			var v map[string]any
			if json.Unmarshal([]byte(value), &v) != nil {
				return errInvalid("DeliveryPolicy: failed to parse JSON")
			}
		}
		sub.DeliveryPolicy = value
	case "SubscriptionRoleArn":
		sub.SubscriptionRoleARN = value
	default:
		return errInvalid("AttributeName")
	}
	return nil
}

// subscribe creates a subscription (or returns the matching existing one).
// authorize checks the caller may deliver to the endpoint.
func (s *Service) subscribe(t Topic, in subscribeInput, authorize func(action, resource string) error) (Subscription, error) {
	switch in.Protocol {
	case "sqs":
		arn := core.CanonicalARN(in.Endpoint)
		if !strings.HasPrefix(arn, "arn:"+core.Partition+":sqs:") {
			if !strings.HasPrefix(arn, "arn:") && !strings.Contains(arn, "/") {
				arn = s.env.ARN("sqs", arn) // the native API accepts a queue name
			} else {
				return Subscription{}, errInvalid("SQS endpoint ARN")
			}
		}
		qarn, ok := s.sqs.QueueARN(sqs.NameFromARN(arn))
		if !ok {
			return Subscription{}, errInvalid("SQS endpoint ARN: queue %s does not exist", arn)
		}
		if err := authorize("sqs:SendMessage", qarn); err != nil {
			return Subscription{}, err
		}
		in.Endpoint = qarn
	case "lambda":
		if t.FIFO {
			return Subscription{}, errInvalid("Invalid protocol type: lambda")
		}
		name := nameFromARN(in.Endpoint)
		if i := strings.Index(in.Endpoint, ":function:"); i >= 0 {
			name, _, _ = strings.Cut(in.Endpoint[i+len(":function:"):], ":")
		}
		if s.lambda == nil || !s.lambda.Exists(name) {
			return Subscription{}, errInvalid("Lambda endpoint ARN: function %s does not exist", name)
		}
		in.Endpoint = s.env.ARN("lambda", "function:"+name)
		if err := authorize("lambda:InvokeFunction", in.Endpoint); err != nil {
			return Subscription{}, err
		}
	case "http", "https":
		if t.FIFO {
			return Subscription{}, errInvalid("Invalid protocol type: %s", in.Protocol)
		}
		u, err := url.Parse(in.Endpoint)
		if err != nil || u.Scheme != in.Protocol || u.Host == "" {
			return Subscription{}, errInvalid("Endpoint must match the specified protocol")
		}
		if err := s.checkURL(in.Endpoint); err != nil {
			return Subscription{}, errInvalid("Endpoint: %v", err)
		}
	case "email", "email-json":
		if t.FIFO {
			return Subscription{}, errInvalid("Invalid protocol type: %s", in.Protocol)
		}
		if at := strings.Index(in.Endpoint, "@"); at < 1 || at == len(in.Endpoint)-1 {
			return Subscription{}, errInvalid("Email address")
		}
	case "sms":
		if t.FIFO {
			return Subscription{}, errInvalid("Invalid protocol type: sms")
		}
		if !phoneRe.MatchString(in.Endpoint) {
			return Subscription{}, errInvalid("SMS endpoint must be a phone number in E.164 format")
		}
	default:
		return Subscription{}, errInvalid("Amazon SNS does not support this protocol string: %s", in.Protocol)
	}
	sub := Subscription{ARN: t.ARN + ":" + uuid(), TopicARN: t.ARN, TopicName: t.Name, Protocol: in.Protocol, Endpoint: in.Endpoint,
		Owner: s.env.AccountID, Principal: in.Principal, Status: statusConfirmed, BaseURL: in.BaseURL, CreatedAt: core.Now()}
	// FilterPolicyScope first, so FilterPolicy is validated against it.
	if v, ok := in.Attributes["FilterPolicyScope"]; ok {
		if v != scopeAttributes && v != scopeBody {
			return Subscription{}, errInvalid("Attributes Reason: FilterPolicyScope: Invalid value [%s]. Please use either MessageBody or MessageAttributes", v)
		}
		sub.FilterPolicyScope = v
	}
	for k, v := range in.Attributes {
		if k == "FilterPolicyScope" {
			continue
		}
		if err := s.setSubAttribute(&sub, k, v); err != nil {
			return Subscription{}, err
		}
	}
	for _, old := range s.subsOf(t.ARN) {
		if old.Protocol == sub.Protocol && old.Endpoint == sub.Endpoint {
			if len(in.Attributes) > 0 && !sameSubAttrs(old, sub) {
				return Subscription{}, errInvalid("Attributes Reason: Subscription already exists with different attributes")
			}
			if old.Status == statusPending {
				go s.sendConfirmation(t, old)
			}
			return old, nil
		}
	}
	if in.Protocol == "http" || in.Protocol == "https" || in.Protocol == "email" || in.Protocol == "email-json" {
		sub.Status, sub.Token = statusPending, core.RandHex(128)
	}
	if err := store.Put(s.env.Store, cSubs, sub.ARN, sub); err != nil {
		return Subscription{}, err
	}
	if sub.Status == statusPending {
		go s.sendConfirmation(t, sub)
	}
	return sub, nil
}

func sameSubAttrs(a, b Subscription) bool {
	return a.RawMessageDelivery == b.RawMessageDelivery && bytes.Equal(a.FilterPolicy, b.FilterPolicy) && a.scope() == b.scope() &&
		a.RedrivePolicy == b.RedrivePolicy && a.DeliveryPolicy == b.DeliveryPolicy
}

func (s *Service) base(sub Subscription) string {
	if sub.BaseURL != "" {
		return sub.BaseURL
	}
	return s.defaultBaseURL()
}

func (s *Service) subscribeURL(sub Subscription) string {
	return s.base(sub) + "/api/v1/sns/confirm-subscription?" + url.Values{"Action": {"ConfirmSubscription"}, "TopicArn": {sub.TopicARN}, "Token": {sub.Token}}.Encode()
}

func (s *Service) unsubscribeURL(sub Subscription) string {
	return s.base(sub) + "/api/v1/sns/unsubscribe?" + url.Values{"Action": {"Unsubscribe"}, "SubscriptionArn": {sub.ARN}}.Encode()
}

func (s *Service) certURL(sub Subscription) string {
	return s.base(sub) + "/api/v1/sns/SimpleNotificationService.pem"
}

// sendConfirmation delivers the SubscriptionConfirmation message.
func (s *Service) sendConfirmation(t Topic, sub Subscription) {
	defer core.Recover("sns subscription confirmation")
	n := &notification{Type: "SubscriptionConfirmation", MessageId: uuid(), Token: sub.Token, TopicArn: sub.TopicARN,
		Message:      fmt.Sprintf("You have chosen to subscribe to the topic %s.\nTo confirm the subscription, visit the SubscribeURL included in this message.", sub.TopicARN),
		SubscribeURL: s.subscribeURL(sub), Timestamp: timestamp(time.Now()), SignatureVersion: sigVersion(t), SigningCertURL: s.certURL(sub)}
	s.signNotification(n)
	switch sub.Protocol {
	case "http", "https":
		if err := s.post(sub, marshal(n), n.Type, n.MessageId, false); err != nil {
			log.Printf("sns: subscription confirmation to %s: %v", sub.Endpoint, err)
			s.record(sub.ARN, err)
		}
	default:
		log.Printf("sns: %s subscription to %s pending; confirm it at %s", sub.Protocol, sub.Endpoint, n.SubscribeURL)
	}
}

// confirm confirms a pending subscription by token.
func (s *Service) confirm(topicARN, token string, authenticated, authOnUnsubscribe bool) (Subscription, error) {
	if _, err := s.getTopic(topicARN); err != nil {
		return Subscription{}, err
	}
	for _, sub := range s.subsOf(core.CanonicalARN(topicARN)) {
		if sub.Token != "" && sub.Token == token {
			return store.Update(s.env.Store, cSubs, sub.ARN, func(x *Subscription) error {
				x.Status = statusConfirmed
				x.ConfirmedByAuth = authenticated
				x.AuthOnUnsubscribe = authOnUnsubscribe && authenticated
				return nil
			})
		}
	}
	return Subscription{}, errInvalid("Token")
}

// unsubscribe deletes a subscription.
func (s *Service) unsubscribe(sub Subscription) error {
	if err := store.Delete(s.env.Store, cSubs, sub.ARN); err != nil {
		return errNoSub()
	}
	_, _ = store.Update(s.env.Store, cTopics, sub.TopicName, func(t *Topic) error { t.SubsDeleted++; return nil })
	if (sub.Protocol == "http" || sub.Protocol == "https") && sub.Status == statusConfirmed {
		go func() {
			defer core.Recover("sns unsubscribe confirmation")
			t, _ := s.getTopic(sub.TopicARN)
			n := &notification{Type: "UnsubscribeConfirmation", MessageId: uuid(), Token: core.RandHex(128), TopicArn: sub.TopicARN,
				Message:      fmt.Sprintf("You have chosen to deactivate subscription %s.\nTo cancel this operation and restore the subscription, visit the SubscribeURL included in this message.", sub.ARN),
				SubscribeURL: s.subscribeURL(sub), Timestamp: timestamp(time.Now()), SignatureVersion: sigVersion(t), SigningCertURL: s.certURL(sub)}
			s.signNotification(n)
			_ = s.post(sub, marshal(n), n.Type, n.MessageId, false)
		}()
	}
	return nil
}

// ---- publishing ----

// PublishInput is one message to publish.
type PublishInput struct {
	Topic      string // name or ARN
	Subject    string
	Message    string
	Structure  string // "json": Message is a JSON object of per-protocol messages
	Attributes map[string]Attribute
	GroupID    string
	DedupID    string
	// Strict applies the AWS API's FIFO rules; otherwise FIFO topics get a
	// default group and deduplication ID (native API, EventBridge, alarms).
	Strict bool
}

type PublishResult struct {
	MessageID      string `json:"message_id"`
	SequenceNumber string `json:"sequence_number,omitempty"`
}

var attrNameRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,256}$`)

func checkAttributes(attrs map[string]Attribute) (int, error) {
	size := 0
	for name, a := range attrs {
		lower := strings.ToLower(name)
		if !attrNameRe.MatchString(name) || strings.HasPrefix(lower, "aws.") || strings.HasPrefix(lower, "amazon.") ||
			strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".") || strings.Contains(name, "..") {
			return 0, errValue("The message attribute name '%s' is invalid.", name)
		}
		base, _, _ := strings.Cut(a.DataType, ".")
		switch {
		case a.DataType == "String.Array":
			var arr []any
			if json.Unmarshal([]byte(a.StringValue), &arr) != nil {
				return 0, errValue("The message attribute '%s' with type 'String.Array' has an invalid value.", name)
			}
		case base == "String":
			if a.StringValue == "" {
				return 0, errValue("The message attribute '%s' must contain non-empty message attribute value for message attribute type 'String'.", name)
			}
		case base == "Number":
			f, err := strconv.ParseFloat(strings.TrimSpace(a.StringValue), 64)
			if err != nil || f > 1e9 || f < -1e9 {
				return 0, errValue("Could not cast message attribute '%s' value to number.", name)
			}
		case base == "Binary":
			if len(a.BinaryValue) == 0 {
				return 0, errValue("The message attribute '%s' must contain non-empty message attribute value for message attribute type 'Binary'.", name)
			}
		default:
			return 0, errValue("The message attribute '%s' has an invalid message attribute type, the set of supported type prefixes is Binary, Number, and String.", name)
		}
		size += len(name) + len(a.DataType) + len(a.StringValue) + len(a.BinaryValue)
	}
	return size, nil
}

func validSubject(s string) bool {
	if len(s) > 100 {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r > 0x7e {
			return false
		}
	}
	return s == "" || s[0] != ' '
}

// message is a validated publish ready for delivery.
type message struct {
	topic     Topic
	id, seq   string
	subject   string
	body      string            // default message
	perProto  map[string]string // MessageStructure=json
	attrs     map[string]Attribute
	groupID   string
	dedupID   string
	timestamp time.Time
}

func (m *message) bodyFor(protocol string) string {
	if v, ok := m.perProto[protocol]; ok {
		return v
	}
	return m.body
}

// prepare validates a publish and assigns its ID (and FIFO sequence number).
// dup reports a FIFO duplicate within the deduplication window.
func (s *Service) prepare(in PublishInput) (m *message, dup bool, err error) {
	t, err := s.getTopic(in.Topic)
	if err != nil {
		return nil, false, err
	}
	if in.Message == "" {
		return nil, false, errInvalid("Empty message")
	}
	if !validSubject(in.Subject) {
		return nil, false, errInvalid("Subject")
	}
	attrSize, err := checkAttributes(in.Attributes)
	if err != nil {
		return nil, false, err
	}
	if len(in.Message)+attrSize > maxMessageBytes {
		return nil, false, errInvalid("Message too long")
	}
	m = &message{topic: t, subject: in.Subject, body: in.Message, attrs: in.Attributes, groupID: in.GroupID, dedupID: in.DedupID, timestamp: time.Now().UTC()}
	switch in.Structure {
	case "":
	case "json":
		var per map[string]any
		if json.Unmarshal([]byte(in.Message), &per) != nil {
			return nil, false, errInvalid("Message Structure - JSON message body failed to parse")
		}
		m.perProto = map[string]string{}
		for k, v := range per {
			str, ok := v.(string)
			if !ok {
				return nil, false, errInvalid("Message Structure - JSON message body values must be strings (key %s)", k)
			}
			m.perProto[k] = str
		}
		def, ok := m.perProto["default"]
		if !ok {
			return nil, false, errInvalid("Message Structure - No default entry in JSON message body")
		}
		m.body = def
	default:
		return nil, false, errInvalid("MessageStructure")
	}
	if len(in.GroupID) > 128 || len(in.DedupID) > 128 {
		return nil, false, errInvalid("MessageGroupId and MessageDeduplicationId can be at most 128 characters")
	}
	m.id = uuid()
	if !t.FIFO {
		if in.DedupID != "" && in.Strict {
			return nil, false, errInvalid("MessageDeduplicationId Reason: The request includes MessageDeduplicationId parameter that is not valid for this topic type")
		}
		return m, false, nil
	}
	if m.groupID == "" {
		if in.Strict {
			return nil, false, errInvalid("The MessageGroupId parameter is required for FIFO topics")
		}
		m.groupID = "default"
	}
	if m.dedupID == "" {
		switch {
		case t.attr("ContentBasedDeduplication") == "true":
			h := sha256.Sum256([]byte(in.Message))
			m.dedupID = hex.EncodeToString(h[:])
		case in.Strict:
			return nil, false, errInvalid("The topic should either have ContentBasedDeduplication enabled or MessageDeduplicationId provided explicitly")
		default:
			m.dedupID = m.id
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.dedup[t.ARN]
	if d == nil {
		d = map[string]dedupEntry{}
		s.dedup[t.ARN] = d
	}
	now := time.Now()
	for k, e := range d {
		if now.Sub(e.at) > dedupWindow {
			delete(d, k)
		}
	}
	if e, ok := d[m.dedupID]; ok {
		m.id, m.seq = e.id, e.seq
		return m, true, nil
	}
	if s.seq[t.ARN] == 0 {
		s.seq[t.ARN] = time.Now().UnixMicro() * 1000
	}
	s.seq[t.ARN]++
	m.seq = fmt.Sprintf("%020d", s.seq[t.ARN])
	d[m.dedupID] = dedupEntry{at: now, id: m.id, seq: m.seq}
	return m, false, nil
}

// PublishMessage validates, publishes and fans out a message.
func (s *Service) PublishMessage(in PublishInput) (PublishResult, error) {
	m, dup, err := s.prepare(in)
	if err != nil {
		return PublishResult{}, err
	}
	if !dup {
		s.fanOut(m)
	}
	return PublishResult{MessageID: m.id, SequenceNumber: m.seq}, nil
}

// Publish fans a message out to every matching subscription and returns its ID.
func (s *Service) Publish(topic, subject, message string, attrs map[string]Attribute) (string, error) {
	r, err := s.PublishMessage(PublishInput{Topic: topic, Subject: subject, Message: message, Attributes: attrs})
	return r.MessageID, err
}

func (s *Service) fanOut(m *message) {
	_, _ = store.Update(s.env.Store, cTopics, m.topic.Name, func(x *Topic) error { x.Published++; return nil })
	for _, sub := range s.subsOf(m.topic.ARN) {
		if sub.Status != statusConfirmed {
			continue
		}
		policy, _ := parsePolicy(sub.FilterPolicy, sub.scope())
		if !matchPolicy(policy, sub.scope(), m.attrs, m.bodyFor(sub.Protocol)) {
			continue
		}
		if sub.Protocol == "sqs" {
			s.deliver(sub, m) // in order, and quick: queues are in memory
		} else {
			go s.deliver(sub, m)
		}
	}
}

// notification is the JSON document SNS delivers (field order as AWS sends it).
type notification struct {
	Type              string             `json:"Type"`
	MessageId         string             `json:"MessageId"`
	SequenceNumber    string             `json:"SequenceNumber,omitempty"`
	Token             string             `json:"Token,omitempty"`
	TopicArn          string             `json:"TopicArn"`
	Subject           string             `json:"Subject,omitempty"`
	Message           string             `json:"Message"`
	SubscribeURL      string             `json:"SubscribeURL,omitempty"`
	Timestamp         string             `json:"Timestamp"`
	SignatureVersion  string             `json:"SignatureVersion"`
	Signature         string             `json:"Signature"`
	SigningCertURL    string             `json:"SigningCertURL"`
	UnsubscribeURL    string             `json:"UnsubscribeURL,omitempty"`
	MessageAttributes map[string]envAttr `json:"MessageAttributes,omitempty"`
}

type envAttr struct {
	Type  string `json:"Type"`
	Value string `json:"Value"`
}

func envAttrs(attrs map[string]Attribute) map[string]envAttr {
	if len(attrs) == 0 {
		return nil
	}
	out := map[string]envAttr{}
	for k, a := range attrs {
		v := a.StringValue
		if strings.HasPrefix(a.DataType, "Binary") {
			v = base64.StdEncoding.EncodeToString(a.BinaryValue)
		}
		out[k] = envAttr{Type: a.DataType, Value: v}
	}
	return out
}

func timestamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func sigVersion(t Topic) string {
	if v := t.attr("SignatureVersion"); v != "" {
		return v
	}
	return "1"
}

func marshal(v any) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return bytes.TrimRight(b.Bytes(), "\n")
}

func (s *Service) envelope(sub Subscription, m *message) *notification {
	n := &notification{Type: "Notification", MessageId: m.id, SequenceNumber: m.seq, TopicArn: m.topic.ARN, Subject: m.subject,
		Message: m.bodyFor(sub.Protocol), Timestamp: timestamp(m.timestamp), SignatureVersion: sigVersion(m.topic),
		SigningCertURL: s.certURL(sub), UnsubscribeURL: s.unsubscribeURL(sub), MessageAttributes: envAttrs(m.attrs)}
	s.signNotification(n)
	return n
}

// lambdaEvent is the SNS event a Lambda subscription receives.
func (s *Service) lambdaEvent(sub Subscription, m *message) []byte {
	n := s.envelope(sub, m)
	var subject any
	if n.Subject != "" {
		subject = n.Subject
	}
	attrs := n.MessageAttributes
	if attrs == nil {
		attrs = map[string]envAttr{}
	}
	rec := map[string]any{"EventSource": "aws:sns", "EventVersion": "1.0", "EventSubscriptionArn": sub.ARN,
		"Sns": map[string]any{"Type": n.Type, "MessageId": n.MessageId, "TopicArn": n.TopicArn, "Subject": subject, "Message": n.Message,
			"Timestamp": n.Timestamp, "SignatureVersion": n.SignatureVersion, "Signature": n.Signature, "SigningCertUrl": n.SigningCertURL,
			"UnsubscribeUrl": n.UnsubscribeURL, "MessageAttributes": attrs}}
	return marshal(map[string]any{"Records": []any{rec}})
}

func sqsAttrs(attrs map[string]Attribute) map[string]sqs.MessageAttribute {
	if len(attrs) == 0 {
		return nil
	}
	out := map[string]sqs.MessageAttribute{}
	for k, a := range attrs {
		out[k] = sqs.MessageAttribute{DataType: a.DataType, StringValue: a.StringValue, BinaryValue: a.BinaryValue}
	}
	return out
}

func (s *Service) deliver(sub Subscription, m *message) {
	defer core.Recover("sns delivery")
	var err error
	switch sub.Protocol {
	case "sqs":
		in := sqs.SendInput{Body: string(marshal(s.envelope(sub, m)))}
		if sub.RawMessageDelivery {
			in.Body, in.MessageAttributes = m.bodyFor("sqs"), sqsAttrs(m.attrs)
		}
		queue := sqs.NameFromARN(sub.Endpoint)
		if strings.HasSuffix(queue, ".fifo") {
			in.GroupID, in.DedupID = m.groupID, m.dedupID
			if in.GroupID == "" {
				in.GroupID = "default"
			}
			if in.DedupID == "" {
				in.DedupID = m.id
			}
		}
		_, err = s.sqs.Send(queue, in)
	case "lambda":
		if s.lambda == nil {
			err = errors.New("lambda is not available")
			break
		}
		var res *lambda.InvokeResult
		res, err = s.lambda.Invoke(context.Background(), nameFromARN(sub.Endpoint), s.lambdaEvent(sub, m))
		if err == nil && res != nil && res.FunctionError != "" {
			err = fmt.Errorf("function error: %s", string(res.Payload))
		}
	case "http", "https":
		body := marshal(s.envelope(sub, m))
		if sub.RawMessageDelivery {
			body = []byte(m.bodyFor(sub.Protocol))
		}
		for attempt := 0; attempt < 3; attempt++ {
			if attempt > 0 {
				time.Sleep(time.Duration(attempt*attempt) * time.Second)
			}
			if err = s.post(sub, body, "Notification", m.id, sub.RawMessageDelivery); err == nil {
				break
			}
		}
	case "email":
		log.Printf("sns: email to %s: subject %q: %s", sub.Endpoint, m.subject, m.bodyFor("email"))
	case "email-json":
		log.Printf("sns: email-json to %s: %s", sub.Endpoint, marshal(s.envelope(sub, m)))
	case "sms":
		log.Printf("sns: sms to %s: %s", sub.Endpoint, m.bodyFor("sms"))
	}
	if err != nil {
		s.deadLetter(sub, m, err)
	}
	s.record(sub.ARN, err)
}

// deadLetter sends a message that could not be delivered to the
// subscription's dead-letter queue.
func (s *Service) deadLetter(sub Subscription, m *message, cause error) {
	if sub.RedrivePolicy == "" {
		return
	}
	var rp struct {
		DeadLetterTargetArn string `json:"deadLetterTargetArn"`
	}
	if json.Unmarshal([]byte(sub.RedrivePolicy), &rp) != nil {
		return
	}
	queue := sqs.NameFromARN(rp.DeadLetterTargetArn)
	in := sqs.SendInput{Body: string(marshal(s.envelope(sub, m))), MessageAttributes: map[string]sqs.MessageAttribute{
		"ErrorCode":    {DataType: "String", StringValue: "DeliveryFailed"},
		"ErrorMessage": {DataType: "String", StringValue: truncate(cause.Error(), 1000)},
		"RequestID":    {DataType: "String", StringValue: m.id},
	}}
	if strings.HasSuffix(queue, ".fifo") {
		in.GroupID, in.DedupID = orDefault(m.groupID, "default"), orDefault(m.dedupID, m.id)
	}
	if _, err := s.sqs.Send(queue, in); err != nil {
		log.Printf("sns: dead-letter %s: %v", rp.DeadLetterTargetArn, err)
	}
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// record updates a subscription's delivery statistics.
func (s *Service) record(arn string, err error) {
	_, _ = store.Update(s.env.Store, cSubs, arn, func(x *Subscription) error {
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
		log.Printf("sns: deliver to %s: %v", arn, err)
	}
}

func (s *Service) post(sub Subscription, body []byte, typ, id string, raw bool) error {
	req, err := http.NewRequest(http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "text/plain; charset=UTF-8")
	req.Header.Set("User-Agent", "Amazon Simple Notification Service Agent")
	req.Header.Set("x-amz-sns-message-type", typ)
	req.Header.Set("x-amz-sns-message-id", id)
	req.Header.Set("x-amz-sns-topic-arn", sub.TopicARN)
	if typ == "Notification" {
		req.Header.Set("x-amz-sns-subscription-arn", sub.ARN)
	}
	if raw {
		req.Header.Set("x-amz-sns-rawdelivery", "true")
	}
	resp, err := s.client().Do(req)
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
		if err := s.checkURL(target); err != nil {
			log.Printf("notify %s: %v", target, err)
			return
		}
		body, _ := json.Marshal(map[string]string{"subject": subject, "message": message})
		resp, err := s.client().Post(target, "application/json", bytes.NewReader(body))
		if err != nil {
			log.Printf("notify %s: %v", target, err)
			return
		}
		resp.Body.Close()
		return
	}
	if len(subject) > 100 {
		subject = subject[:100]
	}
	if _, err := s.Publish(target, subject, message, nil); err != nil {
		log.Printf("notify %s: %v", target, err)
	}
}

// TopicExists reports whether a topic exists.
func (s *Service) TopicExists(name string) bool {
	return store.Has(s.env.Store, cTopics, nameFromARN(name))
}

// ---- native routes ----

func (s *Service) Routes(r *httpx.Router) {
	res := httpx.Res("arn:aws:sns:{region}:{account}:{name}")
	r.Handle("GET /api/v1/sns/topics", "sns:ListTopics", s.listTopics)
	r.Handle("POST /api/v1/sns/topics", "sns:CreateTopic", s.nativeCreateTopic)
	r.Handle("GET /api/v1/sns/topics/{name}", "sns:GetTopicAttributes", s.nativeGetTopic, res)
	r.Handle("PATCH /api/v1/sns/topics/{name}", "sns:SetTopicAttributes", s.updateTopic, res)
	r.Handle("DELETE /api/v1/sns/topics/{name}", "sns:DeleteTopic", s.nativeDeleteTopic, res)
	r.Handle("POST /api/v1/sns/topics/{name}/publish", "sns:Publish", s.publish, res)
	r.Handle("GET /api/v1/sns/subscriptions", "sns:ListSubscriptions", s.listSubs)
	r.Handle("POST /api/v1/sns/topics/{name}/subscriptions", "sns:Subscribe", s.nativeSubscribe, res)
	r.Handle("PATCH /api/v1/sns/subscriptions/{arn}", "sns:SetSubscriptionAttributes", s.updateSub, httpx.Deferred())
	r.Handle("DELETE /api/v1/sns/subscriptions/{arn}", "sns:Unsubscribe", s.nativeUnsubscribe, httpx.Deferred())
	// Links in delivered messages (SubscribeURL, UnsubscribeURL, SigningCertURL) work without credentials, as in AWS.
	r.Handle("GET /api/v1/sns/confirm-subscription", "sns:ConfirmSubscription", s.linkConfirm, httpx.Public())
	r.Handle("GET /api/v1/sns/unsubscribe", "sns:Unsubscribe", s.linkUnsubscribe, httpx.Public())
	r.Handle("GET /api/v1/sns/SimpleNotificationService.pem", "sns:GetSigningCertificate", s.cert, httpx.Public())
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
	n := len(s.subsOf(t.ARN))
	v := map[string]any{"name": t.Name, "arn": t.ARN, "display_name": t.DisplayName, "fifo": t.FIFO, "created_at": t.CreatedAt,
		"tags": t.Tags, "messages_published": t.Published, "subscriptions": n}
	if len(t.Attributes) > 0 {
		v["attributes"] = t.Attributes
	}
	if t.FIFO {
		v["content_based_deduplication"] = t.attr("ContentBasedDeduplication") == "true"
	}
	return v
}

func (s *Service) listTopics(c *httpx.Ctx) (any, error) {
	out := []map[string]any{}
	for _, t := range store.List[Topic](s.env.Store, cTopics) {
		out = append(out, s.topicView(t))
	}
	return out, nil
}

func (s *Service) nativeCreateTopic(c *httpx.Ctx) (any, error) {
	var in struct {
		Name                      string            `json:"name"`
		DisplayName               string            `json:"display_name"`
		ContentBasedDeduplication bool              `json:"content_based_deduplication"`
		Attributes                map[string]string `json:"attributes"`
		Tags                      core.Tags         `json:"tags"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if strings.Contains(in.Name, ":") {
		return nil, core.BadRequest("topic names are 1-256 letters, digits, hyphens or underscores")
	}
	attrs := map[string]string{}
	for k, v := range in.Attributes {
		attrs[k] = v
	}
	if in.DisplayName != "" {
		attrs["DisplayName"] = in.DisplayName
	}
	if in.ContentBasedDeduplication {
		attrs["ContentBasedDeduplication"] = "true"
	}
	if t, err := s.getTopic(in.Name); err == nil {
		return s.topicView(t), nil // CreateTopic is idempotent
	}
	t, err := s.createTopic(in.Name, attrs, in.Tags)
	if err != nil {
		return nil, err
	}
	return s.topicView(t), nil
}

func (s *Service) nativeGetTopic(c *httpx.Ctx) (any, error) {
	t, err := s.getTopic(c.Param("name"))
	if err != nil {
		return nil, core.NotFound("topic", c.Param("name"))
	}
	v := s.topicView(t)
	subs := []Subscription{}
	for _, sub := range s.subsOf(t.ARN) {
		subs = append(subs, sub.public())
	}
	v["subscription_list"] = subs
	return v, nil
}

func (s *Service) updateTopic(c *httpx.Ctx) (any, error) {
	var in struct {
		DisplayName *string           `json:"display_name"`
		Attributes  map[string]string `json:"attributes"`
		Tags        core.Tags         `json:"tags"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	t, err := store.Update(s.env.Store, cTopics, c.Param("name"), func(t *Topic) error {
		if in.DisplayName != nil {
			t.DisplayName = *in.DisplayName
		}
		for k, v := range in.Attributes {
			if err := setTopicAttribute(t, k, v, false); err != nil {
				return err
			}
		}
		if in.Tags != nil {
			t.Tags = in.Tags
		}
		return nil
	})
	if errors.Is(err, store.ErrNotFound) {
		return nil, core.NotFound("topic", c.Param("name"))
	}
	if err != nil {
		return nil, err
	}
	return s.topicView(t), nil
}

func (s *Service) nativeDeleteTopic(c *httpx.Ctx) (any, error) {
	if _, err := s.getTopic(c.Param("name")); err != nil {
		return nil, core.NotFound("topic", c.Param("name"))
	}
	return nil, s.deleteTopic(c.Param("name"))
}

func (s *Service) publish(c *httpx.Ctx) (any, error) {
	var in struct {
		Subject           string               `json:"subject"`
		Message           string               `json:"message"`
		MessageStructure  string               `json:"message_structure"`
		MessageAttributes map[string]Attribute `json:"message_attributes"`
		GroupID           string               `json:"message_group_id"`
		DedupID           string               `json:"message_deduplication_id"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	name, err := topicParam(c)
	if err != nil {
		return nil, err
	}
	r, err := s.PublishMessage(PublishInput{Topic: name, Subject: in.Subject, Message: in.Message, Structure: in.MessageStructure,
		Attributes: in.MessageAttributes, GroupID: in.GroupID, DedupID: in.DedupID})
	if err != nil {
		return nil, err
	}
	return r, nil
}

func (s *Service) listSubs(c *httpx.Ctx) (any, error) {
	out := []Subscription{}
	for _, sub := range store.List[Subscription](s.env.Store, cSubs) {
		if t := c.Query("topic"); t == "" || sub.TopicName == t {
			out = append(out, sub.public())
		}
	}
	return out, nil
}

// nativeAttrs converts native subscription fields to AWS attribute names.
func nativeAttrs(raw *bool, policy json.RawMessage, scope *string) map[string]string {
	attrs := map[string]string{}
	if raw != nil {
		attrs["RawMessageDelivery"] = strconv.FormatBool(*raw)
	}
	if scope != nil && *scope != "" {
		attrs["FilterPolicyScope"] = *scope
	}
	if policy != nil {
		p := strings.TrimSpace(string(policy))
		if p == "null" || p == "{}" {
			p = ""
		}
		attrs["FilterPolicy"] = p
	}
	return attrs
}

func (s *Service) nativeSubscribe(c *httpx.Ctx) (any, error) {
	var in struct {
		Protocol           string          `json:"protocol"`
		Endpoint           string          `json:"endpoint"`
		RawMessageDelivery bool            `json:"raw_message_delivery"`
		FilterPolicy       json.RawMessage `json:"filter_policy"`
		FilterPolicyScope  string          `json:"filter_policy_scope"`
		RedrivePolicy      string          `json:"redrive_policy"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if _, err := topicParam(c); err != nil {
		return nil, err
	}
	t, err := s.getTopic(c.Param("name"))
	if err != nil {
		return nil, core.NotFound("topic", c.Param("name"))
	}
	var raw *bool
	if in.RawMessageDelivery {
		raw = &in.RawMessageDelivery
	}
	attrs := nativeAttrs(raw, in.FilterPolicy, &in.FilterPolicyScope)
	if attrs["FilterPolicy"] == "" {
		delete(attrs, "FilterPolicy")
	}
	if in.RedrivePolicy != "" {
		attrs["RedrivePolicy"] = in.RedrivePolicy
	}
	principal := ""
	if c.P != nil {
		principal = c.P.ARN
	}
	sub, err := s.subscribe(t, subscribeInput{Protocol: in.Protocol, Endpoint: in.Endpoint, Attributes: attrs, BaseURL: BaseURL(c.R), Principal: principal}, c.Authorize)
	if err != nil {
		return nil, err
	}
	return sub.public(), nil
}

func (s *Service) updateSub(c *httpx.Ctx) (any, error) {
	var in struct {
		RawMessageDelivery *bool           `json:"raw_message_delivery"`
		FilterPolicy       json.RawMessage `json:"filter_policy"`
		FilterPolicyScope  *string         `json:"filter_policy_scope"`
	}
	if err := c.Bind(&in); err != nil {
		return nil, err
	}
	if err := s.authorizeSub(c, "sns:SetSubscriptionAttributes"); err != nil {
		return nil, err
	}
	attrs := nativeAttrs(in.RawMessageDelivery, in.FilterPolicy, in.FilterPolicyScope)
	sub, err := store.Update(s.env.Store, cSubs, c.Param("arn"), func(x *Subscription) error {
		if v, ok := attrs["FilterPolicyScope"]; ok {
			x.FilterPolicyScope = v
		}
		for _, k := range []string{"RawMessageDelivery", "FilterPolicy", "FilterPolicyScope"} {
			if v, ok := attrs[k]; ok {
				if err := s.setSubAttribute(x, k, v); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if errors.Is(err, store.ErrNotFound) {
		return nil, core.NotFound("subscription", c.Param("arn"))
	}
	if err != nil {
		return nil, err
	}
	return sub.public(), nil
}

// authorizeSub checks action against the subscription's topic.
func (s *Service) authorizeSub(c *httpx.Ctx, action string) error {
	sub, err := store.Get[Subscription](s.env.Store, cSubs, c.Param("arn"))
	if err != nil {
		return core.NotFound("subscription", c.Param("arn"))
	}
	return c.Authorize(action, sub.TopicARN)
}

func (s *Service) nativeUnsubscribe(c *httpx.Ctx) (any, error) {
	if err := s.authorizeSub(c, "sns:Unsubscribe"); err != nil {
		return nil, err
	}
	sub, err := store.Get[Subscription](s.env.Store, cSubs, c.Param("arn"))
	if err != nil {
		return nil, core.NotFound("subscription", c.Param("arn"))
	}
	return nil, s.unsubscribe(sub)
}

func writeXML(c *httpx.Ctx, status int, body string) (any, error) {
	c.W.Header().Set("Content-Type", "text/xml")
	c.W.WriteHeader(status)
	_, _ = c.W.Write([]byte(body))
	c.MarkWritten()
	return nil, nil
}

// linkConfirm serves SubscribeURL links.
func (s *Service) linkConfirm(c *httpx.Ctx) (any, error) {
	sub, err := s.confirm(c.Query("TopicArn"), c.Query("Token"), false, false)
	if err != nil {
		return writeXML(c, http.StatusBadRequest, `<?xml version="1.0"?><ErrorResponse xmlns="`+xmlns+`"><Error><Type>Sender</Type><Code>InvalidParameter</Code><Message>Invalid token</Message></Error><RequestId>`+uuid()+`</RequestId></ErrorResponse>`)
	}
	return writeXML(c, http.StatusOK, `<?xml version="1.0"?><ConfirmSubscriptionResponse xmlns="`+xmlns+`"><ConfirmSubscriptionResult><SubscriptionArn>`+
		sub.ARN+`</SubscriptionArn></ConfirmSubscriptionResult><ResponseMetadata><RequestId>`+uuid()+`</RequestId></ResponseMetadata></ConfirmSubscriptionResponse>`)
}

// linkUnsubscribe serves UnsubscribeURL links.
func (s *Service) linkUnsubscribe(c *httpx.Ctx) (any, error) {
	sub, err := store.Get[Subscription](s.env.Store, cSubs, c.Query("SubscriptionArn"))
	if err != nil || sub.AuthOnUnsubscribe {
		return writeXML(c, http.StatusBadRequest, `<?xml version="1.0"?><ErrorResponse xmlns="`+xmlns+`"><Error><Type>Sender</Type><Code>InvalidParameter</Code><Message>Invalid parameter: SubscriptionArn</Message></Error><RequestId>`+uuid()+`</RequestId></ErrorResponse>`)
	}
	_ = s.unsubscribe(sub)
	return writeXML(c, http.StatusOK, `<?xml version="1.0"?><UnsubscribeResponse xmlns="`+xmlns+`"><ResponseMetadata><RequestId>`+uuid()+`</RequestId></ResponseMetadata></UnsubscribeResponse>`)
}

// cert serves the certificate that signs SNS messages (SigningCertURL).
func (s *Service) cert(c *httpx.Ctx) (any, error) {
	sg := s.signingKey()
	if sg == nil {
		return nil, core.Errf(http.StatusServiceUnavailable, "ServiceUnavailable", "signing certificate unavailable")
	}
	c.W.Header().Set("Content-Type", "application/x-pem-file")
	_, _ = c.W.Write(sg.certPEM)
	c.MarkWritten()
	return nil, nil
}

// SetHTTP replaces the delivery client and URL check while the service is running.
func (s *Service) SetHTTP(c *http.Client, check func(string) error) {
	s.cfgMu.Lock()
	s.HTTP, s.CheckURL = c, check
	s.cfgMu.Unlock()
}

func (s *Service) client() *http.Client {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.HTTP
}

func (s *Service) checkURL(u string) error {
	s.cfgMu.RLock()
	f := s.CheckURL
	s.cfgMu.RUnlock()
	return f(u)
}
