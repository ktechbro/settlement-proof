package indexer_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/ktechbro/settlement-proof/internal/indexer"
	"github.com/ktechbro/settlement-proof/internal/testchain"
)

func databaseConfig(t *testing.T, url string) indexer.Config {
	t.Helper()
	db := os.Getenv("TEST_DATABASE_URL")
	if db == "" {
		t.Skip("set TEST_DATABASE_URL to exercise real PostgreSQL transactions")
	}
	c := config(url)
	c.DatabaseURL = db
	c.Chain = fmt.Sprintf("test-%d", time.Now().UnixNano())
	return c
}
func openTestStore(t *testing.T, c indexer.Config) *indexer.Store {
	t.Helper()
	s, err := indexer.OpenStore(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Pool.Close)
	return s
}

func TestPostgresRecoveryReplayScopeAndAPI(t *testing.T) {
	fixture := testchain.New()
	fixture.Duplicates = true
	fixture.FailLogsAfter = 1
	p := httptest.NewServer(fixture)
	defer p.Close()
	backup := testchain.New()
	b := httptest.NewServer(backup)
	defer b.Close()
	c := databaseConfig(t, p.URL)
	c.Providers = append(c.Providers, indexer.Provider{Name: "backup", URL: b.URL})
	s := openTestStore(t, c)
	w := indexer.NewWorker(c, s)
	ctx := context.Background()
	if _, err := w.Step(ctx); err != nil {
		t.Fatal(err)
	}
	s.Pool.Close()
	s = openTestStore(t, c)
	w = indexer.NewWorker(c, s)
	for i := 0; i < 5; i++ {
		if _, err := w.Step(ctx); err != nil {
			t.Fatal(err)
		}
	}
	values, err := s.List(ctx, indexer.Filter{To: math.MaxInt64, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 4 {
		t.Fatalf("got %d settlements", len(values))
	}
	total := int64(0)
	for _, v := range values {
		n, _ := strconv.ParseInt(v.Amount, 10, 64)
		total += n
	}
	if total != 66000001 {
		t.Fatalf("wrong total %d", total)
	}
	var count int
	if err = s.Pool.QueryRow(ctx, "SELECT count(*) FROM outbox o JOIN settlement_scopes ss ON ss.settlement_id=o.settlement_id WHERE ss.scope=$1", s.Scope).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 4 {
		t.Fatal("duplicate outbox effects")
	}
	api := httptest.NewServer(indexer.Handler(s, w))
	defer api.Close()
	for _, path := range []string{"/", "/health/live", "/health/ready", "/metrics", "/v1/settlements?limit=2", "/v1/chains/" + c.Chain + "/status", "/v1/chains/" + c.Chain + "/coverage"} {
		resp, e := api.Client().Get(api.URL + path)
		if e != nil {
			t.Fatal(e)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("%s: %d", path, resp.StatusCode)
		}
	}
	resp, err := api.Client().Get(api.URL + "/v1/settlements?limit=0")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatal("invalid pagination accepted")
	}
	// A new target produces a new scope and backfills old history, reusing effects.
	c.Watch = append(c.Watch, testchain.Seller)
	s2 := openTestStore(t, c)
	w2 := indexer.NewWorker(c, s2)
	for i := 0; i < 5; i++ {
		if _, e := w2.Step(ctx); e != nil {
			t.Fatal(e)
		}
	}
	v2, err := s2.List(ctx, indexer.Filter{To: math.MaxInt64, Limit: 100})
	if err != nil || len(v2) != 4 {
		t.Fatalf("new scope did not backfill: %v", err)
	}
	// Replay finalized block conflict is durable and changes readiness.
	backup.Mu.Lock()
	backup.ConflictHeight = 5
	backup.Mu.Unlock()
	fixture.Mu.Lock()
	fixture.Final = 1
	fixture.Mu.Unlock()
	if _, err = w.Step(ctx); !errors.Is(err, indexer.ErrConflict) {
		t.Fatalf("expected finalized conflict: %v", err)
	}
	cp, _ := s.Checkpoint(ctx)
	if !cp.Halted || cp.Height != 5 {
		t.Fatal("halt not durable")
	}
	resp, err = api.Client().Get(api.URL + "/health/ready")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 503 {
		t.Fatal("halted chain ready")
	}
}

func TestPostgresAtomicRollbackAndWorkerFencing(t *testing.T) {
	fixture := testchain.New()
	server := httptest.NewServer(fixture)
	defer server.Close()
	c := databaseConfig(t, server.URL)
	s := openTestStore(t, c)
	ctx := context.Background()
	capture := &memory{}
	w := indexer.NewWorker(c, capture)
	if _, err := w.Step(ctx); err != nil {
		t.Fatal(err)
	}
	batch := capture.batches[0]
	cp, err := s.Checkpoint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	bad := batch
	bad.Settlements = append([]indexer.Settlement(nil), batch.Settlements...)
	bad.Settlements[1].Amount = "not-a-number"
	if err = s.Commit(ctx, cp, bad); err == nil {
		t.Fatal("invalid write committed")
	}
	current, _ := s.Checkpoint(ctx)
	if current != cp {
		t.Fatal("checkpoint escaped rollback")
	}
	var rows int
	if err = s.Pool.QueryRow(ctx, "SELECT count(*) FROM settlements WHERE chain=$1", c.Chain).Scan(&rows); err != nil || rows != 0 {
		t.Fatal("partial financial effect escaped rollback")
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- s.Commit(ctx, cp, batch) }()
	}
	wg.Wait()
	close(results)
	successes, stale := 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, indexer.ErrStale) {
			stale++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || stale != 1 {
		t.Fatalf("workers not fenced: %d/%d", successes, stale)
	}
}

