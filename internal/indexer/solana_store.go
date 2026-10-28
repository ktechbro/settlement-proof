package indexer

import (
	"context"
	"fmt"
)

func (s *Store) CommitSolana(ctx context.Context, expected Checkpoint, b SolanaBatch) error {
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
	if b.From != cp.Height+1 || b.To <= cp.Height || b.FinalizedSlot < b.To || len(b.Blocks) == 0 {
		return fmt.Errorf("invalid or noncontiguous Solana batch")
	}
	parentSlot, parentHash := cp.Height, cp.Hash
	for _, header := range b.Blocks {
		block := header.Block
		if block.ParentSlot == nil || block.Time == nil || *block.ParentSlot != parentSlot || block.ParentHash != parentHash || header.Slot <= parentSlot {
			return fmt.Errorf("invalid Solana parent linkage")
		}
		tag, err := tx.Exec(ctx, `INSERT INTO solana_blocks(chain,slot,hash,parent_slot,parent_hash,block_time,provider) VALUES($1,$2,$3,$4,$5,$6,$7)
   ON CONFLICT(chain,slot) DO UPDATE SET hash=solana_blocks.hash WHERE solana_blocks.hash=EXCLUDED.hash AND solana_blocks.parent_slot=EXCLUDED.parent_slot AND solana_blocks.parent_hash=EXCLUDED.parent_hash AND solana_blocks.block_time=EXCLUDED.block_time`, s.Chain, header.Slot, block.Hash, *block.ParentSlot, block.ParentHash, *block.Time, b.Provider)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("%w: stored Solana block disagrees", ErrConflict)
		}
		parentSlot, parentHash = header.Slot, block.Hash
	}
	if parentSlot != b.To {
		return fmt.Errorf("Solana batch end mismatch")
	}
	for _, v := range b.Settlements {
		if v.Protocol != "solana" || v.BlockNumber < b.From || v.BlockNumber > b.To || v.FinalizedNumber != b.FinalizedSlot || v.FinalizedHash != b.FinalizedHash {
			return fmt.Errorf("invalid Solana settlement boundary")
		}
	}
	if err = s.insertSettlements(ctx, tx, b.Provider, b.Settlements); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO scan_ranges(scope,from_block,to_block,provider,finalized_number,finalized_hash,event_count,protocol,produced_blocks,skipped_slots) VALUES($1,$2,$3,$4,$5,$6,$7,'solana',$8,$9)", s.Scope, b.From, b.To, b.Provider, b.FinalizedSlot, b.FinalizedHash, len(b.Settlements), len(b.Blocks), b.To-b.From+1-int64(len(b.Blocks))); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE checkpoints SET height=$2,hash=$3,revision=revision+1,updated_at=now() WHERE scope=$1", s.Scope, b.To, parentHash); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
