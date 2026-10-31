package indexer_test

import (
	"context"
	"github.com/ktechbro/settlement-proof/internal/indexer"
	"github.com/ktechbro/settlement-proof/internal/testsolana"
	"net/http/httptest"
	"testing"
)

func TestPostgresEscrowLifecycleRecoveryAndReplay(t *testing.T) {
	primary, backup := testsolana.NewEscrow(), testsolana.NewEscrow()
	primary.FailFullAfter = 1
	p, b := httptest.NewServer(primary), httptest.NewServer(backup)
	defer p.Close()
	defer b.Close()
	base := databaseConfig(t, p.URL)
	c := solConfig(p.URL, b.URL)
	c.DatabaseURL = base.DatabaseURL
	c.Chain = base.Chain + "-escrow"
	c.Watch = nil
	c.EscrowPrograms = []string{testsolana.EscrowProgram}
	ctx := context.Background()
	store := openTestStore(t, c)
	w := indexer.NewWorker(c, store)
	if _, err := w.Step(ctx); err != nil {
		t.Fatal(err)
	}
	store.Pool.Close()
	store = openTestStore(t, c)
	w = indexer.NewWorker(c, store)
	for i := 0; i < 6; i++ {
		if _, err := w.Step(ctx); err != nil {
			t.Fatal(err)
		}
	}
	values, err := store.EscrowEvents(ctx, 0, 100)
	if err != nil || len(values) != 5 {
		t.Fatal(len(values), err)
	}
	for _, v := range values {
		if v.Event.SettlementID != v.Settlement.ID || v.Event.Signature != v.Settlement.TransactionHash {
			t.Fatal("unpaired movement")
		}
	}
	for i, want := range []string{"released", "refunded", "funded"} {
		result, err := store.EscrowDetail(ctx, testsolana.EscrowProgram, testsolana.EscrowAddress(byte(i+1)))
		if err != nil || result.State != want {
			t.Fatal(result, err)
		}
		locked := "0"
		if i == 2 {
			locked = "3000000"
		}
		if result.Locked != locked || result.ObservedVaultDelta != locked {
			t.Fatal(result)
		}
	}
	server := httptest.NewServer(indexer.Handler(store, w))
	defer server.Close()
	for _, path := range []string{"/v1/escrow-events", "/v1/escrows/" + testsolana.EscrowProgram + "/" + testsolana.EscrowAddress(1)} {
		r, err := server.Client().Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if r.StatusCode != 200 {
			t.Fatal(path, r.StatusCode)
		}
	}
	c.Watch = []string{testsolana.Source}
	second := openTestStore(t, c)
	w2 := indexer.NewWorker(c, second)
	for i := 0; i < 6; i++ {
		if _, err := w2.Step(ctx); err != nil {
			t.Fatal(err)
		}
	}
	replay, err := second.EscrowEvents(ctx, 0, 100)
	if err != nil || len(replay) != 5 {
		t.Fatal(len(replay), err)
	}
	for i := range replay {
		if replay[i].Event.ID != values[i].Event.ID {
			t.Fatal("canonical native event duplicated")
		}
	}
	// Restart from a completed checkpoint and discover a later unsolicited vault transfer.
	backup.Mu.Lock()
	backup.Blocks[110] = testsolana.Block(110, 109, testsolana.UnsolicitedVaultTransfer(36, 1, "77"))
	backup.Root = 110
	backup.Mu.Unlock()
	primary.Mu.Lock()
	primary.Root = 100
	primary.Mu.Unlock()
	store.Pool.Close()
	store = openTestStore(t, c)
	w = indexer.NewWorker(c, store)
	if _, err := w.Step(ctx); err != nil {
		t.Fatal(err)
	}
	result, err := store.EscrowDetail(ctx, testsolana.EscrowProgram, testsolana.EscrowAddress(1))
	if err != nil || result.State != "exception" || result.ObservedVaultDelta != "77" {
		t.Fatal(result, err)
	}
}
func TestPostgresEscrowAtomicRollback(t *testing.T) {
	fixture := httptest.NewServer(testsolana.NewEscrow())
	defer fixture.Close()
	base := databaseConfig(t, fixture.URL)
	c := solConfig(fixture.URL)
	c.DatabaseURL = base.DatabaseURL
	c.Chain = base.Chain + "-escrow"
	c.EscrowPrograms = []string{testsolana.EscrowProgram}
	capture := solMem()
	worker := indexer.NewWorker(c, capture)
	ctx := context.Background()
	if _, err := worker.Step(ctx); err != nil {
		t.Fatal(err)
	}
	batch := capture.solBatches[0]
	batch.EscrowEvents[0].EventKey = "invalid"
	store := openTestStore(t, c)
	before, err := store.Checkpoint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.CommitSolana(ctx, before, batch); err == nil {
		t.Fatal("expected rejection")
	}
	after, _ := store.Checkpoint(ctx)
	values, err := store.EscrowEvents(ctx, 0, 100)
	if err != nil || before != after || len(values) != 0 {
		t.Fatal("partial native commit", after, err)
	}
	var count int
	err = store.Pool.QueryRow(ctx, "SELECT count(*) FROM settlements WHERE chain=$1", c.Chain).Scan(&count)
	if err != nil || count != 0 {
		t.Fatal("partial financial commit", count, err)
	}
}

func (m *solMemory) EscrowVaults(context.Context) ([]string, error) {
	out := []string{}
	for _, b := range m.solBatches {
		for _, e := range b.EscrowEvents {
			out = append(out, e.Vault)
		}
	}
	return out, nil
}
