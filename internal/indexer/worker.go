package indexer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"strings"
	"sync/atomic"
	"time"
)

type Storage interface {
	Checkpoint(context.Context) (Checkpoint, error)
	Commit(context.Context, Checkpoint, Batch) error
	Halt(context.Context, string) error
}

type Worker struct {
	Config    Config
	Store     Storage
	RPCs      []*RPC
	Failures  atomic.Int64
	Batches   atomic.Int64
	Finalized atomic.Int64
}

func NewWorker(c Config, s Storage) *Worker {
	w := &Worker{Config: c, Store: s}
	for _, p := range c.Providers {
		w.RPCs = append(w.RPCs, NewRPC(p))
	}
	return w
}

func (w *Worker) Run(ctx context.Context) error {
	delay := w.Config.Poll
	for {
		advanced, err := w.Step(ctx)
		if errors.Is(err, ErrConflict) {
			return err
		}
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			slog.Warn("scan failed; retrying", "error", err)
			delay *= 2
			if delay > 30*time.Second {
				delay = 30 * time.Second
			}
		} else {
			delay = w.Config.Poll
		}
		if advanced {
			continue
		}
		timer := time.NewTimer(delay + time.Duration(rand.Int63n(int64(delay/4+1))))
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

func (w *Worker) Step(ctx context.Context) (bool, error) {
	if w.Config.Protocol == "solana" {
		return w.stepSolana(ctx)
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
		batch, err := w.fetch(ctx, rpc, cp)
		if errors.Is(err, ErrConflict) {
			if e := w.Store.Halt(ctx, err.Error()); e != nil {
				return false, fmt.Errorf("persist halt: %w", e)
			}
			return false, err
		}
		if err != nil {
			w.Failures.Add(1)
			last = fmt.Errorf("provider %s: %w", rpc.Provider.Name, err)
			slog.Warn("provider unavailable", "provider", rpc.Provider.Name, "error", err)
			continue
		}
		if batch == nil {
			caughtUp = true
			continue
		}
		if err = w.Store.Commit(ctx, cp, *batch); err != nil {
			if errors.Is(err, ErrConflict) {
				if e := w.Store.Halt(ctx, err.Error()); e != nil {
					return false, e
				}
			}
			return false, err
		}
		w.Batches.Add(1)
		slog.Info("finalized range committed", "provider", rpc.Provider.Name, "from", batch.From, "to", batch.To, "events", len(batch.Settlements))
		return true, nil
	}
	if caughtUp {
		return false, nil
	}
	return false, last
}

func (w *Worker) fetch(ctx context.Context, rpc *RPC, cp Checkpoint) (*Batch, error) {
	c := w.Config
	var chainID string
	if err := rpc.Call(ctx, "eth_chainId", []any{}, &chainID); err != nil {
		return nil, err
	}
	actual, err := Quantity(chainID)
	if err != nil {
		return nil, err
	}
	expected, _ := Quantity(c.ChainID)
	if actual != expected {
		return nil, fmt.Errorf("wrong network identity")
	}
	anchor, err := rpc.Header(ctx, hexHeight(c.AnchorNumber))
	if err != nil {
		return nil, err
	}
	an, _ := Quantity(anchor.Number)
	if an != c.AnchorNumber || !strings.EqualFold(anchor.Hash, c.AnchorHash) {
		return nil, fmt.Errorf("configured trusted anchor mismatch")
	}
	final, err := rpc.Header(ctx, "finalized")
	if err != nil {
		return nil, err
	}
	fn, _ := Quantity(final.Number)
	w.Finalized.Store(fn)
	if fn < cp.Height {
		return nil, fmt.Errorf("provider finalized head behind checkpoint")
	}
	previous, err := rpc.Header(ctx, hexHeight(cp.Height))
	if err != nil {
		return nil, err
	}
	pn, _ := Quantity(previous.Number)
	if pn != cp.Height {
		return nil, fmt.Errorf("wrong checkpoint header number")
	}
	if cp.Hash != "" && !strings.EqualFold(previous.Hash, cp.Hash) {
		return nil, fmt.Errorf("%w: checkpoint %d expected %s got %s", ErrConflict, cp.Height, cp.Hash, previous.Hash)
	}
	if fn == cp.Height {
		if !strings.EqualFold(previous.Hash, final.Hash) {
			return nil, fmt.Errorf("%w: finalized tag disagrees with checkpoint header", ErrConflict)
		}
		return nil, nil
	}
	from := cp.Height - c.Replay + 1
	if from < c.Start {
		from = c.Start
	}
	to := cp.Height + c.BatchSize
	if to > fn {
		to = fn
	}
	// Keep at least one new block. If necessary, shrink optional overlap too.
	for {
		batch, err := w.fetchRange(ctx, rpc, from, to, cp, previous, final)
		if err == nil {
			return batch, nil
		}
		if !rangeError(err) {
			return nil, err
		}
		if to > cp.Height+1 {
			to = cp.Height + (to-cp.Height)/2
		} else if from < cp.Height+1 {
			from = from + (cp.Height+2-from)/2
		} else {
			return nil, err
		}
	}
}

func (w *Worker) fetchRange(ctx context.Context, rpc *RPC, from, to int64, cp Checkpoint, previous, final Header) (*Batch, error) {
	c := w.Config
	b := &Batch{From: from, To: to, Provider: rpc.Provider.Name, Finalized: final}
	headers := map[int64]Header{}
	var parent Header
	for n := from - 1; n <= to; n++ {
		h, err := rpc.Header(ctx, hexHeight(n))
		if err != nil {
			return nil, err
		}
		hn, _ := Quantity(h.Number)
		if hn != n {
			return nil, fmt.Errorf("incorrect range header number")
		}
		h.Hash = strings.ToLower(h.Hash)
		h.ParentHash = strings.ToLower(h.ParentHash)
		if n > from-1 && h.ParentHash != parent.Hash {
			return nil, fmt.Errorf("disconnected range headers")
		}
		if n == cp.Height && !strings.EqualFold(h.Hash, previous.Hash) {
			return nil, fmt.Errorf("%w: checkpoint changed during scan", ErrConflict)
		}
		headers[n] = h
		b.Headers = append(b.Headers, h)
		parent = h
	}
	var logs []Log
	if err := rpc.Call(ctx, "eth_getLogs", []any{map[string]any{"fromBlock": hexHeight(from), "toBlock": hexHeight(to), "address": c.Token, "topics": []any{TransferTopic}}}, &logs); err != nil {
		return nil, err
	}
	watch := map[string]bool{}
	for _, a := range c.Watch {
		watch[a] = true
	}
	seen := map[string]Settlement{}
	for _, l := range logs {
		n, err := Quantity(l.BlockNumber)
		if err != nil {
			return nil, err
		}
		h, ok := headers[n]
		if !ok || n < from || n > to {
			return nil, fmt.Errorf("log outside requested range")
		}
		v, err := decodeLog(l, h, c, rpc.Provider.Name, final)
		if err != nil {
			return nil, err
		}
		if !watch[v.Sender] && !watch[v.Recipient] {
			continue
		}
		key := fmt.Sprintf("%s:%d", v.TransactionHash, v.LogIndex)
		if old, ok := seen[key]; ok {
			if old.BlockHash != v.BlockHash || old.Amount != v.Amount || old.Sender != v.Sender || old.Recipient != v.Recipient {
				return nil, fmt.Errorf("duplicate log with inconsistent content")
			}
			continue
		}
		seen[key] = v
		b.Settlements = append(b.Settlements, v)
	}
	// Recheck the original finality anchor after retrieving the range.
	check, err := rpc.Header(ctx, final.Number)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(check.Hash, final.Hash) {
		return nil, fmt.Errorf("%w: finality anchor changed during scan", ErrConflict)
	}
	fn, _ := Quantity(final.Number)
	if to == fn && !strings.EqualFold(headers[to].Hash, final.Hash) {
		return nil, fmt.Errorf("%w: finality/range disagreement", ErrConflict)
	}
	b.End = headers[to]
	return b, nil
}
