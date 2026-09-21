package notify

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/slymanmrcan/sentinel/internal/config"
	"github.com/slymanmrcan/sentinel/internal/monitor"
	"github.com/slymanmrcan/sentinel/internal/store"
)

type memoryRepo struct {
	mu   sync.Mutex
	raw  string
	fail bool
}

func (r *memoryRepo) Setting(context.Context, string) (string, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail {
		return "", false, errors.New("unavailable")
	}
	return r.raw, r.raw != "", nil
}
func (r *memoryRepo) SetSetting(_ context.Context, _, v string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail {
		return errors.New("unavailable")
	}
	r.raw = v
	return nil
}

type fakeSource struct {
	metric   store.Metric
	services monitor.SystemdSnapshot
	calls    int
}

func (s *fakeSource) Current() store.Metric { return s.metric }
func (s *fakeSource) SystemServices(context.Context) monitor.SystemdSnapshot {
	s.calls++
	return s.services
}
func testConfig() config.Config {
	return config.Config{Telegram: config.Telegram{Enabled: true, Token: "123:secret", ChatID: "42", Timezone: "Europe/Istanbul", Times: []string{"09:00", "15:00", "21:00"}, Hold: time.Minute, RecoveryMargin: 5, BruteWindow: 5 * time.Minute, BruteThreshold: 5}}
}
func testEngine() *Engine {
	e := New(testConfig(), &memoryRepo{}, &fakeSource{})
	e.loaded = true
	return e
}

var epoch = time.Date(2026, 9, 21, 6, 0, 0, 0, time.UTC)

func TestMetricAlarmHoldHysteresisRecoveryAndMissing(t *testing.T) {
	for _, metric := range []string{"memory", "disk", "swap"} {
		t.Run(metric, func(t *testing.T) {
			e := testEngine()
			step := func(sec int, value float64, unavailable bool) {
				now := epoch.Add(time.Duration(sec) * time.Second)
				m := store.Metric{Timestamp: now, CPUPercent: value, RAMPercent: value, DiskPercent: value, SwapPercent: value}
				if unavailable {
					m.Unavailable = []string{metric}
				}
				e.evaluate(sample{m, []store.AlertRule{{ID: metric, Metric: metric, Threshold: 90, Enabled: true}}}, now)
			}
			step(0, 95, false)
			step(30, 20, false)
			step(60, 95, false)
			step(90, 95, false)
			if len(e.p.Queue) != 0 {
				t.Fatal("short spike alarm")
			}
			step(120, 95, false)
			step(150, 98, false)
			if len(e.p.Queue) != 1 {
				t.Fatalf("alarm repeated/missing: %+v", e.p.Queue)
			}
			step(180, 87, false)
			step(210, 87, false)
			step(240, 87, false)
			if len(e.p.Queue) != 1 {
				t.Fatal("hysteresis lost")
			}
			step(270, 0, false)
			step(300, 0, true)
			step(330, 0, false)
			step(360, 0, false)
			if len(e.p.Queue) != 1 {
				t.Fatal("missing measurement counted as recovery")
			}
			step(390, 0, false)
			step(420, 0, false)
			if len(e.p.Queue) != 1 || !strings.Contains(e.p.Queue[0].Text, "normale döndü") {
				t.Fatalf("recovery: %+v", e.p.Queue)
			}
		})
	}
}

func TestStaleRepeatedSamplesAndCollectionGapsDoNotSatisfyHold(t *testing.T) {
	e := testEngine()
	e.transition("cpu", "CPU", true, true, false, epoch, epoch, "")
	e.transition("cpu", "CPU", true, true, false, epoch, epoch.Add(time.Minute), "")
	e.transition("cpu", "CPU", true, true, false, epoch.Add(5*time.Minute), epoch.Add(5*time.Minute), "")
	if len(e.p.Queue) != 0 {
		t.Fatal("duplicate sample or gap satisfied hold")
	}
	m := store.Metric{Timestamp: epoch, CPUPercent: 100}
	e.evaluate(sample{m, []store.AlertRule{{ID: "cpu", Metric: "cpu", Threshold: 90, Enabled: true}}}, epoch.Add(time.Hour))
	if len(e.p.Queue) != 0 {
		t.Fatal("stale metric alarm")
	}
}

