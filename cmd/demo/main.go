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
	"github.com/ktechbro/settlement-proof/internal/testchain"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	primary := testchain.New()
	primary.FailLogsAfter = 1
	primary.Duplicates = true
	backup := testchain.New()
	backup.Duplicates = true
	p := httptest.NewServer(primary)
	defer p.Close()
	b := httptest.NewServer(backup)
	defer b.Close()
	db := os.Getenv("DATABASE_URL")
	if db == "" {
		db = "postgres://settlement:settlement-local@127.0.0.1:55437/settlement?sslmode=disable"
	}
	c := indexer.Config{Chain: "synthetic-base-demo", ChainID: "0x2105", Token: testchain.Token, Watch: []string{testchain.Buyer}, Start: 1, AnchorNumber: 0, AnchorHash: testchain.Hash(0), BatchSize: 2, Replay: 1, Poll: time.Second, DatabaseURL: db, Providers: []indexer.Provider{{Name: "primary", URL: p.URL}, {Name: "backup", URL: b.URL}}}
	if runID := os.Getenv("DEMO_RUN_ID"); runID != "" {
		c.Chain += "-" + runID
	}
	s, err := indexer.OpenStore(ctx, c)
	if err != nil {
		return err
	}
	w := indexer.NewWorker(c, s)
	initial, err := s.Checkpoint(ctx)
	if err != nil {
		s.Pool.Close()
		return err
	}
	_, err = w.Step(ctx)
	if err != nil {
		s.Pool.Close()
		return err
	}
	// Reopen both DB pool and worker to demonstrate durable checkpoint recovery.
	s.Pool.Close()
	s, err = indexer.OpenStore(ctx, c)
	if err != nil {
		return err
	}
	defer s.Pool.Close()
	w = indexer.NewWorker(c, s)
	for i := 0; i < 10; i++ {
		advanced, e := w.Step(ctx)
		if e != nil {
			return e
		}
		if !advanced {
			break
		}
	}
	values, err := s.List(ctx, indexer.Filter{To: math.MaxInt64, Limit: 100})
	if err != nil {
		return err
	}
	expected := map[string]string{testchain.Hash(101) + ":0": "50000000", testchain.Hash(102) + ":0": "7000000", testchain.Hash(102) + ":1": "9000000", testchain.Hash(104) + ":0": "1"}
	if len(values) != len(expected) {
		return fmt.Errorf("oracle mismatch: got %d events", len(values))
	}
	for _, v := range values {
		key := fmt.Sprintf("%s:%d", v.TransactionHash, v.LogIndex)
		if expected[key] != v.Amount {
			return fmt.Errorf("oracle mismatch for %s", key)
		}
	}
	cp, err := s.Checkpoint(ctx)
	if err != nil {
		return err
	}
	if cp.Height != 5 {
		return fmt.Errorf("checkpoint did not reach fixture end")
	}
	var outbox int
	if err = s.Pool.QueryRow(ctx, "SELECT count(*) FROM outbox o JOIN settlement_scopes ss ON ss.settlement_id=o.settlement_id WHERE ss.scope=$1", s.Scope).Scan(&outbox); err != nil {
		return err
	}
	if outbox != 4 {
		return fmt.Errorf("outbox oracle mismatch")
	}
	report := map[string]any{"status": "PASS", "network": "synthetic fixture, not live Base", "settlements": len(values), "total_atomic": "66000001", "checkpoint": cp, "outbox_effects": outbox, "unsafe_block_6_excluded": true, "history_reused": initial.Height == 5, "faults_exercised_this_run": primary.LogCalls > 1 && backup.LogCalls > 0, "durable_restart": true, "primary_log_calls": primary.LogCalls, "backup_log_calls": backup.LogCalls, "note": "Repeat runs reuse the completed scope; set a new DEMO_RUN_ID to exercise failover again."}
	return json.NewEncoder(os.Stdout).Encode(report)
}
