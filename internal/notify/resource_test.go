package notify

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/slymanmrcan/sentinel/internal/store"
)

// Opt-in subprocess probe; real DuckDB, fake host snapshots and HTTP transport.
// Run the compiled test binary under /usr/bin/time to compare the same harness.
func TestNotificationResourceProbe(t *testing.T) {
	mode := os.Getenv("SENTINEL_NOTIFY_PROBE")
	if mode == "" {
		t.Skip("opt-in resource measurement")
	}
	t.Logf("resource probe mode=%s, duration=10s", mode)
	if mode != "disabled" && mode != "idle" && mode != "storm" {
		t.Fatal("invalid probe mode")
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "probe.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	cfg := testConfig()
	cfg.Telegram.Enabled = mode != "disabled"
	cfg.Telegram.Times = nil
	e := New(cfg, db, &fakeSource{})
	if e.sender != nil {
		e.sender.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true}`)), Header: make(http.Header)}, nil
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	if mode == "disabled" {
		close(done)
	} else {
		go func() { defer close(done); e.Run(ctx) }()
	}
	e.Observe(store.Metric{Timestamp: time.Now()}, []store.AlertRule{{ID: "cpu", Metric: "cpu", Threshold: 90, Enabled: true}})
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	end := time.NewTimer(10 * time.Second)
	defer end.Stop()
	eventNumber := 0
	for {
		select {
		case <-end.C:
			cancel()
			<-done
			return
		case <-ticker.C:
			if mode == "storm" {
				for i := 0; i < 10; i++ {
					ip := eventNumber % 2048
					eventNumber++
					e.LoginFailure(fmt.Sprintf("198.51.%d.%d", ip/256, ip%256), "admin", true)
				}
			}
		}
	}
}
