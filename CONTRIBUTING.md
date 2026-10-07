# Contributing to HomeCloud

Thanks for helping. Bug reports, AWS compatibility gaps, docs fixes and code are all welcome. This page
gets you from a fresh clone to a tested pull request.

By participating you agree to the [Code of Conduct](CODE_OF_CONDUCT.md). Pull requests need a signed
[Contributor License Agreement](CLA.md); the CLA bot asks you on your first PR. Security problems go
through [SECURITY.md](SECURITY.md), not public issues.

## Find something to work on

- **[Good first issues](https://github.com/solinode/homecloud/issues?q=is%3Aissue+is%3Aopen+label%3A%22good+first+issue%22)**:
  small, well-scoped tasks with file pointers and acceptance criteria.
- **[Help wanted](https://github.com/solinode/homecloud/issues?q=is%3Aissue+is%3Aopen+label%3A%22help+wanted%22)**
  and the [`aws-api`](https://github.com/solinode/homecloud/issues?q=is%3Aissue+is%3Aopen+label%3Aaws-api) label.
- **Compatibility gaps you hit yourself.** If your Terraform, CLI or SDK code fails against HomeCloud,
  open a [compatibility gap](https://github.com/solinode/homecloud/issues/new?template=compatibility_gap.yml)
  issue, then consider fixing it. [docs/aws-compat.md](docs/aws-compat.md) lists what each service does
  not support yet.

Comment on an issue before starting larger work so nobody duplicates it. Ask on
[Discord](https://discord.gg/pemra9uaC9) or in [Discussions](https://github.com/solinode/homecloud/discussions)
if anything is unclear.

## Prerequisites

| Tool | Needed for |
| --- | --- |
| Go 1.25+ (see `cli/go.mod`) | the server and CLI |
| Docker: Docker Engine, Docker Desktop or OrbStack | running HomeCloud and the Docker-backed tests |
| Node.js 22+ and npm | the web console (optional if you only touch Go) |
| AWS CLI v2 | AWS compatibility tests (skipped without it) |
| Python 3 with `boto3`, `cryptography`, `pycognito` | boto3 compatibility tests (skipped without it) |
| Terraform or OpenTofu | opt-in Terraform tests |

```bash
pip install boto3 cryptography pycognito      # what CI installs
```

## Repository layout

```
cli/                    Go module: server + CLI, one binary
  cmd/                  CLI commands (`homecloud <service> ...`, `serve`, `configure`, ...)
  internal/server       wires every service into one HTTP server
  internal/svc/<name>   one package per service (ec2, s3, lambda, sqs, ...); aws*.go files hold the AWS protocol handlers
  internal/awsapi       AWS protocols: SigV4, awsJson, awsQuery, REST XML/JSON
  internal/awsapi/awstest  test harness that drives the real AWS CLI and boto3
  internal/httpx        native API routing, IAM checks, errors
  internal/runtime      Docker engine wrapper
  internal/store        persistent state
  internal/web          embeds the built console
console/                Next.js web console (static export, embedded into the binary)
docs/                   user docs, AWS compatibility, architecture, designs
examples/terraform/     end-to-end Terraform example
scripts/                install scripts, smoke test, API doc generator
pages/                  landing page and demo console (homecloud.pages.dev)
```

## Build and run

```bash
git clone https://github.com/<you>/homecloud.git && cd homecloud
make build                         # Go only: bin/homecloud, console shows a placeholder page
make                               # console + binary (needs Node.js)
./bin/homecloud serve              # API and console on http://127.0.0.1:8080
```

For console work, run the API and the Next.js dev server side by side:

```bash
cd cli && go run . serve
cd console && npm ci && NEXT_PUBLIC_API_URL=http://127.0.0.1:8080 npm run dev   # http://localhost:3000
```

`make demo` builds the static demo console (sample data, no server) into `pages/demo/`.

To try your build without touching your normal installation, give it its own data directory:
`./bin/homecloud serve --data-dir /tmp/hc-dev` (only one HomeCloud installation can own a Docker host
at a time, so stop the other one first).

## Run the tests

```bash
make test                          # cd cli && go test ./...
make vet
cd cli && go test ./internal/svc/sqs/          # one package
cd cli && go test -race -run TestFIFO ./internal/svc/sqs/
```

Many tests **skip silently** when a tool is missing. Before trusting a green run, make sure the ones that
matter for your change actually ran (`go test -v ./internal/svc/<pkg>/ 2>&1 | grep -i skip`).

**Docker.** Tests that start containers connect to Docker through the default socket and skip when it is
unreachable. On macOS the default `/var/run/docker.sock` often does not exist, so point `DOCKER_HOST` at
your engine:

```bash
export DOCKER_HOST=unix://$HOME/.orbstack/run/docker.sock   # OrbStack
export DOCKER_HOST=unix://$HOME/.docker/run/docker.sock     # Docker Desktop (macOS)
docker info >/dev/null && echo ok                           # check it works
```

If Docker tests skip with "no free 10.88-119.0.0/16 range", earlier runs leaked networks: list them with
`docker network ls --filter label=homecloud.account` and remove the ones labelled `hctest-*`.

**AWS tools.** The compatibility tests run the real tools against an in-process endpoint:

| Variable | Meaning |
| --- | --- |
| `HC_TEST_PYTHON` | Python interpreter with boto3 (default `python3`); e.g. `HC_TEST_PYTHON=$PWD/.venv/bin/python` |
| `HC_TEST_AWS` | path to the `aws` CLI (default: `aws` on `PATH`) |
| `HC_TEST_TERRAFORM` | set to run the Terraform/OpenTofu tests (they download the AWS provider; set `TF_PLUGIN_CACHE_DIR` to reuse it) |
| `HC_TEST_NODE_PATH` | a `node_modules` directory with `amazon-cognito-identity-js`, for the Cognito SRP test in Node.js |

**End to end.** With a server running, `./scripts/smoke.sh` exercises every service through the CLI and
cleans up after itself (`HC=./bin/homecloud ./scripts/smoke.sh` to choose the binary). CI runs it on Linux.

**Console.** `cd console && npx tsc --noEmit && npm run build`.

CI (`.github/workflows/ci.yml`) runs `go vet`, `go test -race`, the console build and the smoke test on
Linux. It skips changes that only touch Markdown, `docs/` or `pages/`.

## Making changes

- **A new AWS operation** goes in the service's `aws*.go` file and shares logic with the native handler.
  Read [Adding operations](docs/aws-compat.md#adding-operations-for-contributors) first. Always call
  `q.Authorize` with the IAM action and resource ARN before acting, and test it with the AWS CLI or boto3
  through `awstest` (look at the service's existing `aws_test.go`).
- **A new native route** declares its IAM action and, when it acts on one resource, that resource's ARN
  (`httpx.Res(...)`, or `httpx.Deferred()` plus `c.Authorize` in the handler); the server refuses to start
  otherwise. Regenerate the API reference with `python3 scripts/gen-api-docs.py`.
- **A new service** is a package in `cli/internal/svc/<name>` with a `Routes(*httpx.Router)` method, wired in
  `cli/internal/server/server.go`.
- Anything that delivers to another resource (a rule target, a subscription, a trigger) must check the
  caller's permission on that resource when it is configured.
- Docker objects must carry `runtime.Labels(...)` so HomeCloud never touches containers it did not create.
- Update the docs with the behavior: [docs/aws-compat.md](docs/aws-compat.md) for AWS operations and
  differences, [docs/architecture.md](docs/architecture.md) for how things work, and a line under
  "Unreleased" in [CHANGELOG.md](CHANGELOG.md) for anything a user would notice.
- Run `gofmt` (or `go vet`) before committing.

## Commits and pull requests

- Branch from `main`; keep a pull request to one topic.
- Write commit messages as a plain sentence saying what changed and, if not obvious, why
  (`SQS GetQueueAttributes returns RedriveAllowPolicy`). Reference issues with `#123`.
- Fill in the pull request template: what changed, how you tested it (which tests ran, not skipped), and the
  docs you updated.
- A maintainer reviews; expect questions about AWS behavior, since matching it closely is the point.
