package indexer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"

	"github.com/ktechbro/settlement-proof/internal/solana"
)

type SolanaHeader struct {
	Slot  int64
	Block solana.Block
}
type SolanaBatch struct {
	EscrowEvents            []solana.EscrowEvent
	From, To, FinalizedSlot int64
	FinalizedHash           string
	Blocks                  []SolanaHeader
	Settlements             []Settlement
	Provider                string
}
type solanaCommitter interface {
	CommitSolana(context.Context, Checkpoint, SolanaBatch) error
}

func (w *Worker) stepSolana(ctx context.Context) (bool, error) {
	store, ok := w.Store.(solanaCommitter)
	if !ok {
		return false, fmt.Errorf("storage does not support Solana batches")
	}
	cp, err := w.Store.Checkpoint(ctx)
	if err != nil {
		return false, err
	}
	if cp.Halted {
		return false, ErrConflict
	}
	var last error
	var caughtUp bool
	for _, rpc := range w.RPCs {
		batch, err := w.fetchSolana(ctx, rpc, cp)
		if errors.Is(err, ErrConflict) {
			if e := w.Store.Halt(ctx, err.Error()); e != nil {
				return false, e
			}
			return false, err
		}
		if err != nil {
			w.Failures.Add(1)
			last = fmt.Errorf("provider %s: %w", rpc.Provider.Name, err)
			slog.Warn("Solana provider unavailable", "provider", rpc.Provider.Name, "error", err)
			continue
		}
		if batch == nil {
			caughtUp = true
			continue
		}
		if err = store.CommitSolana(ctx, cp, *batch); err != nil {
			if errors.Is(err, ErrConflict) {
				if e := w.Store.Halt(ctx, err.Error()); e != nil {
					return false, e
				}
			}
			return false, err
		}
		w.Batches.Add(1)
		slog.Info("finalized Solana range committed", "from", batch.From, "to", batch.To, "provider", batch.Provider, "events", len(batch.Settlements))
		return true, nil
	}
	if caughtUp {
		return false, nil
	}
	return false, last
}

func solBlock(ctx context.Context, rpc *RPC, slot int64, full bool) (solana.Block, error) {
	var block solana.Block
	details := "none"
	if full {
		details = "full"
	}
	err := rpc.Call(ctx, "getBlock", []any{slot, map[string]any{"commitment": "finalized", "encoding": "jsonParsed", "transactionDetails": details, "maxSupportedTransactionVersion": 1, "rewards": false}}, &block)
	if err != nil {
		return block, err
	}
	if !solana.Valid58(block.Hash, 32) || !solana.Valid58(block.ParentHash, 32) || block.ParentSlot == nil || *block.ParentSlot < 0 || *block.ParentSlot >= slot {
		return block, fmt.Errorf("invalid finalized Solana block header")
	}
	return block, nil
}

