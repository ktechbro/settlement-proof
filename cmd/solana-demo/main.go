package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http/httptest"
	"os"
	"time"

	"github.com/ktechbro/settlement-proof/internal/indexer"
	"github.com/ktechbro/settlement-proof/internal/solana"
	"github.com/ktechbro/settlement-proof/internal/testsolana"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	primary := testsolana.New()
	primary.FailFullAfter = 1
	backup := testsolana.New()
	p := httptest.NewServer(primary)
	defer p.Close()
	b := httptest.NewServer(backup)
	defer b.Close()
	db := os.Getenv("DATABASE_URL")
	if db == "" {
		db = "postgres://settlement:settlement-local@127.0.0.1:55437/settlement?sslmode=disable"
	}
	c := indexer.Config{Protocol: "solana", Decimals: 6, Chain: fmt.Sprint("synthetic-solana-demo-", time.Now().UnixNano()), ChainID: testsolana.Genesis, Token: solana.USDCMint, Watch: []string{testsolana.Source}, Start: 101, AnchorNumber: 100, AnchorHash: testsolana.Key(100), BatchSize: 2, Replay: 0, Poll: time.Second, DatabaseURL: db, Providers: []indexer.Provider{{Name: "primary", URL: p.URL}, {Name: "backup", URL: b.URL}}}
	if err := c.Validate(); err != nil {
		return err
	}
	store, err := indexer.OpenStore(ctx, c)
	if err != nil {
		return err
	}
	worker := indexer.NewWorker(c, store)
	if _, err = worker.Step(ctx); err != nil {
		store.Pool.Close()
		return err
	}
	store.Pool.Close()
	store, err = indexer.OpenStore(ctx, c)
	if err != nil {
		return err
	}
	defer store.Pool.Close()
	worker = indexer.NewWorker(c, store)
	for i := 0; i < 6; i++ {
		if _, err = worker.Step(ctx); err != nil {
			return err
		}
	}
	values, err := store.List(ctx, indexer.Filter{To: math.MaxInt64, Limit: 100})
	if err != nil {
		return err
	}
	expected := map[string]string{testsolana.Signature(11) + ":0:-1": "50000000", testsolana.Signature(11) + ":1:-1": "7000000", testsolana.Signature(12) + ":0:0": "9000000", testsolana.Signature(12) + ":0:1": "1", testsolana.Signature(13) + ":0:-1": "11000000"}
	if len(values) != len(expected) {
		return fmt.Errorf("oracle mismatch: want five transfers got %d", len(values))
	}
	for _, v := range values {
		if v.OuterInstruction == nil || v.InnerInstruction == nil {
			return fmt.Errorf("missing instruction identity")
		}
		key := fmt.Sprintf("%s:%d:%d", v.TransactionHash, *v.OuterInstruction, *v.InnerInstruction)
		if expected[key] != v.Amount {
			return fmt.Errorf("event oracle mismatch")
		}
	}
	cp, err := store.Checkpoint(ctx)
	if err != nil || cp.Height != 109 {
		return fmt.Errorf("checkpoint did not recover to finalized root")
	}
	var effects int
	if err = store.Pool.QueryRow(ctx, "SELECT count(*) FROM outbox o JOIN settlement_scopes ss ON ss.settlement_id=o.settlement_id WHERE ss.scope=$1", store.Scope).Scan(&effects); err != nil || effects != 5 {
		return fmt.Errorf("outbox oracle mismatch")
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"status": "PASS", "network": "synthetic Solana RPC fixture; no real token movement", "settlements": 5, "total_atomic": "77000001", "checkpoint": cp, "outbox_effects": effects, "restart_recovery": true, "provider_failover": backup.FullCalls > 0, "inner_instructions": true, "versioned_loaded_addresses": true, "skipped_slot_gaps": true, "failed_and_unfinalized_transactions_excluded": true})
}
