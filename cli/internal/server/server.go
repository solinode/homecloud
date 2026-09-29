// Package server assembles every HomeCloud service behind one HTTP API and
// serves the web console.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/homecloudhq/homecloud/cli/internal/core"
	"github.com/homecloudhq/homecloud/cli/internal/httpx"
	"github.com/homecloudhq/homecloud/cli/internal/runtime"
	"github.com/homecloudhq/homecloud/cli/internal/store"
	"github.com/homecloudhq/homecloud/cli/internal/svc"
	"github.com/homecloudhq/homecloud/cli/internal/svc/cloudwatch"
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
	"github.com/homecloudhq/homecloud/cli/internal/svc/s3"
	"github.com/homecloudhq/homecloud/cli/internal/svc/secrets"
	"github.com/homecloudhq/homecloud/cli/internal/svc/sfn"
	"github.com/homecloudhq/homecloud/cli/internal/svc/sns"
	"github.com/homecloudhq/homecloud/cli/internal/svc/sqs"
	"github.com/homecloudhq/homecloud/cli/internal/svc/ssm"
	"github.com/homecloudhq/homecloud/cli/internal/svc/trail"
	"github.com/homecloudhq/homecloud/cli/internal/svc/vpc"
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
	env := &svc.Env{Cfg: cfg, Store: st, Docker: dk, AccountID: account}

	iamSvc := iam.New(env)
	boot, err := iamSvc.Bootstrap()
	if err != nil {
		return fmt.Errorf("bootstrap iam: %w", err)
	}
	endpoint := "http://" + cfg.APIAddr
	if boot != nil {
		if err := writeCredentials(cfg, Credentials{Endpoint: endpoint, AccessKeyID: boot.AccessKeyID, SecretAccessKey: boot.SecretKey, Region: cfg.Region}); err != nil {
			return err
		}
		logf("first start: created account %s", boot.AccountID)
		logf("  console sign-in   user: root   password: %s", boot.RootPassword)
		logf("  CLI credentials written to %s", CredentialsPath(cfg.DataDir))
		logf("  (the password is shown only once; reset it with `homecloud serve --reset-root-password`)")
	}
	if opts.ResetRootPassword {
		pw, err := iamSvc.ResetRootPassword()
		if err != nil {
			return err
		}
		logf("root console password reset to: %s", pw)
	}

	secSvc, err := secrets.New(env)
	if err != nil {
		return err
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
	s3Svc := s3.New(env, secSvc)
	vpcSvc.AfterCreate = s3Svc.ConnectNetwork
	rdsSvc := rds.New(env, vpcSvc, secSvc)
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
	ecrSvc := ecr.New(env)
	elbSvc := elb.New(env, vpcSvc)
	ecsSvc := ecs.New(env, vpcSvc, elbSvc, secSvc)
	elbSvc.Resolve = func(id string) (string, string, bool) {
		if ip, v, ok := ec2Svc.PrivateIP(id); ok {
			return ip, v, true
		}
		return ecsSvc.PrivateIP(id)
	}
	eventsSvc := events.New(env)
	sfnSvc := sfn.New(env)
	tg := &targets{lambda: lambdaSvc, sqs: sqsSvc, sns: snsSvc, sfn: sfnSvc}
	sfnSvc.Tasks = tg
	eventsSvc.Deliver, eventsSvc.Exists = tg.deliver, tg.exists
	trailSvc, err := trail.New(env)
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	rt := &httpx.Router{Mux: mux, Auth: iamSvc, Account: account, Audit: trailSvc.Record}
	for _, s := range []routable{iamSvc, secSvc, cw, vpcSvc, ec2Svc, s3Svc, rdsSvc, lambdaSvc, sqsSvc, snsSvc, ddb, eventsSvc, kmsSvc, ssmSvc, ecrSvc, elbSvc, ecsSvc, sfnSvc, trailSvc} {
		s.Routes(rt)
	}
	started := time.Now()
	mux.HandleFunc("GET /api/v1/health", func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteJSON(w, 200, map[string]any{"status": "ok", "version": Version, "region": cfg.Region, "uptime_seconds": int(time.Since(started).Seconds())})
	})
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteError(w, core.Errf(http.StatusNotFound, "UnknownOperation", "no API route for %s %s", r.Method, r.URL.Path))
	})
	mux.Handle("/", web.Handler())

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
	go func() {
		if err := ecrSvc.Start(ctx); err != nil {
			logf("ecr: %v", err)
		} else {
			logf("ecr: registry ready at %s", ecrSvc.Host())
		}
	}()
	go func() {
		var networks []string
		for _, v := range vpcList(st) {
			networks = append(networks, v.Network)
		}
		if err := s3Svc.Start(ctx, networks); err != nil {
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
			kmsSvc.Maintain()
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()

	srv := &http.Server{Addr: cfg.APIAddr, Handler: withCORS(mux), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
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

// withCORS allows the console dev server and other origins to call the API.
// Credentials travel in the Authorization header, never cookies, so a wildcard is safe.
func withCORS(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if o := r.Header.Get("Origin"); o != "" {
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
