package elb

import (
	"errors"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/store"
)

// The AWS Elastic Load Balancing v2 API (awsQuery, 2015-12-01): application
// load balancers, target groups, listeners and rules.

const xmlns = "http://elasticloadbalancing.amazonaws.com/doc/2015-12-01/"

type awsOp func(q *awsapi.Req) (any, error)

// RegisterAWS serves ELBv2 over the AWS protocol.
func (s *Service) RegisterAWS() {
	ops := map[string]awsOp{
		"CreateLoadBalancer":             s.awsCreateLB,
		"DescribeLoadBalancers":          s.awsDescribeLBs,
		"DeleteLoadBalancer":             s.awsDeleteLB,
		"DescribeLoadBalancerAttributes": s.awsDescribeLBAttrs,
		"ModifyLoadBalancerAttributes":   s.awsModifyLBAttrs,
		"SetSecurityGroups":              s.awsSetSecurityGroups,
		"SetSubnets":                     s.awsSetSubnets,
		"SetIpAddressType":               s.awsSetIPType,
		"CreateTargetGroup":              s.awsCreateTG,
		"DescribeTargetGroups":           s.awsDescribeTGs,
		"ModifyTargetGroup":              s.awsModifyTG,
		"DeleteTargetGroup":              s.awsDeleteTG,
		"DescribeTargetGroupAttributes":  s.awsDescribeTGAttrs,
		"ModifyTargetGroupAttributes":    s.awsModifyTGAttrs,
		"RegisterTargets":                s.awsRegister,
		"DeregisterTargets":              s.awsDeregister,
		"DescribeTargetHealth":           s.awsTargetHealth,
		"CreateListener":                 s.awsCreateListener,
		"DescribeListeners":              s.awsDescribeListeners,
		"ModifyListener":                 s.awsModifyListener,
		"DeleteListener":                 s.awsDeleteListener,
		"DescribeListenerAttributes":     s.awsDescribeListenerAttrs,
		"ModifyListenerAttributes":       s.awsModifyListenerAttrs,
		"DescribeListenerCertificates":   s.awsDescribeListenerCerts,
		"AddListenerCertificates":        s.awsAddListenerCerts,
		"RemoveListenerCertificates":     s.awsRemoveListenerCerts,
		"CreateRule":                     s.awsCreateRule,
		"DescribeRules":                  s.awsDescribeRules,
		"ModifyRule":                     s.awsModifyRule,
		"DeleteRule":                     s.awsDeleteRule,
		"SetRulePriorities":              s.awsSetRulePriorities,
		"AddTags":                        s.awsAddTags,
		"RemoveTags":                     s.awsRemoveTags,
		"DescribeTags":                   s.awsDescribeTags,
		"DescribeSSLPolicies":            s.awsDescribeSSLPolicies,
		"DescribeAccountLimits":          s.awsDescribeAccountLimits,
	}
	svc := &awsapi.Service{Name: "elasticloadbalancing", XMLNS: xmlns, Ops: map[string]awsapi.Op{}}
	for name, fn := range ops {
		svc.Ops[name] = func(q *awsapi.Req) (any, error) {
			out, err := fn(q)
			if err != nil {
				return nil, elbError(err)
			}
			if out == nil {
				return map[string]any{}, nil // an empty <XResult/>, as ELB does
			}
			return out, nil
		}
	}
	awsapi.Register(svc)
}

func apiErr(code, format string, a ...any) error {
	return awsapi.Errorf(http.StatusBadRequest, code, format, a...)
}

// elbError maps HomeCloud errors to ELBv2 error codes.
func elbError(err error) error {
	var ae *awsapi.Error
	if errors.As(err, &ae) {
		return ae
	}
	var ce *core.Error
	if !errors.As(err, &ce) {
		return err
	}
	code, msg := ce.Code, ce.Message
	switch code {
	case "ResourceNotFound":
		switch {
		case strings.HasPrefix(msg, "load balancer "):
			code = "LoadBalancerNotFound"
		case strings.HasPrefix(msg, "target group "):
			code = "TargetGroupNotFound"
		case strings.HasPrefix(msg, "listener "):
			code = "ListenerNotFound"
		case strings.HasPrefix(msg, "rule "):
			code = "RuleNotFound"
		default:
			code = "InvalidTarget"
		}
	case "Conflict", "ResourceConflict", "AlreadyExists":
		switch {
		case strings.HasPrefix(msg, "load balancer "):
			code = "DuplicateLoadBalancerName"
		case strings.HasPrefix(msg, "target group "):
			code = "DuplicateTargetGroupName"
		case strings.HasPrefix(msg, "a listener already"):
			code = "DuplicateListener"
		default:
			code = "InvalidConfigurationRequest"
		}
	case "ValidationError", "BadRequest":
		code = "ValidationError"
		if strings.Contains(msg, "is not a known instance") {
			code = "InvalidTarget"
		}
	case "AccessDenied":
		return &awsapi.Error{Status: http.StatusForbidden, Code: "AccessDenied", Message: msg}
	}
	status := ce.Status
	if status < 500 {
		status = http.StatusBadRequest
	}
	return &awsapi.Error{Status: status, Code: code, Message: msg}
}

type awsTag struct{ Key, Value string }

func tagsOf(ts []awsTag) core.Tags {
	if len(ts) == 0 {
		return nil
	}
	m := core.Tags{}
	for _, t := range ts {
		m[t.Key] = t.Value
	}
	return m
}

func tagList(m core.Tags) []awsTag {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := []awsTag{}
	for _, k := range keys {
		out = append(out, awsTag{k, m[k]})
	}
	return out
}

type kv struct{ Key, Value string }

func attrList(defaults, over map[string]string) []kv {
	m := map[string]string{}
	for k, v := range defaults {
		m[k] = v
	}
	for k, v := range over {
		m[k] = v
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]kv, 0, len(keys))
	for _, k := range keys {
		out = append(out, kv{k, m[k]})
	}
	return out
}

// mergeAttrs applies requested attributes. Names HomeCloud does not know are
// stored and reported as given, so newer clients keep working.
func mergeAttrs(defaults, cur map[string]string, in []kv) (map[string]string, error) {
	out := map[string]string{}
	for k, v := range cur {
		out[k] = v
	}
	for _, a := range in {
		if a.Key == "" {
			return nil, apiErr("ValidationError", "Attribute key must not be empty")
		}
		out[a.Key] = a.Value
	}
	return out, nil
}

var lbDefaults = map[string]string{
	"access_logs.s3.enabled": "false", "access_logs.s3.bucket": "", "access_logs.s3.prefix": "",
	"connection_logs.s3.enabled": "false", "connection_logs.s3.bucket": "", "connection_logs.s3.prefix": "",
	"deletion_protection.enabled": "false", "idle_timeout.timeout_seconds": "60", "client_keep_alive.seconds": "3600",
	"ipv6.deny_all_igw_traffic": "false", "load_balancing.cross_zone.enabled": "true",
	"routing.http.desync_mitigation_mode": "defensive", "routing.http.drop_invalid_header_fields.enabled": "false",
	"routing.http.preserve_host_header.enabled": "false", "routing.http.x_amzn_tls_version_and_cipher_suite.enabled": "false",
	"routing.http.xff_client_port.enabled": "false", "routing.http.xff_header_processing.mode": "append",
	"routing.http2.enabled": "true", "waf.fail_open.enabled": "false", "zonal_shift.config.enabled": "false",
	"health_check_logs.s3.enabled": "false", "health_check_logs.s3.bucket": "", "health_check_logs.s3.prefix": "",
}

var tgDefaults = map[string]string{
	"deregistration_delay.timeout_seconds": "300", "stickiness.enabled": "false", "stickiness.type": "lb_cookie",
	"stickiness.lb_cookie.duration_seconds": "86400", "stickiness.app_cookie.cookie_name": "",
	"stickiness.app_cookie.duration_seconds": "86400", "load_balancing.algorithm.type": "round_robin",
	"load_balancing.algorithm.anomaly_mitigation": "off", "load_balancing.cross_zone.enabled": "use_load_balancer_configuration",
	"slow_start.duration_seconds": "0", "proxy_protocol_v2.enabled": "false",
	"target_group_health.dns_failover.minimum_healthy_targets.count":                 "off",
	"target_group_health.dns_failover.minimum_healthy_targets.percentage":            "off",
	"target_group_health.unhealthy_state_routing.minimum_healthy_targets.count":      "1",
	"target_group_health.unhealthy_state_routing.minimum_healthy_targets.percentage": "off",
	"target_health_state.unhealthy.connection_termination.enabled":                   "true",
	"target_health_state.unhealthy.draining_interval_seconds":                        "0",
	"lambda.multi_value_headers.enabled":                                             "false",
}

