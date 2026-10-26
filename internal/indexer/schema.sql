CREATE TABLE IF NOT EXISTS scopes (
 scope TEXT PRIMARY KEY, chain TEXT NOT NULL, config JSONB NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS checkpoints (
 scope TEXT PRIMARY KEY REFERENCES scopes, height BIGINT NOT NULL,
 hash TEXT NOT NULL, revision BIGINT NOT NULL DEFAULT 0, halted BOOLEAN NOT NULL DEFAULT false,
 updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS blocks (
 chain TEXT NOT NULL, height BIGINT NOT NULL, hash TEXT NOT NULL, parent_hash TEXT NOT NULL,
 block_time BIGINT NOT NULL, PRIMARY KEY(chain,height)
);
CREATE TABLE IF NOT EXISTS observations (
 scope TEXT NOT NULL REFERENCES scopes, block_hash TEXT NOT NULL, tx_hash TEXT NOT NULL,
 log_index BIGINT NOT NULL, provider TEXT NOT NULL, raw JSONB NOT NULL,
 decoder TEXT NOT NULL DEFAULT 'erc20-transfer-v1', seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 PRIMARY KEY(scope,block_hash,tx_hash,log_index,provider)
);
CREATE TABLE IF NOT EXISTS settlements (
 id BIGSERIAL PRIMARY KEY, chain TEXT NOT NULL, asset TEXT NOT NULL,
 tx_hash TEXT NOT NULL, log_index BIGINT NOT NULL, block_number BIGINT NOT NULL,
 block_hash TEXT NOT NULL, block_time BIGINT NOT NULL, sender TEXT NOT NULL, recipient TEXT NOT NULL,
 amount NUMERIC(78,0) NOT NULL CHECK(amount>=0 AND amount<power(2::numeric,256)),
 provider TEXT NOT NULL, finalized_number BIGINT NOT NULL, finalized_hash TEXT NOT NULL,
 raw JSONB NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
 UNIQUE(chain,tx_hash,log_index), CHECK(finalized_number>=block_number)
);
CREATE TABLE IF NOT EXISTS settlement_scopes (
 scope TEXT NOT NULL REFERENCES scopes, settlement_id BIGINT NOT NULL REFERENCES settlements,
 PRIMARY KEY(scope,settlement_id)
);
CREATE TABLE IF NOT EXISTS scan_ranges (
 id BIGSERIAL PRIMARY KEY, scope TEXT NOT NULL REFERENCES scopes,
 from_block BIGINT NOT NULL, to_block BIGINT NOT NULL CHECK(to_block>=from_block),
 provider TEXT NOT NULL, finalized_number BIGINT NOT NULL, finalized_hash TEXT NOT NULL,
 event_count BIGINT NOT NULL, completed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS outbox (
 id BIGSERIAL PRIMARY KEY, settlement_id BIGINT NOT NULL UNIQUE REFERENCES settlements,
 event_type TEXT NOT NULL DEFAULT 'settlement.finalized', payload JSONB NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS incidents (
 id BIGSERIAL PRIMARY KEY, scope TEXT NOT NULL REFERENCES scopes,
 reason TEXT NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS settlements_chain_block ON settlements(chain,block_number,id);
CREATE INDEX IF NOT EXISTS scan_ranges_scope ON scan_ranges(scope,id);
