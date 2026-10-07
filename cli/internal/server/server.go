// Package server assembles every HomeCloud service behind one HTTP API and
// serves the web console.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	goruntime "runtime"
	"strconv"
	"strings"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/awsapi"
	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/runtime"
	"github.com/homecloudhq/homecloud/cli/internal/store"
	"github.com/homecloudhq/homecloud/cli/internal/svc"
	"github.com/homecloudhq/homecloud/cli/internal/svc/acm"
	"github.com/homecloudhq/homecloud/cli/internal/svc/autoscaling"
	"github.com/homecloudhq/homecloud/cli/internal/svc/cfn"
	"github.com/homecloudhq/homecloud/cli/internal/svc/cloudwatch"
	"github.com/homecloudhq/homecloud/cli/internal/svc/cognito"
	"github.com/homecloudhq/homecloud/cli/internal/svc/dynamodb"
	"github.com/homecloudhq/homecloud/cli/internal/svc/ec2"
	"github.com/homecloudhq/homecloud/cli/internal/svc/ecr"
	"github.com/homecloudhq/homecloud/cli/internal/svc/ecs"
	"github.com/homecloudhq/homecloud/cli/internal/svc/elb"
	"github.com/homecloudhq/homecloud/cli/internal/svc/events"
	"github.com/homecloudhq/homecloud/cli/internal/svc/iam"
	"github.com/homecloudhq/homecloud/cli/internal/svc/kms"
	"github.com/homecloudhq/homecloud/cli/internal/svc/lambda"
	"github.com/homecloudhq/homecloud/cli/internal/svc/rds"
	"github.com/homecloudhq/homecloud/cli/internal/svc/route53"
	"github.com/homecloudhq/homecloud/cli/internal/svc/s3"
	"github.com/homecloudhq/homecloud/cli/internal/svc/secrets"
	"github.com/homecloudhq/homecloud/cli/internal/svc/sfn"
	"github.com/homecloudhq/homecloud/cli/internal/svc/sns"
	"github.com/homecloudhq/homecloud/cli/internal/svc/sqs"
	"github.com/homecloudhq/homecloud/cli/internal/svc/ssm"
	"github.com/homecloudhq/homecloud/cli/internal/svc/trail"
	"github.com/homecloudhq/homecloud/cli/internal/svc/vpc"
	"github.com/homecloudhq/homecloud/cli/internal/system"
	"github.com/homecloudhq/homecloud/cli/internal/web"
)

// Version is set by the CLI at startup.
var Version = "dev"

// Credentials is the file the CLI reads to reach the API (~/.homecloud/credentials).
type Credentials struct {
	Endpoint        string `json:"endpoint"`
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	Region          string `json:"region"`
	CAFile          string `json:"ca_file,omitempty"`
}

func CredentialsPath(dataDir string) string {
	return dataDir + string(os.PathSeparator) + "credentials"
}

type Options struct {
	ResetRootPassword bool
	Logf              func(format string, a ...any)
}

type routable interface{ Routes(r *httpx.Router) }

