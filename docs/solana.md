# Solana settlement indexing

## Coverage

The Solana path observes the configured six-decimal mint through explicitly registered token-account public keys. The mainnet example uses Circle's native USDC mint, `EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v`. Verify it against [Circle's contract list](https://developers.circle.com/stablecoins/usdc-contract-addresses).

`WATCH_ADDRESSES` means **token accounts**, not wallet owner addresses. Accounts can be closed now and still be scanned historically: coverage comes from finalized transaction history rather than today's wallet/account discovery. Register every historical token account you need; registering one wallet address does not discover its accounts. A transaction containing a watched key is decoded, and only transfers whose source/destination is registered become settlement records.

Token identities and wallet owners come from historical pre/post token-balance metadata. The service never guesses historical ownership using current `getAccountInfo` results. Missing owner/mint/program information, or an owner/identity change inside a relevant transaction, blocks advancement for investigation. Accounts created and closed within one transaction can lack sufficient metadata and are deliberately unsupported in that situation.

## Configure

Use `.env.solana.example` as a starting point. Do not overwrite an existing Base configuration unintentionally. The executable reads exported environment variables; Compose's live profile reads `.env`.

| Setting | Meaning |
| --- | --- |
| `CHAIN_PROTOCOL=solana` | Select Solana adapter; default remains EVM |
| `CHAIN` | Stable operator-defined network name, e.g. `solana-mainnet` |
| `CHAIN_ID` | Full `getGenesisHash` value; not the shortened CAIP identifier |
| `TOKEN_ADDRESS` | Case-sensitive mint public key |
| `WATCH_ADDRESSES` | Comma-separated registered token-account keys |
| `ANCHOR_NUMBER`, `ANCHOR_HASH` | Independently checked produced slot greater than zero and its blockhash |
| `START_BLOCK` | Anchor slot + 1; the legacy setting name represents a slot here |
| `BATCH_SIZE` | Slot span after the next produced block, default 5, maximum 100 |
| `REPLAY_BLOCKS=0` | Solana resumes at its atomic finalized cursor; EVM overlapping replay is separate |
| `RPC_PROVIDERS` | Ordered named endpoints; require full historical transaction metadata |

Solana public keys, signatures, mint addresses, and hashes are base58 and case-sensitive. Scope identity includes protocol, genesis, mint, decimals, registered accounts, and start/anchor. Changing any coverage setting creates a new scope. Provider changes reuse the current scope.

Run one process per network/scope. Base and Solana can share PostgreSQL with distinct chain names/scopes and different HTTP listen ports. One process serves one configured chain.

## History and finality algorithm

1. Verify `getGenesisHash`, the configured trusted anchor, the retained-history floor, and the persisted checkpoint hash.
2. Read `getSlot` at finalized commitment and retain the root slot/blockhash as finality evidence.
3. Use `getBlocksWithLimit` to find the next produced block after the checkpoint, even across a large skipped-slot gap.
4. Obtain a bounded produced-slot list with `getBlocks`, then fetch full `getBlock` responses using finalized commitment, `jsonParsed`, and numeric `maxSupportedTransactionVersion: 1`.
5. Require each block's `parentSlot` and `previousBlockhash` to link exactly to the last committed/fetched produced block.
6. Decode watched transactions, rejecting unsupported or incomplete evidence. Failed whole transactions produce no settlements.
7. Recheck the finality anchor after retrieval.
8. In one SQL transaction, fence stale workers, persist block headers, raw event/transaction evidence, settlements, unique outbox effects, coverage, and checkpoint.

The checkpoint always names a **produced** block. Intervening slots are counted as skipped only when a later block's parent link establishes that gap. Trailing slots without such evidence remain uncommitted. A provider omitting a produced block cannot simply classify its slot as skipped: the next block's parent slot/hash will not connect to the cursor.

Null/unavailable blocks, pruned history, lagging roots, malformed metadata, or disconnected parents leave the range pending and try another provider. A disagreement with already stored finalized hashes/economic evidence persists an incident and halt. Restarting cannot bypass a stored halt.

RPC methods: [getBlock](https://solana.com/docs/rpc/http/getblock), [getBlocks](https://solana.com/docs/rpc/http/getblocks), [getBlocksWithLimit](https://solana.com/docs/rpc/http/getblockswithlimit), [getFirstAvailableBlock](https://solana.com/docs/rpc/http/getfirstavailableblock). Historical response structures: [Solana RPC JSON structures](https://solana.com/docs/rpc/json-structures).

## Decoding and event identity

Supported legacy SPL Token instructions:

- Parsed `transfer` and `transferChecked`.
- Compiled equivalents, including little-endian uint64 quantities.
- Direct outer instructions and recorded CPI/inner instructions.
- Legacy, v0, and v1 transactions; parsed v0 lookup-table keys are already expanded, while raw compiled messages use loaded writable then readonly addresses. Version 1 messages have no lookup tables; their `transactionConfig` is retained in raw evidence.

The decoder verifies the mint/program on both token accounts, six decimals, canonical exact uint64 amounts, historical owners, transaction success, and instruction indices. Known non-transfer operations such as approval, account initialization, mint/burn, and close are not reported as payments. Unsupported token operations affecting watched coverage stop advancement.

Current mainnet blocks contain v1 transactions, so requesting a maximum version of 0 can fail an entire block. The fixture explicitly requires the version-1 ceiling, tests all three versions, and checks preservation of v1 configuration. See the [Solana Foundation v1 reader guidance](https://github.com/solana-foundation/solana-dev-skill/blob/main/skills/solana-dev/references/transactions-v1.md).

An overall successful transaction can contain a caught failed CPI. This version conservatively blocks a watched transaction if execution logs show a failed legacy-token invocation or truncated logs. It does not claim to reconstruct the successful subset of that ambiguous trace. Missing inner-instruction/log history is also a coverage error.

Each event is identified by chain + signature + outer index + inner index. Direct transfers use inner index `-1`. The shared legacy `log_index` field packs this path as:

```text
log_index = (outer_instruction << 32) | (inner_instruction + 1)
```

This preserves the existing settlement uniqueness key and importer shape while keeping distinct movements inside one transaction. The API additionally returns `protocol`, `outer_instruction`, `inner_instruction`, `sender_account`, and `recipient_account`.

For Solana, `transaction_hash` contains the transaction signature, `block_number` is the slot, and `sender`/`recipient` are historical wallet-owner identities. Source/destination token-account addresses remain separately available. `raw_log` is retained for API compatibility but contains the Solana block/instruction context and full transaction receipt. `finalized_number` is the finalized root slot, not a confirmation count.

## Schema upgrade

The original EVM schema remains the baseline. Numbered migration `001_solana.sql` adds protocol/instruction/account fields, Solana headers, and produced/skipped coverage counts. Existing settlement IDs, amounts, checkpoints, scope hashes, and outbox effects remain intact. New columns default to EVM for old rows.

Migrations run under the existing startup advisory lock, in one transaction, and record checksums with line endings normalized. Do not edit an applied migration; add the next numbered file. A checksum mismatch prevents startup.

## Demo and tests

```sh
docker compose --profile solana-demo run --build --rm solana-demo
```

The demo starts with a synthetic finalized anchor at slot 100. It persists a first range, closes/reopens the worker and pool, injects a primary-provider outage, and recovers through slot 109 using a backup. Gaps include a skipped interval larger than the configured batch span.

The independent manifest expects five events with quantities `50000000`, `7000000`, `9000000`, `1`, and `11000000`, totaling `77000001` atomic units, plus exactly five outbox effects. A failed transaction and unsafe slot 110 are excluded. No real token moves.

Run the normal Go suite with `TEST_DATABASE_URL` to test decoder errors, parent gaps, provider failover, restart, duplicate history, new scopes, halt persistence, atomic rollback, stale-worker fencing, API shape, and legacy-schema upgrade.

Optional live mainnet verification reads one finalized block, identifies a USDC token account, runs the real scanner, and compares a persisted event with the source transaction. Set both `LIVE_SOLANA_RPC_URL` and `TEST_DATABASE_URL`, then:

```sh
go test -run TestLiveSolanaUSDC -v ./internal/indexer
```

This makes read-only RPC calls, submits no transactions, and is excluded from deterministic CI. A public provider may rate-limit or lack required block methods/history; use an appropriate endpoint.

## Operational limits

Providers remain trusted for correct consensus/finality data and complete transaction/inner-instruction metadata. These checks are not a consensus verifier or provider quorum. Full blocks are scanned to recover history, so this is deliberately more RPC-intensive than a filtered streaming indexer. RPC responses are capped at 32 MiB; oversized blocks are explicit failures, not silently incomplete coverage.

This version covers classic SPL Token USDC transfers for registered accounts. It does not auto-discover historical wallet accounts, decode Token-2022 extensions, classify escrow lifecycle events, or provide a provisional subscription pipeline. It never moves funds or asks for signing keys.
