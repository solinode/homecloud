package core

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"
)

// TargetAction returns the IAM action needed to deliver to a target ARN
// (Lambda function, SQS queue, SNS topic or state machine), or "".
func TargetAction(arn string) string {
	parts := strings.SplitN(CanonicalARN(arn), ":", 6)
	if len(parts) != 6 || parts[0] != "arn" || parts[1] != Partition {
		return ""
	}
	switch parts[2] {
	case "lambda":
		return "lambda:InvokeFunction"
	case "sqs":
		return "sqs:SendMessage"
	case "sns":
		return "sns:Publish"
	case "states":
		return "states:StartExecution"
	}
	return ""
}

// blockedNets are ranges outbound requests made on behalf of users never reach:
// "this network", the cloud metadata services of VPS providers that sit outside
// link-local space, and IPv6 prefixes that embed IPv4 addresses (NAT64, 6to4),
// which could smuggle a blocked IPv4 address past the checks.
var blockedNets = func() []*net.IPNet {
	var out []*net.IPNet
	for _, c := range []string{"0.0.0.0/8", "100.100.100.200/32", "192.0.0.192/32", "fd00:ec2::/32", "64:ff9b::/96", "64:ff9b:1::/48", "2002::/16", "::/128"} {
		_, n, _ := net.ParseCIDR(c)
		out = append(out, n)
	}
	return out
}()

// DenyPrivateTargets, when set (HOMECLOUD_DENY_PRIVATE_TARGETS=1), also blocks
// RFC 1918 / unique-local addresses. They stay reachable by default because
// self-hosters legitimately call services on their LAN and in their VPCs.
var DenyPrivateTargets = os.Getenv("HOMECLOUD_DENY_PRIVATE_TARGETS") == "1"

// blockedIP reports addresses outbound requests made for users may not reach:
// loopback, link-local (including the 169.254.169.254 metadata service),
// unspecified, multicast, the ranges in blockedNets and every address of this
// host itself (its public address and the Docker bridge gateways, through which
// workloads and users would otherwise reach the HomeCloud API and the Docker
// daemon).
func blockedIP(ip net.IP) bool {
	if v4 := ip.To4(); v4 != nil {
		ip = v4 // IPv4-mapped IPv6 addresses are IPv4 addresses
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsInterfaceLocalMulticast() {
		return true
	}
	for _, n := range blockedNets {
		if n.Contains(ip) {
			return true
		}
	}
	if DenyPrivateTargets && ip.IsPrivate() {
		return true
	}
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.Equal(ip) {
			return true
		}
	}
	return false
}

var errBlocked = errors.New("destination address is not allowed (loopback, link-local, metadata or this host)")

// SafeClient is an HTTP client for requests to user-supplied URLs. It refuses
// to connect to blocked addresses at dial time (after DNS resolution, so
// rebinding and redirects cannot reach them), ignores proxy environment
// variables and follows at most maxRedirects redirects.
func SafeClient(timeout time.Duration, maxRedirects int) *http.Client {
	c := WebhookClient(timeout)
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > maxRedirects {
			if maxRedirects == 0 {
				return http.ErrUseLastResponse
			}
			return errors.New("too many redirects")
		}
		return CheckWebhookURL(req.URL.String())
	}
	return c
}

// CheckWebhookURL validates an outbound webhook URL.
func CheckWebhookURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return BadRequest("webhook must be an http(s) URL")
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil && blockedIP(ip) {
		return BadRequest("%v", errBlocked)
	}
	if strings.EqualFold(u.Hostname(), "localhost") {
		return BadRequest("%v", errBlocked)
	}
	return nil
}

// WebhookClient is an HTTP client that refuses to connect to blocked addresses
// at dial time, so DNS answers cannot redirect webhooks to local services.
func WebhookClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, Control: func(network, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return err
		}
		if ip := net.ParseIP(host); ip == nil || blockedIP(ip) {
			return fmt.Errorf("%s: %w", address, errBlocked)
		}
		return nil
	}}
	tr := &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		return dialer.DialContext(ctx, network, addr)
	}, TLSHandshakeTimeout: 10 * time.Second, MaxIdleConns: 10, Proxy: nil}
	return &http.Client{Timeout: timeout, Transport: tr, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return errors.New("too many redirects")
		}
		return nil
	}}
}
