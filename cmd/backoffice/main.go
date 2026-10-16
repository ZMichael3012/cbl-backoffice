// main starts the back-office admin API of the CBL platform: migrations,
// operator authentication, RBAC and the action audit journal (CBL-12.1).
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/cbl/cbl-backoffice/internal/auth"
	"github.com/cbl/cbl-backoffice/internal/config"
	"github.com/cbl/cbl-backoffice/internal/connectorclient"
	"github.com/cbl/cbl-backoffice/internal/payoutclient"
	"github.com/cbl/cbl-backoffice/internal/storage"
	"github.com/cbl/cbl-backoffice/internal/transport"
	"github.com/cbl/cbl-backoffice/internal/usecase"
)

// version is the service build version, replaced at image build time.
var version = "dev"

// main starts the service and exits the process with a non-zero code on error.
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

// run assembles configuration, storage and the HTTP surface, then serves
// requests until a stop signal.
func run() error {
	log, err := zap.NewProduction()
	if err != nil {
		return fmt.Errorf("logger: %w", err)
	}
	defer func() { _ = log.Sync() }()

	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	log.Info("service started", zap.String("service", "cbl-backoffice"), zap.String("version", version))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := storage.Open(cfg.PGDSN)
	if err != nil {
		return err
	}
	defer func() {
		if sqlDB, dbErr := db.DB(); dbErr == nil {
			_ = sqlDB.Close()
		}
	}()

	if err := storage.Migrate(ctx, db, cfg.MigrationsDir); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}

	verifier, err := auth.NewVerifier(cfg.JWTSecret)
	if err != nil {
		return err
	}
	auditStore := storage.NewAuditStore(db)
	auditReader := usecase.NewAuditReader(auditStore)
	auditWriter := usecase.NewAuditWriter(auditStore)

	payoutClient, err := payoutclient.New(cfg.PayoutGRPCAddr, cfg.PayoutTimeout)
	if err != nil {
		return err
	}
	defer func() { _ = payoutClient.Close() }()
	connectorClient, err := connectorclient.New(cfg.ConnectorGRPCAddr, cfg.ConnectorTimeout)
	if err != nil {
		return err
	}
	defer func() { _ = connectorClient.Close() }()
	actions := usecase.NewAdminActions(payoutClient, connectorClient, auditWriter)

	router := transport.NewRouter(transport.Options{
		Log:     log,
		Verify:  verifier.Verify,
		Audit:   auditReader,
		Actions: actions,
		Ping: func(pingCtx context.Context) error {
			return storage.Ready(pingCtx, db)
		},
	})

	httpSrv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	metricsSrv := &http.Server{Addr: cfg.MetricsAddr, Handler: metricsHandler(), ReadHeaderTimeout: 5 * time.Second}

	serveErrors := make(chan error, 2)
	go func() {
		log.Info("http server started", zap.String("addr", cfg.HTTPAddr))
		if serveErr := httpSrv.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			log.Error("http server failed", zap.Error(serveErr))
			serveErrors <- fmt.Errorf("http server: %w", serveErr)
			stop()
		}
	}()
	go func() {
		log.Info("metrics server started", zap.String("addr", cfg.MetricsAddr))
		if serveErr := metricsSrv.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			log.Error("metrics server failed", zap.Error(serveErr))
			serveErrors <- fmt.Errorf("metrics server: %w", serveErr)
			stop()
		}
	}()

	var serveFailure error
	select {
	case <-ctx.Done():
	case serveFailure = <-serveErrors:
	}
	log.Info("shutdown started")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if shutdownErr := httpSrv.Shutdown(shutdownCtx); shutdownErr != nil {
		log.Error("http shutdown failed", zap.Error(shutdownErr))
	}
	if shutdownErr := metricsSrv.Shutdown(shutdownCtx); shutdownErr != nil {
		log.Error("metrics shutdown failed", zap.Error(shutdownErr))
	}
	log.Info("shutdown finished")
	return serveFailure
}

// metricsHandler serves the metrics stub until task 0.7.
func metricsHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = fmt.Fprintln(w, "# metrics endpoint placeholder (task 0.7)")
	})
	return mux
}