func TestServicesWithoutDashboardAndUnavailableDoesNotRecover(t *testing.T) {
	e := testEngine()
	src := e.source.(*fakeSource)
	step := func(minutes int, active string, available bool) {
		now := epoch.Add(time.Duration(minutes) * time.Minute)
		src.services = monitor.SystemdSnapshot{Enabled: true, Available: available, CheckedAt: now, Services: []monitor.SystemdService{{Unit: "ssh.service", LoadState: "loaded", ActiveState: active}}}
		e.checkServices(context.Background(), now)
	}
	step(0, "failed", true)
	step(1, "failed", true)
	step(2, "unknown", false)
	step(3, "active", true)
	if len(e.p.Queue) != 1 {
		t.Fatal("unavailable service caused recovery")
	}
	step(4, "active", true)
	if len(e.p.Queue) != 1 || !strings.Contains(e.p.Queue[0].Text, "normale döndü") || src.calls != 5 {
		t.Fatalf("service lifecycle %+v", e.p.Queue)
	}
}

func TestScheduleRestartAtomicOutboxAndNoCatchup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notify.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	e := New(testConfig(), db, &fakeSource{})
	if !e.load(context.Background()) {
		t.Fatal("load")
	}
	e.schedule(epoch)
	e.schedule(epoch.Add(time.Minute))
	if len(e.p.Queue) != 1 {
		t.Fatal("duplicate summary slot")
	}
	e.p.Alarms["cpu"] = alarm{Active: true, Pending: epoch, Last: epoch}
	e.dirty = true
	if !e.save(context.Background()) {
		t.Fatal("save")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	restarted := New(testConfig(), db, &fakeSource{})
	if !restarted.load(context.Background()) {
		t.Fatal("reload")
	}
	restarted.schedule(epoch.Add(30 * time.Second))
	if len(restarted.p.Queue) != 1 || !restarted.p.Alarms["cpu"].Active || !restarted.p.Alarms["cpu"].Pending.IsZero() {
		t.Fatal("restart lost state or duplicated summary")
	}
	restarted.prune(epoch.Add(12 * time.Hour))
	restarted.schedule(epoch.Add(12*time.Hour + 3*time.Minute))
	if len(restarted.p.Queue) != 0 {
		t.Fatal("missed slots replayed")
	}
	restarted.schedule(epoch.Add(24 * time.Hour))
	if len(restarted.p.Queue) != 1 {
		t.Fatal("next day not scheduled")
	}
}

func TestScheduleTwoTimesTimezoneAndDSTFallback(t *testing.T) {
	e := testEngine()
	e.cfg.Times = []string{"09:00", "21:00"}
	e.schedule(epoch.Add(6 * time.Hour))
	if len(e.p.Queue) != 0 {
		t.Fatal("removed slot sent")
	}
	e.schedule(epoch)
	if len(e.p.Queue) != 1 {
		t.Fatal("Istanbul timezone ignored")
	}
	e = testEngine()
	e.location, _ = time.LoadLocation("America/New_York")
	e.cfg.Times = []string{"01:30"}
	first := time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC)
	e.schedule(first)
	e.p.Queue = nil
	e.schedule(first.Add(time.Hour))
	if len(e.p.Queue) != 0 {
		t.Fatal("DST repeated wall-clock slot sent twice")
	}
}

func TestSummaryUsesExistingDataAndReportsFreshness(t *testing.T) {
	e := testEngine()
	src := e.source.(*fakeSource)
	src.metric = store.Metric{Timestamp: epoch, HostName: "test-host", Uptime: 3600, CPUPercent: 12, RAMPercent: 42, Unavailable: []string{"swap"}, Filesystems: []store.Filesystem{{Mountpoint: "/data", UsedPercent: 80, AvailableStats: true}}}
	e.record("CPU: normale döndü", epoch)
	text := e.summary(epoch)
	for _, want := range []string{"test-host", "1h0m0s", "12.0%", "42.0%", "Swap: eski veya alınamıyor", "/data: 80.0%", "CPU: normale döndü"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %s", want, text)
		}
	}
	if src.calls != 0 {
		t.Fatal("summary recollected services")
	}
	if strings.Contains(e.summary(epoch.Add(time.Hour)), "CPU: 12.0%") {
		t.Fatal("stale value labelled current")
	}
}

