# Adding tests

The common path for a new fixture-backed federation scenario is intentionally
small: write one design record, add one readable Go subtest, and compose the
existing setup, fault, observation, and assertion helpers. You do not need to
understand container startup or report generation unless the scenario requires
a new kind of dependency or evidence source.

## Choose the smallest test tier

| What changed | Put the test here | Docker required |
| --- | --- | --- |
| Pure invariant logic | `assertions/*_test.go` | No |
| Fixture protocol or bounded-fault behavior | `internal/fixture/*_test.go` | No |
| Report parsing or evidence output | `reports/*_test.go` | No |
| Real API coordination against deterministic dependencies | `e2e/federation_test.go` | Yes |
| InterUSS protocol fidelity | real-DSS profile (planned) | Yes |
| Pod, DNS, resource, or Kubernetes network failure | `deploy/chaos-mesh/` | Kubernetes |

Do not promote a service-level unit or migration test into this repository. The
harness exists for claims that cross service or authority boundaries.

## Add a fixture-backed federation scenario

### 1. Write the safety story first

Copy `scenarios/_template.yaml`, give it a stable snake-case name, and fill in:

- one invariant the scenario is trying to falsify;
- the baseline state and named checkpoint;
- one fault bounded by count or duration;
- the operation with the uncertain outcome;
- the independent authorities that prove the result.

Add the story to `scenarios/catalog.md` with an honest status. YAML is a design
record today; it does not become executable merely by being added.

### 2. Copy this subtest shape

Add the subtest under `TestFederation` in `e2e/federation_test.go`:

```go
t.Run("example_failure_fails_closed_then_recovers", func(t *testing.T) {
	resetFederation(t)
	intentID := "01234567-89ab-4cde-8fab-0123456789ab"
	createSubmittedIntent(t, intentID)

	clearFault := armPeerDetailsFailures(t, 3)
	postExpect(t, "/api/v1/operational-intents/"+intentID+"/accept", nil, http.StatusOK)
	awaitCoordination(t, intentID, func(state map[string]any) bool {
		return state["sync_status"] == "retrying"
	})
	assertNoDSSReference(t, intentID)

	clearFault()
	awaitCoordination(t, intentID, func(state map[string]any) bool {
		return state["sync_status"] == "confirmed" &&
			state["confirmed_state"] == "Accepted"
	})
	recordCheckpoint(t, "then", "publication recovered", "pass", nil)
})
```

Use a unique, valid UUID for every operation. `resetFederation` clears local
PostGIS, fixture state, fixture faults, and Toxiproxy state before the subtest.
The suite provisions the containers once and resets authorities between stories;
individual tests must not start their own shared database or use fixed ports.

### 3. Compose the existing scenario verbs

These helpers are the supported starting vocabulary:

| Need | Helper |
| --- | --- |
| Isolated baseline | `resetFederation` |
| Submitted local operation | `createSubmittedIntent` |
| Non-overlapping peer reference | `createNonOverlappingPeerReference` |
| Accepted publication and confirmation | `publishAcceptedAndAwaitConfirmation` |
| Local activation | `activateAndRequireLocalActive` |
| Commit followed by a lost DSS response | `dropNextDSSCreateResponseAfterCommit` |
| Bounded peer-detail `500` responses | `armPeerDetailsFailures` |
| Bounded fixture latency | `armFixtureLatency` |
| Bounded state observation | `awaitCoordination` / `awaitCoordinationWithin` |
| Fixture journal and DSS state | `fixtureEvents` / `fixtureReferences` |
| Local/DSS invariant snapshot | `snapshotInvariant` / `assertInvariant` |
| Explainable evidence step | `recordCheckpoint` |

Read an existing scenario with the closest failure shape and compose these verbs
before adding plumbing. Keep domain decisions in the subtest and hide only
mechanics in helpers; a reviewer should be able to read Given, When, and Then
from the test body without chasing implementation details.

### 4. Make cleanup and observation bounded

Every enabled fault must register `t.Cleanup` immediately. A helper may also
return an idempotent cleanup function when the test needs to recover mid-story.
Never rely on the happy path to remove a fault.

Use the bounded polling helpers for eventually consistent state. Do not add a
fixed sleep as an assertion, do not retry an entire failed scenario, and do not
treat a timeout as success. Record meaningful state transitions so the evidence
bundle explains how convergence failed.

### 5. Assert independent authorities

An API response only proves what the API returned. A federation scenario should
normally compare at least two of:

- local workflow/publication state in PostGIS;
- DSS reference state and version/OVN;
- peer or DSS fixture journal;
- API process lifecycle or lease timestamps;
- network-fault control state.

Put reusable cross-authority rules in `assertions/`. Add a tripwire when
practical: install a known-forbidden state and prove the assertion rejects it.

## When an existing verb is not enough

Extend only the narrowest layer required:

1. add a generic fault shape in `faults/` only if no existing kind represents
   the failure;
2. implement deterministic protocol behavior in `internal/fixture/` and cover
   it with a non-Docker unit test;
3. expose container lifecycle or endpoints through `internal/stack/` and
   `environments/docker/`;
4. add a small `t.Helper()` in `e2e/` so the scenario body stays story-shaped;
5. add or extend an invariant probe when the safety claim needs a new authority.

`scenarios.Runner` is a reusable Given/When/Then execution primitive, but the
current Docker suite does not compile YAML or run its catalog through that
runner. Do not build a parallel scenario DSL as part of an ordinary test. A
future executable catalog needs its own schema, validation, versioning, and
migration design.

## Run one scenario

Fast checks do not start Docker:

```bash
go test ./...
go vet ./...
```

Run a single Docker subtest and retain its evidence bundle:

```bash
AERO_ARC_E2E_RUN='TestFederation/example_failure_fails_closed_then_recovers$' \
  ./scripts/run-e2e.sh
```

The run must produce `go-test.json`, summaries, JUnit, timeline events, and a
stack manifest. On failure, verify container logs were captured before teardown.

## Review checklist

- The scenario names exactly one invariant and one primary fault.
- The fault is bounded, reproducible, and removed with unconditional cleanup.
- The intent IDs are unique and the test uses no fixed ports or shared volumes.
- Polling is bounded and records state transitions.
- Assertions compare independent authorities rather than trusting one response.
- Catalog status distinguishes fixture, real-DSS, and Kubernetes coverage.
- The targeted scenario and fast validation pass.
- Every commit is DCO signed.