var listenerDefaults = map[string]string{
	"routing.http.response.server.enabled": "true", "routing.http.response.strict_transport_security.header_value": "",
	"routing.http.response.access_control_allow_origin.header_value": "",
	"routing.http.response.x_content_type_options.header_value":      "",
	"routing.http.request.x_amzn_mtls_clientcert.header_name":        "",
}

// ---- lookups ----

func (s *Service) lbByARN(arn string) (LoadBalancer, error) {
	p, ok := arnParts(arn, "loadbalancer/app/")
	if !ok || p[0] == "" {
		return LoadBalancer{}, apiErr("ValidationError", "'%s' is not a valid load balancer ARN", arn)
	}
	lb, err := store.Get[LoadBalancer](s.env.Store, cLBs, p[0])
	if err != nil {
		return lb, core.NotFound("load balancer", arn)
	}
	return lb, nil
}

func (s *Service) tgByARN(arn string) (TargetGroup, error) {
	name, ok := TargetGroupName(arn)
	if !ok {
		return TargetGroup{}, apiErr("ValidationError", "'%s' is not a valid target group ARN", arn)
	}
	tg, err := store.Get[TargetGroup](s.env.Store, cTGs, name)
	if err != nil {
		return tg, core.NotFound("target group", arn)
	}
	return tg, nil
}

func (s *Service) listenerByARN(arn string) (LoadBalancer, Listener, error) {
	p, ok := arnParts(arn, "listener/app/")
	if !ok || len(p) < 3 {
		return LoadBalancer{}, Listener{}, apiErr("ValidationError", "'%s' is not a valid listener ARN", arn)
	}
	lb, err := store.Get[LoadBalancer](s.env.Store, cLBs, p[0])
	if err != nil {
		return lb, Listener{}, core.NotFound("listener", arn)
	}
	for _, l := range lb.Listeners {
		if l.ID == p[2] {
			return lb, l, nil
		}
	}
	return lb, Listener{}, core.NotFound("listener", arn)
}

func (s *Service) ruleByARN(arn string) (LoadBalancer, Listener, Rule, error) {
	p, ok := arnParts(arn, "listener-rule/app/")
	if !ok || len(p) < 4 {
		return LoadBalancer{}, Listener{}, Rule{}, apiErr("ValidationError", "'%s' is not a valid rule ARN", arn)
	}
	lb, err := store.Get[LoadBalancer](s.env.Store, cLBs, p[0])
	if err != nil {
		return lb, Listener{}, Rule{}, core.NotFound("rule", arn)
	}
	for _, l := range lb.Listeners {
		if l.ID == p[2] {
			for _, r := range l.Rules {
				if r.ID == p[3] {
					return lb, l, r, nil
				}
			}
		}
	}
	return lb, Listener{}, Rule{}, core.NotFound("rule", arn)
}

// ---- load balancers ----

type awsAZ struct {
	ZoneName string
	SubnetId string
}

type awsState struct {
	Code   string
	Reason string `json:",omitempty"`
}

type awsLB struct {
	LoadBalancerArn       string
	DNSName               string
	CanonicalHostedZoneId string
	CreatedTime           any
	LoadBalancerName      string
	Scheme                string
	VpcId                 string
	State                 awsState
	Type                  string
	AvailabilityZones     []awsAZ
	SecurityGroups        []string
	IpAddressType         string
}

func (s *Service) lbXML(lb LoadBalancer) awsLB {
	subnets := lb.Subnets
	if len(subnets) == 0 || subnets[0] == "" {
		subnets = []string{lb.SubnetID}
	}
	azs := []awsAZ{}
	for _, id := range subnets {
		zone := ""
		if sn, err := s.vpc.GetSubnet(id); err == nil {
			zone = sn.AvailabilityZone
		}
		azs = append(azs, awsAZ{ZoneName: zone, SubnetId: id})
	}
	lb = s.lbView(lb)
	state := map[string]string{"provisioning": "provisioning", "active": "active", "failed": "failed"}[lb.State]
	if state == "" {
		state = "active"
	}
	dns := lb.DNSName
	if lb.Scheme == "internal" {
		dns = "internal-" + dns
	}
	_ = dns
	return awsLB{LoadBalancerArn: s.LoadBalancerARN(lb.Name), DNSName: lb.DNSName, CanonicalHostedZoneId: "Z35SXDOTRQ7X7K",
		CreatedTime: lb.CreatedAt, LoadBalancerName: lb.Name, Scheme: lb.Scheme, VpcId: lb.VpcID,
		State: awsState{Code: state, Reason: lb.StateReason}, Type: "application", AvailabilityZones: azs,
		SecurityGroups: append([]string{}, lb.SecurityGroups...), IpAddressType: "ipv4"}
}

