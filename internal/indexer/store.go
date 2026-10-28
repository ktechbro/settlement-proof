package indexer

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schema string

type Store struct {
	Pool  *pgxpool.Pool
	Scope string
	Chain string
}

func OpenStore(ctx context.Context, c Config) (*Store, error) {
	pool, err := pgxpool.New(ctx, c.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid database configuration")
	}
	s := &Store{Pool: pool, Scope: c.Scope(), Chain: c.Chain}
	if err = s.initialize(ctx, c); err != nil {
		pool.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) initialize(ctx context.Context, c Config) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(284315912)"); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, schema); err != nil {
		return err
	}
	if err = migrate(ctx, tx); err != nil {
		return err
	}
	// Do not persist provider URLs or database credentials in the scope metadata.
	meta, _ := json.Marshal(map[string]any{"token": c.Token, "watch": c.Watch, "start": c.Start, "chain_id": c.ChainID, "anchor_number": c.AnchorNumber, "anchor_hash": c.AnchorHash, "finality_policy": c.FinalityPolicy(), "protocol": c.Protocol})
	if _, err = tx.Exec(ctx, "INSERT INTO scopes(scope,chain,config) VALUES($1,$2,$3) ON CONFLICT DO NOTHING", s.Scope, c.Chain, meta); err != nil {
		return err
	}
	initialHash := ""
	if c.Protocol == "solana" {
		initialHash = c.AnchorHash
	}
	if _, err = tx.Exec(ctx, "INSERT INTO checkpoints(scope,height,hash) VALUES($1,$2,$3) ON CONFLICT DO NOTHING", s.Scope, c.Start-1, initialHash); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) Checkpoint(ctx context.Context) (Checkpoint, error) {
	var c Checkpoint
	err := s.Pool.QueryRow(ctx, "SELECT height,hash,revision,halted FROM checkpoints WHERE scope=$1", s.Scope).Scan(&c.Height, &c.Hash, &c.Revision, &c.Halted)
	return c, err
}

func (s *Store) Halt(ctx context.Context, reason string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "UPDATE checkpoints SET halted=true,updated_at=now() WHERE scope=$1", s.Scope); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO incidents(scope,reason) VALUES($1,$2)", s.Scope, reason); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Commit atomically writes evidence, financial effects, coverage, outbox and cursor.
// A row lock plus revision check fences workers that fetched against old state.
func (s *Store) Commit(ctx context.Context, expected Checkpoint, b Batch) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var cp Checkpoint
	if err = tx.QueryRow(ctx, "SELECT height,hash,revision,halted FROM checkpoints WHERE scope=$1 FOR UPDATE", s.Scope).Scan(&cp.Height, &cp.Hash, &cp.Revision, &cp.Halted); err != nil {
		return err
	}
	if cp.Halted {
		return ErrConflict
	}
	if cp != expected {
		return ErrStale
	}
	if b.To <= cp.Height || b.From > cp.Height+1 {
		return fmt.Errorf("noncontiguous or nonadvancing batch")
	}
	for _, h := range b.Headers {
		n, _ := Quantity(h.Number)
		bt, _ := Quantity(h.Timestamp)
		tag, e := tx.Exec(ctx, `INSERT INTO blocks(chain,height,hash,parent_hash,block_time) VALUES($1,$2,$3,$4,$5)
   ON CONFLICT(chain,height) DO UPDATE SET hash=blocks.hash WHERE blocks.hash=EXCLUDED.hash AND blocks.parent_hash=EXCLUDED.parent_hash AND blocks.block_time=EXCLUDED.block_time`, s.Chain, n, h.Hash, h.ParentHash, bt)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("%w: stored block %d disagrees", ErrConflict, n)
		}
	}
	if err = s.insertSettlements(ctx, tx, b.Provider, b.Settlements); err != nil {
		return err
	}
	fn, _ := Quantity(b.Finalized.Number)
	if _, err = tx.Exec(ctx, "INSERT INTO scan_ranges(scope,from_block,to_block,provider,finalized_number,finalized_hash,event_count) VALUES($1,$2,$3,$4,$5,$6,$7)", s.Scope, b.From, b.To, b.Provider, fn, b.Finalized.Hash, len(b.Settlements)); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE checkpoints SET height=$2,hash=$3,revision=revision+1,updated_at=now() WHERE scope=$1", s.Scope, b.To, b.End.Hash); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type Filter struct {
	After int64
	From  int64
	To    int64
	Limit int
}

func (s *Store) List(ctx context.Context, f Filter) ([]Settlement, error) {
	rows, err := s.Pool.Query(ctx, `SELECT s.id,s.chain,s.asset,s.tx_hash,s.log_index,s.block_number,s.block_hash,s.block_time,s.sender,s.recipient,s.amount::text,s.provider,s.finalized_number,s.finalized_hash,s.raw,s.protocol,s.outer_instruction,s.inner_instruction,coalesce(s.sender_account,''),coalesce(s.recipient_account,'')
 FROM settlements s JOIN settlement_scopes w ON w.settlement_id=s.id
 WHERE w.scope=$1 AND s.id>$2 AND s.block_number>=$3 AND s.block_number<=$4 ORDER BY s.id LIMIT $5`, s.Scope, f.After, f.From, f.To, f.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Settlement{}
	for rows.Next() {
		var v Settlement
		err = rows.Scan(&v.ID, &v.Chain, &v.Asset, &v.TransactionHash, &v.LogIndex, &v.BlockNumber, &v.BlockHash, &v.BlockTime, &v.Sender, &v.Recipient, &v.Amount, &v.Provider, &v.FinalizedNumber, &v.FinalizedHash, &v.Raw, &v.Protocol, &v.OuterInstruction, &v.InnerInstruction, &v.SenderAccount, &v.RecipientAccount)
		if err != nil {
			return nil, err
		}
		result = append(result, v)
	}
	return result, rows.Err()
}

func (s *Store) Coverage(ctx context.Context, after int64, limit int) ([]map[string]any, error) {
	rows, err := s.Pool.Query(ctx, "SELECT id,from_block,to_block,provider,finalized_number,finalized_hash,event_count,protocol,produced_blocks,skipped_slots FROM scan_ranges WHERE scope=$1 AND id>$2 ORDER BY id LIMIT $3", s.Scope, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []map[string]any{}
	for rows.Next() {
		var id, from, to, fn, count int64
		var provider, hash string
		var protocol string
		var produced, skipped *int64
		if err = rows.Scan(&id, &from, &to, &provider, &fn, &hash, &count, &protocol, &produced, &skipped); err != nil {
			return nil, err
		}
		result = append(result, map[string]any{"id": id, "from_block": from, "to_block": to, "provider": provider, "finalized_number": fn, "finalized_hash": hash, "event_count": count, "protocol": protocol, "produced_blocks": produced, "skipped_slots": skipped})
	}
	return result, rows.Err()
}
