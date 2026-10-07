package cloudwatch

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

// CloudWatch Logs vended log delivery: delivery sources (a resource that emits
// logs, such as an EventBridge bus), delivery destinations (a log group, bucket
// or stream) and the deliveries linking them. They are records: HomeCloud keeps
// and reports them so infrastructure tools can manage them (the
// terraform-aws-modules EventBridge module creates a delivery source for every
// bus), but AWS services do not vend logs through them.

const (
	cDeliverySources = "logs_delivery_sources"
	cDeliveryDests   = "logs_delivery_destinations"
	cDeliveries      = "logs_deliveries"
)

type deliverySource struct {
	Name         string    `json:"name"`
	ARN          string    `json:"arn"`
	ResourceARNs []string  `json:"resource_arns"`
	Service      string    `json:"service"`
	LogType      string    `json:"log_type"`
	Tags         core.Tags `json:"tags,omitempty"`
}

type deliveryDest struct {
	Name         string    `json:"name"`
	ARN          string    `json:"arn"`
	Type         string    `json:"type"`
	OutputFormat string    `json:"output_format,omitempty"`
	Destination  string    `json:"destination_resource_arn"`
	Policy       string    `json:"policy,omitempty"`
	Tags         core.Tags `json:"tags,omitempty"`
}

type delivery struct {
	ID             string          `json:"id"`
	ARN            string          `json:"arn"`
	SourceName     string          `json:"source_name"`
	DestinationARN string          `json:"destination_arn"`
	DestType       string          `json:"destination_type"`
	RecordFields   []string        `json:"record_fields,omitempty"`
	FieldDelimiter string          `json:"field_delimiter,omitempty"`
	S3Config       json.RawMessage `json:"s3_delivery_configuration,omitempty"`
	Tags           core.Tags       `json:"tags,omitempty"`
}

func deliveryNotFound(kind, name string) error {
	return logsErr("ResourceNotFoundException", "%s %s does not exist.", kind, name)
}

func (s *Service) sourceOut(d deliverySource) map[string]any {
	return map[string]any{"name": d.Name, "arn": d.ARN, "resourceArns": d.ResourceARNs, "service": d.Service, "logType": d.LogType, "tags": tagsOrEmpty(d.Tags)}
}

func (s *Service) destOut(d deliveryDest) map[string]any {
	m := map[string]any{"name": d.Name, "arn": d.ARN, "deliveryDestinationType": d.Type,
		"deliveryDestinationConfiguration": map[string]string{"destinationResourceArn": d.Destination}, "tags": tagsOrEmpty(d.Tags)}
	if d.OutputFormat != "" {
		m["outputFormat"] = d.OutputFormat
	}
	return m
}

func (s *Service) deliveryOut(d delivery) map[string]any {
	m := map[string]any{"id": d.ID, "arn": d.ARN, "deliverySourceName": d.SourceName, "deliveryDestinationArn": d.DestinationARN,
		"deliveryDestinationType": d.DestType, "tags": tagsOrEmpty(d.Tags)}
	if len(d.RecordFields) > 0 {
		m["recordFields"] = d.RecordFields
	}
	if d.FieldDelimiter != "" {
		m["fieldDelimiter"] = d.FieldDelimiter
	}
	if len(d.S3Config) > 0 {
		m["s3DeliveryConfiguration"] = d.S3Config
	}
	return m
}

func tagsOrEmpty(t core.Tags) core.Tags {
	if t == nil {
		return core.Tags{}
	}
	return t
}

// destType derives a destination's type from its resource ARN.
func destType(arn string) (string, bool) {
	switch p := strings.SplitN(arn, ":", 6); {
	case len(p) < 6:
		return "", false
	case p[2] == "logs":
		return "CWL", true
	case p[2] == "s3":
		return "S3", true
	case p[2] == "firehose":
		return "FH", true
	case p[2] == "xray":
		return "XRAY", true
	}
	return "", false
}

