# Demonstration and evidence

## Reproduce

Copy `.env.example` to `.env`, then run:

```sh
docker compose --profile demo run --build --rm demo
```

The final JSON report must be PASS. Capture stdout/stderr as your demo artifact if desired. Do not edit or hand-author test results.

The fixture includes four finalized transfers: 50,000,000; 7,000,000; 9,000,000; and 1 atomic unit. Two movements share one transaction but have distinct log indexes. An additional 999,000,000-unit transfer exists only in unsafe block 6 and must be absent. A duplicate RPC response must not create another settlement or outbox effect.

## Explain the recovery

The first step commits blocks 1–2. The demo closes and reopens its pool/worker, retaining the database checkpoint. The primary provider rejects subsequent log reads. The backup serves overlapping ranges through finalized block 5. The expected event map validates identities and amounts independently of the scanner output.

Re-running a completed demo proves idempotency but will not inject an outage into already completed work. Set a new `DEMO_RUN_ID` environment value to use a fresh namespace; the report exposes `faults_exercised_this_run` and `history_reused`. The integration test suite creates unique network namespaces automatically. The separate process-crash test exits while an SQL transaction is open and checks PostgreSQL rolls it back.

## Suggested interview walkthrough

1. State the provider trust and coverage boundaries.
2. Run the demo and inspect its final report.
3. Open `worker.go` to show identity checks, finalized scanning, replay, and fallback.
4. Open `store.go` to show the checkpoint row lock, revision fence, uniqueness checks, and one commit boundary.
5. Run the conflict and process-crash tests.
6. Explain why the outbox represents one durable effect while external network delivery would still be at least once.

Use the CI logs or a locally captured test run for measured pass/fail evidence. Do not claim the fixture establishes mainnet throughput or production readiness.
