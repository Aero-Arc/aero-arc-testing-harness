# Harness architecture

## Trust boundaries

The harness observes three authorities independently:

- PostgreSQL owns Aero Arc workflow and reconciliation state.
- The DSS fixture or real InterUSS DSS owns globally visible reference state,
  OVNs, versions, and subscriptions.
- The peer fixture owns the request journal for detail fetches and notifications.

Assertions compare these authorities. A response from one service is not used as
proof of another service's state.

## Docker topology

```text
test process
  |-- PostGIS (authoritative local state)
  |-- DSS/peer fixture (authoritative simulated remote state + journal)
  |-- Toxiproxy (DSS path fault boundary)
  `-- Aero Arc API (real source build)
         |-- PostgreSQL directly
         `-- Toxiproxy --> DSS/peer fixture
```

All containers share a new Testcontainers bridge network. Only control and
observation endpoints are mapped to random host ports. The API addresses its
dependencies by network alias so host-specific routing cannot leak into tests.

## Why both fixture and real DSS tiers exist

The fixture can deterministically perform difficult transitions such as
"commit the remote mutation, then lose the response" or "return a newer OVN on
the first delete." That makes concurrency and recovery failures reproducible.

The real InterUSS tier catches schema, authentication, protocol, and datastore
behavior that a test double cannot prove. The same invariant library should be
used by both tiers even though their fault adapters differ.

## Failure taxonomy

- transport: latency, jitter, timeout, reset, bandwidth, packet loss, partition;
- protocol: malformed JSON, wrong content type, redirects, `429`, `500`, stale
  version, stale OVN, missing fields;
- process: crash before claim, during lease, after remote commit, before local
  confirmation, and during shutdown;
- storage: PostgreSQL restart, connection exhaustion, serialization conflicts,
  slow commits, and disk pressure;
- scheduling: duplicate workers, lease expiry, delayed retry, clock skew, and
  concurrent lifecycle requests;
- peer: detail timeout, inconsistent reference/details, duplicate notification,
  notification `5xx`, and partial fleet outage;
- load: many overlapping intents, high subscriber fan-out, and sustained retry
  backlogs.

## Flake policy

Retries are allowed only around eventually-consistent observations, never
around the scenario itself. A failed scenario is not rerun automatically to
make CI green. Repeated runs use explicit seeds and report the empirical failure
rate separately.
