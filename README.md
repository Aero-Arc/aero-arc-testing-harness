# Aero Arc Test Harness

Standalone end-to-end and chaos testing for Aero Arc federation workflows.

The harness is deliberately separate from every service repository. It builds
the real Aero Arc API from a sibling checkout, starts isolated dependencies with
Testcontainers, injects repeatable faults, asserts safety invariants, and writes
an evidence bundle for humans and CI.

Today the harness has two federation profiles, but is not a complete InterUSS
conformance lab. The fast profile runs the real API against isolated PostGIS and
a deterministic DSS/peer fixture. The protocol-fidelity profile provisions a
real InterUSS CockroachDB, SCD migrations, core service, and dummy OAuth between
two independent Aero Arc API/PostGIS pairs. Both profiles apply the same
cross-authority invariant functions and produce replayable evidence bundles.

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

The real-DSS tier currently proves that USS-A can activate and withdraw against
InterUSS while preserving the local-active invariant, and that an independently
persisted USS-B operation is discovered through the real DSS and authenticated
USS-USS detail route before blocking an overlapping USS-A publication.

The executable fixture tier currently covers happy-path activation, a lost DSS
create response, stale-OVN withdrawal, peer-detail `500` fail-closed recovery,
DSS latency through Toxiproxy, worker death and lease-expiry takeover, and
tripwires that prove the invariant checker rejects known-bad states.

The deterministic fixture is for failure semantics and orchestration speed. The
real InterUSS profile is a separate protocol-fidelity gate; passing either tier
must not be presented as full ASTM or InterUSS conformance.

## Repository layout

```text
cmd/fault-fixture/       Stateful DSS + peer-USS test double
cmd/e2e-report/          go-test JSON to JSON/Markdown/JUnit reports
cmd/e2e-repeat-report/   Aggregate repeatability and timing evidence
internal/stack/          Testcontainers topology and artifact collection
internal/realdss/        Real InterUSS + two-USS Testcontainers topology
internal/toxiproxy/      Small, explicit Toxiproxy control client
e2e/                     Cross-service scenarios and invariant checks
scenarios/               Scenario catalog and expected safety properties
deploy/chaos-mesh/       Opt-in Kubernetes chaos workflow
scripts/run-e2e.sh       Repeatable local/CI entry point
scripts/run-real-dss-e2e.sh  Protocol-fidelity entry point
scripts/repeat-e2e.sh    Multi-run determinism check and aggregate report
```

## Requirements

- Go 1.26.2 or newer;
- Docker Engine;
- sibling source checkout at `../aero-arc-api` by default;
- sibling `../interuss-dss` checkout for the real-DSS tier;
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

Run the real InterUSS + two-USS tier:

```bash
./scripts/run-real-dss-e2e.sh
```

Override either source checkout when they are not siblings:

```bash
AERO_ARC_API_SOURCE=/path/to/aero-arc-api \
AERO_ARC_INTERUSS_SOURCE=/path/to/interuss-dss \
./scripts/run-real-dss-e2e.sh
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
| Real DSS | relevant PR/nightly/manual | Two Aero Arc USS instances against pinned InterUSS SCD + OAuth |
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

To add coverage without learning the container internals, follow
[Adding tests](docs/ADDING_TESTS.md). It includes a copyable scenario record,
subtest scaffold, the supported helper vocabulary, and the boundary for changes
that need new fixture or infrastructure work.

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

See [Architecture](docs/ARCHITECTURE.md) for the trust boundaries,
[Contributing](CONTRIBUTING.md) for DCO and validation requirements, and
[Chaos Mesh](deploy/chaos-mesh/README.md) for the Kubernetes tier.

## License

Mozilla Public License 2.0. See [LICENSE](LICENSE).