func (s *Service) awsCreateLB(q *awsapi.Req) (any, error) {
	var in struct {
		Name           string
		Subnets        []string
		SecurityGroups []string
		Scheme, Type   string
		IpAddressType  string
		SubnetMappings []struct{ SubnetId string }
		Tags           []awsTag
	}
	if err := q.Decode(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("elasticloadbalancing:CreateLoadBalancer", s.LoadBalancerARN(in.Name)); err != nil {
		return nil, err
	}
	if len(in.Tags) > 0 {
		if err := q.Check("elasticloadbalancing:AddTags", s.LoadBalancerARN(in.Name)); err != nil {
			return nil, err
		}
	}
	if in.Type != "" && in.Type != "application" {
		return nil, apiErr("ValidationError", "HomeCloud supports application load balancers only")
	}
	for _, m := range in.SubnetMappings {
		in.Subnets = append(in.Subnets, m.SubnetId)
	}
	if len(in.Subnets) == 0 {
		return nil, apiErr("ValidationError", "At least one subnet must be specified")
	}
	for _, g := range in.SecurityGroups {
		if _, err := s.vpc.GetSecurityGroup(g); err != nil {
			return nil, apiErr("InvalidConfigurationRequest", "The security group '%s' does not exist", g)
		}
	}
	lb, err := s.createLBIn(lbInput{Name: in.Name, Scheme: in.Scheme, Subnets: in.Subnets, SecurityGroups: in.SecurityGroups, Tags: tagsOf(in.Tags)})
	if err != nil {
		return nil, err
	}
	return map[string]any{"LoadBalancers": []awsLB{s.lbXML(lb)}}, nil
}

func (s *Service) awsDescribeLBs(q *awsapi.Req) (any, error) {
	var in struct {
		LoadBalancerArns []string
		Names            []string
	}
	if err := q.Decode(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("elasticloadbalancing:DescribeLoadBalancers", "*"); err != nil {
		return nil, err
	}
	for _, a := range in.LoadBalancerArns {
		if _, err := s.lbByARN(a); err != nil {
			return nil, err
		}
	}
	for _, n := range in.Names {
		if !store.Has(s.env.Store, cLBs, n) {
			return nil, core.NotFound("load balancer", n)
		}
	}
	all := store.List[LoadBalancer](s.env.Store, cLBs)
	slices.SortFunc(all, func(a, b LoadBalancer) int { return strings.Compare(a.Name, b.Name) })
	out := []awsLB{}
	for _, lb := range all {
		if len(in.Names) > 0 && !slices.Contains(in.Names, lb.Name) {
			continue
		}
		if len(in.LoadBalancerArns) > 0 && !slices.Contains(in.LoadBalancerArns, s.LoadBalancerARN(lb.Name)) {
			continue
		}
		out = append(out, s.lbXML(lb))
	}
	return map[string]any{"LoadBalancers": out}, nil
}

func (s *Service) awsDeleteLB(q *awsapi.Req) (any, error) {
	arn := q.Param("LoadBalancerArn")
	if err := q.Authorize("elasticloadbalancing:DeleteLoadBalancer", arn); err != nil {
		return nil, err
	}
	lb, err := s.lbByARN(arn)
	if err != nil {
		return nil, err
	}
	if lb.Attributes["deletion_protection.enabled"] == "true" {
		return nil, apiErr("OperationNotPermitted", "Load balancer '%s' cannot be deleted because deletion protection is enabled", arn)
	}
	return nil, s.deleteLBIn(lb.Name)
}

func (s *Service) awsDescribeLBAttrs(q *awsapi.Req) (any, error) {
	arn := q.Param("LoadBalancerArn")
	if err := q.Authorize("elasticloadbalancing:DescribeLoadBalancerAttributes", arn); err != nil {
		return nil, err
	}
	lb, err := s.lbByARN(arn)
	if err != nil {
		return nil, err
	}
	return map[string]any{"Attributes": attrList(lbDefaults, lb.Attributes)}, nil
}

func (s *Service) awsModifyLBAttrs(q *awsapi.Req) (any, error) {
	var in struct {
		LoadBalancerArn string
		Attributes      []kv
	}
	if err := q.Decode(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("elasticloadbalancing:ModifyLoadBalancerAttributes", in.LoadBalancerArn); err != nil {
		return nil, err
	}
	lb, err := s.lbByARN(in.LoadBalancerArn)
	if err != nil {
		return nil, err
	}
	attrs, err := mergeAttrs(lbDefaults, lb.Attributes, in.Attributes)
	if err != nil {
		return nil, err
	}
	lb, err = store.Update(s.env.Store, cLBs, lb.Name, func(x *LoadBalancer) error { x.Attributes = attrs; return nil })
	if err != nil {
		return nil, err
	}
	return map[string]any{"Attributes": attrList(lbDefaults, lb.Attributes)}, nil
}

func (s *Service) awsSetSecurityGroups(q *awsapi.Req) (any, error) {
	var in struct {
		LoadBalancerArn string
		SecurityGroups  []string
	}
	if err := q.Decode(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("elasticloadbalancing:SetSecurityGroups", in.LoadBalancerArn); err != nil {
		return nil, err
	}
	lb, err := s.lbByARN(in.LoadBalancerArn)
	if err != nil {
		return nil, err
	}
	for _, g := range in.SecurityGroups {
		if _, err := s.vpc.GetSecurityGroup(g); err != nil {
			return nil, apiErr("InvalidConfigurationRequest", "The security group '%s' does not exist", g)
		}
	}
	if _, err := store.Update(s.env.Store, cLBs, lb.Name, func(x *LoadBalancer) error { x.SecurityGroups = in.SecurityGroups; return nil }); err != nil {
		return nil, err
	}
	s.vpc.FirewallChanged()
	return map[string]any{"SecurityGroupIds": append([]string{}, in.SecurityGroups...)}, nil
}

func (s *Service) awsSetSubnets(q *awsapi.Req) (any, error) {
	var in struct {
		LoadBalancerArn string
		Subnets         []string
		SubnetMappings  []struct{ SubnetId string }
	}
	if err := q.Decode(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("elasticloadbalancing:SetSubnets", in.LoadBalancerArn); err != nil {
		return nil, err
	}
	lb, err := s.lbByARN(in.LoadBalancerArn)
	if err != nil {
		return nil, err
	}
	for _, m := range in.SubnetMappings {
		in.Subnets = append(in.Subnets, m.SubnetId)
	}
	for _, id := range in.Subnets {
		v, err := s.vpc.SubnetVPC(id)
		if err != nil || v != lb.VpcID {
			return nil, apiErr("InvalidSubnet", "The subnet '%s' is not in the load balancer's VPC", id)
		}
	}
	lb, err = store.Update(s.env.Store, cLBs, lb.Name, func(x *LoadBalancer) error { x.Subnets = in.Subnets; return nil })
	if err != nil {
		return nil, err
	}
	return map[string]any{"AvailabilityZones": s.lbXML(lb).AvailabilityZones, "IpAddressType": "ipv4"}, nil
}

func (s *Service) awsSetIPType(q *awsapi.Req) (any, error) {
	arn := q.Param("LoadBalancerArn")
	if err := q.Authorize("elasticloadbalancing:SetIpAddressType", arn); err != nil {
		return nil, err
	}
	if _, err := s.lbByARN(arn); err != nil {
		return nil, err
	}
	if t := q.Param("IpAddressType"); t != "ipv4" {
		return nil, apiErr("ValidationError", "HomeCloud load balancers support ipv4 only")
	}
	return map[string]any{"IpAddressType": "ipv4"}, nil
}

// ---- target groups ----

type awsMatcher struct {
	HttpCode string `json:",omitempty"`
	GrpcCode string `json:",omitempty"`
}

type awsTG struct {
	TargetGroupArn             string
	TargetGroupName            string
	Protocol                   string `json:",omitempty"`
	Port                       int    `json:",omitempty"`
	VpcId                      string `json:",omitempty"`
	HealthCheckProtocol        string
	HealthCheckPort            string
	HealthCheckEnabled         bool
	HealthCheckIntervalSeconds int
	HealthCheckTimeoutSeconds  int
	HealthyThresholdCount      int
	UnhealthyThresholdCount    int
	HealthCheckPath            string `json:",omitempty"`
	Matcher                    awsMatcher
	LoadBalancerArns           []string
	TargetType                 string
	ProtocolVersion            string `json:",omitempty"`
	IpAddressType              string
}

func (s *Service) tgXML(tg TargetGroup) awsTG {
	h := tg.HealthCheck
	hc := func(v, def string) string {
		if v == "" {
			return def
		}
		return v
	}
	arns := []string{}
	for _, n := range s.usedBy(tg.Name) {
		arns = append(arns, s.LoadBalancerARN(n))
	}
	x := awsTG{TargetGroupArn: s.TargetGroupARN(tg.Name), TargetGroupName: tg.Name, Protocol: tg.Protocol, Port: tg.Port, VpcId: tg.VpcID,
		HealthCheckProtocol: hc(h.Protocol, tg.Protocol), HealthCheckPort: hc(h.Port, "traffic-port"), HealthCheckEnabled: !h.Disabled,
		HealthCheckIntervalSeconds: h.IntervalSeconds, HealthCheckTimeoutSeconds: h.TimeoutSeconds, HealthyThresholdCount: h.HealthyThreshold,
		UnhealthyThresholdCount: h.UnhealthyThreshold, HealthCheckPath: h.Path, Matcher: awsMatcher{HttpCode: hc(h.Matcher, "200")},
		LoadBalancerArns: arns, TargetType: hc(tg.TargetType, "instance"), IpAddressType: "ipv4"}
	if x.HealthCheckTimeoutSeconds == 0 {
		x.HealthCheckTimeoutSeconds = 5
	}
	if tg.TargetType == "lambda" {
		x.Protocol, x.VpcId, x.HealthCheckProtocol, x.HealthCheckPort = "", "", "", ""
	}
	x.ProtocolVersion = hc(tg.ProtocolVersion, "HTTP1")
	return x
}

// HCInput is the health check part of Create/ModifyTargetGroup.
type HCInput struct {
	HealthCheckProtocol        string
	HealthCheckPort            string
	HealthCheckEnabled         *bool
	HealthCheckPath            string
	HealthCheckIntervalSeconds int
	HealthCheckTimeoutSeconds  int
	HealthyThresholdCount      int
	UnhealthyThresholdCount    int
	Matcher                    *awsMatcher
}

func (in HCInput) apply(h *HealthCheck) {
	if in.HealthCheckProtocol != "" {
		h.Protocol = strings.ToUpper(in.HealthCheckProtocol)
	}
	if in.HealthCheckPort != "" {
		h.Port = in.HealthCheckPort
	}
	if in.HealthCheckEnabled != nil {
		h.Disabled = !*in.HealthCheckEnabled
	}
	if in.HealthCheckPath != "" {
		h.Path = in.HealthCheckPath
	}
	if in.HealthCheckIntervalSeconds > 0 {
		h.IntervalSeconds = in.HealthCheckIntervalSeconds
	}
	if in.HealthCheckTimeoutSeconds > 0 {
		h.TimeoutSeconds = in.HealthCheckTimeoutSeconds
	}
	if in.HealthyThresholdCount > 0 {
		h.HealthyThreshold = in.HealthyThresholdCount
	}
	if in.UnhealthyThresholdCount > 0 {
		h.UnhealthyThreshold = in.UnhealthyThresholdCount
	}
	if in.Matcher != nil && in.Matcher.HttpCode != "" {
		h.Matcher = in.Matcher.HttpCode
	}
}

func (s *Service) awsCreateTG(q *awsapi.Req) (any, error) {
	var in struct {
		HCInput
		Name, Protocol, ProtocolVersion, VpcId, TargetType string
		Port                                               int
		Tags                                               []awsTag
	}
	if err := q.Decode(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("elasticloadbalancing:CreateTargetGroup", s.TargetGroupARN(in.Name)); err != nil {
		return nil, err
	}
	if len(in.Tags) > 0 {
		if err := q.Check("elasticloadbalancing:AddTags", s.TargetGroupARN(in.Name)); err != nil {
			return nil, err
		}
	}
	switch strings.ToUpper(in.Protocol) {
	case "HTTP", "HTTPS", "":
	default:
		return nil, apiErr("ValidationError", "target group protocol %s is not supported (HTTP or HTTPS)", in.Protocol)
	}
	switch in.TargetType {
	case "", "instance", "ip":
	default:
		return nil, apiErr("ValidationError", "target type %s is not supported (instance or ip)", in.TargetType)
	}
	if in.Port == 0 {
		return nil, apiErr("ValidationError", "A port must be specified")
	}
	// AWS defaults for health checks.
	hc := HealthCheck{Path: "/", IntervalSeconds: 30, TimeoutSeconds: 5, HealthyThreshold: 5, UnhealthyThreshold: 2, Matcher: "200"}
	in.apply(&hc)
	tg, err := s.createTGIn(TargetGroup{Name: in.Name, Protocol: strings.ToUpper(in.Protocol), Port: in.Port, VpcID: in.VpcId, HealthCheck: hc,
		TargetType: in.TargetType, ProtocolVersion: in.ProtocolVersion, Tags: tagsOf(in.Tags)})
	if err != nil {
		return nil, err
	}
	return map[string]any{"TargetGroups": []awsTG{s.tgXML(tg)}}, nil
}

func (s *Service) awsDescribeTGs(q *awsapi.Req) (any, error) {
	var in struct {
		LoadBalancerArn string
		TargetGroupArns []string
		Names           []string
	}
	if err := q.Decode(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("elasticloadbalancing:DescribeTargetGroups", "*"); err != nil {
		return nil, err
	}
	for _, a := range in.TargetGroupArns {
		if _, err := s.tgByARN(a); err != nil {
			return nil, err
		}
	}
	for _, n := range in.Names {
		if !store.Has(s.env.Store, cTGs, n) {
			return nil, core.NotFound("target group", n)
		}
	}
	var lbUsers []string
	if in.LoadBalancerArn != "" {
		lb, err := s.lbByARN(in.LoadBalancerArn)
		if err != nil {
			return nil, err
		}
		for _, l := range lb.Listeners {
			lbUsers = append(lbUsers, l.DefaultTargetGroup)
			for _, r := range l.Rules {
				lbUsers = append(lbUsers, r.TargetGroup)
			}
		}
	}
	all := store.List[TargetGroup](s.env.Store, cTGs)
	slices.SortFunc(all, func(a, b TargetGroup) int { return strings.Compare(a.Name, b.Name) })
	out := []awsTG{}
	for _, tg := range all {
		if (len(in.Names) > 0 && !slices.Contains(in.Names, tg.Name)) ||
			(len(in.TargetGroupArns) > 0 && !slices.Contains(in.TargetGroupArns, s.TargetGroupARN(tg.Name))) ||
			(in.LoadBalancerArn != "" && !slices.Contains(lbUsers, tg.Name)) {
			continue
		}
		out = append(out, s.tgXML(tg))
	}
	return map[string]any{"TargetGroups": out}, nil
}

func (s *Service) awsModifyTG(q *awsapi.Req) (any, error) {
	var in struct {
		HCInput
		TargetGroupArn string
	}
	if err := q.Decode(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("elasticloadbalancing:ModifyTargetGroup", in.TargetGroupArn); err != nil {
		return nil, err
	}
	tg, err := s.tgByARN(in.TargetGroupArn)
	if err != nil {
		return nil, err
	}
	tg, err = store.Update(s.env.Store, cTGs, tg.Name, func(t *TargetGroup) error {
		in.apply(&t.HealthCheck)
		return normalizeHC(&t.HealthCheck)
	})
	if err != nil {
		return nil, err
	}
	s.pushUsers(tg.Name) // the health check path may have changed
	return map[string]any{"TargetGroups": []awsTG{s.tgXML(tg)}}, nil
}

func (s *Service) awsDeleteTG(q *awsapi.Req) (any, error) {
	arn := q.Param("TargetGroupArn")
	if err := q.Authorize("elasticloadbalancing:DeleteTargetGroup", arn); err != nil {
		return nil, err
	}
	tg, err := s.tgByARN(arn)
	if err != nil {
		return nil, err
	}
	return nil, s.deleteTGIn(tg.Name)
}

func (s *Service) awsDescribeTGAttrs(q *awsapi.Req) (any, error) {
	arn := q.Param("TargetGroupArn")
	if err := q.Authorize("elasticloadbalancing:DescribeTargetGroupAttributes", arn); err != nil {
		return nil, err
	}
	tg, err := s.tgByARN(arn)
	if err != nil {
		return nil, err
	}
	return map[string]any{"Attributes": attrList(tgDefaults, tg.Attributes)}, nil
}

func (s *Service) awsModifyTGAttrs(q *awsapi.Req) (any, error) {
	var in struct {
		TargetGroupArn string
		Attributes     []kv
	}
	if err := q.Decode(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("elasticloadbalancing:ModifyTargetGroupAttributes", in.TargetGroupArn); err != nil {
		return nil, err
	}
	tg, err := s.tgByARN(in.TargetGroupArn)
	if err != nil {
		return nil, err
	}
	attrs, err := mergeAttrs(tgDefaults, tg.Attributes, in.Attributes)
	if err != nil {
		return nil, err
	}
	tg, err = store.Update(s.env.Store, cTGs, tg.Name, func(x *TargetGroup) error { x.Attributes = attrs; return nil })
	if err != nil {
		return nil, err
	}
	return map[string]any{"Attributes": attrList(tgDefaults, tg.Attributes)}, nil
}

type awsTarget struct {
	Id               string
	Port             int    `json:",omitempty"`
	AvailabilityZone string `json:",omitempty"`
}

func (s *Service) awsRegister(q *awsapi.Req) (any, error) {
	var in struct {
		TargetGroupArn string
		Targets        []awsTarget
	}
	if err := q.Decode(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("elasticloadbalancing:RegisterTargets", in.TargetGroupArn); err != nil {
		return nil, err
	}
	tg, err := s.tgByARN(in.TargetGroupArn)
	if err != nil {
		return nil, err
	}
	var ts []Target
	for _, t := range in.Targets {
		ts = append(ts, Target{ID: t.Id, Port: t.Port})
	}
	if _, err := s.registerIn(tg.Name, ts); err != nil {
		return nil, err
	}
	return nil, nil
}

func (s *Service) awsDeregister(q *awsapi.Req) (any, error) {
	var in struct {
		TargetGroupArn string
		Targets        []awsTarget
	}
	if err := q.Decode(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("elasticloadbalancing:DeregisterTargets", in.TargetGroupArn); err != nil {
		return nil, err
	}
	tg, err := s.tgByARN(in.TargetGroupArn)
	if err != nil {
		return nil, err
	}
	for _, t := range in.Targets {
		port := t.Port
		if port == 0 {
			port = tg.Port
		}
		if _, err := s.deregisterIn(tg.Name, t.Id, port); err != nil {
			var ce *core.Error
			if errors.As(err, &ce) && strings.HasPrefix(ce.Message, "target \"") {
				continue // deregistering an unregistered target succeeds
			}
			return nil, err
		}
	}
	return nil, nil
}

func (s *Service) awsTargetHealth(q *awsapi.Req) (any, error) {
	var in struct {
		TargetGroupArn string
		Targets        []awsTarget
	}
	if err := q.Decode(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("elasticloadbalancing:DescribeTargetHealth", in.TargetGroupArn); err != nil {
		return nil, err
	}
	tg, err := s.tgByARN(in.TargetGroupArn)
	if err != nil {
		return nil, err
	}
	type health struct {
		State       string
		Reason      string `json:",omitempty"`
		Description string `json:",omitempty"`
	}
	type desc struct {
		Target          awsTarget
		HealthCheckPort string
		TargetHealth    health
	}
	out := []desc{}
	for _, t := range s.tgView(tg).Targets {
		if len(in.Targets) > 0 && !slices.ContainsFunc(in.Targets, func(x awsTarget) bool { return x.Id == t.ID && (x.Port == 0 || x.Port == t.Port) }) {
			continue
		}
		h := health{State: t.Health, Description: t.Reason}
		switch t.Health {
		case "unused":
			h.Reason = "Target.NotInUse"
		case "initial":
			h.Reason = "Elb.InitialHealthChecking"
		case "unhealthy":
			h.Reason = "Target.FailedHealthChecks"
		case "unavailable":
			h.State, h.Reason = "unhealthy", "Target.NotRegistered"
		}
		out = append(out, desc{Target: awsTarget{Id: t.ID, Port: t.Port}, HealthCheckPort: strconv.Itoa(t.Port), TargetHealth: h})
	}
	return map[string]any{"TargetHealthDescriptions": out}, nil
}

// ---- actions and conditions ----

// normalizeActions validates actions and derives the native routing target:
// the forward target group (highest weight), a redirect or a fixed response.
func (s *Service) normalizeActions(in []Action) (acts []Action, tg string, red *RedirectConfig, fx *FixedResponseConfig, err error) {
	if len(in) == 0 {
		return nil, "", nil, nil, apiErr("ValidationError", "Actions cannot be empty")
	}
	acts = slices.Clone(in)
	sort.SliceStable(acts, func(i, j int) bool {
		return acts[i].Order != 0 && (acts[j].Order == 0 || acts[i].Order < acts[j].Order)
	})
	routed := false
	for i := range acts {
		a := &acts[i]
		a.Order = i + 1
		switch a.Type {
		case "forward":
			if routed {
				continue
			}
			var arn string
			switch {
			case a.ForwardConfig != nil && len(a.ForwardConfig.TargetGroups) > 0:
				best := a.ForwardConfig.TargetGroups[0]
				for _, t := range a.ForwardConfig.TargetGroups {
					if t.Weight > best.Weight {
						best = t
					}
				}
				arn = best.TargetGroupArn
				if len(a.ForwardConfig.TargetGroups) == 1 && a.TargetGroupArn == "" {
					a.TargetGroupArn = arn
				}
			case a.TargetGroupArn != "":
				arn = a.TargetGroupArn
				a.ForwardConfig = &ForwardConfig{TargetGroups: []TargetGroupTuple{{TargetGroupArn: arn, Weight: 1}},
					TargetGroupStickinessConfig: &StickinessConfig{Enabled: false}}
			default:
				return nil, "", nil, nil, apiErr("ValidationError", "A forward action requires a target group")
			}
			t, err := s.tgByARN(arn)
			if err != nil {
				return nil, "", nil, nil, err
			}
			tg, routed = t.Name, true
		case "redirect":
			if routed {
				continue
			}
			c := a.RedirectConfig
			if c == nil || (c.StatusCode != "HTTP_301" && c.StatusCode != "HTTP_302") {
				return nil, "", nil, nil, apiErr("ValidationError", "A redirect action requires RedirectConfig with StatusCode HTTP_301 or HTTP_302")
			}
			for _, v := range []string{c.Protocol, c.Port, c.Host, c.Path, c.Query} {
				if !redirectRe.MatchString(v) {
					return nil, "", nil, nil, apiErr("ValidationError", "RedirectConfig contains unsupported characters")
				}
			}
			if c.Protocol != "" && c.Protocol != "HTTP" && c.Protocol != "HTTPS" && c.Protocol != "#{protocol}" {
				return nil, "", nil, nil, apiErr("ValidationError", "RedirectConfig Protocol must be HTTP, HTTPS or #{protocol}")
			}
			def := func(p *string, v string) {
				if *p == "" {
					*p = v
				}
			}
			def(&c.Protocol, "#{protocol}")
			def(&c.Port, "#{port}")
			def(&c.Host, "#{host}")
			def(&c.Path, "/#{path}")
			def(&c.Query, "#{query}")
			cp := *c
			red, routed = &cp, true
		case "fixed-response":
			if routed {
				continue
			}
			c := a.FixedResponseConfig
			if c == nil || len(c.StatusCode) != 3 {
				return nil, "", nil, nil, apiErr("ValidationError", "A fixed-response action requires FixedResponseConfig with a StatusCode")
			}
			if n, err := strconv.Atoi(c.StatusCode); err != nil || n < 200 || n > 599 {
				return nil, "", nil, nil, apiErr("ValidationError", "FixedResponseConfig StatusCode must be 2XX, 4XX or 5XX")
			}
			if c.ContentType != "" && !ctypeRe.MatchString(c.ContentType) {
				return nil, "", nil, nil, apiErr("ValidationError", "FixedResponseConfig ContentType is not valid")
			}
			cp := *c
			fx, routed = &cp, true
		case "authenticate-oidc", "authenticate-cognito":
			return nil, "", nil, nil, apiErr("ValidationError", "%s actions are not supported", a.Type)
		default:
			return nil, "", nil, nil, apiErr("ValidationError", "Action type '%s' is not valid", a.Type)
		}
	}
	return acts, tg, red, fx, nil
}

// normalizeConditions validates rule conditions and derives host and path lists.
func normalizeConditions(in []Condition) (conds []Condition, hosts, paths []string, err error) {
	if len(in) == 0 {
		return nil, nil, nil, apiErr("ValidationError", "Conditions cannot be empty")
	}
	for _, c := range in {
		vals := c.Values
		var cfg *ValuesConfig
		switch c.Field {
		case "path-pattern":
			cfg = c.PathPatternConfig
		case "host-header":
			cfg = c.HostHeaderConfig
		default:
			return nil, nil, nil, apiErr("ValidationError", "Condition field '%s' is not supported by HomeCloud (path-pattern and host-header are)", c.Field)
		}
		if cfg != nil {
			vals = cfg.Values
		}
		if len(vals) == 0 {
			return nil, nil, nil, apiErr("ValidationError", "Condition %s needs at least one value", c.Field)
		}
		for _, v := range vals {
			if c.Field == "path-pattern" {
				if !pathRe.MatchString(v) {
					return nil, nil, nil, apiErr("ValidationError", "path pattern '%s' is not supported: it must start with / and use letters, digits and . _ ~ %% / * -", v)
				}
				paths = append(paths, v)
			} else {
				if !hostRe.MatchString(v) {
					return nil, nil, nil, apiErr("ValidationError", "host pattern '%s' is not supported", v)
				}
				hosts = append(hosts, v)
			}
		}
		n := Condition{Field: c.Field, Values: vals}
		if c.Field == "path-pattern" {
			n.PathPatternConfig = &ValuesConfig{Values: vals}
		} else {
			n.HostHeaderConfig = &ValuesConfig{Values: vals}
		}
		conds = append(conds, n)
	}
	return conds, hosts, paths, nil
}

// ---- listeners ----

type awsCert struct{ CertificateArn string }

type awsListener struct {
	ListenerArn     string
	LoadBalancerArn string
	Port            int
	Protocol        string
	Certificates    []awsCert `json:",omitempty"`
	SslPolicy       string    `json:",omitempty"`
	DefaultActions  []Action
}

func (s *Service) listenerXML(lb LoadBalancer, l Listener) awsListener {
	x := awsListener{ListenerArn: s.listenerARN(lb.Name, l.ID), LoadBalancerArn: s.LoadBalancerARN(lb.Name), Port: l.Port, Protocol: l.Protocol}
	acts := l.Actions
	if len(acts) == 0 { // created through the native API
		switch {
		case l.DefaultTargetGroup != "":
			acts = []Action{{Type: "forward", Order: 1, TargetGroupArn: s.TargetGroupARN(l.DefaultTargetGroup)}}
		case l.RedirectHTTPSPort > 0:
			acts = []Action{{Type: "redirect", Order: 1, RedirectConfig: &RedirectConfig{Protocol: "HTTPS", Port: strconv.Itoa(l.RedirectHTTPSPort),
				Host: "#{host}", Path: "/#{path}", Query: "#{query}", StatusCode: "HTTP_301"}}}
		}
	}
	x.DefaultActions = append([]Action{}, acts...)
	if l.Protocol == "HTTPS" {
		x.Certificates = []awsCert{{l.CertificateARN}}
		x.SslPolicy = l.SSLPolicy
		if x.SslPolicy == "" {
			x.SslPolicy = "ELBSecurityPolicy-2016-08"
		}
	}
	return x
}

type ListenerIn struct {
	Protocol       string
	Port           int
	SslPolicy      string
	Certificates   []awsCert
	DefaultActions []Action
	AlpnPolicy     []string
}

// setActions stores actions on a listener and derives its native routing.
func (s *Service) setListenerActions(l *Listener, in []Action) error {
	acts, tg, red, fx, err := s.normalizeActions(in)
	if err != nil {
		return err
	}
	l.Actions, l.DefaultTargetGroup, l.Redirect, l.Fixed, l.RedirectHTTPSPort = acts, tg, red, fx, 0
	return nil
}

func (s *Service) awsCreateListener(q *awsapi.Req) (any, error) {
	var in struct {
		ListenerIn
		LoadBalancerArn string
		Tags            []awsTag
	}
	if err := q.Decode(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("elasticloadbalancing:CreateListener", in.LoadBalancerArn); err != nil {
		return nil, err
	}
	if len(in.Tags) > 0 {
		if err := q.Check("elasticloadbalancing:AddTags", in.LoadBalancerArn); err != nil {
			return nil, err
		}
	}
	lb, err := s.lbByARN(in.LoadBalancerArn)
	if err != nil {
		return nil, err
	}
	l := Listener{Port: in.Port, Protocol: in.Protocol, SSLPolicy: in.SslPolicy, Tags: tagsOf(in.Tags)}
	if len(in.Certificates) > 0 {
		l.CertificateARN = in.Certificates[0].CertificateArn
		for _, c := range in.Certificates[1:] {
			l.ExtraCerts = append(l.ExtraCerts, c.CertificateArn)
		}
	}
	if err := s.setListenerActions(&l, in.DefaultActions); err != nil {
		return nil, err
	}
	if _, err := s.changeListeners(lb.Name, true, func(x *LoadBalancer) error {
		if err := s.checkListener(&l, x.VpcID, x.Listeners); err != nil {
			return err
		}
		x.Listeners = append(x.Listeners, l)
		return checkRedirects(x.Listeners)
	}); err != nil {
		return nil, err
	}
	return map[string]any{"Listeners": []awsListener{s.listenerXML(lb, l)}}, nil
}

func (s *Service) awsDescribeListeners(q *awsapi.Req) (any, error) {
	var in struct {
		LoadBalancerArn string
		ListenerArns    []string
	}
	if err := q.Decode(&in); err != nil {
		return nil, err
	}
	res := in.LoadBalancerArn
	if res == "" {
		res = "*"
	}
	if err := q.Authorize("elasticloadbalancing:DescribeListeners", res); err != nil {
		return nil, err
	}
	out := []awsListener{}
	switch {
	case len(in.ListenerArns) > 0:
		for _, a := range in.ListenerArns {
			lb, l, err := s.listenerByARN(a)
			if err != nil {
				return nil, err
			}
			out = append(out, s.listenerXML(lb, l))
		}
	case in.LoadBalancerArn != "":
		lb, err := s.lbByARN(in.LoadBalancerArn)
		if err != nil {
			return nil, err
		}
		for _, l := range lb.Listeners {
			out = append(out, s.listenerXML(lb, l))
		}
	default:
		return nil, apiErr("ValidationError", "You must specify either listener ARNs or a load balancer ARN")
	}
	return map[string]any{"Listeners": out}, nil
}

func (s *Service) awsModifyListener(q *awsapi.Req) (any, error) {
	var in struct {
		ListenerIn
		ListenerArn string
	}
	if err := q.Decode(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("elasticloadbalancing:ModifyListener", in.ListenerArn); err != nil {
		return nil, err
	}
	lb, cur, err := s.listenerByARN(in.ListenerArn)
	if err != nil {
		return nil, err
	}
	recreate := in.Port != 0 && in.Port != cur.Port
	if err := s.updateListener(lb.Name, cur.ID, recreate, func(l *Listener, x *LoadBalancer) error {
		n := *l
		if in.Port != 0 {
			n.Port = in.Port
		}
		if in.Protocol != "" {
			n.Protocol = in.Protocol
		}
		if in.SslPolicy != "" {
			n.SSLPolicy = in.SslPolicy
		}
		if len(in.Certificates) > 0 {
			n.CertificateARN = in.Certificates[0].CertificateArn
		}
		if len(in.DefaultActions) > 0 {
			if err := s.setListenerActions(&n, in.DefaultActions); err != nil {
				return err
			}
		}
		var others []Listener
		for _, o := range x.Listeners {
			if o.ID != l.ID {
				others = append(others, o)
			}
		}
		id, rules := l.ID, l.Rules
		if err := s.checkListener(&n, x.VpcID, others); err != nil {
			return err
		}
		n.ID, n.Rules = id, rules
		*l = n
		return nil
	}); err != nil {
		return nil, err
	}
	lb, l, err := s.listenerByARN(in.ListenerArn)
	if err != nil {
		return nil, err
	}
	return map[string]any{"Listeners": []awsListener{s.listenerXML(lb, l)}}, nil
}

func (s *Service) awsDeleteListener(q *awsapi.Req) (any, error) {
	arn := q.Param("ListenerArn")
	if err := q.Authorize("elasticloadbalancing:DeleteListener", arn); err != nil {
		return nil, err
	}
	lb, l, err := s.listenerByARN(arn)
	if err != nil {
		return nil, err
	}
	_, err = s.removeListener(lb.Name, l.ID, true)
	return nil, err
}

func (s *Service) awsDescribeListenerAttrs(q *awsapi.Req) (any, error) {
	arn := q.Param("ListenerArn")
	if err := q.Authorize("elasticloadbalancing:DescribeListenerAttributes", arn); err != nil {
		return nil, err
	}
	_, l, err := s.listenerByARN(arn)
	if err != nil {
		return nil, err
	}
	return map[string]any{"Attributes": attrList(listenerDefaults, l.Attributes)}, nil
}

func (s *Service) awsModifyListenerAttrs(q *awsapi.Req) (any, error) {
	var in struct {
		ListenerArn string
		Attributes  []kv
	}
	if err := q.Decode(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("elasticloadbalancing:ModifyListenerAttributes", in.ListenerArn); err != nil {
		return nil, err
	}
	lb, l, err := s.listenerByARN(in.ListenerArn)
	if err != nil {
		return nil, err
	}
	attrs, err := mergeAttrs(listenerDefaults, l.Attributes, in.Attributes)
	if err != nil {
		return nil, err
	}
	if err := s.updateListener(lb.Name, l.ID, false, func(x *Listener, _ *LoadBalancer) error { x.Attributes = attrs; return nil }); err != nil {
		return nil, err
	}
	return map[string]any{"Attributes": attrList(listenerDefaults, attrs)}, nil
}

func (s *Service) awsDescribeListenerCerts(q *awsapi.Req) (any, error) {
	arn := q.Param("ListenerArn")
	if err := q.Authorize("elasticloadbalancing:DescribeListenerCertificates", arn); err != nil {
		return nil, err
	}
	_, l, err := s.listenerByARN(arn)
	if err != nil {
		return nil, err
	}
	type cert struct {
		CertificateArn string
		IsDefault      bool
	}
	out := []cert{}
	if l.CertificateARN != "" {
		out = append(out, cert{l.CertificateARN, true})
	}
	for _, c := range l.ExtraCerts {
		out = append(out, cert{c, false})
	}
	return map[string]any{"Certificates": out}, nil
}

func (s *Service) awsAddListenerCerts(q *awsapi.Req) (any, error) {
	var in struct {
		ListenerArn  string
		Certificates []awsCert
	}
	if err := q.Decode(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("elasticloadbalancing:AddListenerCertificates", in.ListenerArn); err != nil {
		return nil, err
	}
	lb, l, err := s.listenerByARN(in.ListenerArn)
	if err != nil {
		return nil, err
	}
	for _, c := range in.Certificates {
		if s.Certs == nil || !s.Certs.Exists(c.CertificateArn) {
			return nil, apiErr("CertificateNotFound", "Certificate '%s' not found", c.CertificateArn)
		}
	}
	err = s.updateListener(lb.Name, l.ID, false, func(x *Listener, _ *LoadBalancer) error {
		for _, c := range in.Certificates {
			if c.CertificateArn != x.CertificateARN && !slices.Contains(x.ExtraCerts, c.CertificateArn) {
				x.ExtraCerts = append(x.ExtraCerts, c.CertificateArn)
			}
		}
		return nil
	})
	return map[string]any{"Certificates": in.Certificates}, err
}

func (s *Service) awsRemoveListenerCerts(q *awsapi.Req) (any, error) {
	var in struct {
		ListenerArn  string
		Certificates []awsCert
	}
	if err := q.Decode(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("elasticloadbalancing:RemoveListenerCertificates", in.ListenerArn); err != nil {
		return nil, err
	}
	lb, l, err := s.listenerByARN(in.ListenerArn)
	if err != nil {
		return nil, err
	}
	return nil, s.updateListener(lb.Name, l.ID, false, func(x *Listener, _ *LoadBalancer) error {
		for _, c := range in.Certificates {
			if c.CertificateArn == x.CertificateARN {
				return apiErr("OperationNotPermitted", "The default certificate cannot be removed")
			}
			x.ExtraCerts = slices.DeleteFunc(x.ExtraCerts, func(a string) bool { return a == c.CertificateArn })
		}
		return nil
	})
}

// ---- rules ----

type awsRule struct {
	RuleArn    string
	Priority   string
	Conditions []Condition
	Actions    []Action
	IsDefault  bool
}

func (s *Service) ruleXML(lb LoadBalancer, l Listener, r Rule) awsRule {
	x := awsRule{RuleArn: s.ruleARN(lb.Name, l.ID, r.ID), Priority: strconv.Itoa(r.Priority), Conditions: r.Conditions, Actions: r.Actions}
	if len(x.Conditions) == 0 { // created through the native API
		x.Conditions = []Condition{}
		if r.PathPrefix != "" {
			v := []string{r.PathPrefix}
			x.Conditions = append(x.Conditions, Condition{Field: "path-pattern", Values: v, PathPatternConfig: &ValuesConfig{v}})
		}
		if r.HostHeader != "" {
			v := []string{r.HostHeader}
			x.Conditions = append(x.Conditions, Condition{Field: "host-header", Values: v, HostHeaderConfig: &ValuesConfig{v}})
		}
	}
	if len(x.Actions) == 0 {
		x.Actions = []Action{{Type: "forward", Order: 1, TargetGroupArn: s.TargetGroupARN(r.TargetGroup)}}
	}
	return x
}

func (s *Service) defaultRuleXML(lb LoadBalancer, l Listener) awsRule {
	return awsRule{RuleArn: s.ruleARN(lb.Name, l.ID, "default"), Priority: "default", Conditions: []Condition{},
		Actions: s.listenerXML(lb, l).DefaultActions, IsDefault: true}
}

func (s *Service) awsCreateRule(q *awsapi.Req) (any, error) {
	var in struct {
		ListenerArn string
		Conditions  []Condition
		Priority    int
		Actions     []Action
		Tags        []awsTag
	}
	if err := q.Decode(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("elasticloadbalancing:CreateRule", in.ListenerArn); err != nil {
		return nil, err
	}
	lb, l, err := s.listenerByARN(in.ListenerArn)
	if err != nil {
		return nil, err
	}
	if in.Priority < 1 || in.Priority > 50000 {
		return nil, apiErr("ValidationError", "Priority must be between 1 and 50000")
	}
	conds, hosts, paths, err := normalizeConditions(in.Conditions)
	if err != nil {
		return nil, err
	}
	acts, tg, red, fx, err := s.normalizeActions(in.Actions)
	if err != nil {
		return nil, err
	}
	r := Rule{ID: core.RandHex(16), Priority: in.Priority, Hosts: hosts, Paths: paths, Conditions: conds, Actions: acts,
		TargetGroup: tg, Redirect: red, Fixed: fx, Tags: tagsOf(in.Tags)}
	if _, err := s.addRuleIn(lb.Name, l.ID, &r); err != nil {
		return nil, err
	}
	return map[string]any{"Rules": []awsRule{s.ruleXML(lb, l, r)}}, nil
}

func (s *Service) awsDescribeRules(q *awsapi.Req) (any, error) {
	var in struct {
		ListenerArn string
		RuleArns    []string
	}
	if err := q.Decode(&in); err != nil {
		return nil, err
	}
	res := in.ListenerArn
	if res == "" {
		res = "*"
	}
	if err := q.Authorize("elasticloadbalancing:DescribeRules", res); err != nil {
		return nil, err
	}
	out := []awsRule{}
	switch {
	case in.ListenerArn != "":
		lb, l, err := s.listenerByARN(in.ListenerArn)
		if err != nil {
			return nil, err
		}
		rs := slices.Clone(l.Rules)
		sort.SliceStable(rs, func(i, j int) bool { return rs[i].Priority < rs[j].Priority })
		for _, r := range rs {
			out = append(out, s.ruleXML(lb, l, r))
		}
		out = append(out, s.defaultRuleXML(lb, l))
	case len(in.RuleArns) > 0:
		for _, a := range in.RuleArns {
			if p, ok := arnParts(a, "listener-rule/app/"); ok && len(p) >= 4 && p[3] == "default" {
				lb, l, err := s.listenerByARN(strings.Replace(a, "listener-rule/", "listener/", 1)[:strings.LastIndex(a, "/")])
				if err != nil {
					return nil, err
				}
				out = append(out, s.defaultRuleXML(lb, l))
				continue
			}
			lb, l, r, err := s.ruleByARN(a)
			if err != nil {
				return nil, err
			}
			out = append(out, s.ruleXML(lb, l, r))
		}
	default:
		return nil, apiErr("ValidationError", "You must specify either listener ARN or rule ARNs")
	}
	return map[string]any{"Rules": out}, nil
}

func (s *Service) awsModifyRule(q *awsapi.Req) (any, error) {
	var in struct {
		RuleArn    string
		Conditions []Condition
		Actions    []Action
	}
	if err := q.Decode(&in); err != nil {
		return nil, err
	}
	if err := q.Authorize("elasticloadbalancing:ModifyRule", in.RuleArn); err != nil {
		return nil, err
	}
	lb, l, cur, err := s.ruleByARN(in.RuleArn)
	if err != nil {
		return nil, err
	}
	n := cur
	if len(in.Conditions) > 0 {
		if n.Conditions, n.Hosts, n.Paths, err = normalizeConditions(in.Conditions); err != nil {
			return nil, err
		}
		n.PathPrefix, n.HostHeader = "", ""
	}
	if len(in.Actions) > 0 {
		if n.Actions, n.TargetGroup, n.Redirect, n.Fixed, err = s.normalizeActions(in.Actions); err != nil {
			return nil, err
		}
	}
	if err := s.updateListener(lb.Name, l.ID, false, func(x *Listener, v *LoadBalancer) error {
		for i := range x.Rules {
			if x.Rules[i].ID == cur.ID {
				x.Rules[i] = n
				return nil
			}
		}
		return core.NotFound("rule", in.RuleArn)
	}); err != nil {
		return nil, err
	}
	return map[string]any{"Rules": []awsRule{s.ruleXML(lb, l, n)}}, nil
}

func (s *Service) awsDeleteRule(q *awsapi.Req) (any, error) {
	arn := q.Param("RuleArn")
	if err := q.Authorize("elasticloadbalancing:DeleteRule", arn); err != nil {
		return nil, err
	}
	lb, l, r, err := s.ruleByARN(arn)
	if err != nil {
		return nil, err
	}
	return nil, s.updateListener(lb.Name, l.ID, false, func(x *Listener, _ *LoadBalancer) error {
		x.Rules = slices.DeleteFunc(x.Rules, func(o Rule) bool { return o.ID == r.ID })
		return nil
	})
}

func (s *Service) awsSetRulePriorities(q *awsapi.Req) (any, error) {
	var in struct {
		RulePriorities []struct {
			RuleArn  string
			Priority int
		}
	}
	if err := q.Decode(&in); err != nil {
		return nil, err
	}
	var out []awsRule
	byListener := map[string]map[string]int{} // listener ARN -> rule ID -> priority
	var owners = map[string]LoadBalancer{}
	for _, rp := range in.RulePriorities {
		if err := q.Authorize("elasticloadbalancing:SetRulePriorities", rp.RuleArn); err != nil {
			return nil, err
		}
		lb, l, r, err := s.ruleByARN(rp.RuleArn)
		if err != nil {
			return nil, err
		}
		if rp.Priority < 1 || rp.Priority > 50000 {
			return nil, apiErr("ValidationError", "Priority must be between 1 and 50000")
		}
		key := lb.Name + "/" + l.ID
		if byListener[key] == nil {
			byListener[key] = map[string]int{}
		}
		byListener[key][r.ID] = rp.Priority
		owners[key] = lb
	}
	for key, prios := range byListener {
		lbName, lid, _ := strings.Cut(key, "/")
		err := s.updateListener(lbName, lid, false, func(x *Listener, _ *LoadBalancer) error {
			for i := range x.Rules {
				if p, ok := prios[x.Rules[i].ID]; ok {
					x.Rules[i].Priority = p
				}
			}
			seen := map[int]bool{}
			for _, r := range x.Rules {
				if seen[r.Priority] {
					return core.Errf(http.StatusConflict, "PriorityInUse", "priority %d is used by more than one rule", r.Priority)
				}
				seen[r.Priority] = true
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	for _, rp := range in.RulePriorities {
		lb, l, r, err := s.ruleByARN(rp.RuleArn)
		if err != nil {
			return nil, err
		}
		out = append(out, s.ruleXML(lb, l, r))
	}
	return map[string]any{"Rules": out}, nil
}

// ---- tags ----

// editTags reads (fn == nil) or changes the tags of a load balancer, target group, listener or rule.
func (s *Service) editTags(arn string, fn func(core.Tags) core.Tags) (core.Tags, error) {
	switch {
	case strings.Contains(arn, ":loadbalancer/"):
		lb, err := s.lbByARN(arn)
		if err != nil || fn == nil {
			return lb.Tags, err
		}
		lb, err = store.Update(s.env.Store, cLBs, lb.Name, func(x *LoadBalancer) error { x.Tags = fn(x.Tags); return nil })
		return lb.Tags, err
	case strings.Contains(arn, ":targetgroup/"):
		tg, err := s.tgByARN(arn)
		if err != nil || fn == nil {
			return tg.Tags, err
		}
		tg, err = store.Update(s.env.Store, cTGs, tg.Name, func(x *TargetGroup) error { x.Tags = fn(x.Tags); return nil })
		return tg.Tags, err
	case strings.Contains(arn, ":listener/"):
		lb, l, err := s.listenerByARN(arn)
		if err != nil || fn == nil {
			return l.Tags, err
		}
		return l.Tags, s.updateListener(lb.Name, l.ID, false, func(x *Listener, _ *LoadBalancer) error { x.Tags = fn(x.Tags); return nil })
	case strings.Contains(arn, ":listener-rule/"):
		lb, l, r, err := s.ruleByARN(arn)
		if err != nil || fn == nil {
			return r.Tags, err
		}
		return r.Tags, s.updateListener(lb.Name, l.ID, false, func(x *Listener, _ *LoadBalancer) error {
			for i := range x.Rules {
				if x.Rules[i].ID == r.ID {
					x.Rules[i].Tags = fn(x.Rules[i].Tags)
				}
			}
			return nil
		})
	}
	return nil, apiErr("ValidationError", "'%s' is not a valid resource ARN", arn)
}

func (s *Service) awsAddTags(q *awsapi.Req) (any, error) {
	var in struct {
		ResourceArns []string
		Tags         []awsTag
	}
	if err := q.Decode(&in); err != nil {
		return nil, err
	}
	for _, a := range in.ResourceArns {
		if err := q.Authorize("elasticloadbalancing:AddTags", a); err != nil {
			return nil, err
		}
		if _, err := s.editTags(a, func(t core.Tags) core.Tags {
			if t == nil {
				t = core.Tags{}
			}
			for _, x := range in.Tags {
				t[x.Key] = x.Value
			}
			return t
		}); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

func (s *Service) awsRemoveTags(q *awsapi.Req) (any, error) {
	var in struct {
		ResourceArns []string
		TagKeys      []string
	}
	if err := q.Decode(&in); err != nil {
		return nil, err
	}
	for _, a := range in.ResourceArns {
		if err := q.Authorize("elasticloadbalancing:RemoveTags", a); err != nil {
			return nil, err
		}
		if _, err := s.editTags(a, func(t core.Tags) core.Tags {
			for _, k := range in.TagKeys {
				delete(t, k)
			}
			return t
		}); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

func (s *Service) awsDescribeTags(q *awsapi.Req) (any, error) {
	arns := q.List("ResourceArns")
	type desc struct {
		ResourceArn string
		Tags        []awsTag
	}
	out := []desc{}
	for _, a := range arns {
		if err := q.Authorize("elasticloadbalancing:DescribeTags", a); err != nil {
			return nil, err
		}
		t, err := s.editTags(a, nil)
		if err != nil {
			return nil, err
		}
		out = append(out, desc{a, tagList(t)})
	}
	return map[string]any{"TagDescriptions": out}, nil
}

// ---- misc ----

func (s *Service) awsDescribeSSLPolicies(q *awsapi.Req) (any, error) {
	if err := q.Authorize("elasticloadbalancing:DescribeSSLPolicies", "*"); err != nil {
		return nil, err
	}
	names := q.List("Names")
	type cipher struct {
		Name     string
		Priority int
	}
	type policy struct {
		Name                       string
		SslProtocols               []string
		Ciphers                    []cipher
		SupportedLoadBalancerTypes []string
	}
	all := []struct {
		name   string
		protos []string
	}{
		{"ELBSecurityPolicy-2016-08", []string{"TLSv1", "TLSv1.1", "TLSv1.2"}},
		{"ELBSecurityPolicy-TLS-1-2-2017-01", []string{"TLSv1.2"}},
		{"ELBSecurityPolicy-TLS-1-2-Ext-2018-06", []string{"TLSv1.2"}},
		{"ELBSecurityPolicy-FS-1-2-2019-08", []string{"TLSv1.2"}},
		{"ELBSecurityPolicy-TLS13-1-2-2021-06", []string{"TLSv1.2", "TLSv1.3"}},
		{"ELBSecurityPolicy-TLS13-1-2-Res-2021-06", []string{"TLSv1.2", "TLSv1.3"}},
	}
	out := []policy{}
	for _, p := range all {
		if len(names) > 0 && !slices.Contains(names, p.name) {
			continue
		}
		out = append(out, policy{Name: p.name, SslProtocols: p.protos, Ciphers: []cipher{{"ECDHE-RSA-AES128-GCM-SHA256", 1}},
			SupportedLoadBalancerTypes: []string{"application"}})
	}
	for _, n := range names {
		if !slices.ContainsFunc(all, func(p struct {
			name   string
			protos []string
		}) bool {
			return p.name == n
		}) {
			return nil, apiErr("SSLPolicyNotFound", "The specified SSL policy '%s' does not exist", n)
		}
	}
	return map[string]any{"SslPolicies": out}, nil
}

func (s *Service) awsDescribeAccountLimits(q *awsapi.Req) (any, error) {
	if err := q.Authorize("elasticloadbalancing:DescribeAccountLimits", "*"); err != nil {
		return nil, err
	}
	type lim struct{ Name, Max string }
	return map[string]any{"Limits": []lim{{"application-load-balancers", "50"}, {"target-groups", "3000"}, {"targets-per-application-load-balancer", "1000"},
		{"listeners-per-application-load-balancer", "50"}, {"rules-per-application-load-balancer", "100"}}}, nil
}