func TestQueuePriorityBoundsTTLAndCoalescing(t *testing.T) {
	e := testEngine()
	e.enqueue("summary", "summary", 0, epoch, 0)
	for i := 1; i < queueLimit; i++ {
		e.enqueue(fmt.Sprint(i), "alarm", 2, epoch, 0)
	}
	e.enqueue("critical", "critical", 2, epoch, 0)
	if len(e.p.Queue) != queueLimit || e.p.Queue[0].Key == "summary" {
		t.Fatal("priority eviction failed")
	}
	if e.enqueue("overflow", "alarm", 2, epoch, 0) {
		t.Fatal("unbounded queue")
	}
	e.enqueue("critical", "duplicate", 2, epoch, 0)
	if len(e.p.Queue) != queueLimit {
		t.Fatal("dedup failed")
	}
	m, ok := e.next(epoch)
	if !ok || m.Priority != 2 {
		t.Fatal("critical priority")
	}
	e.prune(epoch.Add(25 * time.Hour))
	if len(e.p.Queue) != 0 {
		t.Fatal("no expiry")
	}
	for i := 0; i < 1000; i++ {
		e.record("event", epoch)
	}
	if len(e.p.Events) != 50 {
		t.Fatal("unbounded history")
	}
	e.prune(epoch.Add(8 * 24 * time.Hour))
	if len(e.p.Events) != 0 {
		t.Fatal("history did not expire")
	}
}

func TestFailureWindowsDistributedDetectionBoundsAndLockouts(t *testing.T) {
	e := testEngine()
	for i := 0; i < 4; i++ {
		e.failure(failure{At: epoch, IP: "192.0.2.1", Account: "account", Target: "Sentinel paneli"})
	}
	if len(e.p.Queue) != 0 {
		t.Fatal("single/few failures alarm")
	}
	e.failure(failure{At: epoch.Add(time.Second), IP: "192.0.2.1", Account: "account", Target: "Sentinel paneli", Locked: true})
	if len(e.p.Queue) != 1 || !strings.Contains(e.p.Queue[0].Text, "5 deneme, 1 kilitli ret") {
		t.Fatal("IP threshold/lockout")
	}
	e.failure(failure{At: epoch.Add(2 * time.Second), IP: "192.0.2.2", Account: "account", Target: "Sentinel paneli"})
	if len(e.p.Queue) != 2 || !strings.Contains(e.p.Queue[1].Text, "farklı IP") {
		t.Fatal("distributed account detection")
	}
	for i := 0; i < 100; i++ {
		e.failure(failure{At: epoch.Add(3 * time.Second), IP: "192.0.2.2", Account: "account", Target: "Sentinel paneli"})
	}
	if len(e.p.Queue) != 3 {
		t.Fatal("storm not suppressed")
	}
	for i := 0; i < 10000; i++ {
		e.failure(failure{At: epoch, IP: fmt.Sprint(i), Account: fmt.Sprint(i), Target: "Sentinel paneli"})
	}
	if len(e.counters) > counterLimit {
		t.Fatal("unbounded attacker cardinality")
	}
	e.pruneCounters(epoch.Add(e.cfg.BruteWindow + time.Minute))
	if len(e.counters) != 0 {
		t.Fatal("counters did not expire")
	}
}

func TestNonblockingIngressAndDisabled(t *testing.T) {
	e := testEngine()
	for i := 0; i < 10000; i++ {
		e.LoginFailure("192.0.2.1", "secret-account", false)
		e.Observe(store.Metric{}, nil)
	}
	if len(e.failures) != 256 || len(e.samples) != 2 || e.dropped.Load() == 0 {
		t.Fatal("ingress bounds")
	}
	f := <-e.failures
	if f.Account == "secret-account" {
		t.Fatal("raw account retained")
	}
	off := New(config.Config{}, nil, nil)
	off.Run(context.Background())
	off.LoginFailure("ip", "login", false)
	off.Observe(store.Metric{}, nil)
	if off.Test() || off.sender != nil || off.samples != nil || off.failures != nil {
		t.Fatal("disabled allocated worker/network state")
	}
}