func Run(ctx context.Context, cfg core.Config, opts Options) error {
	logf := opts.Logf
	if logf == nil {
		logf = log.Printf
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return err
	}
	if err := migrateLegacyRegion(&cfg, logf); err != nil {
		return fmt.Errorf("migrate region: %w", err)
	}
	if cfg.Region == "" {
		cfg.Region = core.DefaultRegion
	}
	core.Region = cfg.Region
	st, err := store.Open(cfg.Path("state.json"))
	if err != nil {
		return err
	}
	dk, err := runtime.New()
	if err != nil {
		return err
	}
	account, _, err := iam.LoadAccount(st)
	if err != nil {
		return err
	}
	runtime.Account = account
	if cs, err := dk.ManagedContainers(); err == nil {
		for _, c := range cs {
			if other := c.Labels[core.LabelAccount]; other != "" && other != account {
				return fmt.Errorf("this Docker host already runs HomeCloud account %s (container %s); "+
					"start HomeCloud with that installation's --data-dir, or remove its containers first", other, strings.TrimPrefix(c.Names[0], "/"))
			}
		}
	}
	env := &svc.Env{Cfg: cfg, Store: st, Docker: dk, AccountID: account}
	selfName := ""
	if self := dk.Self(); self != "" {
		selfName = self[:12]
		if c, err := dk.Inspect(self); err == nil {
			selfName = strings.TrimPrefix(c.Name, "/")
		}
		logf("running in container %s: managing its Docker host's containers; workloads reach the API at the container's own address", selfName)
		if host, _, _ := net.SplitHostPort(cfg.APIAddr); host == "127.0.0.1" || host == "localhost" || host == "::1" {
			logf("warning: the API listens on %s, which only this container reaches; serve with --addr 0.0.0.0:%s and publish the port", cfg.APIAddr, cfg.APIPort())
		}
	}
	_, apiPort, _ := net.SplitHostPort(cfg.APIAddr)
	scheme0 := "http"
	if cfg.TLSCert != "" {
		scheme0 = "https"
	}
	env.ContainerAPI = scheme0 + "://host.docker.internal:" + apiPort
	var workloadLn net.Listener
	if cfg.TLS() {
		// Workloads can't verify the API's certificate under host.docker.internal:
		// give them a plain-HTTP endpoint only they can reach.
		if addr, ok := workloadListenAddr(goruntime.GOOS, dk.BridgeGateway(), dk.Self() != ""); ok {
			if ln, err := net.Listen("tcp", addr); err != nil {
				logf("workload endpoint on %s: %v (workloads will call the HTTPS API)", addr, err)
			} else {
				workloadLn = ln
				env.ContainerAPI = "http://host.docker.internal:" + strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
			}
		}
	}

	secSvc, err := secrets.New(env)
	if err != nil {
		return err
	}
	iamSvc := iam.New(env)
	iamSvc.Seal = secSvc
	boot, err := iamSvc.Bootstrap()
	if err != nil {
		return fmt.Errorf("bootstrap iam: %w", err)
	}
	scheme := "http"
	if cfg.TLSCert != "" {
		scheme = "https"
	}
	endpoint := scheme + "://" + cfg.APIAddr
	fixWildcardEndpoint(cfg)
	if boot != nil {
		creds := Credentials{Endpoint: ClientEndpoint(cfg), AccessKeyID: boot.AccessKeyID, SecretAccessKey: boot.SecretKey, Region: cfg.Region}
		if cfg.TLSCert == cfg.Path("tls", "cert.pem") {
			creds.CAFile = cfg.TLSCert // self-signed: let the CLI trust it
		}
		if err := writeCredentials(cfg, creds); err != nil {
			return err
		}
		logf("first start: created account %s", boot.AccountID)
		logf("  console sign-in   user: root   password: %s", boot.RootPassword)
		logf("  CLI credentials written to %s", CredentialsPath(cfg.DataDir))
		if selfName != "" {
			logf("  AWS CLI on this machine: eval \"$(docker exec %s homecloud aws-env)\"", selfName)
		}
		logf("  (the password is shown only once; reset it with `homecloud serve --reset-root-password`, or choose one with `homecloud admin set-root-password` while the server is stopped)")
	}
	if opts.ResetRootPassword {
		pw, err := iamSvc.ResetRootPassword()
		if err != nil {
			return err
		}
		logf("root console password reset to: %s", pw)
	}

	cw, err := cloudwatch.New(env)
	if err != nil {
		return err
	}
	vpcSvc := vpc.New(env)
	if err := vpcSvc.EnsureDefault(ctx); err != nil {
		return fmt.Errorf("default vpc: %w", err)
	}
	ec2Svc := ec2.New(env, vpcSvc)
	ec2Svc.Recover()
	s3Svc := s3.New(env, secSvc)
	vpcSvc.AfterCreate = func(v vpc.VPC) {
		s3Svc.ConnectNetwork(v)
		ec2Svc.VPCCreated(v)
	}
	vpcSvc.BeforeDelete = ec2Svc.VPCDeleted
	vpcSvc.NetworkChanged = ec2Svc.NetworkChanged
	ec2Svc.Roles = ec2Roles{iamSvc}
	rdsSvc := rds.New(env, vpcSvc, secSvc)
	rdsSvc.Recover()
	lambdaSvc := lambda.New(env, cw, vpcSvc)
	lambdaSvc.Auth = iamSvc
	sqsSvc := sqs.New(env)
	lambdaSvc.Queues = sqsSvc
	snsSvc := sns.New(env, sqsSvc, lambdaSvc)
	cw.Notify = snsSvc.Notify
	ddb, err := dynamodb.New(env)
	if err != nil {
		return err
	}
	defer ddb.Close()
	kmsSvc := kms.New(env, secSvc)
	ssmSvc := ssm.New(env, kmsSvc)
	secSvc.KMS, ssmSvc.Secrets = kmsSvc, secSvc
	// Secrets Manager runs rotation functions through Lambda.
	secSvc.Lambda = secrets.InvokerFunc(func(ctx context.Context, name string, payload []byte) ([]byte, string, error) {
		r, err := lambdaSvc.Invoke(ctx, name, payload)
		if err != nil {
			return nil, "", err
		}
		return r.Payload, r.FunctionError, nil
	})
	ecrSvc := ecr.New(env)
	elbSvc := elb.New(env, vpcSvc)
	acmSvc := acm.New(env, secSvc)
	elbSvc.Certs = acmSvc
	ec2Svc.OnTerminate = elbSvc.DropTarget
	// Deregister instances that were terminated before this hook existed.
	go func() {
		defer core.Recover("stale targets")
		alive := map[string]bool{}
		for _, i := range ec2Svc.Instances() {
			alive[i.ID] = i.State != "terminated"
		}
		for _, id := range elbSvc.TargetIDs() {
			if strings.HasPrefix(id, "i-") && !alive[id] {
				elbSvc.DropTarget(id)
			}
		}
	}()
	acmSvc.InUse, acmSvc.OnRenew = elbSvc.UsesCertificate, elbSvc.CertificateRenewed
	acmSvc.UsedBy = elbSvc.CertificateUsers
	ecsSvc := ecs.New(env, vpcSvc, elbSvc, secSvc)
	elbSvc.Resolve = func(id string) (string, string, bool) {
		if ip, v, ok := ec2Svc.PrivateIP(id); ok {
			return ip, v, true
		}
		return ecsSvc.PrivateIP(id)
	}
	elbSvc.Recover() // resolvers and certificates are wired by now
	eventsSvc := events.New(env)
	sfnSvc := sfn.New(env)
	cfnSvc := cfn.New(env)
	cfnSvc.Refresh = iamSvc.Refresh
	cfnSvc.RolePrincipal = func(roleARN string) (*httpx.Principal, error) {
		return iamSvc.ServiceRolePrincipal(roleARN, "cloudformation.amazonaws.com", "HomeCloudCloudFormation")
	}
	cfnSvc.Recover()
	cognitoSvc := cognito.New(env, secSvc)
	asgSvc := autoscaling.New(env, ec2Svc, elbSvc, cw)
	dnsSvc := route53.New(env, vpcSvc)
	ec2Svc.DNSFor, ecsSvc.DNSFor, lambdaSvc.DNSFor = dnsSvc.DNSFor, dnsSvc.DNSFor, dnsSvc.DNSFor
	dnsSvc.Resolve = func(id string) (string, bool) {
		if ip, _, ok := ec2Svc.PrivateIP(id); ok {
			return ip, true
		}
		if ip, _, ok := ecsSvc.PrivateIP(id); ok {
			return ip, true
		}
		if ip, ok := rdsSvc.PrivateIP(id); ok {
			return ip, true
		}
		return elbSvc.PrivateIP(id)
	}
	lambdaSvc.VerifyJWT = cognitoSvc.VerifyToken
	lambdaSvc.CheckAuthorizer = cognitoSvc.CheckClient
	tg := &targets{lambda: lambdaSvc, sqs: sqsSvc, sns: snsSvc, sfn: sfnSvc, account: account}
	sfnSvc.Tasks = tg
	sfnSvc.Call = func(ctx context.Context, p *httpx.Principal, service, op string, in any) (json.RawMessage, error) {
		return awsapi.Call(ctx, p, account, service, op, in)
	}
	sfnSvc.Role = func(arn string) (*httpx.Principal, error) {
		return iamSvc.ServiceRolePrincipal(arn, "states.amazonaws.com", "HomeCloudStepFunctions")
	}
	eventsSvc.Deliver, eventsSvc.Exists = tg.deliver, tg.exists
	cw.Deliver = tg.deliver
	trailSvc, err := trail.New(env)
	if err != nil {
		return err
	}

	backup := &system.Backup{Cfg: cfg, Docker: dk, AccountID: account, Version: Version,
		Snapshots: map[string]func(io.Writer) error{"dynamodb.db": ddb.Snapshot}}
	mux := http.NewServeMux()
	rt := &httpx.Router{Mux: mux, Auth: iamSvc, Account: account, Audit: trailSvc.Record}
	for _, s := range []routable{iamSvc, secSvc, cw, vpcSvc, ec2Svc, s3Svc, rdsSvc, lambdaSvc, sqsSvc, snsSvc, ddb, eventsSvc, kmsSvc, ssmSvc, ecrSvc, elbSvc, ecsSvc, sfnSvc, cfnSvc, cognitoSvc, asgSvc, acmSvc, dnsSvc, trailSvc, backup} {
		s.Routes(rt)
	}
	iamSvc.RegisterAWS()
	cfnSvc.RegisterAWS()
	sqsSvc.RegisterAWS()
	snsSvc.RegisterAWS()
	secSvc.RegisterAWS()
	kmsSvc.RegisterAWS()
	ssmSvc.RegisterAWS()
	ddb.RegisterAWS()
	wireLambda(lambdaSvc, iamSvc, tg, eventsSvc, s3Svc, ecrSvc)
	lambdaSvc.Streams = ddbStreams{ddb}
	s3Svc.RegisterAWS()
	cw.RegisterAWS()
	eventsSvc.RegisterAWS()
	sfnSvc.RegisterAWS()
	ec2Svc.RegisterAWS()
	ec2Svc.RegisterEFSAWS()
	elbSvc.RegisterAWS()
	rdsSvc.RegisterAWS()
	ecrSvc.RegisterAWS()
	ecsSvc.RegisterAWS()
	trailSvc.RegisterAWS()
	cognitoSvc.RegisterAWS()
	wireTrail(trailSvc, s3Svc)
	ecsSvc.Roles, ecsSvc.Params, ecsSvc.RegistryHost = ecsRoles{iamSvc}, ssmSvc, ecrSvc.Host()
	ecsSvc.Logs = func(group, stream string, ts []time.Time, msgs []string) error {
		evs := make([]cloudwatch.LogEvent, len(msgs))
		for i := range msgs {
			evs[i] = cloudwatch.LogEvent{Timestamp: ts[i], Message: msgs[i]}
		}
		return cw.Append(group, stream, evs...)
	}
	asgSvc.RegisterAWS()
	dnsSvc.RegisterAWS()
	acmSvc.RegisterAWS()
	ec2Svc.TemplateInUse = asgSvc.TemplateInUse
	awsHandler := &awsapi.Handler{Creds: iamSvc, Account: account, Audit: trailSvc.Record}
	if len(httpx.Unscoped) > 0 {
		return fmt.Errorf("internal error: routes without a resource ARN: %v", httpx.Unscoped)
	}
	started := time.Now()
	mux.HandleFunc("GET /api/v1/health", func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteJSON(w, 200, map[string]any{"status": "ok", "version": Version, "region": cfg.Region, "uptime_seconds": int(time.Since(started).Seconds())})
	})
	// The instance metadata service, reached through the homecloud-imds helper.
	mux.Handle(ec2.IMDSPath, ec2Svc.IMDSHandler())
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteError(w, core.Errf(http.StatusNotFound, "UnknownOperation", "no API route for %s %s", r.Method, r.URL.Path))
	})
	mux.Handle("/", web.Handler())
	cfnSvc.Handler = mux

	// Background work.
	go cw.Run(ctx)
	go rdsSvc.Run(ctx)
	go lambdaSvc.Run(ctx)
	go lambdaSvc.PollQueues(ctx)
	go sqsSvc.Run(ctx)
	go ddb.Run(ctx)
	go eventsSvc.Run(ctx)
	go elbSvc.Run(ctx)
	go ecsSvc.Run(ctx)
	go asgSvc.Run(ctx)
	go trailSvc.Run(ctx)
	go dnsSvc.Run(ctx)
	go ec2Svc.RunIMDS(ctx)
	go vpcSvc.RunFirewall(ctx)
	go func() {
		if err := ecrSvc.Start(ctx); err != nil {
			logf("ecr: %v", err)
		} else {
			logf("ecr: registry ready at %s", ecrSvc.Host())
		}
	}()
	go func() {
		if err := s3Svc.Start(ctx, vpcList(st)); err != nil {
			logf("s3: %v", err)
		} else {
			logf("s3: MinIO ready at %s", s3Svc.Endpoint())
		}
	}()
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			trailSvc.Prune()
			secSvc.PurgeExpired()
			iamSvc.PurgeExpired()
			kmsSvc.Maintain()
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()

	native := withCORS(sandboxUserContent(mux))
	root := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, s3.PresignPrefix) {
			s3Svc.ServePresigned(w, r)
			return
		}
		// AWS SDK/CLI requests (SigV4-signed, or X-Amz-Target) take the AWS protocols.
		if awsapi.Match(r) && !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			awsHandler.ServeHTTP(w, r)
			return
		}
		native.ServeHTTP(w, r)
	})
	trust, err := httpx.ParseTrustedProxies(cfg.TrustedProxies)
	if err != nil {
		return err
	}
	var handler http.Handler = trust.Wrap(root)
	srv := &http.Server{Addr: cfg.APIAddr, Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 256 << 10}
	if workloadLn != nil {
		// Workloads are never proxies: their listeners ignore forwarding headers.
		defer serveWorkloads(workloadLn, root, logf)()
		logf("workloads reach the API over plain HTTP at %s (not reachable from outside this machine)", env.ContainerAPI)
	}
	errc := make(chan error, 1)
	go func() {
		if cfg.TLSCert != "" {
			errc <- srv.ListenAndServeTLS(cfg.TLSCert, cfg.TLSKey)
			return
		}
		errc <- srv.ListenAndServe()
	}()
	// On Linux, containers reach the host through the bridge gateway, which a
	// loopback-only API address doesn't cover: listen there too.
	if goruntime.GOOS == "linux" && dk.Self() == "" {
		if host, _, _ := net.SplitHostPort(cfg.APIAddr); host == "127.0.0.1" || host == "localhost" {
			if gw := dk.BridgeGateway(); gw != "" {
				extra := &http.Server{Addr: net.JoinHostPort(gw, apiPort), Handler: root, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 256 << 10}
				go func() {
					var err error
					if cfg.TLSCert != "" {
						err = extra.ListenAndServeTLS(cfg.TLSCert, cfg.TLSKey)
					} else {
						err = extra.ListenAndServe()
					}
					if err != nil && !errors.Is(err, http.ErrServerClosed) {
						logf("container endpoint %s: %v", extra.Addr, err)
					}
				}()
				defer extra.Close()
			}
		}
	}
	logf("HomeCloud %s listening on %s (data: %s)", Version, endpoint, cfg.DataDir)
	select {
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := srv.Shutdown(sctx)
		time.Sleep(300 * time.Millisecond) // let background loops flush state
		return err
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func vpcList(st *store.Store) []vpc.VPC { return store.List[vpc.VPC](st, "vpc_vpcs") }

// inlineObjectCSP is the policy of object bytes shown from the native API: an
// opaque origin, no scripts, forms or navigation, and only inline styles and
// data:/same-origin media.
const inlineObjectCSP = "sandbox; default-src 'none'; style-src 'unsafe-inline'; img-src data: 'self'; media-src data: 'self'; font-src data:; form-action 'none'; base-uri 'none'"

// sandboxUserContent isolates responses whose bytes come from users (static
// websites, function URLs, HTTP APIs and inline object views). They share an
// origin with the console, so without a sandbox a page could read the console's
// session token. The CSP sandbox gives them an opaque origin.
func sandboxUserContent(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if strings.HasPrefix(p, "/api/v1/s3/") && strings.HasSuffix(p, "/object") {
			// The console opens objects with ?access_token=<session> in the URL, which
			// a script in the object could read from its own location and send away.
			// So inline views run no scripts, load nothing from elsewhere and send no Referer.
			w.Header().Set("Content-Security-Policy", inlineObjectCSP)
			w.Header().Set("X-Content-Type-Options", "nosniff")
		} else if strings.HasPrefix(p, "/website/") || strings.HasPrefix(p, "/lambda-url/") || strings.HasPrefix(p, "/apigw/") {
			w.Header().Set("Content-Security-Policy", "sandbox allow-scripts allow-forms allow-popups allow-modals allow-downloads")
			w.Header().Set("X-Content-Type-Options", "nosniff")
		}
		if strings.HasPrefix(p, "/api/") {
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Referrer-Policy", "no-referrer")
		}
		if csp := w.Header().Get("Content-Security-Policy"); csp != "" {
			// Functions and HTTP integrations choose their own response headers: pin
			// the sandbox so they cannot replace it and run on the console's origin.
			w = &pinnedHeaders{ResponseWriter: w, pins: map[string]string{"Content-Security-Policy": csp, "X-Content-Type-Options": "nosniff"}}
		}
		h.ServeHTTP(w, r)
	})
}

