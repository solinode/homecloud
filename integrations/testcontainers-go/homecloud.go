// Package homecloud is a testcontainers-go module for HomeCloud, a self-hosted
// AWS: one container that speaks the AWS APIs (S3, SQS, DynamoDB, Lambda, ...)
// on a single port with SigV4.
//
//	hc, err := homecloud.Run(ctx, homecloud.DefaultImage)
//	defer testcontainers.TerminateContainer(hc)
//	cfg := hc.AWSConfig()
//	s3c := s3.NewFromConfig(cfg, func(o *s3.Options) { o.UsePathStyle = true })
//
// HomeCloud runs each service's backing containers (MinIO, Lambda runtimes,
// databases) on the Docker host itself, so the container mounts the Docker
// socket and uses host networking: it reaches those containers on the host's
// loopback ports, and they reach it back through host.docker.internal. That
// works on Linux, OrbStack and Docker Desktop with host networking enabled.
//
// One HomeCloud runs per Docker host at a time (its helper containers have fixed
// names and host ports), so tests that use it must not run in parallel with
// each other or next to a HomeCloud you run yourself on the same Docker host.
package homecloud

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/testcontainers/testcontainers-go"
	tcexec "github.com/testcontainers/testcontainers-go/exec"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	// DefaultImage is the official HomeCloud image.
	DefaultImage = "ghcr.io/solinode/homecloud:latest"
	// DefaultPort is the port the API listens on, on the Docker host. It is not
	// HomeCloud's usual 8080, which is often taken on developer machines.
	DefaultPort = 18080
	// DefaultDockerSocket is the Docker socket on the Docker host.
	DefaultDockerSocket = "/var/run/docker.sock"

	dataDir      = "/data"
	accountLabel = "homecloud.account"
)

// readyLines are the log lines of services that start containers in the background.
var readyLines = map[string]string{
	"s3":      "s3: MinIO ready",
	"ecr":     "ecr: registry ready",
	"route53": "route53: DNS ready",
}

// Credentials are the root access key HomeCloud creates on first start.
type Credentials struct {
	AccessKeyID     string
	SecretAccessKey string
	Region          string
}

// Container is a running HomeCloud.
type Container struct {
	testcontainers.Container
	endpoint string
	creds    Credentials
	account  string
}

type options struct {
	port     int
	socket   string
	services []string
}

// Option configures Run. It also satisfies testcontainers.ContainerCustomizer,
// so it can be passed next to the generic testcontainers options.
type Option func(*options)

// Customize is a no-op: Options are read by Run itself.
func (Option) Customize(*testcontainers.GenericContainerRequest) error { return nil }

// WithPort sets the port the API listens on, on the Docker host (default DefaultPort).
func WithPort(port int) Option { return func(o *options) { o.port = port } }

// WithDockerSocket sets the path of the Docker socket on the Docker host
// (default /var/run/docker.sock, which is right for Linux, OrbStack and Docker Desktop).
func WithDockerSocket(path string) Option { return func(o *options) { o.socket = path } }

// WithWaitForServices sets which background services Run waits for besides the
// API: "s3", "ecr", "route53". The default is "s3". Every other service runs
// inside the HomeCloud process and is ready with the API.
func WithWaitForServices(services ...string) Option {
	return func(o *options) { o.services = services }
}

// Run starts HomeCloud from img (DefaultImage, or a locally built image) and
// waits until the API and the requested services are ready.
func Run(ctx context.Context, img string, opts ...testcontainers.ContainerCustomizer) (*Container, error) {
	o := options{port: DefaultPort, socket: DefaultDockerSocket, services: []string{"s3"}}
	for _, opt := range opts {
		if f, ok := opt.(Option); ok {
			f(&o)
		}
	}
	strategies := []wait.Strategy{wait.ForLog("listening on")}
	for _, s := range o.services {
		line, ok := readyLines[strings.ToLower(s)]
		if !ok {
			continue
		}
		strategies = append(strategies, wait.ForLog(line))
	}
	base := []testcontainers.ContainerCustomizer{
		testcontainers.WithCmd("serve", "--data-dir", dataDir, "--addr", "127.0.0.1:"+strconv.Itoa(o.port)),
		testcontainers.WithEnv(map[string]string{"HOMECLOUD_DATA_DIR": dataDir}),
		testcontainers.WithHostConfigModifier(func(hc *container.HostConfig) {
			hc.NetworkMode = "host"
			hc.Binds = append(hc.Binds, o.socket+":/var/run/docker.sock")
		}),
		testcontainers.WithWaitStrategyAndDeadline(3*time.Minute, wait.ForAll(strategies...)),
	}
	ctr, err := testcontainers.Run(ctx, img, append(base, opts...)...)
	var c *Container
	if ctr != nil {
		c = &Container{Container: ctr}
	}
	if err != nil {
		return c, fmt.Errorf("run homecloud: %w", err)
	}
	if c.account, err = c.readAccount(ctx); err != nil {
		return c, err
	}
	host, err := ctr.Host(ctx)
	if err != nil {
		return c, err
	}
	if host == "localhost" {
		host = "127.0.0.1" // keep SDKs from trying IPv6 first or bucket.localhost names
	}
	c.endpoint = "http://" + host + ":" + strconv.Itoa(o.port)
	if err := c.waitHealthy(ctx, 30*time.Second); err != nil {
		return c, err
	}
	if c.creds, err = c.readCredentials(ctx); err != nil {
		return c, err
	}
	return c, nil
}

