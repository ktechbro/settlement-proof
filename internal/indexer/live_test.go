package indexer_test

import (
	"context"
	"fmt"
	"math"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ktechbro/settlement-proof/internal/indexer"
)

// Explicit opt-in: reads public chain data and writes only to the test database.
// Never submits a transaction or accesses a wallet.
func TestLiveBaseReadOnly(t *testing.T) {
	endpoint := os.Getenv("LIVE_BASE_RPC_URL")
	if endpoint == "" {
		t.Skip("set LIVE_BASE_RPC_URL and TEST_DATABASE_URL for opt-in live-chain verification")
	}
	c := databaseConfig(t, endpoint)
	c.Token = "0x833589fcd6edb6e08f4c7c32d4f71b54bda02913"
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	rpc := indexer.NewRPC(indexer.Provider{Name: "live-base", URL: endpoint})
	final, err := rpc.Header(ctx, "finalized")
	if err != nil {
		t.Fatal(err)
	}
	height, err := indexer.Quantity(final.Number)
	if err != nil || height < 1 {
		t.Fatal("invalid finalized height")
	}
	anchor, err := rpc.Header(ctx, fmt.Sprintf("0x%x", height-1))
	if err != nil {
		t.Fatal(err)
	}
	var logs []indexer.Log
	err = rpc.Call(ctx, "eth_getLogs", []any{map[string]any{"address": c.Token, "fromBlock": final.Number, "toBlock": final.Number, "topics": []any{indexer.TransferTopic}}}, &logs)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) == 0 {
		t.Fatal("finalized fixture block has no USDC transfers; retry against a later block")
	}
	expected := logs[0]
	if len(expected.Topics) != 3 || len(expected.Topics[1]) != 66 {
		t.Fatal("unexpected transfer shape")
	}
	c.Watch = []string{"0x" + strings.ToLower(expected.Topics[1][26:])}
	c.Start = height
	c.AnchorNumber = height - 1
	c.AnchorHash = strings.ToLower(anchor.Hash)
	c.BatchSize = 1
	c.Replay = 0
	if err = c.Validate(); err != nil {
		t.Fatal(err)
	}
	s := openTestStore(t, c)
	w := indexer.NewWorker(c, s)
	if advanced, err := w.Step(ctx); err != nil || !advanced {
		t.Fatalf("live scanner did not advance: %v", err)
	}
	values, err := s.List(ctx, indexer.Filter{To: math.MaxInt64, Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	index, _ := indexer.Quantity(expected.LogIndex)
	amount, ok := new(big.Int).SetString(strings.TrimPrefix(expected.Data, "0x"), 16)
	if !ok {
		t.Fatal("invalid expected amount")
	}
	for _, v := range values {
		if v.TransactionHash == strings.ToLower(expected.TransactionHash) && v.LogIndex == index {
			if v.Amount != amount.String() || v.BlockNumber != height || v.BlockHash != strings.ToLower(final.Hash) {
				t.Fatal("live evidence mismatch")
			}
			t.Logf("verified live Base USDC transfer: block=%d tx=%s log=%d amount_atomic=%s", height, v.TransactionHash, index, v.Amount)
			return
		}
	}
	t.Fatal("expected live transfer missing from persisted evidence")
}
