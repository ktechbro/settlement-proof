package indexer_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"testing"
	"time"

	"github.com/ktechbro/settlement-proof/internal/indexer"
	"github.com/ktechbro/settlement-proof/internal/solana"
)

// Opt-in, read-only mainnet smoke test. No wallet or transaction submission.
func TestLiveSolanaUSDC(t *testing.T) {
	endpoint := os.Getenv("LIVE_SOLANA_RPC_URL")
	if endpoint == "" {
		t.Skip("set LIVE_SOLANA_RPC_URL and TEST_DATABASE_URL for live verification")
	}
	base := databaseConfig(t, endpoint)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	rpc := indexer.NewRPC(indexer.Provider{Name: "live-solana", URL: endpoint})
	var genesis string
	if err := rpc.Call(ctx, "getGenesisHash", []any{}, &genesis); err != nil {
		t.Fatal(err)
	}
	var root int64
	if err := rpc.Call(ctx, "getSlot", []any{map[string]any{"commitment": "finalized"}}, &root); err != nil {
		t.Fatal(err)
	}
	params := func(slot int64, full bool) []any {
		details := "none"
		if full {
			details = "full"
		}
		return []any{slot, map[string]any{"commitment": "finalized", "encoding": "jsonParsed", "transactionDetails": details, "maxSupportedTransactionVersion": 1, "rewards": false}}
	}
	var block solana.Block
	if err := rpc.Call(ctx, "getBlock", params(root, true), &block); err != nil {
		t.Fatal(err)
	}
	if block.ParentSlot == nil {
		t.Fatal("missing parent slot")
	}
	var txs []solana.Transaction
	if err := json.Unmarshal(block.Transactions, &txs); err != nil {
		t.Fatal(err)
	}
	var account string
	var expected solana.Event
	var last error
	tried := map[string]bool{}
	for _, tx := range txs {
		if tx.Meta == nil {
			continue
		}
		for _, balance := range append(append([]solana.Balance(nil), tx.Meta.Pre...), tx.Meta.Post...) {
			if balance.Mint != solana.USDCMint || balance.Index < 0 || balance.Index >= len(tx.Transaction.Message.Keys) {
				continue
			}
			var key struct {
				Pubkey string `json:"pubkey"`
			}
			if json.Unmarshal(tx.Transaction.Message.Keys[balance.Index], &key) != nil || !solana.Valid58(key.Pubkey, 32) || tried[key.Pubkey] {
				continue
			}
			tried[key.Pubkey] = true
			events, err := (solana.Decoder{Mint: solana.USDCMint, Decimals: 6, Accounts: []string{key.Pubkey}}).Decode(block)
			if err != nil {
				last = err
				continue
			}
			if len(events) > 0 {
				account = key.Pubkey
				expected = events[0]
				break
			}
			if len(tried) >= 30 {
				break
			}
		}
		if account != "" || len(tried) >= 30 {
			break
		}
	}
	if account == "" {
		t.Fatalf("no supported USDC transfer in selected finalized block; retry another block (last decoder error: %v)", last)
	}
	var anchor solana.Block
	if err := rpc.Call(ctx, "getBlock", params(*block.ParentSlot, false), &anchor); err != nil {
		t.Fatal(err)
	}
	c := indexer.Config{Protocol: "solana", Decimals: 6, Chain: base.Chain + "-solana-live", ChainID: genesis, Token: solana.USDCMint, Watch: []string{account}, Start: *block.ParentSlot + 1, AnchorNumber: *block.ParentSlot, AnchorHash: anchor.Hash, BatchSize: 1, Replay: 0, Poll: time.Second, DatabaseURL: base.DatabaseURL, Providers: []indexer.Provider{{Name: "live-solana", URL: endpoint}}}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	store := openTestStore(t, c)
	worker := indexer.NewWorker(c, store)
	if advanced, err := worker.Step(ctx); err != nil || !advanced {
		t.Fatal("live scanner failed", err)
	}
	values, err := store.List(ctx, indexer.Filter{To: math.MaxInt64, Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range values {
		if value.TransactionHash == expected.Signature && value.OuterInstruction != nil && value.InnerInstruction != nil && *value.OuterInstruction == expected.Outer && *value.InnerInstruction == expected.Inner {
			if value.Amount != expected.Amount || value.BlockNumber != root || value.BlockHash != block.Hash {
				t.Fatal("live settlement evidence mismatch")
			}
			t.Logf("verified live Solana USDC transfer: slot=%d signature=%s outer=%d inner=%d amount_atomic=%s token_account=%s", root, value.TransactionHash, *value.OuterInstruction, *value.InnerInstruction, value.Amount, account)
			return
		}
	}
	t.Fatal(fmt.Sprint("live transfer missing from persisted evidence for slot ", root))
}
