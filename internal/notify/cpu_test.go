package notify

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/slymanmrcan/sentinel/internal/config"
	"github.com/slymanmrcan/sentinel/internal/store"
)

type cpuRepo struct {
	memoryRepo
	ref        store.CPUReference
	reads      int
	historyErr error
}

func (r *cpuRepo) CPUReference(ctx context.Context, host string, now time.Time, floor float64) (store.CPUReference, error) {
	r.reads++
	if _, ok := ctx.Deadline(); !ok {
		panic("unbounded history query")
	}
	return r.ref, r.historyErr
}
func readyReference(now time.Time, median float64) store.CPUReference {
	return store.CPUReference{Median: median, Count: 121, First: now.Add(-2 * time.Hour), Last: now.Add(-5*time.Minute - time.Second), Latest: now.Add(-5*time.Minute - time.Second)}
}
func cpuEngine(median float64) *Engine {
	e := testEngine()
	e.p.CPU.Host = "host"
	e.p.CPU.ReferenceHost = "host"
	e.p.CPU.Reference = readyReference(epoch, median)
	e.p.CPU.ReferenceAt = epoch
	e.p.CPU.ReferenceReady = true
	return e
}
func cpuSample(at time.Time, value float64) sample {
	return sample{Metric: store.Metric{Timestamp: at, CPUPercent: value, HostName: "host"}, Rules: []store.AlertRule{{ID: "cpu-high", Metric: "cpu", Threshold: 90, Enabled: true}}}
}
func cpuStep(e *Engine, seconds int, value float64) {
	now := epoch.Add(time.Duration(seconds) * time.Second)
	e.evaluate(cpuSample(now, value), now)
}