func TestDatabaseOutageKeepsDirtyStateAndVisibleStatus(t *testing.T) {
	e := testEngine()
	db := e.db.(*memoryRepo)
	e.enqueue("alert", "CPU alarm", 2, epoch, 0)
	db.fail = true
	if e.save(context.Background()) || !e.dirty || e.Status().StorageError == "" {
		t.Fatal("DB outage hidden or state discarded")
	}
	db.fail = false
	if !e.save(context.Background()) || e.dirty || e.Status().StorageError != "" {
		t.Fatal("DB recovery failed")
	}
}

func BenchmarkNotificationIngress(b *testing.B) {
	for _, enabled := range []bool{false, true} {
		b.Run(fmt.Sprintf("enabled=%t", enabled), func(b *testing.B) {
			cfg := testConfig()
			cfg.Telegram.Enabled = enabled
			e := New(cfg, nil, nil)
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				e.LoginFailure("192.0.2.1", "admin", false)
			}
		})
	}
}
func BenchmarkFailureStorm(b *testing.B) {
	e := testEngine()
	for i := 0; i < counterLimit; i++ {
		e.failure(failure{At: epoch, IP: fmt.Sprint(i), Account: "account", Target: "Sentinel paneli"})
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		e.failure(failure{At: epoch, IP: "192.0.2.1", Account: "account", Target: "Sentinel paneli"})
	}
}

func TestCoalescedTransitionsReplaceUndeliveredOldState(t *testing.T) {
	e := testEngine()
	e.enqueue("cpu", "CPU sorun", 2, epoch, 0)
	e.p.Queue[0].Next = epoch.Add(5 * time.Minute)
	e.enqueue("cpu", "CPU normale döndü", 2, epoch.Add(time.Minute), 0)
	if len(e.p.Queue) != 1 || !strings.Contains(e.p.Queue[0].Text, "normale döndü") || !strings.Contains(e.p.Queue[0].Text, "birleştirildi") {
		t.Fatal("undelivered obsolete alarm retained")
	}
	if _, ok := e.next(epoch.Add(time.Minute)); ok {
		t.Fatal("coalescing bypassed retry backoff")
	}
}

func TestMissingMountInterruptsPendingAlarm(t *testing.T) {
	e := testEngine()
	step := func(seconds int, present bool) {
		now := epoch.Add(time.Duration(seconds) * time.Second)
		m := store.Metric{Timestamp: now}
		if present {
			m.Filesystems = []store.Filesystem{{Mountpoint: "/data", UsedPercent: 95, AvailableStats: true}}
		}
		e.evaluate(sample{m, []store.AlertRule{{ID: "disk", Metric: "disk", Threshold: 85, Enabled: true}}}, now)
	}
	step(0, true)
	step(30, false)
	step(60, true)
	if len(e.p.Queue) != 0 {
		t.Fatal("missing mount satisfied alarm hold")
	}
	step(90, true)
	step(120, true)
	if len(e.p.Queue) != 1 || !strings.Contains(e.p.Queue[0].Text, "/data") {
		t.Fatal("mount alarm not emitted")
	}
}

