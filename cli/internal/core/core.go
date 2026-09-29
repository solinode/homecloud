// Package core holds the types shared by every HomeCloud service: configuration,
// resource identifiers, ARNs and API errors.
package core

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"
)

const (
	// LabelManaged marks every Docker object HomeCloud owns. HomeCloud never
	// touches containers, networks or volumes without it.
	LabelManaged  = "homecloud.managed"
	LabelService  = "homecloud.service"
	LabelResource = "homecloud.resource"
	LabelAccount  = "homecloud.account"
	DefaultRegion = "local-1"
)

type Config struct {
	DataDir       string `json:"data_dir"`
	APIAddr       string `json:"api_addr"`
	PublicHost    string `json:"public_host"` // host name clients use to reach published ports
	S3Port        int    `json:"s3_port"`
	S3ConsolePort int    `json:"s3_console_port"`
	ECRPort       int    `json:"ecr_port"`
	DNSPort       int    `json:"dns_port"`           // host port serving public hosted zones (UDP and TCP)
	TLSCert       string `json:"tls_cert,omitempty"` // PEM files; empty = plain HTTP
	TLSKey        string `json:"tls_key,omitempty"`
	Region        string `json:"region"`
}

func DefaultDataDir() string {
	if d := os.Getenv("HOMECLOUD_DATA_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".homecloud"
	}
	return filepath.Join(home, ".homecloud")
}

func DefaultConfig() Config {
	return Config{
		DataDir:       DefaultDataDir(),
		APIAddr:       "127.0.0.1:8080",
		PublicHost:    "localhost",
		S3Port:        9500,
		S3ConsolePort: 9501,
		ECRPort:       5500,
		DNSPort:       8053,
		Region:        DefaultRegion,
	}
}

func (c Config) Path(parts ...string) string {
	return filepath.Join(append([]string{c.DataDir}, parts...)...)
}

// NewID returns an AWS-style identifier such as "i-0a1b2c3d4e5f67890".
func NewID(prefix string) string {
	return prefix + "-" + RandHex(17)
}

func RandHex(n int) string {
	b := make([]byte, (n+1)/2)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)[:n]
}

const alnumUpper = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"
const alnum = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

func randFrom(alphabet string, n int) string {
	var sb strings.Builder
	for i := 0; i < n; i++ {
		k, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			panic(err)
		}
		sb.WriteByte(alphabet[k.Int64()])
	}
	return sb.String()
}

func NewAccessKeyID() string { return "HCIA" + randFrom(alnumUpper, 16) }
func NewSecret(n int) string { return randFrom(alnum, n) }

func NewAccountID() string {
	return randFrom("0123456789", 12)
}

func ARN(account, service, resource string) string {
	return fmt.Sprintf("arn:hc:%s:%s:%s:%s", service, DefaultRegion, account, resource)
}

func Now() time.Time { return time.Now().UTC().Truncate(time.Second) }

// Error is an API error with an AWS-style code and an HTTP status.
type Error struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func Errf(status int, code, format string, a ...any) *Error {
	return &Error{Status: status, Code: code, Message: fmt.Sprintf(format, a...)}
}

func NotFound(kind, id string) *Error {
	return Errf(http.StatusNotFound, "ResourceNotFound", "%s %q does not exist", kind, id)
}

func BadRequest(format string, a ...any) *Error {
	return Errf(http.StatusBadRequest, "ValidationError", format, a...)
}

func Conflict(format string, a ...any) *Error {
	return Errf(http.StatusConflict, "ResourceConflict", format, a...)
}

// Tags are free-form key/value labels attached to resources.
type Tags map[string]string

// Recover logs a panic in a background goroutine instead of crashing the server.
// Use as: defer core.Recover("what is running")
func Recover(what string) {
	if r := recover(); r != nil {
		log.Printf("panic in %s: %v\n%s", what, r, debug.Stack())
	}
}
