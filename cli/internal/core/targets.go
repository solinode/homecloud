package core

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
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

// blockedIP reports addresses outbound webhooks may not reach: loopback,
// link-local (including cloud metadata), unspecified and multicast.
func blockedIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsInterfaceLocalMulticast()
}

var errBlocked = errors.New("destination address is not allowed (loopback, link-local or unspecified)")

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
	}, TLSHandshakeTimeout: 10 * time.Second, MaxIdleConns: 10}
	return &http.Client{Timeout: timeout, Transport: tr, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return errors.New("too many redirects")
		}
		return nil
	}}
}