func TestDeliveredAlarmHasExactlyOneRecoveryTransition(t *testing.T) {
	e := testEngine()
	e.transition("cpu", "CPU", true, true, false, epoch, epoch, "")
	e.transition("cpu", "CPU", true, true, false, epoch.Add(time.Minute), epoch.Add(time.Minute), "")
	if len(e.p.Queue) != 1 {
		t.Fatal("alarm missing")
	}
	e.complete(delivery{ID: e.p.Queue[0].ID, OK: true}, epoch.Add(time.Minute))
	for i := 2; i <= 8; i++ {
		now := epoch.Add(time.Duration(i) * time.Minute)
		e.transition("cpu", "CPU", true, false, true, now, now, "")
	}
	if len(e.p.Queue) != 1 || !strings.Contains(e.p.Queue[0].Text, "normale döndü") || len(e.p.Events) != 2 {
		t.Fatal("recovery repeated or missing")
	}
}
func TestDSTSpringForwardDoesNotInventSummarySlot(t *testing.T) {
	e := testEngine()
	e.location, _ = time.LoadLocation("America/New_York")
	e.cfg.Times = []string{"02:30"}
	for _, hour := range []int{6, 7} {
		e.schedule(time.Date(2026, 3, 8, hour, 30, 0, 0, time.UTC))
	}
	if len(e.p.Queue) != 0 {
		t.Fatal("nonexistent local time normalized into a different slot")
	}
}

func TestUnknownIPDoesNotClaimSharedOrDistributedSource(t *testing.T) {
	e := testEngine()
	for i := 0; i < 20; i++ {
		e.failure(failure{At: epoch, IP: "bilinmiyor", Account: "account", Target: "Sentinel paneli"})
	}
	if len(e.p.Queue) != 0 {
		t.Fatal("unknown IP treated as a reliably identified source")
	}
	e.failure(failure{At: epoch, IP: "192.0.2.1", Account: "account", Target: "Sentinel paneli"})
	if len(e.p.Queue) != 0 {
		t.Fatal("one known plus unknown IP claimed distributed attack")
	}
	e.failure(failure{At: epoch, IP: "192.0.2.2", Account: "account", Target: "Sentinel paneli"})
	if len(e.p.Queue) != 1 || strings.Contains(e.p.Queue[0].Text, "bilinmiyor") {
		t.Fatal("reliable source samples incorrect")
	}
}
func TestSummaryWatermarkAdvancesOnlyAfterSuccessfulDelivery(t *testing.T) {
	e := testEngine()
	e.cfg.Times = []string{"09:00", "09:01"}
	e.record("first event", epoch)
	e.schedule(epoch)
	e.record("second event", epoch.Add(time.Minute))
	e.schedule(epoch.Add(time.Minute))
	seq := e.p.Queue[0].EventSeq
	if seq != e.p.Events[1].Seq {
		t.Fatal("coalesced summary lost event boundary")
	}
	e.complete(delivery{ID: e.p.Queue[0].ID, Error: "timeout"}, epoch)
	if e.p.SummarySeq != 0 {
		t.Fatal("failed summary consumed events")
	}
	e.complete(delivery{ID: e.p.Queue[0].ID, OK: true}, epoch)
	if e.p.SummarySeq != seq || strings.Contains(e.summary(epoch), "second event") {
		t.Fatal("delivered summary did not advance boundary")
	}
}

func TestSummaryKeepsServiceStatusWithManyDisks(t *testing.T) {
	e := testEngine()
	src := e.source.(*fakeSource)
	src.metric.Timestamp = epoch
	for i := 0; i < 128; i++ {
		src.metric.Filesystems = append(src.metric.Filesystems, store.Filesystem{Mountpoint: fmt.Sprintf("/long-mount-%d-%s", i, strings.Repeat("x", 80)), AvailableStats: true})
	}
	e.services = monitor.SystemdSnapshot{Enabled: true, Available: true, CheckedAt: epoch, Services: []monitor.SystemdService{{Unit: "ssh.service", LoadState: "loaded", ActiveState: "failed"}}}
	e.record("important event", epoch)
	text := e.summary(epoch)
	for _, want := range []string{"Servisler: 0 çalışıyor, 1 sorun", "ssh.service: failed", "important event"} {
		if !strings.Contains(text, want) {
			t.Fatalf("summary lost %q", want)
		}
	}
	if len([]rune(text)) > messageLimit {
		t.Fatal("summary over limit")
	}
}
func TestShutdownCancellationIsNotReportedAsStorageOutage(t *testing.T) {
	e := testEngine()
	e.db.(*memoryRepo).fail = true
	e.dirty = true
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e.save(ctx) || e.Status().StorageError != "" || !e.dirty {
		t.Fatal("shutdown reported as storage outage or lost unsaved state")
	}
}