func (w *Worker) fetchSolana(ctx context.Context, rpc *RPC, cp Checkpoint) (*SolanaBatch, error) {
	c := w.Config
	var genesis string
	if err := rpc.Call(ctx, "getGenesisHash", []any{}, &genesis); err != nil {
		return nil, err
	}
	if genesis != c.ChainID {
		return nil, fmt.Errorf("wrong Solana genesis identity")
	}
	anchor, err := solBlock(ctx, rpc, c.AnchorNumber, false)
	if err != nil {
		return nil, err
	}
	if anchor.Hash != c.AnchorHash {
		return nil, fmt.Errorf("configured trusted Solana anchor mismatch")
	}
	var root int64
	if err = rpc.Call(ctx, "getSlot", []any{map[string]any{"commitment": "finalized"}}, &root); err != nil {
		return nil, err
	}
	if root < cp.Height {
		return nil, fmt.Errorf("provider finalized root behind checkpoint")
	}
	w.Finalized.Store(root)
	var firstAvailable int64
	if err = rpc.Call(ctx, "getFirstAvailableBlock", []any{}, &firstAvailable); err != nil {
		return nil, err
	}
	if firstAvailable > cp.Height {
		return nil, fmt.Errorf("provider has pruned required checkpoint history")
	}
	previous, err := solBlock(ctx, rpc, cp.Height, false)
	if err != nil {
		return nil, err
	}
	if previous.Hash != cp.Hash {
		return nil, fmt.Errorf("%w: Solana checkpoint %d expected %s got %s", ErrConflict, cp.Height, cp.Hash, previous.Hash)
	}
	if root == cp.Height {
		return nil, nil
	}
	final, err := solBlock(ctx, rpc, root, false)
	if err != nil {
		return nil, err
	}
	from := cp.Height + 1
	var first []int64
	if err = rpc.Call(ctx, "getBlocksWithLimit", []any{from, 1, map[string]any{"commitment": "finalized"}}, &first); err != nil {
		return nil, err
	}
	if len(first) != 1 || first[0] < from || first[0] > root {
		return nil, fmt.Errorf("next finalized produced block unavailable")
	}
	to := root
	if c.BatchSize-1 < root-first[0] {
		to = first[0] + c.BatchSize - 1
	}
	var slots []int64
	if err = rpc.Call(ctx, "getBlocks", []any{first[0], to, map[string]any{"commitment": "finalized"}}, &slots); err != nil {
		return nil, err
	}
	if len(slots) == 0 || slots[0] != first[0] {
		return nil, fmt.Errorf("inconsistent produced-slot listing")
	}
	batch := &SolanaBatch{From: from, FinalizedSlot: root, FinalizedHash: final.Hash, Provider: rpc.Provider.Name}
	parentSlot := cp.Height
	parentHash := cp.Hash
	accounts := append([]string(nil), c.Watch...)
	if len(c.EscrowPrograms) > 0 {
		source, ok := w.Store.(interface {
			EscrowVaults(context.Context) ([]string, error)
		})
		if !ok {
			return nil, fmt.Errorf("storage does not support escrow vault recovery")
		}
		vaults, err := source.EscrowVaults(ctx)
		if err != nil {
			return nil, err
		}
		accounts = append(accounts, vaults...)
	}
	for _, slot := range slots {
		if slot <= parentSlot || slot > to {
			return nil, fmt.Errorf("invalid produced-slot ordering")
		}
		block, err := solBlock(ctx, rpc, slot, true)
		if err != nil {
			return nil, err
		}
		if *block.ParentSlot != parentSlot || block.ParentHash != parentHash {
			return nil, fmt.Errorf("missing produced history or disconnected Solana parent")
		}
		if block.Time == nil || *block.Time < 0 {
			return nil, fmt.Errorf("missing historical block time")
		}
		events, err := (solana.Decoder{Mint: c.Token, Decimals: c.Decimals, Accounts: accounts, EscrowPrograms: c.EscrowPrograms}).Decode(block)
		if err != nil {
			return nil, err
		}
		escrowEvents, err := solana.DecodeEscrows(block, c.Token, c.EscrowPrograms, events)
		if err != nil {
			return nil, err
		}
		for _, e := range escrowEvents {
			e.Chain = c.Chain
			e.Slot = slot
			e.BlockHash = block.Hash
			e.BlockTime = *block.Time
			e.FinalizedSlot = root
			e.FinalizedHash = final.Hash
			e.EventKey = e.Key()
			batch.EscrowEvents = append(batch.EscrowEvents, e)
			accounts = append(accounts, e.Vault)
		}
		for _, event := range events {
			if event.Outer > math.MaxInt32 || int64(event.Inner) >= math.MaxUint32 {
				return nil, fmt.Errorf("instruction locator exceeds supported range")
			}
			// Direct outer instruction has inner=-1, encoded low word zero.
			locator := (int64(event.Outer) << 32) | int64(event.Inner+1)
			raw, err := json.Marshal(map[string]any{"slot": slot, "blockhash": block.Hash, "parent_slot": block.ParentSlot, "previous_blockhash": block.ParentHash, "outer_instruction": event.Outer, "inner_instruction": event.Inner, "transaction": event.Transaction, "decoder": "spl-token-transfer-v1", "token_program": solana.TokenProgram})
			if err != nil {
				return nil, err
			}
			outer, inner := event.Outer, event.Inner
			batch.Settlements = append(batch.Settlements, Settlement{Protocol: "solana", Chain: c.Chain, Asset: c.Token, TransactionHash: event.Signature, LogIndex: locator, OuterInstruction: &outer, InnerInstruction: &inner, SenderAccount: event.SenderAccount, RecipientAccount: event.RecipientAccount, BlockNumber: slot, BlockHash: block.Hash, BlockTime: *block.Time, Sender: event.Sender, Recipient: event.Recipient, Amount: event.Amount, Provider: rpc.Provider.Name, FinalizedNumber: root, FinalizedHash: final.Hash, Raw: raw})
		}
		batch.Blocks = append(batch.Blocks, SolanaHeader{Slot: slot, Block: block})
		parentSlot = slot
		parentHash = block.Hash
	}
	batch.To = parentSlot
	// Trailing slots are not declared skipped until a later produced block links them.
	if batch.To == root && parentHash != final.Hash {
		return nil, fmt.Errorf("%w: finalized Solana head disagreement", ErrConflict)
	}
	check, err := solBlock(ctx, rpc, root, false)
	if err != nil {
		return nil, err
	}
	if check.Hash != final.Hash {
		return nil, fmt.Errorf("%w: Solana finality anchor changed", ErrConflict)
	}
	return batch, nil
}
