# Environments

The environment layer owns service topology, readiness, isolation, and
diagnostic capture. It does not contain scenario assertions.

## `docker`

The default Testcontainers environment starts:

- one real Aero Arc USS/API built from `../aero-arc-api`;
- one isolated PostGIS database;
- one deterministic DSS + peer-USS fixture;
- one Toxiproxy hop on the DSS path.

## `realdss`

The protocol-fidelity Testcontainers environment starts:

- one pinned InterUSS CockroachDB node;
- the real InterUSS SCD migration and core-service image built from source;
- the InterUSS dummy-OAuth service and test signing key;
- Aero Arc USS-A with its own PostGIS;
- Aero Arc USS-B with a separate PostGIS.

Both USS instances use distinct OAuth subjects, advertise independent USS base
URLs, and communicate only through the real DSS and authenticated USS-USS HTTP
routes. A direct observer token queries the DSS independently for invariant
evidence. This tier is intentionally separate from the deterministic fixture.

## Kubernetes

The Kubernetes environment deploys the same logical authorities with a unique
`aero-arc.io/test-run` label. Chaos Mesh is permitted to select only resources
carrying that label in the dedicated `aero-arc-e2e` namespace.