func TestCPUAdaptiveWarningEscalationAndSingleRecovery(t *testing.T) {
	e := cpuEngine(10)
	for seconds := 0; seconds < 300; seconds += 30 {
		cpuStep(e, seconds, 40)
	}
	if len(e.p.Events) != 0 {
		t.Fatal("warning before five minutes")
	}
	cpuStep(e, 300, 40)
	if len(e.p.Queue) != 1 || e.p.CPU.Level != cpuWarning || !strings.Contains(e.p.Queue[0].Text, "%10.0") {
		t.Fatalf("warning missing: %+v", e.p.Queue)
	}
	e.complete(delivery{ID: e.p.Queue[0].ID, OK: true}, epoch.Add(300*time.Second))
	for seconds := 330; seconds <= 600; seconds += 30 {
		cpuStep(e, seconds, 40)
	}
	if len(e.p.Queue) != 0 {
		t.Fatal("sustained warning repeated")
	}
	for seconds := 630; seconds < 750; seconds += 30 {
		cpuStep(e, seconds, 85)
	}
	if len(e.p.Queue) != 0 {
		t.Fatal("critical before two minutes")
	}
	cpuStep(e, 750, 85)
	if len(e.p.Queue) != 1 || e.p.CPU.Level != cpuCritical || e.p.Queue[0].Priority != 3 {
		t.Fatal("warning did not escalate")
	}
	e.complete(delivery{ID: e.p.Queue[0].ID, OK: true}, epoch.Add(750*time.Second))
	for seconds := 780; seconds <= 1020; seconds += 30 {
		cpuStep(e, seconds, 25)
	}
	if len(e.p.Queue) != 0 || !e.p.CPU.Frozen {
		t.Fatal("early recovery or reference thaw")
	}
	for seconds := 1050; seconds < 1230; seconds += 30 {
		cpuStep(e, seconds, 15)
	}
	if len(e.p.Queue) != 0 {
		t.Fatal("recovery before three minutes")
	}
	for seconds := 1230; seconds <= 1500; seconds += 30 {
		cpuStep(e, seconds, 15)
	}
	if len(e.p.Queue) != 1 || e.p.CPU.Level != cpuNormal || e.p.CPU.Frozen || !strings.Contains(e.p.Queue[0].Text, "normale döndü") || len(e.p.Events) != 3 {
		t.Fatalf("recovery repeated/missing: %+v", e.p.Queue)
	}
}
func TestCPUAbsoluteFloorRelativeRiseAndCriticalGuard(t *testing.T) {
	for _, tc := range []struct {
		name          string
		median, value float64
		level         int
	}{
		{"floor ignores small triples", 1, 20, cpuNormal},
		{"relative condition", 20, 40, cpuNormal},
		{"three times baseline", 20, 60, cpuWarning},
		{"critical regardless of baseline", 30, 80, cpuCritical},
		{"zero normal is valid", 0, 35, cpuWarning},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := cpuEngine(tc.median)
			for sec := 0; sec <= 600; sec += 30 {
				cpuStep(e, sec, tc.value)
			}
			if e.p.CPU.Level != tc.level {
				t.Fatalf("level=%d want=%d", e.p.CPU.Level, tc.level)
			}
		})
	}
}
func TestCPUColdStartAndStaleHistoryUseFixedThresholds(t *testing.T) {
	for _, stale := range []bool{false, true} {
		e := cpuEngine(20)
		if stale {
			e.p.CPU.ReferenceAt = epoch.Add(-time.Hour)
		} else {
			e.p.CPU.ReferenceReady = false
		}
		for sec := 0; sec <= 300; sec += 30 {
			cpuStep(e, sec, 40)
		}
		if e.p.CPU.Level != cpuWarning || e.cpuReferenceUsable(epoch.Add(5*time.Minute)) {
			t.Fatal("missing/stale reference hid warning")
		}
		if !strings.Contains(e.p.Queue[0].Text, "Geçmiş yetersiz") {
			t.Fatal("fallback not disclosed")
		}
	}
}
func TestCPUInvalidDataGapsAndRepeatsInterruptHolds(t *testing.T) {
	for _, invalid := range []string{"missing", "stale", "nan", "range", "gap", "duplicate"} {
		t.Run(invalid, func(t *testing.T) {
			e := cpuEngine(10)
			for sec := 0; sec <= 270; sec += 30 {
				cpuStep(e, sec, 40)
			}
			now := epoch.Add(300 * time.Second)
			s := cpuSample(now, 40)
			switch invalid {
			case "missing":
				s.Metric.Unavailable = []string{"cpu"}
			case "stale":
				s.Metric.Timestamp = epoch
			case "nan":
				s.Metric.CPUPercent = math.NaN()
			case "range":
				s.Metric.CPUPercent = 101
			case "gap":
				s.Metric.Timestamp = epoch.Add(450 * time.Second)
				now = s.Metric.Timestamp
			case "duplicate":
				s.Metric.Timestamp = epoch.Add(270 * time.Second)
			}
			e.evaluate(s, now)
			if len(e.p.Queue) != 0 {
				t.Fatal("invalid/repeated measurement satisfied hold")
			}
		})
	}
	e := cpuEngine(10)
	for sec := 0; sec <= 120; sec += 30 {
		cpuStep(e, sec, 90)
	}
	for sec := 150; sec <= 300; sec += 30 {
		cpuStep(e, sec, 10)
	}
	s := cpuSample(epoch.Add(330*time.Second), 0)
	s.Metric.Unavailable = []string{"cpu"}
	e.evaluate(s, s.Metric.Timestamp)
	for sec := 360; sec < 540; sec += 30 {
		cpuStep(e, sec, 10)
	}
	if e.p.CPU.Level != cpuCritical {
		t.Fatal("unknown data counted as recovery")
	}
	cpuStep(e, 540, 10)
	if e.p.CPU.Level != cpuNormal {
		t.Fatal("fresh recovery failed")
	}
}
func TestCPUReferenceCacheFreezeAndHostIsolation(t *testing.T) {
	e := testEngine()
	repo := &cpuRepo{ref: readyReference(epoch, 10)}
	e.db = repo
	step := func(sec int, value float64) {
		now := epoch.Add(time.Duration(sec) * time.Second)
		s := cpuSample(now, value)
		e.refreshCPUReference(context.Background(), s, now)
		e.evaluate(s, now)
	}
	step(0, 10)
	step(30, 10)
	if repo.reads != 1 || !e.p.CPU.ReferenceReady {
		t.Fatal("cache not used")
	}
	repo.ref = readyReference(epoch.Add(5*time.Minute), 20)
	step(300, 10)
	if repo.reads != 2 || e.p.CPU.Reference.Median != 20 {
		t.Fatal("cache not refreshed")
	}
	step(330, 40) // Freeze even before the relative threshold of 60 is met.
	for sec := 360; sec <= 8*3600; sec += 30 {
		step(sec, 40)
	}
	if repo.reads != 2 || e.p.CPU.Reference.Median != 20 || !e.cpuReferenceUsable(epoch.Add(8*time.Hour)) {
		t.Fatal("long elevated usage learned as normal")
	}
	now := epoch.Add(8*time.Hour + 30*time.Second)
	s := cpuSample(now, 40)
	s.Metric.HostName = "other-host"
	e.refreshCPUReference(context.Background(), s, now)
	e.evaluate(s, now)
	if e.cpuReferenceUsable(now) || e.cpuWarningThreshold(now) != 35 {
		t.Fatal("reference crossed host boundary")
	}
}
func TestCPUHistoryInsufficientStaleAndFailuresAreExplicit(t *testing.T) {
	for _, kind := range []string{"empty", "sparse", "stale", "error"} {
		t.Run(kind, func(t *testing.T) {
			e := testEngine()
			ref := readyReference(epoch, 10)
			repo := &cpuRepo{ref: ref}
			e.db = repo
			switch kind {
			case "empty":
				repo.ref = store.CPUReference{}
			case "sparse":
				repo.ref.First = repo.ref.Last.Add(-time.Minute)
			case "stale":
				repo.ref.Latest = epoch.Add(-2 * time.Hour)
			case "error":
				repo.historyErr = errors.New("DB unavailable")
			}
			e.refreshCPUReference(context.Background(), cpuSample(epoch, 10), epoch)
			if e.p.CPU.ReferenceReady || e.cpuHistoryIssue == "" {
				t.Fatal("invalid reference accepted/hidden")
			}
			e.refreshCPUReference(context.Background(), cpuSample(epoch.Add(time.Minute), 10), epoch.Add(time.Minute))
			if repo.reads != 1 {
				t.Fatal("failure retried on every metric")
			}
		})
	}
}
func TestCPUFrozenIncidentAndOutboxSurviveRestart(t *testing.T) {
	e := cpuEngine(10)
	for sec := 0; sec <= 300; sec += 30 {
		cpuStep(e, sec, 40)
	}
	if !e.save(context.Background()) {
		t.Fatal("save")
	}
	restarted := New(testConfig(), e.db, &fakeSource{})
	if !restarted.load(context.Background()) {
		t.Fatal("load")
	}
	now := epoch.Add(24 * time.Hour)
	for i := 0; i < 20; i++ {
		s := cpuSample(now.Add(time.Duration(i)*30*time.Second), 40)
		restarted.evaluate(s, s.Metric.Timestamp)
	}
	if len(restarted.p.Queue) != 1 || len(restarted.p.Events) != 1 || !restarted.p.CPU.Frozen || restarted.p.CPU.Reference.Median != 10 {
		t.Fatal("restart repeated warning or lost frozen reference")
	}
	if !restarted.p.CPU.CriticalSince.IsZero() {
		t.Fatal("unexpected critical candidate")
	}
}
func TestCPUPendingHoldsRestartAndLegacyMigration(t *testing.T) {
	e := cpuEngine(10)
	for sec := 0; sec <= 270; sec += 30 {
		cpuStep(e, sec, 40)
	}
	if !e.save(context.Background()) {
		t.Fatal("save")
	}
	r := New(testConfig(), e.db, &fakeSource{})
	if !r.load(context.Background()) {
		t.Fatal("load")
	}
	cpuStep(r, 300, 40)
	if len(r.p.Queue) != 0 {
		t.Fatal("restart carried pending hold")
	}
	legacy := testEngine()
	legacy.p.Alarms["metric:cpu-high"] = alarm{Active: true, Last: epoch.Add(-time.Minute)}
	for sec := 0; sec <= 300; sec += 30 {
		cpuStep(legacy, sec, 90)
	}
	if len(legacy.p.Queue) != 0 || legacy.p.CPU.Level != cpuCritical {
		t.Fatal("legacy incident duplicated")
	}
	for sec := 330; sec <= 510; sec += 30 {
		cpuStep(legacy, sec, 10)
	}
	if len(legacy.p.Queue) != 1 || !strings.Contains(legacy.p.Queue[0].Text, "normale döndü") {
		t.Fatal("legacy recovery lost")
	}
}
func TestCPUQueuePressureEscalationCoalescesAndDisabledIsIdle(t *testing.T) {
	e := cpuEngine(10)
	for sec := 0; sec <= 300; sec += 30 {
		cpuStep(e, sec, 40)
	}
	for sec := 330; sec <= 450; sec += 30 {
		cpuStep(e, sec, 90)
	}
	if len(e.p.Queue) != 1 || e.p.Queue[0].Priority != 3 || !strings.Contains(e.p.Queue[0].Text, "kritik") {
		t.Fatal("pending warning not replaced by critical")
	}
	repo := &cpuRepo{}
	off := New(config.Config{}, repo, nil)
	off.refreshCPUReference(context.Background(), cpuSample(epoch, 10), epoch)
	if repo.reads != 0 {
		t.Fatal("disabled feature queried CPU history")
	}
	e = cpuEngine(10)
	s := cpuSample(epoch, 90)
	s.Rules[0].Enabled = false
	e.refreshCPUReference(context.Background(), s, epoch)
	e.evaluate(s, epoch)
	if len(e.p.Queue) != 0 || e.cpuMeasurementAvailable {
		t.Fatal("disabled CPU rule still monitored")
	}
}

