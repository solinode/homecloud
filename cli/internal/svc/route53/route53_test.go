package route53

import (
	"strings"
	"testing"
)

func TestFQDN(t *testing.T) {
	z := "corp.internal."
	cases := map[string]string{"@": z, "": z, "db": "db.corp.internal.", "db.corp.internal": "db.corp.internal.", "x.example.com.": "x.example.com."}
	for in, want := range cases {
		if got := fqdn(in, z); got != want {
			t.Errorf("fqdn(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidate(t *testing.T) {
	z := Zone{Name: "home.arpa."}
	good := []Record{
		{Name: "nas", Type: "a", Values: []string{"192.168.1.2"}},
		{Name: "www", Type: "CNAME", Values: []string{"nas.home.arpa."}},
		{Name: "@", Type: "TXT", Values: []string{"v=spf1 -all"}},
		{Name: "db", Type: "A", Alias: "pg1"},
	}
	for _, r := range good {
		if err := validate(z, &r); err != nil {
			t.Errorf("%+v rejected: %v", r, err)
		}
	}
	bad := []Record{
		{Name: "nas", Type: "A", Values: []string{"not-an-ip"}},
		{Name: "nas", Type: "AAAA", Values: []string{"10.0.0.1"}},
		{Name: "@", Type: "CNAME", Values: []string{"x.example.com."}},
		{Name: "x", Type: "TXT", Values: []string{"a\nb"}},
		{Name: "x.other.org.", Type: "A", Values: []string{"10.0.0.1"}},
		{Name: "x", Type: "A", Values: []string{"10.0.0.1"}, Alias: "i-1"},
		{Name: "x", Type: "BOGUS", Values: []string{"1"}},
	}
	for _, r := range bad {
		if err := validate(z, &r); err == nil {
			t.Errorf("%+v accepted", r)
		}
	}
}

func TestZoneFile(t *testing.T) {
	s := &Service{Resolve: func(id string) (string, bool) {
		if id == "pg1" {
			return "10.88.0.9", true
		}
		return "", false
	}}
	z := Zone{Name: "corp.internal.", Serial: 42, Records: []Record{
		{Name: "db", Type: "A", TTL: 60, Alias: "pg1"},
		{Name: "gone", Type: "A", TTL: 60, Alias: "i-missing"},
		{Name: "@", Type: "TXT", TTL: 300, Values: []string{`say "hi"`}},
	}}
	f := s.zoneFile(z, "10.88.0.2")
	for _, want := range []string{"SOA ns.corp.internal. hostmaster.corp.internal. 42 ", "db.corp.internal. 60 IN A 10.88.0.9", `corp.internal. 300 IN TXT "say \"hi\""`, "ns.corp.internal. 3600 IN A 10.88.0.2"} {
		if !strings.Contains(f, want) {
			t.Errorf("zone file missing %q:\n%s", want, f)
		}
	}
	if strings.Contains(f, "gone") {
		t.Error("an alias to a missing resource was rendered")
	}
}
