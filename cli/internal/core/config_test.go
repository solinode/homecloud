package core

import "testing"

func TestPublicBase(t *testing.T) {
	c := DefaultConfig()
	c.PublicHost = "lab.example"
	if got := c.PublicBase(); got != "http://lab.example:8080" {
		t.Fatalf("default: %s", got)
	}
	c.TLSCert = "cert.pem"
	if got := c.PublicBase(); got != "https://lab.example:8080" {
		t.Fatalf("tls follows the scheme: %s", got)
	}
	c.PublicURL = "https://cloud.example.com/"
	if got := c.PublicBase(); got != "https://cloud.example.com" {
		t.Fatalf("public url: %s", got)
	}
	if c.PublicHostname() != "cloud.example.com" {
		t.Fatalf("hostname %s", c.PublicHostname())
	}
	c.PublicURL = "https://cloud.example.com:8443/hc"
	if c.PublicBase() != "https://cloud.example.com:8443/hc" || c.PublicHostname() != "cloud.example.com" {
		t.Fatalf("with port and prefix: %s", c.PublicBase())
	}
	c.APIAddr, c.PublicURL = "[::1]:9000", ""
	c.PublicHost = "::1"
	if got := c.PublicBase(); got != "https://[::1]:9000" {
		t.Fatalf("ipv6: %s", got)
	}
}

func TestPublicURLValidationAndNormalize(t *testing.T) {
	for _, bad := range []string{"cloud.example.com", "ftp://x", "https://", "https://a/b?x=1", "https://u:p@h"} {
		if ValidatePublicURL(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if err := ValidatePublicURL("https://cloud.example.com"); err != nil {
		t.Fatal(err)
	}
	c := DefaultConfig()
	c.PublicURL = "https://cloud.example.com"
	c.Normalize()
	if c.PublicHost != "cloud.example.com" {
		t.Fatalf("public host not derived: %s", c.PublicHost)
	}
	c = DefaultConfig()
	c.PublicHost, c.PublicURL = "10.0.0.5", "https://cloud.example.com"
	c.Normalize()
	if c.PublicHost != "10.0.0.5" {
		t.Fatalf("explicit public host overwritten: %s", c.PublicHost)
	}
}

func TestServiceBindDefaultsToLoopback(t *testing.T) {
	c := DefaultConfig()
	if c.ServiceBindAddr() != "127.0.0.1" || c.DNSBindAddr() != "127.0.0.1" {
		t.Fatal("services must not be exposed by default")
	}
	c.ServiceBind, c.DNSBind = "0.0.0.0", "10.0.0.1"
	if c.ServiceBindAddr() != "0.0.0.0" || c.DNSBindAddr() != "10.0.0.1" {
		t.Fatal("bind overrides ignored")
	}
}
