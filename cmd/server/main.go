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

	"github.com/11DingKing/urban-sports-safety-hub/internal/audit"
	"github.com/11DingKing/urban-sports-safety-hub/internal/auth"
	"github.com/11DingKing/urban-sports-safety-hub/internal/bootstrap"
	"github.com/11DingKing/urban-sports-safety-hub/internal/config"
	"github.com/11DingKing/urban-sports-safety-hub/internal/enrollment"
	"github.com/11DingKing/urban-sports-safety-hub/internal/equipment"
	"github.com/11DingKing/urban-sports-safety-hub/internal/httpapi"
	dbstore "github.com/11DingKing/urban-sports-safety-hub/internal/storage/sqlite"
	"github.com/11DingKing/urban-sports-safety-hub/internal/worker"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	store, err := dbstore.Open(rootCtx, cfg.DatabasePath)
	if err != nil {
		return err
	}
	defer store.Close()
	authService := auth.New(store, cfg.SessionTTL)
	if err := bootstrap.Administrator(rootCtx, store, authService); err != nil {
		return err
	}
	if err := bootstrap.DemoData(rootCtx, store.DB()); err != nil {
		return err
	}
	auditService := audit.New(store)
	enrollmentService := enrollment.New(store, auditService)
	equipmentService := equipment.New(store, auditService)
	api := httpapi.New(authService, enrollmentService, equipmentService, store, logger)
	runner := worker.New(store, logger, cfg.WorkerInterval, cfg.WorkerBatch)
	runner.Start(rootCtx)
	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	result := make(chan error, 1)
	go func() {
		logger.Info("http server listening", "address", cfg.HTTPAddr)
		result <- server.ListenAndServe()
	}()
	select {
	case err := <-result:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-rootCtx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return err
	}
	runner.Wait()
	return nil
}