// pinnedHeaders re-applies fixed headers just before the response is written,
// whatever the handler set in between.
type pinnedHeaders struct {
	http.ResponseWriter
	pins map[string]string
}

func (p *pinnedHeaders) pin() {
	for k, v := range p.pins {
		p.Header().Set(k, v)
	}
}
func (p *pinnedHeaders) WriteHeader(code int) { p.pin(); p.ResponseWriter.WriteHeader(code) }
func (p *pinnedHeaders) Write(b []byte) (int, error) {
	p.pin()
	return p.ResponseWriter.Write(b)
}
func (p *pinnedHeaders) Flush() {
	p.pin()
	if f, ok := p.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
func (p *pinnedHeaders) Unwrap() http.ResponseWriter { return p.ResponseWriter }

// withCORS allows the console dev server and other origins to call the API.
// Credentials travel in the Authorization header, never cookies, so a wildcard is safe.
func withCORS(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		userContent := strings.HasPrefix(p, "/apigw/") || strings.HasPrefix(p, "/lambda-url/") || strings.HasPrefix(p, "/website/")
		if o := r.Header.Get("Origin"); o != "" && !userContent { // HTTP APIs apply their own CORS setting
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-HC-Meta-*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Expose-Headers", "Content-Disposition, ETag")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		h.ServeHTTP(w, r)
	})
}

func writeCredentials(cfg core.Config, c Credentials) error {
	b, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(CredentialsPath(cfg.DataDir), b, 0o600)
}
