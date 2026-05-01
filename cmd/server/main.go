package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/yuno/transaction-health-monitor/internal/clock"
	"github.com/yuno/transaction-health-monitor/internal/config"
	"github.com/yuno/transaction-health-monitor/internal/httpapi"
	"github.com/yuno/transaction-health-monitor/internal/repository"
	"github.com/yuno/transaction-health-monitor/internal/repository/sqlite"
	"github.com/yuno/transaction-health-monitor/internal/service"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if err := run(logger); err != nil {
		logger.Error("server failed", "err", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	repo, err := sqlite.Open(cfg.SQLiteDSN)
	if err != nil {
		return fmt.Errorf("open sqlite: %w", err)
	}
	defer repo.Close()

	if err := repo.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	clk := clock.New()
	limboTh := repository.PendingLimboThresholds{
		Pix:    cfg.PixPendingThreshold,
		Boleto: cfg.BoletoPendingThreshold,
	}

	ingestSvc := service.NewIngestService(repo)
	healthSvc := service.NewHealthService(repo, clk, limboTh)
	anomaliesSvc := service.NewAnomaliesService(repo, clk, limboTh)
	alertsSvc := service.NewAlertsService(repo, clk, limboTh, service.AlertThresholds{
		OrphanedMax: cfg.AlertOrphanedThreshold,
		GhostMax:    cfg.AlertGhostThreshold,
		HealthMin:   cfg.AlertHealthMin,
	}, healthSvc)

	router := httpapi.NewRouter(httpapi.Deps{
		Logger:         logger,
		Ingest:         ingestSvc,
		Health:         healthSvc,
		Anomalies:      anomaliesSvc,
		Alerts:         alertsSvc,
		SQLiteRepo:     repo,
		RequestTimeout: 30 * time.Second,
	})

	addr := net.JoinHostPort("", strconv.Itoa(cfg.Port))
	srv := &http.Server{
		Addr:         addr,
		Handler:      router,
		ReadTimeout:  cfg.ServerReadTimeout,
		WriteTimeout: cfg.ServerWriteTimeout,
		IdleTimeout:  cfg.ServerIdleTimeout,
	}

	logger.Info("server starting",
		slog.String("addr", addr),
		slog.String("dsn", cfg.SQLiteDSN),
		slog.Duration("pix_threshold", cfg.PixPendingThreshold),
		slog.Duration("boleto_threshold", cfg.BoletoPendingThreshold),
	)

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("listen: %w", err)
		}
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancelShutdown()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "err", err)
		return err
	}
	logger.Info("server stopped cleanly")
	return nil
}
