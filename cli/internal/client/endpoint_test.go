package client

import "testing"

func TestAWSEndpoint(t *testing.T) {
	for in, want := range map[string]string{
		"http://127.0.0.1:8080":     "http://localhost:8080",
		"http://127.0.0.1:18080/":   "http://localhost:18080/",
		"https://127.0.0.1:8443":    "https://localhost:8443",
		"http://[::1]:8080":         "http://localhost:8080",
		"http://127.0.0.1":          "http://localhost",
		"http://localhost:8080":     "http://localhost:8080",
		"http://192.168.1.20:8080":  "http://192.168.1.20:8080", // another machine: keep its address
		"http://127.0.0.2:8080":     "http://127.0.0.2:8080",    // localhost would not reach it
		"https://cloud.example.com": "https://cloud.example.com",
		"http://homelab:8080":       "http://homelab:8080",
		"":                          "",
	} {
		if got := AWSEndpoint(in); got != want {
			t.Errorf("AWSEndpoint(%q) = %q, want %q", in, got, want)
		}
	}
}
