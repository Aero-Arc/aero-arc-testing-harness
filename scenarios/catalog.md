# Initial federation scenario catalog

## `federated_conflict_blocks_publication`

**Given** USS-B owns an Accepted operation and Aero Arc USS-A proposes an
overlapping operation.

**When** A queries the DSS, fetches authoritative B details, and evaluates the
normalized candidate set.

**Then** A records a potential conflict, does not publish the desired version,
and cannot activate locally.

Invariant: `publication blocked => no DSS reference for desired version`.

## `lost_dss_create_response`

**Given** B is clear of A and all authorities are healthy.

**When** A creates its DSS reference, the DSS commits, and the response
connection is closed before A receives the receipt.

**Then** A enters retrying state, reads the existing reference, recovers its
OVN/version, converges to confirmed Accepted, and never creates a duplicate.

Invariant: `local Active => same intent version confirmed Activated in DSS`.

## `stale_ovn_withdrawal`

**Given** A is published and the DSS reference advances outside A's last local
receipt.

**When** A cancels and its first delete carries the stale OVN.

**Then** the delete is rejected, reconciliation reads the current reference,
retries with the new OVN, and converges to confirmed withdrawal.

Invariant: a stale OVN cannot delete or overwrite a newer reference.

## `peer_500_fails_closed`

**Given** the DSS returns a relevant peer reference.

**When** the peer details endpoint returns bounded `500` responses.

**Then** the deconfliction result is indeterminate rather than clear,
publication/activation remain blocked, and recovery resumes after the peer
becomes healthy.

Invariant: `provider unavailable => posture != clear`.

## `worker_dies_mid_lease`

**Given** a publication row is claimed with a finite lease.

**When** the owning API process dies after remote mutation but before local
confirmation.

**Then** no other worker commits behind the live lease, the row becomes
reclaimable after expiry, and the next worker reconciles idempotently.

Invariant: one authoritative confirmation per publication revision.
