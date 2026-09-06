package monitor

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	gnet "github.com/shirou/gopsutil/v3/net"
	"github.com/slymanmrcan/sentinel/internal/config"
	"github.com/slymanmrcan/sentinel/internal/store"
)

func TestRateHandlesDeltasAndCounterReset(t *testing.T) {
	if got := rate(250, 100, 5); got != 30 {
		t.Fatalf("rate() = %v, want 30", got)
	}
	if got := rate(10, 100, 5); got != 0 {
		t.Fatalf("rate() after counter reset = %v, want 0", got)
	}
	if got := rate(100, 100, 0); got != 0 {
		t.Fatalf("rate() with zero elapsed time = %v, want 0", got)
	}
}

func TestContainerIntervalPersists(t *testing.T) {
	ctx := context.Background()
	dataStore, err := store.Open(filepath.Join(t.TempDir(), "interval.db"))
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = dataStore.Close() })

	collector := New(dataStore, config.Config{ContainerInterval: 30 * time.Second})
	if err := collector.SetContainerInterval(ctx, 45); err != nil {
		t.Fatalf("SetContainerInterval() error = %v", err)
	}
	reloaded := New(dataStore, config.Config{ContainerInterval: 30 * time.Second})
	if err := reloaded.LoadSettings(ctx); err != nil {
		t.Fatalf("LoadSettings() error = %v", err)
	}
	if got := reloaded.ContainerInterval(); got != 45*time.Second {
		t.Fatalf("ContainerInterval() = %v, want 45s", got)
	}
	if err := reloaded.SetContainerInterval(ctx, 20); err == nil {
		t.Fatal("SetContainerInterval(20) error = nil, want validation error")
	}
}

func TestSelectedNetworkCounters(t *testing.T) {
	counters := []gnet.IOCountersStat{
		{Name: "eth0", BytesRecv: 100, BytesSent: 40},
		{Name: "docker0", BytesRecv: 500, BytesSent: 300},
		{Name: "tailscale0", BytesRecv: 25, BytesSent: 10},
	}
	received, sent, found := selectedNetworkCounters(counters, []string{"eth0", "tailscale0"})
	if received != 125 || sent != 50 || !found {
		t.Fatalf("selectedNetworkCounters() = (%d, %d, %t), want (125, 50, true)", received, sent, found)
	}
	if _, _, found := selectedNetworkCounters(counters, []string{"missing0"}); found {
		t.Fatal("selectedNetworkCounters() reported a missing interface as found")
	}
}

func TestHasUnavailable(t *testing.T) {
	if !hasUnavailable([]string{"network", "cpu"}, "cpu", "memory") {
		t.Fatal("hasUnavailable() did not find cpu")
	}
	if hasUnavailable([]string{"network"}, "cpu", "memory") {
		t.Fatal("hasUnavailable() returned true for unrelated fields")
	}
}

func TestProcessCPUPercentUsesSamplingWindow(t *testing.T) {
	if got := processCPUPercent(14, 10, 2); got != 200 {
		t.Fatalf("processCPUPercent() = %v, want 200", got)
	}
	if got := processCPUPercent(2, 10, 2); got != 0 {
		t.Fatalf("processCPUPercent() after reset = %v, want 0", got)
	}
}

func TestAlertsIgnoreOnlyUnavailableMetric(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "alerts.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	c := New(db, config.Config{})
	c.evaluateAlerts(context.Background(), store.Metric{Timestamp: time.Now(), CPUPercent: 95, RAMPercent: 99, Unavailable: []string{"memory"}})
	events, err := db.AlertEvents(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Metric != "cpu" {
		t.Fatalf("unavailable metric affected alarms: %+v", events)
	}
}

func TestPartialCollectionStillStoresHistoryAndRaisesOtherAlerts(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "partial.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	c := New(db, config.Config{})
	c.recordMetric(context.Background(), store.Metric{Timestamp: time.Now(), CPUPercent: 95, Unavailable: []string{"swap"}})
	events, err := db.AlertEvents(context.Background(), 10)
	if err != nil || len(events) != 1 || events[0].Metric != "cpu" {
		t.Fatalf("CPU alert lost because swap is missing: %+v %v", events, err)
	}
	history, err := db.History(context.Background(), "1h")
	if err != nil || len(history) != 1 || history[0].CPUPercent != 95 {
		t.Fatalf("valid history lost: %+v %v", history, err)
	}
}

func TestCurrentMarksOldSamplesStaleWithoutMutatingCache(t *testing.T) {
	c := New(nil, config.Config{})
	c.current = store.Metric{Timestamp: time.Now().Add(-5 * time.Minute)}
	if !hasUnavailable(c.Current().Unavailable, "stale") {
		t.Fatal("old sample not marked stale")
	}
	if len(c.current.Unavailable) != 0 {
		t.Fatal("reading current mutated cache")
	}
	c.current.Timestamp = time.Now()
	if hasUnavailable(c.Current().Unavailable, "stale") {
		t.Fatal("fresh sample marked stale")
	}
}

func TestMetricWriteFailureDoesNotPreventAlertEvaluation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "failed-write.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	connection, err := sql.Open("duckdb", path)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	// Make only metric persistence fail; alert rules/events remain writable.
	if _, err := connection.Exec("DROP TABLE metrics"); err != nil {
		t.Fatal(err)
	}
	c := New(db, config.Config{})
	c.recordMetric(context.Background(), store.Metric{Timestamp: time.Now(), CPUPercent: 95})
	events, err := db.AlertEvents(context.Background(), 10)
	if err != nil || len(events) != 1 {
		t.Fatalf("metric write failure suppressed alert: %+v %v", events, err)
	}
	if !hasUnavailable(c.Current().Unavailable, "history") {
		t.Fatal("failed persistence was not exposed")
	}
}
