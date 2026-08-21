# Aero Arc Test Harness

Standalone end-to-end and chaos testing for Aero Arc federation workflows.

The harness is deliberately separate from every service repository. It builds
the real Aero Arc API from a sibling checkout, starts isolated dependencies with
Testcontainers, injects repeatable faults, asserts safety invariants, and writes
an evidence bundle for humans and CI.

Today this is a fixture-backed federation safety harness, not a complete
InterUSS conformance lab. It runs the real API against isolated PostGIS and a
deterministic DSS/peer fixture, injects ambiguous coordination failures, checks
cross-system invariants, and produces replayable evidence bundles. The next
fidelity milestone is replacing the fixture with a real InterUSS DSS between
two independently provisioned Aero Arc USS instances.

## What the first slice covers

- real `aero-arc-api` container built from local source;
- pinned PostGIS dependency on a private, per-run Docker network;
- stateful DSS and peer-USS protocol fixture with a control plane and event log;
- Toxiproxy between Aero Arc and the fixture for latency, timeouts, resets, and
  partitions;
- invariant assertions across authoritative PostgreSQL and simulated DSS state;
- bounded polling, deterministic seeds, per-scenario deadlines, and cleanup;
- JSONL events, raw `go test -json`, JSON summary, Markdown summary, JUnit XML,
  and failure-time container logs;
- optional Chaos Mesh workflows for Kubernetes-only faults.

The executable fixture tier currently covers happy-path activation, a lost DSS
create response, stale-OVN withdrawal, peer-detail `500` fail-closed recovery,
DSS latency through Toxiproxy, worker death and lease-expiry takeover, and
tripwires that prove the invariant checker rejects known-bad states.

The deterministic fixture is for failure semantics and orchestration speed. A
real InterUSS DSS profile remains a separate fidelity gate; passing the fixture
tier must never be presented as full standards conformance.

## Repository layout

```text
cmd/fault-fixture/       Stateful DSS + peer-USS test double
cmd/e2e-report/          go-test JSON to JSON/Markdown/JUnit reports
cmd/e2e-repeat-report/   Aggregate repeatability and timing evidence
internal/stack/          Testcontainers topology and artifact collection
internal/toxiproxy/      Small, explicit Toxiproxy control client
e2e/                     Cross-service scenarios and invariant checks
scenarios/               Scenario catalog and expected safety properties
deploy/chaos-mesh/       Opt-in Kubernetes chaos workflow
scripts/run-e2e.sh       Repeatable local/CI entry point
scripts/repeat-e2e.sh    Multi-run determinism check and aggregate report
```

## Requirements

- Go 1.26.2 or newer;
- Docker Engine;
- sibling source checkout at `../aero-arc-api` by default;
- enough disk to build the API image and pull pinned test images.

No host ports are fixed. Concurrent runs are isolated by Testcontainers network
and resource labels.

`AERO_ARC_E2E_SEED` is currently provenance: it identifies artifact paths,
resource labels, and manifests so a run can be correlated and selected again.
The fixture scenarios themselves contain no random choices yet. When seeded
variation is introduced, the same value will drive that variation; until then,
reports must not imply that changing the seed changes scenario behavior.

## Quick start

Run the fast non-Docker validation:

```bash
make test
```

Run the Docker E2E tier:

```bash
./scripts/run-e2e.sh
```

Run the deterministic suite repeatedly and produce aggregate p95/max timing:

```bash
AERO_ARC_E2E_REPETITIONS=20 ./scripts/repeat-e2e.sh
```

Generate a deliberately failing artifact to fire-drill the invariant and
reporting pipeline (a non-zero exit is the expected result):

```bash
AERO_ARC_E2E_RUN='TestFailureArtifact$' \
  AERO_ARC_E2E_DEMONSTRATE_FAILURE=true \
  ./scripts/run-e2e.sh
```

Override the Aero Arc API checkout or select scenarios:

```bash
AERO_ARC_API_SOURCE=/path/to/aero-arc-api \
AERO_ARC_E2E_RUN='TestFederation/(happy_path|stale_OVN)' \
./scripts/run-e2e.sh
```

Each run writes to `artifacts/<UTC timestamp>-<seed>/`. Failed runs additionally
contain logs for every started container and a stack manifest with image names,
allocated endpoints, and the recorded seed.

## Test tiers

| Tier | Runs | Purpose |
| --- | --- | --- |
| Unit | every change | Fixture, report, retry, and control-plane correctness |
| Docker E2E | PR/scheduled/manual | Real API + PostGIS + fixture + Toxiproxy |
| Real DSS | nightly/release | Aero Arc against the pinned local InterUSS DSS sandbox |
| Chaos Mesh | scheduled/manual | Pod, network, DNS, clock, I/O, and resource failures |

The Docker tier is the default development loop. The real-DSS and Kubernetes
tiers are intentionally slower and should publish their result bundles even
when setup fails.

## Scenario design

Every scenario follows the same phases:

1. establish a known baseline and record the seed;
2. drive the workflow to a named checkpoint;
3. arm exactly one bounded fault;
4. perform the operation whose outcome may become ambiguous;
5. remove the fault in unconditional cleanup;
6. observe convergence with bounded polling;
7. assert the safety invariant and absence of forbidden states;
8. persist the event timeline and service logs.

Start with one fault at a time. Multi-fault scenarios belong in a separate soak
suite after each individual failure has a deterministic oracle.

The YAML files under `scenarios/` are design records, not executable inputs.
The tagged Go tests are the source of truth for current coverage. Converting the
YAML catalog into executable input would require a separate, versioned compiler
and validation milestone; until then each YAML file links to its Go test status.

## Result bundle

The result directory is intended to be attached to CI runs or incident reviews:

```text
go-test.json       complete Go test event stream
summary.json       counts, durations, failures, and skipped tests
summary.md         compact human-readable report
junit.xml          CI test report
events.jsonl       harness and fault timeline
stack.json         seed, images, IDs, and allocated endpoints
logs/*.log         service logs captured before teardown on failure
```

See [Architecture](docs/ARCHITECTURE.md) for the trust boundaries and
[Chaos Mesh](deploy/chaos-mesh/README.md) for the Kubernetes tier.

## License

Mozilla Public License 2.0. See [LICENSE](LICENSE).
