// Command server runs the Lighthouse account-health platform.
//
// Configuration (environment variables):
//
//	ADDR               listen address              (default :8080, or :$PORT)
//	DATA_DIR           folder with source exports  (default ./data)
//	REFRESH_INTERVAL   how often to re-read sources (default 5m)
//	ANTHROPIC_API_KEY  enables Claude briefs        (optional)
//	LLM_MODEL          Claude model id              (default claude-sonnet-5-5)
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"lighthouse/internal/api"
	"lighthouse/internal/insight"
	"lighthouse/internal/obs"
	"lighthouse/internal/service"
	"lighthouse/internal/source"
	"lighthouse/web"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	addr := env("ADDR", "")
	if addr == "" {
		addr = ":" + env("PORT", "8080")
	}
	dataDir := env("DATA_DIR", "data")
	every, err := time.ParseDuration(env("REFRESH_INTERVAL", "5m"))
	if err != nil {
		return err
	}

	svc := service.New(service.Config{
		Sources: []source.Source{
			source.CRMFile{Path: filepath.Join(dataDir, "crm.json")},
			source.SupportCSV{Path: filepath.Join(dataDir, "support.csv")},
			source.UsageFile{Path: filepath.Join(dataDir, "usage.json")},
		},
		Timeout: 5 * time.Second,
		Logger:  log,
	})

	var primary insight.Briefer
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		primary = insight.Claude{APIKey: key, Model: env("LLM_MODEL", "claude-sonnet-5-5")}
		log.Info("claude briefs enabled")
	}
	briefer := insight.Fallback{Primary: primary, Secondary: insight.Rules{}, Logger: log}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	metrics := obs.New()
	srv := api.New(svc, briefer, metrics, log, web.Static())

	// Start serving even if the first refresh fails: /readyz reports it.
	if err := svc.Refresh(ctx); err != nil {
		log.Error("initial refresh failed", "error", err)
	}
	srv.RecordSourceGauges()
	go svc.Run(ctx, every)

	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      45 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", addr)
		errCh <- httpSrv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		log.Info("shutting down")
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return httpSrv.Shutdown(shutdownCtx)
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
