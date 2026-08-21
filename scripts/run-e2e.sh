#!/usr/bin/env bash
set -uo pipefail

repo_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
seed=${AERO_ARC_E2E_SEED:-$(date -u +%s)}
run_id=$(date -u +%Y%m%dT%H%M%SZ)-${seed}
artifact_dir=${AERO_ARC_E2E_ARTIFACT_DIR:-${repo_dir}/artifacts/${run_id}}
test_pattern=${AERO_ARC_E2E_RUN:-'Test(Federation|InvariantTripwires)$'}

if ! command -v docker >/dev/null 2>&1; then
  printf 'error: Docker is required for the Aero Arc E2E tier but was not found\n' >&2
  exit 2
fi
if ! docker info >/dev/null 2>&1; then
  printf 'error: Docker is installed but its daemon is unavailable; start Docker and retry\n' >&2
  exit 2
fi
api_source=${AERO_ARC_API_SOURCE:-${repo_dir}/../aero-arc-api}
if [[ ! -f "${api_source}/Dockerfile" || ! -f "${api_source}/go.mod" ]]; then
  printf 'error: Aero Arc API source was not found at %s; set AERO_ARC_API_SOURCE\n' "${api_source}" >&2
  exit 2
fi
export AERO_ARC_API_SOURCE=${api_source}

mkdir -p "${artifact_dir}"
export AERO_ARC_E2E_SEED=${seed}
export AERO_ARC_E2E_ARTIFACT_DIR=${artifact_dir}

set +e
go test -tags=e2e -count=1 -timeout=12m -run "${test_pattern}" -json ./e2e >"${artifact_dir}/go-test.json"
test_status=$?
set -e

go run ./cmd/e2e-report -input "${artifact_dir}/go-test.json" -output "${artifact_dir}" -run-id "${run_id}"
printf 'Aero Arc E2E artifacts: %s\n' "${artifact_dir}"
exit "${test_status}"