// EndpointURL is the URL of the HomeCloud API, e.g. http://127.0.0.1:18080:
// what AWS_ENDPOINT_URL or an SDK's base endpoint should be set to.
func (c *Container) EndpointURL() string { return c.endpoint }

// Endpoint returns the API address as testcontainers.Container.Endpoint does
// ("127.0.0.1:18080", or "http://127.0.0.1:18080" for proto "http"). It
// replaces the generic one, which looks for a mapped port and so does not work
// with host networking.
func (c *Container) Endpoint(_ context.Context, proto string) (string, error) {
	addr := strings.TrimPrefix(c.endpoint, "http://")
	if proto == "" {
		return addr, nil
	}
	return proto + "://" + addr, nil
}

// Credentials returns the root access key and the region.
func (c *Container) Credentials() Credentials { return c.creds }

// AccountID is the AWS account ID HomeCloud created on first start.
func (c *Container) AccountID() string { return c.account }

// AWSConfig returns an aws-sdk-go-v2 config pointed at this HomeCloud. Pass
// it to any service client's NewFromConfig. For S3 also set UsePathStyle.
func (c *Container) AWSConfig() aws.Config {
	return aws.Config{
		Region:       c.creds.Region,
		Credentials:  credentials.NewStaticCredentialsProvider(c.creds.AccessKeyID, c.creds.SecretAccessKey, ""),
		BaseEndpoint: aws.String(c.endpoint),
	}
}

// Env returns the AWS_* environment variables for tools that read them (AWS
// CLI, Terraform, SDKs in a subprocess).
func (c *Container) Env() map[string]string {
	return map[string]string{
		"AWS_ENDPOINT_URL":      c.endpoint,
		"AWS_ACCESS_KEY_ID":     c.creds.AccessKeyID,
		"AWS_SECRET_ACCESS_KEY": c.creds.SecretAccessKey,
		"AWS_REGION":            c.creds.Region,
		"AWS_DEFAULT_REGION":    c.creds.Region,
	}
}

// Terminate stops HomeCloud and removes every container, network and volume it
// created on the Docker host (MinIO, Lambda runtimes, databases, ...).
func (c *Container) Terminate(ctx context.Context, opts ...testcontainers.TerminateOption) error {
	if c == nil || c.Container == nil {
		return nil
	}
	err := c.Container.Terminate(ctx, opts...)
	return errors.Join(err, RemoveResources(ctx, c.account))
}

// RemoveResources removes the containers, networks and volumes HomeCloud
// created for account on the Docker host. Terminate calls it; call it yourself
// to clean up after a test process that was killed.
func RemoveResources(ctx context.Context, account string) error {
	if account == "" {
		return nil
	}
	cli, err := testcontainers.NewDockerClientWithOpts(ctx)
	if err != nil {
		return err
	}
	defer cli.Close()
	f := client.Filters{}.Add("label", accountLabel+"="+account)
	var errs []error
	cs, err := cli.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: f})
	errs = append(errs, err)
	for _, ct := range cs.Items {
		_, err := cli.ContainerRemove(ctx, ct.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
		errs = append(errs, err)
	}
	ns, err := cli.NetworkList(ctx, client.NetworkListOptions{Filters: f})
	errs = append(errs, err)
	for _, n := range ns.Items {
		_, err := cli.NetworkRemove(ctx, n.ID, client.NetworkRemoveOptions{})
		errs = append(errs, err)
	}
	vs, err := cli.VolumeList(ctx, client.VolumeListOptions{Filters: f})
	errs = append(errs, err)
	for _, v := range vs.Items {
		_, err := cli.VolumeRemove(ctx, v.Name, client.VolumeRemoveOptions{Force: true})
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

var accountRE = regexp.MustCompile(`first start: created account (\d+)`)

func (c *Container) readAccount(ctx context.Context) (string, error) {
	rc, err := c.Logs(ctx)
	if err != nil {
		return "", err
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		return "", err
	}
	m := accountRE.FindSubmatch(b)
	if m == nil {
		return "", errors.New("homecloud: no account in the first-start log")
	}
	return string(m[1]), nil
}

func (c *Container) waitHealthy(ctx context.Context, d time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	var last error
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint+"/api/v1/health", nil)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			err = fmt.Errorf("status %s", resp.Status)
		}
		last = err
		select {
		case <-ctx.Done():
			return fmt.Errorf("homecloud: %s/api/v1/health not reachable (does this Docker setup support host networking?): %w", c.endpoint, last)
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// readCredentials runs `homecloud aws-env` in the container.
func (c *Container) readCredentials(ctx context.Context) (Credentials, error) {
	code, r, err := c.Exec(ctx, []string{"homecloud", "aws-env"}, tcexec.Multiplexed())
	if err != nil {
		return Credentials{}, fmt.Errorf("homecloud aws-env: %w", err)
	}
	out, _ := io.ReadAll(r)
	if code != 0 {
		return Credentials{}, fmt.Errorf("homecloud aws-env exited %d: %s", code, out)
	}
	vars := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimPrefix(sc.Text(), "export "), "=")
		if !ok {
			continue
		}
		if u, err := strconv.Unquote(v); err == nil {
			v = u
		}
		vars[k] = v
	}
	cr := Credentials{AccessKeyID: vars["AWS_ACCESS_KEY_ID"], SecretAccessKey: vars["AWS_SECRET_ACCESS_KEY"], Region: vars["AWS_REGION"]}
	if cr.AccessKeyID == "" || cr.SecretAccessKey == "" {
		return cr, fmt.Errorf("homecloud aws-env printed no credentials: %s", out)
	}
	if cr.Region == "" {
		cr.Region = "us-east-1"
	}
	return cr, nil
}
