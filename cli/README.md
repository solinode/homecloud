# HomeCloud server and CLI

This module builds the single `homecloud` binary: the API server (`homecloud serve`), the
embedded web console and the CLI for every service.

```bash
go build -o ../bin/homecloud .   # or `make build` from the repository root
go test ./...                    # set HC_TEST_PYTHON to a Python with boto3 for AWS tests
```

See the repository [README](../README.md), [architecture](../docs/architecture.md) and
[AWS compatibility](../docs/aws-compat.md) docs.
