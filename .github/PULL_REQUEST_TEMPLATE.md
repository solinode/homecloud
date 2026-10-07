## What and why

<!-- What does this change, and why? Link the issue: "Fixes #123". -->

## How it was tested

<!-- Which tests ran (and did not skip)? For AWS operations: the AWS CLI / boto3 test in the service's aws_test.go.
     Docker-backed tests skip without a reachable Docker; on macOS set DOCKER_HOST (see CONTRIBUTING.md). -->

- [ ] `make test` (or the affected packages: `go test ./internal/svc/<name>/`)
- [ ] AWS CLI / boto3 compatibility tests ran (`HC_TEST_PYTHON` set, `aws` on `PATH`)
- [ ] Docker-backed tests ran (not skipped), if the change touches containers
- [ ] Console: `npx tsc --noEmit && npm run build`, if the change touches `console/`

## Checklist

- [ ] New routes and AWS operations authorize with the right IAM action and resource ARN
- [ ] Docs updated: `docs/aws-compat.md` (operations, differences from AWS), `docs/architecture.md`, `docs/api.md` (`python3 scripts/gen-api-docs.py`) as needed
- [ ] A line under "Unreleased" in `CHANGELOG.md` for user-visible changes
