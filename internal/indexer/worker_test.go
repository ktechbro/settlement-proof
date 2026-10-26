package indexer_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ktechbro/settlement-proof/internal/indexer"
	"github.com/ktechbro/settlement-proof/internal/testchain"
)

type memory struct {
	cp      indexer.Checkpoint
	batches []indexer.Batch
	halted  bool
}

func (m *memory) Checkpoint(context.Context) (indexer.Checkpoint, error) { return m.cp, nil }
func (m *memory) Commit(_ context.Context, cp indexer.Checkpoint, b indexer.Batch) error {
	if cp != m.cp {
		return indexer.ErrStale
	}
	m.batches = append(m.batches, b)
	m.cp = indexer.Checkpoint{Height: b.To, Hash: b.End.Hash, Revision: cp.Revision + 1}
	return nil
}
func (m *memory) Halt(_ context.Context, _ string) error {
	m.halted = true
	m.cp.Halted = true
	return nil
}

func config(urls ...string) indexer.Config {
	c := indexer.Config{Chain: "test-base", ChainID: "0x2105", Token: testchain.Token, Watch: []string{testchain.Buyer}, Start: 1, AnchorNumber: 0, AnchorHash: testchain.Hash(0), BatchSize: 2, Replay: 1, Poll: time.Second, DatabaseURL: "test"}
	for i, u := range urls {
		c.Providers = append(c.Providers, indexer.Provider{Name: fmt.Sprintf("provider-%d", i), URL: u})
	}
	return c
}

