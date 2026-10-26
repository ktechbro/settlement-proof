package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ktechbro/settlement-proof/internal/indexer"
)

func main() {
	if err := run(); err != nil {
		slog.Error("service stopped", "error", err)
		os.Exit(1)
	}
}
func run() error {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	c, err := indexer.LoadConfig()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	initCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	s, err := indexer.OpenStore(initCtx, c)
	cancel()
	if err != nil {
		return errors.New("database initialization failed")
	}
	defer s.Pool.Close()
	w := indexer.NewWorker(c, s)
	server := &http.Server{Addr: c.Listen, Handler: indexer.Handler(s, w), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 60 * time.Second}
	serverErr := make(chan error, 1)
	go func() { serverErr <- server.ListenAndServe() }()
	workerErr := make(chan error, 1)
	go func() { workerErr <- w.Run(ctx) }()
	slog.Info("indexer started", "chain", c.Chain, "scope", c.Scope(), "listen", c.Listen)
	var result error
	select {
	case <-ctx.Done():
	case err = <-serverErr:
		if !errors.Is(err, http.ErrServerClosed) {
			result = err
		}
		stop()
	case err = <-workerErr:
		if errors.Is(err, indexer.ErrConflict) {
			slog.Error("chain halted; API remains available for investigation")
			select {
			case <-ctx.Done():
			case err = <-serverErr:
				result = err
				stop()
			}
		} else {
			result = err
			stop()
		}
	}
	shutdownCtx, finish := context.WithTimeout(context.Background(), 10*time.Second)
	defer finish()
	_ = server.Shutdown(shutdownCtx)
	return result
}
