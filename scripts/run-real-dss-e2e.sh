#!/usr/bin/env bash
set -uo pipefail

repo_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
seed=${AERO_ARC_E2E_SEED:-$(date -u +%s)}
run_id=$(date -u +%Y%m%dT%H%M%SZ)-real-dss-${seed}
artifact_dir=${AERO_ARC_E2E_ARTIFACT_DIR:-${repo_dir}/artifacts/${run_id}}
test_pattern=${AERO_ARC_REAL_DSS_E2E_RUN:-'TestRealFederation$'}

if ! command -v docker >/dev/null 2>&1; then
  printf 'error: Docker is required for the real-DSS tier but was not found\n' >&2
  exit 2
fi
if ! docker info >/dev/null 2>&1; then
  printf 'error: Docker is installed but its daemon is unavailable; start Docker and retry\n' >&2
  exit 2
fi

api_source=${AERO_ARC_API_SOURCE:-${repo_dir}/../aero-arc-api}
interuss_source=${AERO_ARC_INTERUSS_SOURCE:-${repo_dir}/../interuss-dss}
if [[ ! -f "${api_source}/Dockerfile" || ! -f "${api_source}/go.mod" ]]; then
  printf 'error: Aero Arc API source was not found at %s; set AERO_ARC_API_SOURCE\n' "${api_source}" >&2
  exit 2
fi
if [[ ! -f "${interuss_source}/Dockerfile" || ! -f "${interuss_source}/build/test-certs/auth2.pem" ]]; then
  printf 'error: InterUSS DSS source was not found at %s; set AERO_ARC_INTERUSS_SOURCE\n' "${interuss_source}" >&2
  exit 2
fi

mkdir -p "${artifact_dir}"
export AERO_ARC_API_SOURCE=${api_source}
export AERO_ARC_INTERUSS_SOURCE=${interuss_source}
export AERO_ARC_API_REVISION=${AERO_ARC_API_REVISION:-$(git -C "${api_source}" rev-parse HEAD 2>/dev/null || printf unknown)}
export AERO_ARC_INTERUSS_REVISION=${AERO_ARC_INTERUSS_REVISION:-$(git -C "${interuss_source}" rev-parse HEAD 2>/dev/null || printf unknown)}
export AERO_ARC_E2E_SEED=${seed}
export AERO_ARC_E2E_ARTIFACT_DIR=${artifact_dir}

set +e
go test -tags=realdss -count=1 -timeout=25m -run "${test_pattern}" -json ./e2e >"${artifact_dir}/go-test.json"
test_status=$?
set -e

go run ./cmd/e2e-report -input "${artifact_dir}/go-test.json" -output "${artifact_dir}" -run-id "${run_id}"
printf 'Aero Arc real-DSS E2E artifacts: %s\n' "${artifact_dir}"
exit "${test_status}"
