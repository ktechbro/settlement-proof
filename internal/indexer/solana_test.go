package indexer_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ktechbro/settlement-proof/internal/indexer"
	"github.com/ktechbro/settlement-proof/internal/solana"
	"github.com/ktechbro/settlement-proof/internal/testsolana"
)

func solConfig(urls ...string) indexer.Config {
	c := indexer.Config{Protocol: "solana", Decimals: 6, Chain: "synthetic-solana", ChainID: testsolana.Genesis, Token: solana.USDCMint, Watch: []string{testsolana.Source}, Start: 101, AnchorNumber: 100, AnchorHash: testsolana.Key(100), BatchSize: 2, Replay: 0, Poll: time.Second, DatabaseURL: "test"}
	for i, url := range urls {
		c.Providers = append(c.Providers, indexer.Provider{Name: fmt.Sprint("solana-", i), URL: url})
	}
	return c
}

type solMemory struct {
	memory
	solBatches []indexer.SolanaBatch
}

func (m *solMemory) CommitSolana(_ context.Context, cp indexer.Checkpoint, b indexer.SolanaBatch) error {
	if m.cp != cp {
		return indexer.ErrStale
	}
	m.solBatches = append(m.solBatches, b)
	m.cp = indexer.Checkpoint{Height: b.To, Hash: b.Blocks[len(b.Blocks)-1].Block.Hash, Revision: cp.Revision + 1}
	return nil
}
func solMem() *solMemory {
	return &solMemory{memory: memory{cp: indexer.Checkpoint{Height: 100, Hash: testsolana.Key(100)}}}
}

