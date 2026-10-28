ALTER TABLE settlements ADD COLUMN protocol TEXT NOT NULL DEFAULT 'evm' CHECK(protocol IN ('evm','solana'));
ALTER TABLE settlements ADD COLUMN outer_instruction INTEGER;
ALTER TABLE settlements ADD COLUMN inner_instruction INTEGER;
ALTER TABLE settlements ADD COLUMN sender_account TEXT;
ALTER TABLE settlements ADD COLUMN recipient_account TEXT;
ALTER TABLE settlements ADD CONSTRAINT solana_locator CHECK (
 protocol<>'solana' OR (outer_instruction IS NOT NULL AND outer_instruction>=0 AND inner_instruction IS NOT NULL AND inner_instruction>=-1 AND sender_account IS NOT NULL AND recipient_account IS NOT NULL)
);
ALTER TABLE scan_ranges ADD COLUMN protocol TEXT NOT NULL DEFAULT 'evm';
ALTER TABLE scan_ranges ADD COLUMN produced_blocks BIGINT;
ALTER TABLE scan_ranges ADD COLUMN skipped_slots BIGINT;
CREATE TABLE solana_blocks (
 chain TEXT NOT NULL, slot BIGINT NOT NULL, hash TEXT NOT NULL, parent_slot BIGINT NOT NULL,
 parent_hash TEXT NOT NULL, block_time BIGINT NOT NULL, provider TEXT NOT NULL,
 PRIMARY KEY(chain,slot), CHECK(parent_slot<slot)
);
