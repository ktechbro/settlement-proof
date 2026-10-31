CREATE TABLE escrow_events (
 id BIGSERIAL PRIMARY KEY, event_key TEXT NOT NULL UNIQUE, chain TEXT NOT NULL,
 program TEXT NOT NULL, escrow TEXT NOT NULL, signature TEXT NOT NULL, outer_instruction INTEGER NOT NULL,
 settlement_id BIGINT NOT NULL REFERENCES settlements, fingerprint TEXT NOT NULL, body JSONB NOT NULL,
 UNIQUE(chain,program,signature,outer_instruction)
);
CREATE TABLE escrow_event_scopes (
 scope TEXT NOT NULL REFERENCES scopes, event_id BIGINT NOT NULL REFERENCES escrow_events,
 PRIMARY KEY(scope,event_id)
);
CREATE INDEX escrow_events_deal ON escrow_events(chain,program,escrow);