func TestSolanaFinalizedBackfillFailoverAndSkippedSlots(t *testing.T) {
	primary := testsolana.New()
	primary.FailFullAfter = 1
	backup := testsolana.New()
	p := httptest.NewServer(primary)
	defer p.Close()
	b := httptest.NewServer(backup)
	defer b.Close()
	c := solConfig(p.URL, b.URL)
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	m := solMem()
	w := indexer.NewWorker(c, m)
	for i := 0; i < 6; i++ {
		if _, err := w.Step(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if m.cp.Height != 109 {
		t.Fatal("did not recover produced slot history", m.cp)
	}
	values := []indexer.Settlement{}
	for _, batch := range m.solBatches {
		values = append(values, batch.Settlements...)
	}
	checkSolanaOracle(t, values)
	if m.solBatches[1].Provider != "solana-1" {
		t.Fatal("backup was not used")
	}
	if m.solBatches[2].From != 104 || m.solBatches[2].To != 109 {
		t.Fatal("long skipped interval was not bridged", m.solBatches[2])
	}
}

func checkSolanaOracle(t *testing.T, values []indexer.Settlement) {
	t.Helper()
	expected := map[string]string{testsolana.Signature(11) + ":0:-1": "50000000", testsolana.Signature(11) + ":1:-1": "7000000", testsolana.Signature(12) + ":0:0": "9000000", testsolana.Signature(12) + ":0:1": "1", testsolana.Signature(13) + ":0:-1": "11000000"}
	if len(values) != 5 {
		t.Fatalf("want five finalized transfers got %d", len(values))
	}
	seen := map[string]bool{}
	for _, v := range values {
		if v.OuterInstruction == nil || v.InnerInstruction == nil {
			t.Fatal("instruction locator lost")
		}
		key := fmt.Sprintf("%s:%d:%d", v.TransactionHash, *v.OuterInstruction, *v.InnerInstruction)
		if seen[key] || expected[key] != v.Amount {
			t.Fatal("event oracle mismatch", key, v.Amount)
		}
		seen[key] = true
		if v.Protocol != "solana" || v.Sender != testsolana.Buyer || v.Recipient != testsolana.Seller || v.SenderAccount != testsolana.Source || v.BlockNumber > 109 {
			t.Fatal("identity/finality mismatch", v)
		}
	}
}

func TestSolanaIncompleteHistoryNeverAdvances(t *testing.T) {
	for _, name := range []string{"omitted produced block", "null full block", "pruned checkpoint", "wrong genesis", "missing token identity"} {
		t.Run(name, func(t *testing.T) {
			fixture := testsolana.New()
			switch name {
			case "omitted produced block":
				fixture.HiddenSlot = 101
			case "null full block":
				fixture.NullFullSlot = 101
			case "pruned checkpoint":
				fixture.FirstAvailable = 101
			case "wrong genesis":
				fixture.WrongGenesis = true
			case "missing token identity":
				tx := testsolana.Transaction(11, []string{"5"}, false)
				tx["meta"].(map[string]any)["preTokenBalances"] = nil
				tx["meta"].(map[string]any)["postTokenBalances"] = nil
				fixture.Blocks[101] = testsolana.Block(101, 100, tx)
			}
			server := httptest.NewServer(fixture)
			defer server.Close()
			m := solMem()
			w := indexer.NewWorker(solConfig(server.URL), m)
			if _, err := w.Step(context.Background()); err == nil {
				t.Fatal("incomplete source accepted")
			}
			if m.cp.Height != 100 || len(m.solBatches) != 0 {
				t.Fatal("checkpoint advanced over a gap")
			}
		})
	}
}

func TestSolanaFinalizedConflictIsDurableHalt(t *testing.T) {
	fixture := testsolana.New()
	server := httptest.NewServer(fixture)
	defer server.Close()
	m := solMem()
	w := indexer.NewWorker(solConfig(server.URL), m)
	if _, err := w.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	fixture.Mu.Lock()
	fixture.ConflictSlot = 101
	fixture.Mu.Unlock()
	if _, err := w.Step(context.Background()); !errors.Is(err, indexer.ErrConflict) {
		t.Fatal("missing finality conflict", err)
	}
	if !m.halted || m.cp.Height != 101 {
		t.Fatal("finalized conflict was not halted")
	}
}

func TestSolanaConfigPreservesCaseAndScope(t *testing.T) {
	c := solConfig("http://example.test")
	t.Setenv("CHAIN_PROTOCOL", "solana")
	t.Setenv("CHAIN", c.Chain)
	t.Setenv("CHAIN_ID", c.ChainID)
	t.Setenv("TOKEN_ADDRESS", c.Token)
	t.Setenv("ANCHOR_NUMBER", "100")
	t.Setenv("ANCHOR_HASH", c.AnchorHash)
	t.Setenv("START_BLOCK", "101")
	t.Setenv("WATCH_ADDRESSES", testsolana.Source)
	t.Setenv("RPC_PROVIDERS", `[{"name":"test","url":"http://example.test"}]`)
	t.Setenv("DATABASE_URL", "test")
	t.Setenv("REPLAY_BLOCKS", "0")
	loaded, err := indexer.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Token != solana.USDCMint || loaded.AnchorHash != c.AnchorHash || loaded.Watch[0] != testsolana.Source {
		t.Fatal("base58 identity lowercased")
	}
	original := c.Scope()
	c.Providers[0].URL = "http://replacement.test"
	if original != c.Scope() {
		t.Fatal("provider changed scope")
	}
	c.Watch = append(c.Watch, testsolana.Destination)
	if original == c.Scope() {
		t.Fatal("new accounts inherited an old scope")
	}
}

func TestPostgresSolanaMigrationRecoveryAndAPI(t *testing.T) {
	fixture := testsolana.New()
	fixture.FailFullAfter = 1
	backup := testsolana.New()
	p := httptest.NewServer(fixture)
	defer p.Close()
	b := httptest.NewServer(backup)
	defer b.Close()
	base := databaseConfig(t, p.URL)
	c := solConfig(p.URL, b.URL)
	c.DatabaseURL = base.DatabaseURL
	c.Chain = base.Chain + "-solana"
	s := openTestStore(t, c)
	w := indexer.NewWorker(c, s)
	ctx := context.Background()
	if _, err := w.Step(ctx); err != nil {
		t.Fatal(err)
	}
	s.Pool.Close()
	s = openTestStore(t, c)
	w = indexer.NewWorker(c, s)
	for i := 0; i < 6; i++ {
		if _, err := w.Step(ctx); err != nil {
			t.Fatal(err)
		}
	}
	values, err := s.List(ctx, indexer.Filter{To: math.MaxInt64, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	checkSolanaOracle(t, values)
	var outbox int
	if err = s.Pool.QueryRow(ctx, "SELECT count(*) FROM outbox o JOIN settlement_scopes ss ON ss.settlement_id=o.settlement_id WHERE ss.scope=$1", s.Scope).Scan(&outbox); err != nil || outbox != 5 {
		t.Fatal("outbox effects were duplicated", outbox, err)
	}
	coverage, err := s.Coverage(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(coverage) != 3 || coverage[2]["skipped_slots"].(*int64) == nil || *coverage[2]["skipped_slots"].(*int64) != 4 {
		t.Fatal("skipped-slot evidence missing", coverage)
	}
	server := httptest.NewServer(indexer.Handler(s, w))
	defer server.Close()
	for _, path := range []string{"/v1/settlements", "/v1/chains/" + c.Chain + "/status", "/health/ready"} {
		response, err := server.Client().Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 200 {
			t.Fatal(path, response.StatusCode)
		}
	}
	// A new watch scope replays history but reuses the same canonical financial effects.
	c.Watch = append(c.Watch, testsolana.Destination)
	second := openTestStore(t, c)
	w2 := indexer.NewWorker(c, second)
	for i := 0; i < 5; i++ {
		if _, err := w2.Step(ctx); err != nil {
			t.Fatal(err)
		}
	}
	again, err := second.List(ctx, indexer.Filter{To: math.MaxInt64, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	checkSolanaOracle(t, again)
	backup.Mu.Lock()
	backup.ConflictSlot = 109
	backup.Mu.Unlock()
	fixture.Mu.Lock()
	fixture.Root = 100
	fixture.Mu.Unlock()
	if _, err = w.Step(ctx); !errors.Is(err, indexer.ErrConflict) {
		t.Fatal(err)
	}
	cp, err := s.Checkpoint(ctx)
	if err != nil || !cp.Halted {
		t.Fatal("halt not persisted", cp, err)
	}
}

func TestPostgresSolanaAtomicRollbackAndFence(t *testing.T) {
	fixture := testsolana.New()
	server := httptest.NewServer(fixture)
	defer server.Close()
	base := databaseConfig(t, server.URL)
	c := solConfig(server.URL)
	c.DatabaseURL = base.DatabaseURL
	c.Chain = base.Chain + "-solana"
	captured := solMem()
	w := indexer.NewWorker(c, captured)
	if _, err := w.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	batch := captured.solBatches[0]
	store := openTestStore(t, c)
	ctx := context.Background()
	cp, err := store.Checkpoint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	bad := batch
	bad.Settlements = append([]indexer.Settlement(nil), batch.Settlements...)
	bad.Settlements[1].Amount = "invalid"
	if err = store.CommitSolana(ctx, cp, bad); err == nil {
		t.Fatal("invalid financial evidence committed")
	}
	current, err := store.Checkpoint(ctx)
	if err != nil || current != cp {
		t.Fatal("checkpoint escaped rollback", err)
	}
	values, err := store.List(ctx, indexer.Filter{To: math.MaxInt64, Limit: 100})
	if err != nil || len(values) != 0 {
		t.Fatal("partial events escaped rollback", err)
	}
	if err = store.CommitSolana(ctx, cp, batch); err != nil {
		t.Fatal(err)
	}
	if err = store.CommitSolana(ctx, cp, batch); !errors.Is(err, indexer.ErrStale) {
		t.Fatal("stale Solana worker not fenced", err)
	}
}
