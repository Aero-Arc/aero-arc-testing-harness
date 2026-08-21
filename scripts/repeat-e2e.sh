#!/usr/bin/env bash
set -uo pipefail

repo_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
repetitions=${AERO_ARC_E2E_REPETITIONS:-20}
if [[ ! "${repetitions}" =~ ^[1-9][0-9]*$ ]]; then
  printf 'error: AERO_ARC_E2E_REPETITIONS must be a positive integer\n' >&2
  exit 2
fi
base_seed=${AERO_ARC_E2E_SEED:-$(date -u +%s)}
repeat_id=$(date -u +%Y%m%dT%H%M%SZ)-repeat-${repetitions}
repeat_dir=${AERO_ARC_E2E_REPEAT_DIR:-${repo_dir}/artifacts/${repeat_id}}
mkdir -p "${repeat_dir}"

status=0
for iteration in $(seq 1 "${repetitions}"); do
  seed=$((base_seed + iteration - 1))
  run_dir=$(printf '%s/run-%03d-seed-%s' "${repeat_dir}" "${iteration}" "${seed}")
  printf '\n[%d/%d] seed=%s\n' "${iteration}" "${repetitions}" "${seed}"
  if ! AERO_ARC_E2E_SEED="${seed}" AERO_ARC_E2E_ARTIFACT_DIR="${run_dir}" "${repo_dir}/scripts/run-e2e.sh"; then
    status=1
  fi
done

go run "${repo_dir}/cmd/e2e-repeat-report" -input "${repeat_dir}"
printf 'Aero Arc repeatability artifacts: %s\n' "${repeat_dir}"
exit "${status}"
