# Aero Arc end-to-end harness contributor context

## Purpose

This repository proves cross-service safety properties under realistic failure.
It does not replace unit, component, contract, or migration tests owned by each
service repository.

## Safety rules

- Every chaos scenario must name the invariant it is trying to falsify.
- Faults are bounded by count or duration, are removed in cleanup, and must be
  reproducible from a committed seed.
- A timeout is a test failure with diagnostics; it is never an implicit pass.
- Polling assertions use bounded exponential backoff and record every observed
  state transition.
- Tests must not use fixed host ports, shared databases, or persistent volumes.
- Testcontainers owns all Docker resources and cleanup. Preserve logs before
  terminating a failed container.
- The fast Docker tier uses the deterministic fixture. Keep a separate real-DSS
  tier for protocol fidelity and a Chaos Mesh tier for Kubernetes failure modes.
- Never point destructive fixtures or chaos manifests at a non-test namespace.
  Kubernetes selectors must include `aero-arc.io/test-run`.

## Core federation invariants

- Local `active` is impossible unless the same intent version is confirmed
  `Activated` by the DSS.
- Ambiguous remote outcomes fail closed and converge through reconciliation.
- A stale OVN cannot silently withdraw or overwrite a newer DSS reference.
- Peer-detail failures and peer `5xx` responses cannot produce a false clear.
- Worker lease expiry permits takeover without duplicate authoritative commits.
- Terminal local state eventually converges to confirmed DSS withdrawal.

## DCO

- Every commit must carry a `Signed-off-by: Name <email>` trailer matching the
  commit author or committer. Create it with `git commit --signoff` (or
  `git commit -s`); never invent or copy another contributor's sign-off.
- Before pushing, run `./scripts/check-dco.sh <base-ref> HEAD`. If commits are
  rewritten to add sign-offs, coordinate with other branch users and push with
  `--force-with-lease`, never an unconditional force push.
- See `CONTRIBUTING.md` for the certificate and repair instructions.

## Adding tests

- Start with `docs/ADDING_TESTS.md` and copy `scenarios/_template.yaml` for a
  new federation story.
- Treat scenario YAML as a human-readable design record. A scenario is only
  executable when it has a tagged Go subtest in `e2e/` or an implementation in
  the tier named by its catalog status.
- Prefer the existing scenario verbs in `e2e/federation_test.go` before adding
  new fixture, fault, polling, or assertion plumbing.
- Keep the Given/When/Then flow visible in the test body. Put reusable mechanics
  in helpers and cross-authority safety decisions in `assertions/`.
- Update `scenarios/catalog.md` with the exact coverage status and invariant;
  do not describe planned real-DSS or Chaos Mesh coverage as executable.

## Validation

Run before handoff:

```bash
gofmt -w ./cmd ./internal ./e2e
go test ./...
go vet ./...
git diff --check
```

Docker scenarios are opt-in:

```bash
./scripts/run-e2e.sh
```

Verify DCO on the commits being proposed (replace `origin/main` when the base is
different):

```bash
./scripts/check-dco.sh origin/main HEAD
```
