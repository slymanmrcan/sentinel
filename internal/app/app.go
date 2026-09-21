package app

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/slymanmrcan/sentinel/internal/auth"
	"github.com/slymanmrcan/sentinel/internal/config"
	"github.com/slymanmrcan/sentinel/internal/httpapi"
	"github.com/slymanmrcan/sentinel/internal/monitor"
	"github.com/slymanmrcan/sentinel/internal/notify"
	"github.com/slymanmrcan/sentinel/internal/store"
)

type App struct {
	notifications *notify.Engine
	store         *store.Store
	collector     *monitor.Collector
	server        *http.Server
}

func New(ctx context.Context, cfg config.Config) (*App, error) {
	dataStore, err := store.Open(cfg.DBPath)
	if err != nil {
		return nil, err
	}

	authService, err := auth.New(ctx, dataStore, cfg)
	if err != nil {
		_ = dataStore.Close()
		return nil, fmt.Errorf("initialize authentication: %w", err)
	}
	collector := monitor.New(dataStore, cfg)
	if err := collector.LoadSettings(ctx); err != nil {
		_ = dataStore.Close()
		return nil, fmt.Errorf("load monitor settings: %w", err)
	}
	notifications := notify.New(cfg, dataStore, collector)
	if cfg.Telegram.Enabled {
		collector.AlertObserver = notifications.Observe
		authService.FailureObserver = notifications.LoginFailure
	}
	api := httpapi.New(cfg, dataStore, authService, collector)
	api.SetNotifications(notifications)

	return &App{
		notifications: notifications, store: dataStore, collector: collector, server: api.HTTPServer(),
	}, nil
}

func (a *App) Run(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	notifyDone := make(chan struct{})
	if a.notifications != nil && a.notifications.Status().Enabled {
		go func() { defer close(notifyDone); a.notifications.Run(runCtx) }()
	} else {
		close(notifyDone)
	}
	collectorDone := make(chan struct{})
	go func() { defer close(collectorDone); a.collector.Start(runCtx) }()

	serverErrors := make(chan error, 1)
	go func() {
		log.Printf("Sentinel dashboard and API listening on %s", a.server.Addr)
		if err := a.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverErrors <- err
		}
	}()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)

	select {
	case <-ctx.Done():
	case signal := <-signals:
		log.Printf("Received %s, shutting down", signal)
	case err := <-serverErrors:
		cancel()
		<-collectorDone
		<-notifyDone
		_ = a.store.Close()
		return fmt.Errorf("HTTP server failed: %w", err)
	}

	cancel()
	<-collectorDone
	<-notifyDone
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := a.server.Shutdown(shutdownCtx); err != nil {
		log.Printf("HTTP shutdown failed: %v", err)
	}
	if err := a.store.Close(); err != nil {
		return fmt.Errorf("close database: %w", err)
	}
	return nil
}
