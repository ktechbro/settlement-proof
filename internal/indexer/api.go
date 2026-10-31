package indexer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"math"
	"net/http"
	"strconv"
	"time"
)

func Handler(s *Store, w *Worker) http.Handler {
	mux := http.NewServeMux()
	write := func(rw http.ResponseWriter, status int, value any) {
		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(status)
		_ = json.NewEncoder(rw).Encode(value)
	}
	fail := func(rw http.ResponseWriter) { write(rw, 503, map[string]string{"error": "database unavailable"}) }
	mux.HandleFunc("GET /v1/escrow-events", func(rw http.ResponseWriter, r *http.Request) {
		f, err := parseFilter(r)
		if err != nil {
			write(rw, 400, map[string]string{"error": err.Error()})
			return
		}
		values, err := s.EscrowEvents(r.Context(), f.After, f.Limit)
		if err != nil {
			fail(rw)
			return
		}
		next := f.After
		if len(values) > 0 {
			next = values[len(values)-1].Event.ID
		}
		write(rw, 200, map[string]any{"data": values, "next_cursor": strconv.FormatInt(next, 10), "scope": s.Scope})
	})
	mux.HandleFunc("GET /v1/escrows/{program}/{escrow}", func(rw http.ResponseWriter, r *http.Request) {
		value, err := s.EscrowDetail(r.Context(), r.PathValue("program"), r.PathValue("escrow"))
		if errors.Is(err, pgx.ErrNoRows) {
			write(rw, 404, map[string]string{"error": "not found"})
			return
		}
		if err != nil {
			fail(rw)
			return
		}
		write(rw, 200, value)
	})
	mux.HandleFunc("GET /health/live", func(rw http.ResponseWriter, r *http.Request) { write(rw, 200, map[string]string{"status": "alive"}) })
	mux.HandleFunc("GET /health/ready", func(rw http.ResponseWriter, r *http.Request) {
		cp, err := s.Checkpoint(r.Context())
		if err != nil {
			fail(rw)
			return
		}
		if cp.Halted {
			write(rw, 503, map[string]string{"status": "halted"})
			return
		}
		write(rw, 200, map[string]string{"status": "ready"})
	})
	mux.HandleFunc("GET /v1/settlements", func(rw http.ResponseWriter, r *http.Request) {
		f, err := parseFilter(r)
		if err != nil {
			write(rw, 400, map[string]string{"error": err.Error()})
			return
		}
		if chain := r.URL.Query().Get("chain"); chain != "" && chain != s.Chain {
			write(rw, 400, map[string]string{"error": "unknown chain"})
			return
		}
		values, err := s.List(r.Context(), f)
		if err != nil {
			fail(rw)
			return
		}
		next := f.After
		if len(values) > 0 {
			next = values[len(values)-1].ID
		}
		write(rw, 200, map[string]any{"data": values, "next_cursor": strconv.FormatInt(next, 10), "scope": s.Scope})
	})
	mux.HandleFunc("GET /v1/settlements/{id}", func(rw http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id < 1 {
			write(rw, 400, map[string]string{"error": "invalid id"})
			return
		}
		values, err := s.List(r.Context(), Filter{After: id - 1, To: math.MaxInt64, Limit: 1})
		if err != nil {
			fail(rw)
			return
		}
		if len(values) == 0 || values[0].ID != id {
			write(rw, 404, map[string]string{"error": "not found"})
			return
		}
		write(rw, 200, values[0])
	})
	mux.HandleFunc("GET /v1/chains/{chain}/status", func(rw http.ResponseWriter, r *http.Request) {
		if r.PathValue("chain") != s.Chain {
			write(rw, 404, map[string]string{"error": "unknown chain"})
			return
		}
		cp, err := s.Checkpoint(r.Context())
		if err != nil {
			fail(rw)
			return
		}
		protocol := w.Config.Protocol
		if protocol == "" {
			protocol = "evm"
		}
		write(rw, 200, map[string]any{"chain": s.Chain, "scope": s.Scope, "checkpoint": cp, "last_provider_finalized_height": w.Finalized.Load(), "finality_policy": w.Config.FinalityPolicy(), "protocol": protocol})
	})
	mux.HandleFunc("GET /v1/chains/{chain}/coverage", func(rw http.ResponseWriter, r *http.Request) {
		if r.PathValue("chain") != s.Chain {
			write(rw, 404, map[string]string{"error": "unknown chain"})
			return
		}
		f, err := parseFilter(r)
		if err != nil {
			write(rw, 400, map[string]string{"error": err.Error()})
			return
		}
		values, err := s.Coverage(r.Context(), f.After, f.Limit)
		if err != nil {
			fail(rw)
			return
		}
		next := f.After
		if len(values) > 0 {
			next = values[len(values)-1]["id"].(int64)
		}
		write(rw, 200, map[string]any{"data": values, "next_cursor": strconv.FormatInt(next, 10), "scope": s.Scope, "coverage": "configured token transfers involving registered addresses only"})
	})
	mux.HandleFunc("GET /metrics", func(rw http.ResponseWriter, r *http.Request) {
		cp, err := s.Checkpoint(r.Context())
		if err != nil {
			fail(rw)
			return
		}
		halt := 0
		if cp.Halted {
			halt = 1
		}
		rw.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintf(rw, "# TYPE settlement_rpc_failures_total counter\nsettlement_rpc_failures_total %d\n# TYPE settlement_batches_total counter\nsettlement_batches_total %d\n# TYPE settlement_checkpoint_height gauge\nsettlement_checkpoint_height %d\n# TYPE settlement_halted gauge\nsettlement_halted %d\n# TYPE settlement_finalized_head gauge\nsettlement_finalized_head %d\n", w.Failures.Load(), w.Batches.Load(), cp.Height, halt, w.Finalized.Load())
	})
	mux.HandleFunc("GET /", func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(rw, r)
			return
		}
		rw.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(rw, evidencePage)
	})
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("X-Content-Type-Options", "nosniff")
		rw.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; frame-ancestors 'none'")
		rw.Header().Set("Cache-Control", "no-store")
		ctx, cancel := contextTimeout(r)
		defer cancel()
		mux.ServeHTTP(rw, r.WithContext(ctx))
	})
}