type blockingCPURepo struct {
	memoryRepo
	started, release chan struct{}
}

func (r *blockingCPURepo) CPUReference(ctx context.Context, _ string, now time.Time, _ float64) (store.CPUReference, error) {
	close(r.started)
	select {
	case <-r.release:
		return readyReference(now, 10), nil
	case <-ctx.Done():
		return store.CPUReference{}, ctx.Err()
	}
}
func TestCPUHistoryRunsOffCollectorAndDoesNotBlockIngress(t *testing.T) {
	repo := &blockingCPURepo{started: make(chan struct{}), release: make(chan struct{})}
	cfg := testConfig()
	cfg.Telegram.Times = nil
	e := New(cfg, repo, &fakeSource{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); e.Run(ctx) }()
	defer func() { cancel(); <-done }()
	s := cpuSample(time.Now(), 10)
	e.Observe(s.Metric, s.Rules)
	select {
	case <-repo.started:
	case <-time.After(3 * time.Second):
		t.Fatal("CPU history not queried by coordinator")
	}
	producerDone := make(chan struct{})
	go func() {
		defer close(producerDone)
		for i := 0; i < 1000; i++ {
			e.Observe(s.Metric, s.Rules)
		}
	}()
	select {
	case <-producerDone:
	case <-time.After(time.Second):
		t.Fatal("collector ingress waited for history")
	}
	// Stop before any possible Telegram dispatch. This test exercises local history only.
	cancel()
	close(repo.release)
}
func TestCPUShortSpikeAndFullQueueDoNotConsumeTransitions(t *testing.T) {
	e := cpuEngine(10)
	for sec := 0; sec <= 270; sec += 30 {
		cpuStep(e, sec, 40)
	}
	cpuStep(e, 300, 10)
	cpuStep(e, 330, 40)
	if len(e.p.Queue) != 0 {
		t.Fatal("short spike triggered warning")
	}
	for i := 0; i < queueLimit; i++ {
		e.enqueue(fmt.Sprintf("urgent-%d", i), "urgent", 3, epoch, 0)
	}
	for sec := 360; sec <= 630; sec += 30 {
		cpuStep(e, sec, 40)
	}
	if e.p.CPU.Level != cpuNormal || len(e.p.Queue) != queueLimit {
		t.Fatal("full outbox consumed warning or exceeded cap")
	}
	e.p.Queue = e.p.Queue[1:]
	cpuStep(e, 660, 40)
	if e.p.CPU.Level != cpuWarning || len(e.p.Queue) != queueLimit {
		t.Fatal("warning not retried after capacity recovered")
	}
}
func TestCPUWarningDoesNotResetCriticalHold(t *testing.T) {
	e := cpuEngine(10)
	for sec := 0; sec <= 240; sec += 30 {
		cpuStep(e, sec, 40)
	}
	for sec := 270; sec <= 360; sec += 30 {
		cpuStep(e, sec, 90)
	}
	if e.p.CPU.Level != cpuWarning {
		t.Fatal("warning missing at five minutes")
	}
	cpuStep(e, 390, 90)
	if e.p.CPU.Level != cpuCritical {
		t.Fatal("warning emission restarted critical debounce")
	}
}

func TestCPUInitialStatusUsesConfiguredLimitsButDoesNotInventHistory(t *testing.T) {
	status := testEngine().Status().CPU
	if status.WarningThreshold != 35 || status.CriticalThreshold != 80 || status.HistoryReady || status.MeasurementAvailable {
		t.Fatalf("misleading initial CPU status: %+v", status)
	}
}