func TestFinalizedOnlyOverlapAndMultipleMovements(t *testing.T) {
	fixture := testchain.New()
	fixture.Duplicates = true
	server := httptest.NewServer(fixture)
	defer server.Close()
	m := &memory{}
	w := indexer.NewWorker(config(server.URL), m)
	for i := 0; i < 4; i++ {
		if _, err := w.Step(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if m.cp.Height != 5 {
		t.Fatalf("checkpoint=%d", m.cp.Height)
	}
	seen := map[string]string{}
	for _, b := range m.batches {
		for _, v := range b.Settlements {
			if v.BlockNumber > 5 {
				t.Fatal("unsafe event finalized")
			}
			seen[fmt.Sprintf("%s:%d", v.TransactionHash, v.LogIndex)] = v.Amount
		}
	}
	if len(seen) != 4 {
		t.Fatalf("got %d unique movements", len(seen))
	}
	if got := seen[testchain.Hash(102)+":1"]; got != "9000000" {
		t.Fatalf("second movement lost: %s", got)
	}
}

func TestProviderFailoverAndLag(t *testing.T) {
	primary := testchain.New()
	primary.FailLogsAfter = 1
	backup := testchain.New()
	p := httptest.NewServer(primary)
	defer p.Close()
	b := httptest.NewServer(backup)
	defer b.Close()
	m := &memory{}
	w := indexer.NewWorker(config(p.URL, b.URL), m)
	if _, err := w.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if m.batches[1].Provider != "provider-1" {
		t.Fatal("did not fail over")
	}
	primary.Mu.Lock()
	primary.Final = 1
	primary.Mu.Unlock()
	if _, err := w.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if m.cp.Height != 5 {
		t.Fatal("lagging provider prevented progress")
	}
}

func TestFinalizedConflictHaltsBeforeFallback(t *testing.T) {
	fixture := testchain.New()
	s := httptest.NewServer(fixture)
	defer s.Close()
	m := &memory{}
	w := indexer.NewWorker(config(s.URL), m)
	if _, err := w.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	fixture.Mu.Lock()
	fixture.ConflictHeight = 2
	fixture.Mu.Unlock()
	if _, err := w.Step(context.Background()); !errors.Is(err, indexer.ErrConflict) {
		t.Fatalf("expected conflict: %v", err)
	}
	if !m.halted || m.cp.Height != 2 {
		t.Fatal("conflict advanced history or did not halt")
	}
}

func TestRangeLimitShrinksForwardWindow(t *testing.T) {
	fixture := testchain.New()
	fixture.RangeLimit = 2
	s := httptest.NewServer(fixture)
	defer s.Close()
	c := config(s.URL)
	c.BatchSize = 5
	c.Replay = 0
	m := &memory{}
	w := indexer.NewWorker(c, m)
	if _, err := w.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if m.cp.Height < 1 || m.cp.Height > 2 {
		t.Fatalf("unexpected shrunk checkpoint %d", m.cp.Height)
	}
}

func TestRangeLimitCanShrinkReplayOverlap(t *testing.T) {
	fixture := testchain.New()
	fixture.RangeLimit = 1
	s := httptest.NewServer(fixture)
	defer s.Close()
	c := config(s.URL)
	c.BatchSize = 5
	c.Replay = 10
	m := &memory{}
	w := indexer.NewWorker(c, m)
	for i := 0; i < 5; i++ {
		if _, err := w.Step(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if m.cp.Height != 5 {
		t.Fatal("overlap prevented forward progress")
	}
}

func TestMalformedOrMissingDataNeverAdvances(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testchain.Server)
	}{
		{"null finality", func(s *testchain.Server) { s.NullFinal = true }},
		{"removed log", func(s *testchain.Server) { s.Logs[0].Removed = true }},
		{"wrong block hash", func(s *testchain.Server) { s.Logs[0].BlockHash = testchain.Hash(900) }},
		{"malformed amount", func(s *testchain.Server) { s.Logs[0].Data = "0x01" }},
		{"malformed topic", func(s *testchain.Server) { s.Logs[0].Topics[1] = "0x01" }},
		{"conflicting duplicate", func(s *testchain.Server) {
			v := s.Logs[0]
			v.Data = "0x" + strings.Repeat("0", 63) + "1"
			s.Logs = append(s.Logs, v)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := testchain.New()
			tt.mutate(fixture)
			s := httptest.NewServer(fixture)
			defer s.Close()
			m := &memory{}
			w := indexer.NewWorker(config(s.URL), m)
			if _, err := w.Step(context.Background()); err == nil {
				t.Fatal("accepted bad evidence")
			}
			if len(m.batches) != 0 || m.cp.Height != 0 {
				t.Fatal("advanced despite missing evidence")
			}
		})
	}
}

func TestUint256PreservedExactly(t *testing.T) {
	fixture := testchain.New()
	max := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1)).String()
	fixture.Logs = []indexer.Log{testchain.Transfer(1, 1, 0, max)}
	s := httptest.NewServer(fixture)
	defer s.Close()
	m := &memory{}
	w := indexer.NewWorker(config(s.URL), m)
	if _, err := w.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := m.batches[0].Settlements[0].Amount; got != max {
		t.Fatal("amount lost precision")
	}
}

func TestScopeChangesWithCoverageButNotProvider(t *testing.T) {
	c := config("http://one")
	original := c.Scope()
	c.Providers[0].URL = "http://two"
	if c.Scope() != original {
		t.Fatal("provider switch changed scope")
	}
	c.Watch = append(c.Watch, testchain.Seller)
	if c.Scope() == original {
		t.Fatal("watch change reused checkpoint")
	}
	reversed := c
	c.Watch = []string{testchain.Seller, testchain.Buyer}
	if c.Scope() != reversed.Scope() {
		t.Fatal("watch order changed scope")
	}
}

func TestRPCRejectsWrongEnvelopeAndRedactsSecrets(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 999, "result": "secret"})
	}))
	defer s.Close()
	r := indexer.NewRPC(indexer.Provider{Name: "test", URL: s.URL + "/secret-token"})
	var out string
	err := r.Call(context.Background(), "eth_chainId", []any{}, &out)
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unsafe error: %v", err)
	}
}

func TestInvalidConfiguration(t *testing.T) {
	c := config("http://example.com")
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.AnchorNumber = c.Start
	if c.Validate() == nil {
		t.Fatal("anchor must precede scan")
	}
}
