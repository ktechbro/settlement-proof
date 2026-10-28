# Recorded validation

Validated on 26 October 2026. These observations describe the tested build and environment; they are not a throughput benchmark or a production-readiness certification.

## Deterministic checks

- `go test -count=1 ./...` with a real PostgreSQL 17 database: passed.
- `go vet ./...`: passed.
- `go build ./cmd/...`: passed.
- Docker multi-stage build and fresh-namespace demo: passed.
- Initial GitHub Actions run, including Linux race detection and PostgreSQL integration tests: [successful run](https://github.com/ktechbro/settlement-proof/actions/runs/40304503614).

Fresh Docker demo result:

```json
{
  "status": "PASS",
  "settlements": 4,
  "total_atomic": "66000001",
  "outbox_effects": 4,
  "checkpoint_height": 5,
  "primary_log_calls": 3,
  "backup_log_calls": 2,
  "faults_exercised_this_run": true,
  "history_reused": false,
  "unsafe_block_6_excluded": true
}
```

The deterministic suite also exercises finalized-conflict halts, SQL rollback, actual process death during an open transaction, worker revision fencing, and new-scope historical backfill.

## Real-chain verification

The opt-in `TestLiveBaseReadOnly` passed using the [PublicNode Base RPC](https://base.publicnode.com/). It selected an actual transfer from a finalized block, configured its sender as the watch target, ran the real worker, and compared the persisted event with the chain response.

| Field | Observed value |
| --- | --- |
| Network | Base mainnet, chain ID `0x2105` |
| Asset | Native USDC, `0x833589fcd6edb6e08f4c7c32d4f71b54bda02913` |
| Block | `53109686` |
| Transaction | [`0x5fffda69d9acebeed8b157e8fdfb20bedbb9588af4536603ae3bcd12928560c7`](https://basescan.org/tx/0x5fffda69d9acebeed8b157e8fdfb20bedbb9588af4536603ae3bcd12928560c7) |
| Log index | `7` |
| Atomic amount | `10000` |

The first attempt against `https://mainnet.base.org` returned HTTP 429. The subsequent PublicNode run succeeded. This validates compatibility with a live provider under the documented trust assumptions, not independent consensus verification. No transaction was submitted and no signing key was used.

The live test is intentionally opt-in and is skipped by deterministic CI when `LIVE_BASE_RPC_URL` is absent.

## Solana extension validation

The Base regression suite and new Solana/PostgreSQL tests passed, including finalized backfill, provider failure, restart, missing history, long skipped-slot gaps, direct/CPI/compiled transfers, loaded addresses, v1 configuration, new-scope replay, rollback/fencing, and persisted halts. A separate isolated-schema test upgraded the original EVM schema and verified preservation of its settlement amount and checkpoint.

The synthetic Solana demo returned PASS with five transfers, total `77000001` atomic units, five outbox effects, and checkpoint slot 109. It excludes a failed transaction and unsafe slot 110.

The optional read-only Solana mainnet test also passed using PublicNode:

| Field | Observed value |
| --- | --- |
| Slot | `462644894` |
| Transaction | [`5WrNM3GwUhD6JgQ6MmmorUpxFbSzeJHuDyfqmhGGXW5Di2yt4JrRvAG9AsJ6BWM4pyBoYuznAHrJp4vvF9wDZpuY`](https://solscan.io/tx/5WrNM3GwUhD6JgQ6MmmorUpxFbSzeJHuDyfqmhGGXW5Di2yt4JrRvAG9AsJ6BWM4pyBoYuznAHrJp4vvF9wDZpuY) |
| Instruction path | Outer `0`, inner `1` |
| Atomic amount | `249984224` |
| Registered token account | `SeR9og31Db3XQSjb4tUVVEhDT1ReEsXtWiQo4MQ5Bqg` |

An initial request capped at transaction version 0 returned RPC error -32015 because the block contained version 1 transactions. The implementation was updated to request/support version 1 and preserve its transaction configuration; the subsequent live scan persisted and verified the actual transfer. No wallet or transaction submission was involved.
