# Architecture and implementation

## Components

`cmd/indexer` initializes the schema and configured scope, starts one worker and one HTTP server, and handles shutdown. `internal/indexer` owns RPC access, validation, the scanner, persistence, and APIs. `internal/testchain` is a synthetic HTTP fixture used by tests and `cmd/demo`.

The RPC client uses bounded HTTP requests, rejects null/malformed/mismatched envelopes, and redacts transport URLs and provider error messages. Providers are tried in configured order. Retrying a service loop uses capped exponential backoff with jitter; a successful advancing batch immediately continues backfill.

## One worker step

1. Read the durable checkpoint and reject a halted scope.
2. Check `eth_chainId`, the configured trusted anchor, the `finalized` head, and the checkpoint block hash.
3. Reject a lagging provider. Another provider may still advance the scan.
4. Select an overlapping replay range plus a bounded new forward range.
5. Retrieve all headers from the range's predecessor through its end; require parent continuity and checkpoint consistency.
6. Query only the configured token's transfer signature. Decode every returned transfer envelope and retain movements involving a configured address.
7. Recheck the original finalized anchor after retrieval. No provisional/latest head is used for settlement.
8. Lock the checkpoint row and compare its revision with the pre-fetch revision.
9. Insert headers, observations, settlements, scope links, coverage, and unique outbox rows; advance the checkpoint in the same transaction.

`-32005` and `-32602` log-query errors trigger range shrinking. Other failures leave the checkpoint untouched and try the next provider. Providers may use other range-limit error codes; tune their adapter before relying on automatic shrinking.

## Persistence

The embedded `schema.sql` is the initial schema, applied transactionally under a PostgreSQL advisory lock. Numbered SQL migrations run under the same lock and track checksums, preserving existing EVM rows and scope hashes. Future changes require another numbered migration; do not edit an applied migration or silently alter the baseline.

Raw observations retain provider provenance and a decoder version. The key includes scope, block hash, transaction hash, log index, and provider. Because this implementation reads finalized history only, it does not maintain a provisional fork store.

Settlement uniqueness is `(chain, transaction_hash, log_index)`. A repeated identity is accepted only if all persisted economic fields agree. Competing block evidence or changed settlement content raises a finalized-history conflict. An outbox row has a unique settlement ID; overlapping watch scopes cannot generate additional financial effects.

The Solana adapter shares this financial commit helper but has a protocol-specific finalized block scanner and `solana_blocks` header table. Its legacy numeric locator packs outer/inner instruction indices; the API also exposes the original indices and token-account addresses. It verifies produced-block parent slots across skipped gaps, resumes from a produced-block checkpoint, and records produced/skipped counts. See [the Solana design](solana.md).

`NUMERIC(78,0)` holds a uint256 quantity exactly, with a database check enforcing its range. API serialization returns the amount as a decimal string. Display decimals are intentionally a client concern; the observer never makes accounting conversions.

## Concurrency and crash boundaries

Workers fetch without a long-lived SQL lock. The checkpoint row is locked only for commit, and a revision comparison rejects a worker whose fetched basis is stale. On restart, the checkpoint either includes the complete transaction or includes none of it. A client disconnect after a successful commit can cause a replay, which uniqueness constraints make harmless.

This avoids a distributed lease for the initial single-partition worker. Additional partitions require independent coverage/checkpoints and deliberate assignment. Schema initialization uses a separate global migration lock.

## Scope changes

A SHA-256 digest of network identity, chain name, token, start, trusted anchor, and sorted watch addresses identifies the scan scope. Provider URLs are not part of the digest. New coverage requirements get a fresh checkpoint; they cannot incorrectly inherit old coverage. Settlements remain shared under the chain name.

Use one stable chain name per actual network. The service does not protect you from intentionally configuring two names for the same network or reusing a name with incompatible identities; deployment configuration is an operator trust boundary.

## Outbox consumption

The worker creates durable effects but has no external dispatcher. A future dispatcher should claim rows transactionally, deliver outside the claim transaction using expiring leases, and acknowledge only after success. Use a stable event key at the receiver.

Do not assume a global `id > last_id` query is safe with arbitrary concurrent producers: PostgreSQL sequence allocation is not commit order. The scanner serializes commits per scope, but different scopes can interleave. For a general dispatcher, track delivery per row using claim state or a consumer-delivery table rather than a bare global high-water ID.

## Reference behavior

Base derives finalized L2 history from L1 finality. This implementation trusts the RPC `finalized` tag and preserves its number/hash, rather than implementing an L1 verifier. See [Base's derivation specification](https://docs.base.org/specifications/base-protocol/consensus/derivation).
