package elb

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Wire shapes of the ELBv2 API. The same types are stored on listeners and
// rules, so what a client sent is what Describe returns.

type Action struct {
	Type                string               `json:"Type"`
	Order               int                  `json:"Order,omitempty"`
	TargetGroupArn      string               `json:"TargetGroupArn,omitempty"`
	ForwardConfig       *ForwardConfig       `json:"ForwardConfig,omitempty"`
	RedirectConfig      *RedirectConfig      `json:"RedirectConfig,omitempty"`
	FixedResponseConfig *FixedResponseConfig `json:"FixedResponseConfig,omitempty"`
}

type ForwardConfig struct {
	TargetGroups                []TargetGroupTuple `json:"TargetGroups,omitempty"`
	TargetGroupStickinessConfig *StickinessConfig  `json:"TargetGroupStickinessConfig,omitempty"`
}

type TargetGroupTuple struct {
	TargetGroupArn string `json:"TargetGroupArn"`
	Weight         int    `json:"Weight"`
}

type StickinessConfig struct {
	Enabled         bool `json:"Enabled"`
	DurationSeconds int  `json:"DurationSeconds,omitempty"`
}

type RedirectConfig struct {
	Protocol   string `json:"Protocol,omitempty"`
	Port       string `json:"Port,omitempty"`
	Host       string `json:"Host,omitempty"`
	Path       string `json:"Path,omitempty"`
	Query      string `json:"Query,omitempty"`
	StatusCode string `json:"StatusCode,omitempty"`
}

type FixedResponseConfig struct {
	MessageBody string `json:"MessageBody,omitempty"`
	StatusCode  string `json:"StatusCode,omitempty"`
	ContentType string `json:"ContentType,omitempty"`
}

type Condition struct {
	Field                   string        `json:"Field"`
	Values                  []string      `json:"Values,omitempty"`
	PathPatternConfig       *ValuesConfig `json:"PathPatternConfig,omitempty"`
	HostHeaderConfig        *ValuesConfig `json:"HostHeaderConfig,omitempty"`
	HttpRequestMethodConfig *ValuesConfig `json:"HttpRequestMethodConfig,omitempty"`
	SourceIpConfig          *ValuesConfig `json:"SourceIpConfig,omitempty"`
}

type ValuesConfig struct {
	Values []string `json:"Values"`
}

// ---- ARNs ----

func stableID(name string) string {
	h := sha256.Sum256([]byte(name))
	return hex.EncodeToString(h[:8])
}

// LoadBalancerARN is the AWS-style ARN of a load balancer.
func (s *Service) LoadBalancerARN(name string) string {
	return s.env.ARN("elasticloadbalancing", "loadbalancer/app/"+name+"/"+stableID("lb/"+name))
}

// TargetGroupARN is the AWS-style ARN of a target group.
func (s *Service) TargetGroupARN(name string) string {
	return s.env.ARN("elasticloadbalancing", "targetgroup/"+name+"/"+stableID("tg/"+name))
}

func (s *Service) listenerARN(lb, id string) string {
	return s.env.ARN("elasticloadbalancing", "listener/app/"+lb+"/"+stableID("lb/"+lb)+"/"+id)
}

func (s *Service) ruleARN(lb, listener, id string) string {
	return s.env.ARN("elasticloadbalancing", "listener-rule/app/"+lb+"/"+stableID("lb/"+lb)+"/"+listener+"/"+id)
}

// arnParts returns the "/"-separated segments of an ARN's resource after prefix.
func arnParts(arn, prefix string) ([]string, bool) {
	p := strings.SplitN(arn, ":", 6)
	if len(p) != 6 || p[0] != "arn" || p[2] != "elasticloadbalancing" || !strings.HasPrefix(p[5], prefix) {
		return nil, false
	}
	return strings.Split(strings.TrimPrefix(p[5], prefix), "/"), true
}

// TargetGroupName extracts the name of a target group ARN (or a bare name).
func TargetGroupName(arn string) (string, bool) {
	if !strings.HasPrefix(arn, "arn:") {
		return arn, arn != ""
	}
	p, ok := arnParts(arn, "targetgroup/")
	if !ok || p[0] == "" {
		return "", false
	}
	return p[0], true
}

// ---- rule matching ----

type pathMatch struct{ mod, path string }

func (r Rule) hostList() []string {
	switch {
	case len(r.Hosts) > 0:
		return r.Hosts
	case r.HostHeader != "":
		return []string{r.HostHeader}
	}
	return []string{""}
}

// pathList converts a rule's path conditions to nginx locations. AWS path
// patterns are exact unless they contain wildcards; a trailing "*" is a prefix.
func (r Rule) pathList() []pathMatch {
	if len(r.Paths) == 0 {
		p := r.PathPrefix
		if p == "" {
			p = "/"
		}
		return []pathMatch{{"^~", p}}
	}
	var out []pathMatch
	for _, p := range r.Paths {
		i := strings.IndexByte(p, '*')
		switch {
		case i < 0:
			out = append(out, pathMatch{"=", p})
		case i == len(p)-1:
			if p = strings.TrimSuffix(p, "*"); p == "" {
				p = "/"
			}
			out = append(out, pathMatch{"^~", p})
		default:
			re := strings.ReplaceAll(strings.ReplaceAll(p, ".", `\.`), "*", ".*")
			out = append(out, pathMatch{"~", "^" + re + "$"})
		}
	}
	return out
}

// ---- redirects and fixed responses ----

var (
	redirectRe = regexp.MustCompile(`^[A-Za-z0-9._~%/?&=+#{}:*,;@!-]*$`)
	ctypeRe    = regexp.MustCompile(`^[A-Za-z0-9.+/-]+$`)
)

func expandVars(s string) string {
	return strings.NewReplacer("#{protocol}", "$scheme", "#{host}", "$host", "#{port}", "$server_port",
		"#{path}", "$uri", "#{query}", "$args").Replace(s)
}

// redirectDirective renders a redirect action as an nginx "return".
func redirectDirective(c RedirectConfig, lb LoadBalancer) string {
	proto, host, port, path, query := c.Protocol, c.Host, c.Port, c.Path, c.Query
	if proto == "" {
		proto = "#{protocol}"
	}
	if host == "" {
		host = "#{host}"
	}
	if port == "" {
		port = "#{port}"
	}
	if path == "" {
		path = "/#{path}"
	}
	if query == "" {
		query = "#{query}"
	}
	if n, err := strconv.Atoi(port); err == nil {
		if hp := lb.PublicPorts[fmt.Sprintf("%d/tcp", n)]; hp > 0 {
			port = strconv.Itoa(hp) // clients reach published ports
		}
	}
	code := 301
	if c.StatusCode == "HTTP_302" {
		code = 302
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	path = strings.ReplaceAll(path, "/#{path}", "#{path}") // $uri already starts with a slash
	url := expandVars(strings.ToLower(proto) + "://" + host + ":" + port + path)
	if q := expandVars(query); q != "" {
		url += "?" + q
	}
	return fmt.Sprintf("return %d \"%s\";", code, url)
}

func fixedDirective(c FixedResponseConfig) string {
	code, _ := strconv.Atoi(c.StatusCode)
	if code == 0 {
		code = 200
	}
	body := strings.NewReplacer("'", "", `\`, "", "$", "", "\n", " ").Replace(c.MessageBody)
	ct := c.ContentType
	if ct == "" {
		ct = "text/plain"
	}
	return fmt.Sprintf("default_type %s; return %d '%s';", ct, code, body)
}