const evidencePage = `<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>Settlement Proof</title><style>body{font:16px system-ui;background:#10151d;color:#dce7f5;max-width:950px;margin:60px auto;padding:20px}a{color:#66d9b0}pre{white-space:pre-wrap;overflow-wrap:anywhere;background:#19232f;padding:20px;border-radius:12px}h1{letter-spacing:-1px}</style><h1>Settlement Proof</h1><p>Finalized transfer evidence · Base and Solana · exact amounts · durable checkpoints</p><p><a href="/v1/settlements">Settlements</a> · <a href="/health/ready">Readiness</a> · <a href="/metrics">Metrics</a></p><p>This observer covers its configured token and registered addresses/accounts. Records appear after finality evidence permits them.</p><pre id="data">Loading evidence…</pre><script>fetch('/v1/settlements?limit=10').then(r=>r.json()).then(x=>document.getElementById('data').textContent=JSON.stringify(x,null,2)).catch(()=>document.getElementById('data').textContent='Unable to load evidence');</script></html>`

func parseFilter(r *http.Request) (Filter, error) {
	f := Filter{To: math.MaxInt64, Limit: 100}
	for _, item := range []struct {
		key string
		ptr *int64
	}{{"cursor", &f.After}, {"from", &f.From}, {"to", &f.To}} {
		if value := r.URL.Query().Get(item.key); value != "" {
			n, err := strconv.ParseInt(value, 10, 64)
			if err != nil || n < 0 {
				return f, fmt.Errorf("invalid %s", item.key)
			}
			*item.ptr = n
		}
	}
	if value := r.URL.Query().Get("limit"); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 500 {
			return f, fmt.Errorf("limit must be 1..500")
		}
		f.Limit = n
	}
	if f.From > f.To {
		return f, fmt.Errorf("from exceeds to")
	}
	return f, nil
}

// Limit database work even when clients disconnect or send slow requests.
func contextTimeout(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 10*time.Second)
}
