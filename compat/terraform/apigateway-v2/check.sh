#!/usr/bin/env bash
# The Lambda routes answer, and the JWT route refuses a request without a token.
set -euo pipefail
api=$($TF output -raw api_endpoint)
curl -fsS "$api/hello" | grep -q '"path": "/hello"'
curl -fsS -X POST "$api/items/42" | grep -q '"path": "/items/42"'
code=$(curl -s -o /dev/null -w '%{http_code}' "$api/private")
[ "$code" = 401 ] || { echo "GET /private without a token returned $code"; exit 1; }
echo "apigateway-v2 ok"
