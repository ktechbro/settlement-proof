# Operator runbook

## Inspect progress

Check `/health/ready`, `/v1/chains/{chain}/status`, `/v1/chains/{chain}/coverage`, and `/metrics`. The checkpoint is durable; provider failure/batch counters reset on process restart. `last_provider_finalized_height` is an observation, not a multi-provider consensus value.

## Provider outage or rate limiting

The scanner leaves pending work untouched and tries the next configured provider. Confirm network identity, finalized-tag support, historical availability, and credentials outside the logs. Reduce `BATCH_SIZE` if the provider rejects broad ranges. Replay is safe; keep the configured scope stable when changing only providers.

## Finalized conflict

1. Leave the scope halted and collect the checkpoint plus `incidents` rows.
2. Compare its stored hash with independent trusted sources and inspect provider network identity.
3. Preserve the database or a backup before corrective work.
4. Diagnose whether the cause is misconfiguration, corrupted data, provider error, or a genuine chain incident.
5. Resume only after a documented, reviewed database repair or a clean backfill into a separate database. There is deliberately no public unhalt endpoint.

Creating another scope does not erase shared finalized block/settlement evidence. Do not use a watch-list edit to bypass a conflict.

## Database failure or restore

Restore a consistent PostgreSQL backup. Scope metadata, checkpoints, blocks, observations, settlements, outbox, coverage, and incidents must remain together. Restart against the same configuration; the worker checks the restored checkpoint hash before advancing.

Do not copy checkpoints without their settlement/evidence tables. After a restore, any downstream external consumer must deduplicate replayed deliveries by stable chain/transaction/log identity.

## Missing historical data

Use an RPC provider able to serve the needed range. A scan failure is visible through logs and a stalled checkpoint; no completed coverage row is created for it. This version does not maintain a separate persistent failed-range table. Do not move `START_BLOCK` forward and describe the resulting shorter scope as complete older history.

## Change watch addresses

Update the configured address list. The new digest creates a fresh scope and backfills from `START_BLOCK`. Prior scope evidence remains stored. The API exposes the newly configured scope. Sender and recipient membership use exact normalized EVM addresses.

## Local lifecycle

```sh
docker compose ps
docker compose logs --tail 100 indexer
docker compose stop indexer
docker compose down
```

`down` retains the named PostgreSQL volume. Volume removal is destructive; do it only for an intentionally disposable environment.

## Deployment boundary

The API has no authentication or tenant administration. Keep localhost binding or add an authenticated gateway. Protect PostgreSQL credentials and backups. Do not publish a real `.env`. The observer needs RPC read access only, with no signing keys.