func (s *Service) awsPutDeliverySource(q *awsapi.Req) (any, error) {
	var in struct {
		Name        string            `json:"name"`
		ResourceArn string            `json:"resourceArn"`
		LogType     string            `json:"logType"`
		Tags        map[string]string `json:"tags"`
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	arn := s.env.ARN("logs", "delivery-source:"+in.Name)
	if err := q.Authorize("logs:PutDeliverySource", arn); err != nil {
		return nil, err
	}
	p := strings.SplitN(in.ResourceArn, ":", 6)
	if in.Name == "" || len(p) < 6 || in.LogType == "" {
		return nil, logsErr("ValidationException", "name, resourceArn (an ARN) and logType are required")
	}
	if err := checkTags(in.Tags); err != nil {
		return nil, err
	}
	d, err := store.Get[deliverySource](s.env.Store, cDeliverySources, in.Name)
	if err == nil && (!slices.Contains(d.ResourceARNs, in.ResourceArn) || d.LogType != in.LogType) {
		return nil, logsErr("ConflictException", "Delivery source %s already exists for another resource or log type.", in.Name)
	}
	d = deliverySource{Name: in.Name, ARN: arn, ResourceARNs: []string{in.ResourceArn}, Service: p[2], LogType: in.LogType, Tags: d.Tags}
	if len(in.Tags) > 0 {
		d.Tags = in.Tags
	}
	if err := store.Put(s.env.Store, cDeliverySources, d.Name, d); err != nil {
		return nil, err
	}
	return map[string]any{"deliverySource": s.sourceOut(d)}, nil
}

func (s *Service) awsGetDeliverySource(q *awsapi.Req) (any, error) {
	var in struct {
		Name string `json:"name"`
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("logs:GetDeliverySource", s.env.ARN("logs", "delivery-source:"+in.Name)); err != nil {
		return nil, err
	}
	d, err := store.Get[deliverySource](s.env.Store, cDeliverySources, in.Name)
	if err != nil {
		return nil, deliveryNotFound("Delivery source", in.Name)
	}
	return map[string]any{"deliverySource": s.sourceOut(d)}, nil
}

func (s *Service) awsDescribeDeliverySources(q *awsapi.Req) (any, error) {
	if err := q.Authorize("logs:DescribeDeliverySources", "*"); err != nil {
		return nil, err
	}
	all := store.List[deliverySource](s.env.Store, cDeliverySources)
	slices.SortFunc(all, func(a, b deliverySource) int { return strings.Compare(a.Name, b.Name) })
	out := []map[string]any{}
	for _, d := range all {
		out = append(out, s.sourceOut(d))
	}
	return map[string]any{"deliverySources": out}, nil
}

func (s *Service) awsDeleteDeliverySource(q *awsapi.Req) (any, error) {
	var in struct {
		Name string `json:"name"`
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("logs:DeleteDeliverySource", s.env.ARN("logs", "delivery-source:"+in.Name)); err != nil {
		return nil, err
	}
	if !store.Has(s.env.Store, cDeliverySources, in.Name) {
		return nil, deliveryNotFound("Delivery source", in.Name)
	}
	for _, d := range store.List[delivery](s.env.Store, cDeliveries) {
		if d.SourceName == in.Name {
			return nil, logsErr("ConflictException", "Delivery source %s is used by delivery %s.", in.Name, d.ID)
		}
	}
	return nil, store.Delete(s.env.Store, cDeliverySources, in.Name)
}

func (s *Service) awsPutDeliveryDestination(q *awsapi.Req) (any, error) {
	var in struct {
		Name          string `json:"name"`
		OutputFormat  string `json:"outputFormat"`
		Configuration struct {
			DestinationResourceArn string `json:"destinationResourceArn"`
		} `json:"deliveryDestinationConfiguration"`
		DeliveryDestinationType string            `json:"deliveryDestinationType"`
		Tags                    map[string]string `json:"tags"`
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	arn := s.env.ARN("logs", "delivery-destination:"+in.Name)
	if err := q.Authorize("logs:PutDeliveryDestination", arn); err != nil {
		return nil, err
	}
	typ, ok := destType(in.Configuration.DestinationResourceArn)
	if in.DeliveryDestinationType == "XRAY" {
		typ, ok = "XRAY", true
	}
	if in.Name == "" || !ok {
		return nil, logsErr("ValidationException", "name and a log group, bucket or Firehose stream ARN are required")
	}
	if err := checkTags(in.Tags); err != nil {
		return nil, err
	}
	d, _ := store.Get[deliveryDest](s.env.Store, cDeliveryDests, in.Name)
	d.Name, d.ARN, d.Type, d.OutputFormat, d.Destination = in.Name, arn, typ, in.OutputFormat, in.Configuration.DestinationResourceArn
	if len(in.Tags) > 0 {
		d.Tags = in.Tags
	}
	if err := store.Put(s.env.Store, cDeliveryDests, d.Name, d); err != nil {
		return nil, err
	}
	return map[string]any{"deliveryDestination": s.destOut(d)}, nil
}

func (s *Service) awsGetDeliveryDestination(q *awsapi.Req) (any, error) {
	var in struct {
		Name string `json:"name"`
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("logs:GetDeliveryDestination", s.env.ARN("logs", "delivery-destination:"+in.Name)); err != nil {
		return nil, err
	}
	d, err := store.Get[deliveryDest](s.env.Store, cDeliveryDests, in.Name)
	if err != nil {
		return nil, deliveryNotFound("Delivery destination", in.Name)
	}
	return map[string]any{"deliveryDestination": s.destOut(d)}, nil
}

func (s *Service) awsDescribeDeliveryDestinations(q *awsapi.Req) (any, error) {
	if err := q.Authorize("logs:DescribeDeliveryDestinations", "*"); err != nil {
		return nil, err
	}
	all := store.List[deliveryDest](s.env.Store, cDeliveryDests)
	slices.SortFunc(all, func(a, b deliveryDest) int { return strings.Compare(a.Name, b.Name) })
	out := []map[string]any{}
	for _, d := range all {
		out = append(out, s.destOut(d))
	}
	return map[string]any{"deliveryDestinations": out}, nil
}

func (s *Service) awsDeleteDeliveryDestination(q *awsapi.Req) (any, error) {
	var in struct {
		Name string `json:"name"`
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	arn := s.env.ARN("logs", "delivery-destination:"+in.Name)
	if err := q.Authorize("logs:DeleteDeliveryDestination", arn); err != nil {
		return nil, err
	}
	if !store.Has(s.env.Store, cDeliveryDests, in.Name) {
		return nil, deliveryNotFound("Delivery destination", in.Name)
	}
	for _, d := range store.List[delivery](s.env.Store, cDeliveries) {
		if d.DestinationARN == arn {
			return nil, logsErr("ConflictException", "Delivery destination %s is used by delivery %s.", in.Name, d.ID)
		}
	}
	return nil, store.Delete(s.env.Store, cDeliveryDests, in.Name)
}

func (s *Service) awsCreateDelivery(q *awsapi.Req) (any, error) {
	var in struct {
		Source         string            `json:"deliverySourceName"`
		DestinationArn string            `json:"deliveryDestinationArn"`
		RecordFields   []string          `json:"recordFields"`
		FieldDelimiter string            `json:"fieldDelimiter"`
		S3Config       json.RawMessage   `json:"s3DeliveryConfiguration"`
		Tags           map[string]string `json:"tags"`
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("logs:CreateDelivery", s.env.ARN("logs", "delivery:*")); err != nil {
		return nil, err
	}
	if !store.Has(s.env.Store, cDeliverySources, in.Source) {
		return nil, deliveryNotFound("Delivery source", in.Source)
	}
	var dest *deliveryDest
	for _, d := range store.List[deliveryDest](s.env.Store, cDeliveryDests) {
		if d.ARN == in.DestinationArn {
			dest = &d
		}
	}
	if dest == nil {
		return nil, deliveryNotFound("Delivery destination", in.DestinationArn)
	}
	for _, d := range store.List[delivery](s.env.Store, cDeliveries) {
		if d.SourceName == in.Source && d.DestinationARN == in.DestinationArn {
			return nil, logsErr("ConflictException", "A delivery from %s to %s already exists.", in.Source, in.DestinationArn)
		}
	}
	if err := checkTags(in.Tags); err != nil {
		return nil, err
	}
	id := strings.ToUpper(core.RandHex(8))
	d := delivery{ID: id, ARN: s.env.ARN("logs", "delivery:"+id), SourceName: in.Source, DestinationARN: in.DestinationArn, DestType: dest.Type,
		RecordFields: in.RecordFields, FieldDelimiter: in.FieldDelimiter, Tags: in.Tags}
	if len(in.S3Config) > 0 && string(in.S3Config) != "null" {
		d.S3Config = in.S3Config
	}
	if err := store.Put(s.env.Store, cDeliveries, d.ID, d); err != nil {
		return nil, err
	}
	return map[string]any{"delivery": s.deliveryOut(d)}, nil
}

func (s *Service) awsGetDelivery(q *awsapi.Req) (any, error) {
	var in struct {
		ID string `json:"id"`
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("logs:GetDelivery", s.env.ARN("logs", "delivery:"+in.ID)); err != nil {
		return nil, err
	}
	d, err := store.Get[delivery](s.env.Store, cDeliveries, in.ID)
	if err != nil {
		return nil, deliveryNotFound("Delivery", in.ID)
	}
	return map[string]any{"delivery": s.deliveryOut(d)}, nil
}

func (s *Service) awsDescribeDeliveries(q *awsapi.Req) (any, error) {
	if err := q.Authorize("logs:DescribeDeliveries", "*"); err != nil {
		return nil, err
	}
	all := store.List[delivery](s.env.Store, cDeliveries)
	slices.SortFunc(all, func(a, b delivery) int { return strings.Compare(a.ID, b.ID) })
	out := []map[string]any{}
	for _, d := range all {
		out = append(out, s.deliveryOut(d))
	}
	return map[string]any{"deliveries": out}, nil
}

func (s *Service) awsDeleteDelivery(q *awsapi.Req) (any, error) {
	var in struct {
		ID string `json:"id"`
	}
	if err := q.Bind(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("logs:DeleteDelivery", s.env.ARN("logs", "delivery:"+in.ID)); err != nil {
		return nil, err
	}
	if !store.Has(s.env.Store, cDeliveries, in.ID) {
		return nil, deliveryNotFound("Delivery", in.ID)
	}
	return nil, store.Delete(s.env.Store, cDeliveries, in.ID)
}

// deliveryTags reads and changes the tags of a delivery source, destination or
// delivery by ARN; ok is false for other ARNs (log groups).
func (s *Service) deliveryTags(q *awsapi.Req, action, arn string, set map[string]string, unset []string) (tags core.Tags, ok bool, err error) {
	var coll, key string
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) < 6 || parts[2] != "logs" {
		return nil, false, nil
	}
	switch res := parts[5]; {
	case strings.HasPrefix(res, "delivery-source:"):
		coll, key = cDeliverySources, strings.TrimPrefix(res, "delivery-source:")
	case strings.HasPrefix(res, "delivery-destination:"):
		coll, key = cDeliveryDests, strings.TrimPrefix(res, "delivery-destination:")
	case strings.HasPrefix(res, "delivery:"):
		coll, key = cDeliveries, strings.TrimPrefix(res, "delivery:")
	default:
		return nil, false, nil
	}
	if err := q.Authorize(action, arn); err != nil {
		return nil, true, err
	}
	if err := checkTags(set); err != nil {
		return nil, true, err
	}
	update := func(t *core.Tags) {
		if *t == nil {
			*t = core.Tags{}
		}
		for k, v := range set {
			(*t)[k] = v
		}
		for _, k := range unset {
			delete(*t, k)
		}
		tags = *t
	}
	switch coll {
	case cDeliverySources:
		_, err = store.Update(s.env.Store, coll, key, func(x *deliverySource) error { update(&x.Tags); return nil })
	case cDeliveryDests:
		_, err = store.Update(s.env.Store, coll, key, func(x *deliveryDest) error { update(&x.Tags); return nil })
	default:
		_, err = store.Update(s.env.Store, coll, key, func(x *delivery) error { update(&x.Tags); return nil })
	}
	if err != nil {
		return nil, true, awsapi.Errorf(http.StatusBadRequest, "ResourceNotFoundException", "The specified resource %s does not exist.", arn)
	}
	return tags, true, nil
}