// The child exits without cleanup while a database transaction is open.
func TestCrashHelper(t *testing.T) {
	if os.Getenv("SETTLEMENT_CRASH_HELPER") != "1" {
		return
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		os.Exit(20)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		os.Exit(21)
	}
	if _, err = tx.Exec(ctx, "UPDATE checkpoints SET height=999 WHERE scope=$1", os.Getenv("SETTLEMENT_CRASH_SCOPE")); err != nil {
		os.Exit(22)
	}
	os.Exit(23)
}

func TestPostgresProcessCrashRollsBack(t *testing.T) {
	c := databaseConfig(t, "http://unused")
	s := openTestStore(t, c)
	cp, err := s.Checkpoint(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestCrashHelper$")
	cmd.Env = append(os.Environ(), "SETTLEMENT_CRASH_HELPER=1", "SETTLEMENT_CRASH_SCOPE="+s.Scope)
	err = cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 23 {
		t.Fatalf("child did not crash at intended point: %v", err)
	}
	got, err := s.Checkpoint(context.Background())
	if err != nil || got != cp {
		t.Fatalf("uncommitted checkpoint survived crash: %+v %v", got, err)
	}
}

func TestPostgresFreshProcessDemo(t *testing.T) {
	db := os.Getenv("TEST_DATABASE_URL")
	if db == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	// Exercise the actual CLI in a fresh process, including pool/worker restart.
	cmd := exec.Command("go", "run", "../../cmd/demo")
	cmd.Env = append(os.Environ(), "DATABASE_URL="+db, fmt.Sprintf("DEMO_RUN_ID=test-%d", time.Now().UnixNano()))
	cmd.Dir = filepath.Clean(".")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("demo failed: %s: %v", output, err)
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	var report map[string]any
	if err = json.Unmarshal([]byte(lines[len(lines)-1]), &report); err != nil {
		t.Fatal(err)
	}
	if report["status"] != "PASS" {
		t.Fatalf("demo report: %s", output)
	}
	if report["faults_exercised_this_run"] != true {
		t.Fatal("fresh demo did not exercise failures")
	}
}
