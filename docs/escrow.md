# Escrow settlement evidence

The opt-in `signed-verdict-escrow-v1` adapter observes the concrete Anchor `deposit`, `settle`, and `refund` ABI used by the sibling signed-verdict-escrow project. It records funding, seller release, and buyer refund as separate economic actions. This is a read-only observer; it never signs or submits transactions.

## Enable

Start from the Solana configuration in `docs/solana.md`. Set `ESCROW_PROGRAMS` to a comma-separated list of trusted deployments of this exact ABI. The demo program identity is `EPobTKNafzYrWNkQ4RXH2CH896ficZyPr4cigk9Hrpgm`; configuring an address does not establish that its deployed code is trusted or unchanged. Pin and review the deployment and its upgrade authority operationally. The supported token program is classic SPL Token with the configured USDC mint, not Token-2022.

`WATCH_ADDRESSES` may be empty when an escrow program is configured. The worker scans finalized produced blocks for those programs, discovers their vaults, and persists those discoveries so later unrelated transfers involving a known vault survive process restarts. Program registration is part of the scan scope; changing the registry creates a fresh anchored replay. Each registered deployment must use this ABI. Unsupported instructions, nested escrow CPI calls, malformed or truncated native logs, and mismatched token effects fail the range without advancing its checkpoint. `create_config` is recognized but has no settlement action.

Only top-level native escrow calls are supported. The native call's SPL Token transfer is an inner instruction. The decoder verifies the instruction discriminator/accounts/arguments, runtime-attributed Anchor event, and exactly one matching verified token CPI. Program text pretending to be another emitter cannot establish provenance. Failed transactions are excluded. Refund instructions omit config/deal ID; these are recovered from earlier funding in the projection, never invented in the observed refund event.

## APIs

- `GET /v1/escrow-events?cursor=0&limit=25`: scope-bound append-only native actions bundled with the original financial settlement. `next_cursor` is the native database ID, independent of settlement cursors.
- `GET /v1/escrows/{program}/{escrow}`: ordered lifecycle evidence, principal funded/released/refunded/locked, observed vault transfer delta, and findings.

Funding locks buyer funds; a release transfers principal to the seller; a refund returns principal to the buyer. This ABI permits one full terminal action, not partial payouts. Missing funding history yields `locked_atomic: "unknown"` and an exception. Duplicate terminals, changed principal/parties/config, early refunds, invalid release windows, or unrelated vault transfers produce findings. The observed delta is a transfer sum over this scope, not a live account-balance attestation. Set the anchor before funding for complete lifecycle accounting. Unsolicited transfers preceding discovery and non-transfer account changes require independent coverage/account auditing.

Settlement rows, raw native evidence, scope links, checkpoint revision, coverage and the existing financial outbox effects commit in one PostgreSQL transaction. Replays reuse physical `(chain, program, signature, outer instruction)` identities and original evidence. Native events do not create a second financial outbox effect.

## Reproduce locally

```powershell
docker compose up -d postgres
$env:ESCROW_EXPORT_PATH = 'artifacts/escrow-fixture.json'
New-Item -ItemType Directory -Force artifacts | Out-Null
go run ./cmd/escrow-demo
```

The synthetic RPC fixture produces three deals: 1 USDC released, 2 USDC refunded after its deadline, and 3 USDC still locked. It checks five financial movements, five native actions, restart recovery and primary-provider failure against real PostgreSQL. Set `ESCROW_DEMO_LISTEN=127.0.0.1:8080` to keep the resulting evidence API running until interrupted. Exported JSON can be consumed by Settlement Reconciler's `cmd/escrow-demo`; see that repository's `docs/escrow.md`.

```powershell
$env:TEST_DATABASE_URL = 'postgres://settlement:settlement-local@127.0.0.1:55437/settlement?sslmode=disable'
go test ./...
```

Tests cover owned-emitter spoofing, CPI disagreement/missing effects, missing funding history, PostgreSQL atomic rollback, provider failover, process restart, scope replay, and unsolicited transfers to a discovered vault. Fixtures are synthetic ABI-shaped execution records, not validator-generated transactions. This feature has not been validated against a funded live escrow deployment. Finality and execution success rely on the configured RPC and trusted program; the observer does not independently re-execute the native Ed25519 verifier.
