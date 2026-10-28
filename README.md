# Settlement Proof

A Go/PostgreSQL service that turns finalized Base ERC-20 and Solana SPL Token transfers into durable settlement evidence. Built by **[ktechbro](https://github.com/ktechbro)** as a blockchain infrastructure portfolio project.

It answers: **Which payments actually finalized, and can we recover from an RPC outage without missing or counting them twice?**

## What works

- Bounded historical backfill and continuous finalized-block polling.
- Configured USDC contract and watched sender/recipient addresses.
- Exact uint256 quantities, represented as decimal strings in the API.
- Durable PostgreSQL checkpoints committed with settlements, raw logs, scan coverage, and outbox effects.
- Network identity and trusted-anchor validation; overlapping replay when using another RPC provider.
- Persistent halt on conflicting finalized history.
- Revision-fenced concurrent workers, bounded RPC timeouts, retry backoff, and adaptive range shrinking.
- REST evidence/coverage endpoints, a small evidence viewer, and Prometheus metrics.
- A synthetic demo with primary-provider failure, duplicate RPC logs, multiple movements in one transaction, restart recovery, and exclusion of an unfinalized payment.
- Unit and real-PostgreSQL integration tests, including abrupt process death during a transaction.
- Solana block-history recovery, registered token-account coverage, direct/CPI transfers, legacy/v0/v1 transactions, and explicit skipped-slot evidence.

**Scope:** Base uses registered sender/recipient addresses; Solana uses registered token accounts and the legacy SPL Token program. Both paths ingest finalized history only. Solana wallet discovery, Token-2022, escrow-specific lifecycle events, and authorization decisions are outside this observer. It never signs transactions or holds wallet keys. See [correctness and limitations](docs/correctness.md).

## Solana quickstart

Run a deterministic recovery demonstration using real PostgreSQL and synthetic Solana RPC providers:

```sh
cp .env.example .env
docker compose --profile solana-demo run --build --rm solana-demo
```

PowerShell: `Copy-Item .env.example .env` if `.env` does not already exist. Preserve any existing live configuration. The demo verifies five transfer identities/amounts, direct and inner instructions, loaded addresses, long skipped-slot gaps, restart recovery, provider failure, and exactly five outbox effects. Its final JSON report must say `"status":"PASS"`.

For live Solana, use [.env.solana.example](.env.solana.example), select `CHAIN_PROTOCOL=solana`, register USDC **token accounts**, and provide the full genesis hash plus an independently verified produced-block anchor. Then run the existing `indexer` executable or Compose live profile. Base58 identifiers remain case-sensitive. Full setup, supported instructions, event locators, and provider requirements: [Solana operations guide](docs/solana.md).

## Run the deterministic demo

Prerequisites: Docker with Compose. No wallet, RPC subscription, or real funds needed.

```sh
cp .env.example .env
docker compose --profile demo run --build --rm demo
```

PowerShell: use `Copy-Item .env.example .env` for the first line.

The demo uses the **real worker and PostgreSQL**, with two synthetic HTTP JSON-RPC servers. The fixture token is a mock, not Circle-issued USDC. On a fresh database it:

1. Processes the first finalized range with duplicate log responses.
2. Closes and reopens its database pool and worker.
3. Encounters a primary-provider outage and continues with the backup.
4. Replays overlapping history and checks event identities/amounts against a separate expected manifest.
5. Asserts four settlements, four outbox effects, checkpoint 5, and total `66000001` atomic units. The fifth transfer, in block 6, remains unfinalized and absent.

The final JSON line reports `"status":"PASS"`. Re-running is idempotent and reuses its completed demo scope; the report identifies whether faults were exercised in that run. Set a new `DEMO_RUN_ID` environment value before the Compose command to exercise failover in a fresh namespace without deleting prior runs. The test suite does this automatically.

For local Go execution:

```sh
docker compose up -d --wait postgres
export DATABASE_URL='postgres://settlement:settlement-local@127.0.0.1:55437/settlement?sslmode=disable'
go run ./cmd/demo
```

PowerShell environment assignment: `$env:DATABASE_URL='postgres://settlement:settlement-local@127.0.0.1:55437/settlement?sslmode=disable'`.

## Observe live Base payments

Edit `.env` using [.env.example](.env.example). Supply:

- `WATCH_ADDRESSES`: comma-separated EVM addresses; incoming and outgoing transfers are included.
- `START_BLOCK`: decimal block number at least 1. A recent historical start keeps backfill small.
- `ANCHOR_NUMBER`, `ANCHOR_HASH`: an independently checked earlier block and its hash.
- `RPC_PROVIDERS`: JSON array of named HTTP(S) providers supporting `finalized`, headers, and historical `eth_getLogs`.

The example config uses Base mainnet chain ID `0x2105` and Circle's native USDC contract. Verify the address against [Circle's official network list](https://developers.circle.com/stablecoins/usdc-contract-addresses) before use. For Base Sepolia, configure that network's chain identity, anchor, and testnet asset together.

```sh
docker compose --profile live up --build -d
```

Open [the local evidence viewer](http://127.0.0.1:8080). Docker publishes the service and database on localhost only. The API is a single-operator local interface without authentication: place it behind authentication and TLS before remote exposure.

To run the Go service directly, export the `.env` values in your shell first; the binary deliberately does not auto-load files:

```sh
go run ./cmd/indexer
```

## API

| Route | Result |
| --- | --- |
| `GET /v1/settlements?cursor=0&limit=100&from=1&to=500` | Scoped settlements; inclusive block bounds; next cursor |
| `GET /v1/settlements/{id}` | One settlement with raw event and finality evidence |
| `GET /v1/chains/{chain}/status` | Durable checkpoint, halt flag, last provider finality head |
| `GET /v1/chains/{chain}/coverage?cursor=0&limit=100` | Successfully scanned ranges, including empty ranges |
| `GET /health/live` | HTTP process is alive |
| `GET /health/ready` | Database/scope available and chain not halted |
| `GET /metrics` | Process counters, checkpoint, finalized head, halt status |

Amounts are strings of atomic units. Block times are Unix seconds. IDs are database-local event handles. Cursor pagination is ascending by ID; use the returned `next_cursor` and keep the same filters. An empty page means no further rows currently exist. New scope backfills can add older chain activity with newer IDs.

The API shows only its configured scope. Changing the watch set, start, token, chain identity, or trusted anchor creates a new scope and backfills it. Settlements and outbox effects remain unique across overlapping scopes on the same named chain.

## Test

Use Go 1.27.3 (the module pins the toolchain; recent Go versions can download it automatically).

```sh
go test ./...
go vet ./...
docker compose up -d --wait postgres
export TEST_DATABASE_URL='postgres://settlement:settlement-local@127.0.0.1:55437/settlement?sslmode=disable'
go test -race -count=1 ./...
```

PowerShell: set `$env:TEST_DATABASE_URL='postgres://settlement:settlement-local@127.0.0.1:55437/settlement?sslmode=disable'` before the last command. Race tests require a supported C toolchain; CI runs them on Linux. Without `TEST_DATABASE_URL`, PostgreSQL tests explicitly skip. CI supplies a real PostgreSQL service and fails if the required suite fails.

Tests cover finalized-only ingestion, duplicate/overlapping delivery, provider outage and lag, range limits, malformed evidence, exact uint256 values, durable rollback, process crash, concurrent workers, new-scope backfill, API validation, and persisted finality-conflict halts.

An optional real-chain smoke test reads one finalized Base block, derives a watched address from an actual USDC transfer, runs the real scanner, and verifies the persisted event. It submits no transactions. With `TEST_DATABASE_URL` set, export `LIVE_BASE_RPC_URL` to a Base mainnet endpoint and run `go test -run TestLiveBaseReadOnly -v ./internal/indexer`. This network-dependent test stays out of the deterministic CI suite.

## Design

```mermaid
flowchart LR
    A[Primary / backup RPC] --> B[Identity + trusted anchor]
    B --> C[Finalized head and continuous headers]
    C --> D[Transfer decoding + watched addresses]
    D --> E[One SQL transaction]
    E --> F[Raw observations + settlements]
    E --> G[Coverage + checkpoint]
    E --> H[Unique outbox effect]
    F --> I[Read-only evidence API]
```

See [architecture](docs/architecture.md), [correctness](docs/correctness.md), [runbook](docs/runbook.md), [demo procedure](docs/demo.md), and [recorded validation](docs/validation.md).

## Outbox

Each new settlement creates one durable `settlement.finalized` outbox row. This MVP does not ship an outbound webhook dispatcher. A downstream consumer can read rows in commit-safe batches with its own durable cursor and deduplicate by settlement identity. Do not describe external delivery as exactly once. See the architecture document before implementing multiple outbox readers.

## License

MIT. Independent portfolio work; not an official Saifuro product or integration.
