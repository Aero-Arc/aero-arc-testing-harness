# Initial federation scenario catalog

This catalog is a set of design records. Scenario YAML is not loaded at runtime;
the tagged Go tests in `e2e/` are the executable source of truth. Status labels
below distinguish implemented coverage from the real-DSS roadmap.

## `federated_conflict_blocks_publication`

Status: **planned fixture scenario**.

**Given** USS-B owns an Accepted operation and Aero Arc USS-A proposes an
overlapping operation.

**When** A queries the DSS, fetches authoritative B details, and evaluates the
normalized candidate set.

**Then** A records a potential conflict, does not publish the desired version,
and cannot activate locally.

Invariant: `publication blocked => no DSS reference for desired version`.

## `lost_dss_create_response`

Status: **implemented in the fixture-backed Docker tier**.

**Given** B is clear of A and all authorities are healthy.

**When** A creates its DSS reference, the DSS commits, and the response
connection is closed before A receives the receipt.

**Then** A enters retrying state, reads the existing reference, recovers its
OVN/version, converges to confirmed Accepted, and never creates a duplicate.

Invariant: `local Active => same intent version confirmed Activated in DSS`.

## `stale_ovn_withdrawal`

Status: **implemented in the fixture-backed Docker tier**.

**Given** A is published and the DSS reference advances outside A's last local
receipt.

**When** A cancels and its first delete carries the stale OVN.

**Then** the delete is rejected, reconciliation reads the current reference,
retries with the new OVN, and converges to confirmed withdrawal.

Invariant: a stale OVN cannot delete or overwrite a newer reference.

## `peer_500_fails_closed`

Status: **implemented in the fixture-backed Docker tier**.

**Given** the DSS returns a relevant peer reference.

**When** the peer details endpoint returns bounded `500` responses.

**Then** the deconfliction result is indeterminate rather than clear,
publication/activation remain blocked, and recovery resumes after the peer
becomes healthy.

Invariant: `provider unavailable => posture != clear`.

## `worker_dies_mid_lease`

Status: **implemented by restarting the real API against preserved PostGIS and
fixture state**.

**Given** a publication row is claimed with a finite lease.

**When** the owning API process dies after remote mutation but before local
confirmation.

**Then** no other worker commits behind the live lease, the row becomes
reclaimable after expiry, and the next worker reconciles idempotently.

Invariant: one authoritative confirmation per publication revision.

## `dss_latency_through_toxiproxy`

Status: **implemented in the fixture-backed Docker tier**.

**Given** A has a submitted operation and the DSS path is healthy.

**When** downstream DSS responses are delayed beyond Aero Arc's request timeout
through Toxiproxy.

**Then** publication enters retrying state without creating a DSS reference,
the bounded toxic is removed, and reconciliation converges to confirmed
Accepted.

Invariant: ambiguous transport outcomes fail closed and eventually converge.
