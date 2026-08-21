# Environments

The environment layer owns service topology, readiness, isolation, and
diagnostic capture. It does not contain scenario assertions.

## `docker`

The default Testcontainers environment starts:

- one real Aero Arc USS/API built from `../aero-arc-api`;
- one isolated PostGIS database;
- one deterministic DSS + peer-USS fixture;
- one Toxiproxy hop on the DSS path.

The next fidelity increment is a second Aero Arc API/PostGIS pair so the same
scenario can run Aero Arc USS-A against Aero Arc USS-B.

## Real InterUSS DSS

The real-DSS gate will compose the sibling `../interuss-dss` sandbox (pinned
CockroachDB, migrations, DSS core, and dummy OAuth) and replace only the DSS
adapter. Scenarios and invariant checks remain unchanged. This is intentionally
separate from the fast deterministic fixture tier.

## Kubernetes

The Kubernetes environment deploys the same logical authorities with a unique
`aero-arc.io/test-run` label. Chaos Mesh is permitted to select only resources
carrying that label in the dedicated `aero-arc-e2e` namespace.
