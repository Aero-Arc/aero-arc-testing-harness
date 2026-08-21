#!/usr/bin/env bash
set -euo pipefail

if [[ $# -lt 1 || $# -gt 2 ]]; then
  printf 'usage: %s <base-ref> [head-ref]\n' "$0" >&2
  exit 2
fi

base_ref=$1
head_ref=${2:-HEAD}

git rev-parse --verify "${base_ref}^{commit}" >/dev/null
git rev-parse --verify "${head_ref}^{commit}" >/dev/null

failed=0
while IFS= read -r commit; do
  author_identity=$(git show -s --format='%an <%ae>' "${commit}")
  committer_identity=$(git show -s --format='%cn <%ce>' "${commit}")
  message=$(git show -s --format='%B' "${commit}")
  trailers=$(git interpret-trailers --parse <<<"${message}")

  if grep -Fxiq -- "Signed-off-by: ${author_identity}" <<<"${trailers}"; then
    continue
  fi
  if grep -Fxiq -- "Signed-off-by: ${committer_identity}" <<<"${trailers}"; then
    continue
  fi

  subject=$(git show -s --format='%s' "${commit}")
  printf 'DCO failure: %s %s\n' "${commit:0:12}" "${subject}" >&2
  printf '  expected Signed-off-by: %s\n' "${author_identity}" >&2
  if [[ "${committer_identity}" != "${author_identity}" ]]; then
    printf '  or       Signed-off-by: %s\n' "${committer_identity}" >&2
  fi
  failed=1
done < <(git rev-list --reverse "${base_ref}..${head_ref}")

if [[ ${failed} -ne 0 ]]; then
  printf 'Add the trailer with git commit --signoff; see CONTRIBUTING.md.\n' >&2
  exit 1
fi

printf 'DCO check passed for %s..%s\n' "${base_ref}" "${head_ref}"
