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
