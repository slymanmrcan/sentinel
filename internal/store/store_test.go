package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestSwapBaselineAndAlertRuleAreAvailable(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "swap.db")
	dataStore, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	now := time.Now()
	for index, value := range []float64{10, 20} {
		if err := dataStore.InsertMetric(ctx, Metric{
			Timestamp:   now.Add(time.Duration(index) * time.Second),
			SwapPercent: value,
		}); err != nil {
			t.Fatalf("InsertMetric() error = %v", err)
		}
	}
	baseline, err := dataStore.Baseline(ctx, "swap")
	if err != nil {
		t.Fatalf("Baseline() error = %v", err)
	}
	if baseline.Count != 2 || baseline.Mean != 15 {
		t.Fatalf("swap baseline = %#v, want count 2 and mean 15", baseline)
	}

	rules, err := dataStore.AlertRules(ctx)
	if err != nil {
		t.Fatalf("AlertRules() error = %v", err)
	}
	if !containsRule(rules, "swap-high") {
		t.Fatalf("AlertRules() missing swap-high: %#v", rules)
	}
	if err := dataStore.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("second Open() error = %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	rules, err = reopened.AlertRules(ctx)
	if err != nil {
		t.Fatalf("second AlertRules() error = %v", err)
	}
	if len(rules) != 4 {
		t.Fatalf("alert rule count after restart = %d, want 4", len(rules))
	}
}

func containsRule(rules []AlertRule, id string) bool {
	for _, rule := range rules {
		if rule.ID == id {
			return true
		}
	}
	return false
}

func TestSettingRoundTrip(t *testing.T) {
	ctx := context.Background()
	dataStore, err := Open(filepath.Join(t.TempDir(), "settings.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = dataStore.Close() })

	if _, found, err := dataStore.Setting(ctx, "container_interval_seconds"); err != nil || found {
		t.Fatalf("missing Setting() = found %t, error %v", found, err)
	}
	if err := dataStore.SetSetting(ctx, "container_interval_seconds", "45"); err != nil {
		t.Fatalf("SetSetting() error = %v", err)
	}
	value, found, err := dataStore.Setting(ctx, "container_interval_seconds")
	if err != nil || !found || value != "45" {
		t.Fatalf("Setting() = (%q, %t, %v), want (45, true, nil)", value, found, err)
	}
}

func TestUnavailableMeasurementsDoNotBecomeZeroBaselines(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "partial.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	for _, metric := range []Metric{
		{Timestamp: time.Now().Add(-time.Minute), RAMPercent: 80},
		{Timestamp: time.Now(), CPUPercent: 95, Unavailable: []string{"memory"}},
	} {
		if err := db.InsertMetric(ctx, metric); err != nil {
			t.Fatal(err)
		}
	}
	b, err := db.Baseline(ctx, "memory")
	if err != nil || b.Count != 1 || b.Mean != 80 {
		t.Fatalf("missing reading polluted baseline: %+v, %v", b, err)
	}
	history, err := db.History(ctx, "1h")
	if err != nil || len(history) != 2 {
		t.Fatalf("partial history: %+v, %v", history, err)
	}
	if len(history[1].Unavailable) != 1 || history[1].Unavailable[0] != "memory" {
		t.Fatalf("missing reading was presented as zero: %+v", history[1])
	}
}
