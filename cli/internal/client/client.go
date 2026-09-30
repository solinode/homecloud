// Package client is the Go client for the HomeCloud API, used by the CLI.
package client

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
)

type Profile struct {
	Endpoint        string `json:"endpoint"`
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	Region          string `json:"region,omitempty"`
	CAFile          string `json:"ca_file,omitempty"` // extra CA to trust (self-signed servers)
}

func CredentialsFile() string { return filepath.Join(core.DefaultDataDir(), "credentials") }

// LoadFile reads the credentials file only (no environment overrides).
func LoadFile() Profile {
	var p Profile
	if b, err := os.ReadFile(CredentialsFile()); err == nil {
		_ = json.Unmarshal(b, &p)
	}
	return p
}

// Merge returns cur with every non-empty field of set applied, so rewriting the
// credentials file keeps what the caller did not change (region, ca_file).
func Merge(cur, set Profile) Profile {
	if set.Endpoint != "" {
		cur.Endpoint = set.Endpoint
	}
	if set.AccessKeyID != "" {
		cur.AccessKeyID = set.AccessKeyID
	}
	if set.SecretAccessKey != "" {
		cur.SecretAccessKey = set.SecretAccessKey
	}
	if set.Region != "" {
		cur.Region = set.Region
	}
	if set.CAFile != "" {
		cur.CAFile = set.CAFile
	}
	return cur
}

// Load reads credentials from the environment, falling back to the credentials file.
func Load() (Profile, error) {
	var p Profile
	if b, err := os.ReadFile(CredentialsFile()); err == nil {
		_ = json.Unmarshal(b, &p)
	}
	if v := os.Getenv("HOMECLOUD_ENDPOINT"); v != "" {
		p.Endpoint = v
	}
	if v := os.Getenv("HOMECLOUD_ACCESS_KEY_ID"); v != "" {
		p.AccessKeyID = v
	}
	if v := os.Getenv("HOMECLOUD_SECRET_ACCESS_KEY"); v != "" {
		p.SecretAccessKey = v
	}
	if p.Endpoint == "" {
		p.Endpoint = "http://127.0.0.1:8080"
	}
	if p.AccessKeyID == "" || p.SecretAccessKey == "" {
		return p, errors.New("no credentials: start the server with `homecloud serve` (it writes " + CredentialsFile() + ") or run `homecloud configure`")
	}
	return p, nil
}

func Save(p Profile) error {
	if err := os.MkdirAll(filepath.Dir(CredentialsFile()), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(p, "", "  ")
	return os.WriteFile(CredentialsFile(), b, 0o600)
}

type Client struct {
	P    Profile
	HTTP *http.Client
}

func New() (*Client, error) {
	p, err := Load()
	if err != nil {
		return nil, err
	}
	hc := &http.Client{Timeout: 10 * time.Minute}
	if p.CAFile != "" {
		pem, err := os.ReadFile(p.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read ca_file: %w", err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		pool.AppendCertsFromPEM(pem)
		hc.Transport = &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}
	}
	return &Client{P: p, HTTP: hc}, nil
}

// APIError mirrors the server's error body.
type APIError struct {
	Status  int
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *APIError) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Message) }

func (c *Client) Token() string { return c.P.AccessKeyID + ":" + c.P.SecretAccessKey }

// Request sends body (JSON-encoded unless it is an io.Reader) and returns the raw response.
func (c *Client) Request(method, path string, body any, header http.Header) (*http.Response, error) {
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case io.Reader:
		rd = b
	default:
		buf, err := json.Marshal(b)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(buf)
	}
	req, err := http.NewRequest(method, strings.TrimRight(c.P.Endpoint, "/")+path, rd)
	if err != nil {
		return nil, err
	}
	if f, ok := body.(*os.File); ok {
		if st, err := f.Stat(); err == nil {
			req.ContentLength = st.Size()
		}
	}
	for k, v := range header {
		req.Header[k] = v
	}
	if req.Header.Get("Content-Type") == "" && body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+c.Token())
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot reach HomeCloud at %s (is `homecloud serve` running?): %w", c.P.Endpoint, err)
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		var eb struct {
			Error APIError `json:"error"`
		}
		b, _ := io.ReadAll(resp.Body)
		if json.Unmarshal(b, &eb) != nil || eb.Error.Code == "" {
			return nil, &APIError{Status: resp.StatusCode, Code: resp.Status, Message: strings.TrimSpace(string(b))}
		}
		eb.Error.Status = resp.StatusCode
		return nil, &eb.Error
	}
	return resp, nil
}

// Do performs a JSON call and decodes the response into out (if non-nil).
func (c *Client) Do(method, path string, body, out any) error {
	resp, err := c.Request(method, path, body, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
