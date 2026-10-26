# Guarantees and limits

## Guarantees under the stated provider assumptions

- Only events at or below an RPC finalized head become settlements.
- Every committed range records headers and coverage, including zero-transfer ranges.
- Records, evidence, coverage, outbox effects, and checkpoint changes are atomic.
- Repeated observations and overlapping watch scopes cannot add duplicate settlement effects.
- Monetary values retain exact integer precision through decoding, storage, and JSON output.
- Provider/network/anchor mismatches cannot silently advance the checkpoint.
- A conflict with stored finalized history halts the scope and records an incident.
- A new watch scope scans from its own declared start.

## Assumptions

An RPC provider must faithfully return complete logs, correct headers, and a meaningful finalized tag. Header continuity and anchors detect several inconsistent responses; they do not verify Ethereum consensus or prove that a provider has not omitted a log. Independent providers are used for availability, not as a consensus quorum. A malicious provider could fabricate coherent responses or label unsafe history finalized.

The trusted anchor is operator supplied and precedes the scan. The service verifies that the provider knows it. It does not reconstruct every header between that anchor and the configured start when the two are far apart.

The scanner queries a whole token contract's transfer logs for each bounded range, then filters locally. This is simple and validates returned transfers consistently, but high-volume USDC ranges can require small batches and a paid/archive-capable provider. It is not a chain-wide indexer.

## Deliberate exclusions

- Solana is not implemented. A later adapter must handle commitment, token-account coverage, closed accounts, instruction paths, and historical transaction availability separately.
- No provisional subscriptions or reorg rollback are implemented. Unsafe blocks never enter this finalized-only pipeline. A finalized-history conflict requires operator investigation.
- No authorization matching or escrow state reconstruction is implemented. These are projects 2 and 4 in the original plan.
- No transaction submission, private-key custody, bridge processing, pricing, or financial bookkeeping.
- No guarantee that a declared wallet's full economic history is covered before `START_BLOCK` or through unregistered contracts/accounts.
- No receipt proof verification or consensus client. Successful canonical token logs are trusted to describe EVM execution under the provider assumptions.
- No outbound event delivery service. The durable outbox prepares a future at-least-once dispatcher.

## Failure semantics

Unavailable providers, missing results, malformed logs, or unsupported RPC behavior leave the current pending range uncommitted. Retries and alternate providers can recover. The status/metrics show last successful progress; a chain outage does not mean the HTTP process is dead.

Conflicting finalized checkpoints, disconnected stored canonical history, or changed economic fields persist a halt flag. The API remains available, readiness returns 503, and a restart cannot resume the scope automatically. RPC URLs/error messages are excluded from operator-facing logs to avoid exposing credentials.

An unexpected response is not treated as proof of no payments. Empty *successful* log responses are accepted under the completeness assumption. Silent omission by a provider remains a documented blind spot.

## Verification strategy

Unit tests use an actual HTTP JSON-RPC fixture with an independent expected event set. Integration tests exercise PostgreSQL constraints, transactions, new scope backfill, API behavior, abrupt process exit, and concurrent commits. The demonstration has synthetic finality progression; it is not evidence of live Base or Solana performance.
