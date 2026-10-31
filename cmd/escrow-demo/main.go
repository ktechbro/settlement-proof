package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"syscall"
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
	primary := testsolana.NewEscrow()
	primary.FailFullAfter = 1
	backup := testsolana.NewEscrow()
	p := httptest.NewServer(primary)
	defer p.Close()
	b := httptest.NewServer(backup)
	defer b.Close()
	db := os.Getenv("DATABASE_URL")
	if db == "" {
		db = "postgres://settlement:settlement-local@127.0.0.1:55437/settlement?sslmode=disable"
	}
	c := indexer.Config{Protocol: "solana", Decimals: 6, Chain: fmt.Sprint("synthetic-escrow-demo-", time.Now().UnixNano()), ChainID: testsolana.Genesis, Token: solana.USDCMint, EscrowPrograms: []string{testsolana.EscrowProgram}, Start: 101, AnchorNumber: 100, AnchorHash: testsolana.Key(100), BatchSize: 2, Replay: 0, Poll: time.Second, DatabaseURL: db, Providers: []indexer.Provider{{Name: "primary", URL: p.URL}, {Name: "backup", URL: b.URL}}}
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
	if len(values) != 5 {
		return fmt.Errorf("want five financial movements, got %d", len(values))
	}
	states := map[string]string{}
	for i, want := range []string{"released", "refunded", "funded"} {
		projection, err := store.EscrowDetail(ctx, testsolana.EscrowProgram, testsolana.EscrowAddress(byte(i+1)))
		if err != nil {
			return err
		}
		if projection.State != want {
			return fmt.Errorf("escrow %d: want %s, got %+v", i+1, want, projection)
		}
		locked := "0"
		if i == 2 {
			locked = "3000000"
		}
		if projection.Locked != locked {
			return fmt.Errorf("locked principal mismatch")
		}
		states[fmt.Sprint(i+1)] = projection.State
	}
	events, err := store.EscrowEvents(ctx, 0, 100)
	if err != nil {
		return err
	}
	if len(events) != 5 {
		return fmt.Errorf("native event count mismatch")
	}
	if export := os.Getenv("ESCROW_EXPORT_PATH"); export != "" {
		raw, err := json.MarshalIndent(map[string]any{"data": events, "next_cursor": fmt.Sprint(events[len(events)-1].Event.ID), "scope": store.Scope}, "", "  ")
		if err != nil {
			return err
		}
		if err = os.WriteFile(export, raw, 0600); err != nil {
			return err
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
	if err = json.NewEncoder(os.Stdout).Encode(map[string]any{"status": "PASS", "network": "synthetic Solana RPC fixture; no real funds", "states": states, "escrow_events": len(events), "settlements": len(values), "outbox_effects": effects, "scope": store.Scope, "restart_recovery": true, "provider_failover": backup.FullCalls > 0}); err != nil {
		return err
	}
	if listen := os.Getenv("ESCROW_DEMO_LISTEN"); listen != "" {
		service := &http.Server{Addr: listen, Handler: indexer.Handler(store, worker), ReadHeaderTimeout: 5 * time.Second}
		signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		go func() {
			<-signalCtx.Done()
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = service.Shutdown(shutdown)
		}()
		if err = service.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			return err
		}
	}
	return nil
}
