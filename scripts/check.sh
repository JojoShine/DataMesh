#!/bin/sh
set -eu
cd "$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
case "${1:-contracts}" in
  contracts)
    .local/tools/bin/python -m unittest discover -s tests/contracts -v
    .local/tools/bin/python -m openapi_spec_validator api/openapi/platform.yaml
    ;;
  environment)
    scripts/dev-env.sh preflight "${2:-base}"
    docker compose --project-name datamesh --env-file .local/runtime.env config --quiet
    ;;
  tooling)
    python3 -m unittest discover -s tests/tooling -v
    python3 -m compileall -q scripts tests/contracts tests/tooling
    ;;
  go)
    docker compose --project-name datamesh --env-file .local/runtime.env run --rm go-tools sh -c 'test -z "$(gofmt -l cmd internal tests/integration)" && go vet ./... && go test ./internal/... && go build ./cmd/...'
    ;;
  integration)
    exec python3 scripts/test_identity.py
    ;;
  governance-code)
    test -n "${GOVERNANCE_CODE_QA_STATE:-}" || { echo "Set GOVERNANCE_CODE_QA_STATE to an isolated runner state file" >&2; exit 2; }
    python3 scripts/smoke/governance_code.py --state "$GOVERNANCE_CODE_QA_STATE"
    ;;
  data)
    exec python3 scripts/check_data.py
    ;;
  web)
    npm --prefix web run check
    npm --prefix web test
    npm --prefix web run build
    ;;
  business)
    python3 scripts/bootstrap_business.py
    python3 scripts/smoke/business.py
    .local/tools/bin/python scripts/smoke/contracts_http.py
    ;;
  all)
    "$0" contracts
    "$0" environment
    "$0" tooling
    "$0" go
    "$0" integration
    "$0" data
    python3 scripts/smoke/identity.py
    "$0" web
    "$0" business
    python3 scripts/smoke/restore.py
    ;;
  *) echo 'usage: scripts/check.sh contracts|environment|tooling|go|integration|governance-code|data|web|business|all' >&2; exit 2 ;;
esac
