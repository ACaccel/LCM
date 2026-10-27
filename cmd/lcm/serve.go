package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/ACaccel/lcm/internal/api"
	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/buildinfo"
	"github.com/ACaccel/lcm/internal/config"
	"github.com/ACaccel/lcm/internal/store"
)

const shutdownTimeout = 10 * time.Second

func (a *app) serve(ctx context.Context, args []string) error {
	cfg, err := config.FromEnv(a.getenv)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(a.stderr, "Usage: lcm serve [flags]")
		fs.PrintDefaults()
	}
	cfg.RegisterFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return usagef("serve: unexpected argument %q", fs.Arg(0))
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}
	if err := cfg.EnsureDataDir(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	log := cfg.NewLogger(a.stderr)
	if !cfg.IsLoopback() {
		log.Warn("listening on a non-loopback address; the API is served over plain HTTP, put a TLS proxy in front",
			"listen", cfg.Listen, "cookie_secure", cfg.CookieSecure)
	}

	db, err := store.Open(ctx, cfg.DBPath())
	if err != nil {
		return err
	}
	defer func() {
		if err := db.Close(); err != nil {
			log.Error("close database", "error", err)
		}
	}()

	authSvc, err := auth.NewService(db, cfg.SessionTTL)
	if err != nil {
		return err
	}
	info := buildinfo.Get()
	handler := api.New(api.Deps{
		DB: db, Auth: authSvc, Audit: audit.New(db), Logger: log,
		Version: info.Version, CookieSecure: cfg.CookieSecure,
	}).Handler()

	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slogErrorLog(log),
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	log.Info("server started", "addr", ln.Addr().String(), "version", info.String(), "data_dir", cfg.DataDir)
	if a.onListen != nil {
		a.onListen(ln.Addr())
	}

	select {
	case err := <-serveErr:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}

	log.Info("shutting down", "timeout", shutdownTimeout)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		_ = srv.Close()
		return fmt.Errorf("shutdown: %w", err)
	}
	if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}
	log.Info("server stopped")
	return nil
}
