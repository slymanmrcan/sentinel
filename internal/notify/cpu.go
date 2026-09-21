package notify

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/slymanmrcan/sentinel/internal/store"
)

const (
	cpuReferenceRefresh = 5 * time.Minute
	cpuReferenceMaxAge  = 30 * time.Minute
	cpuNormal           = 0
	cpuWarning          = 1
	cpuCritical         = 2
)

type cpuState struct {
	Key, Host                                        string
	Level                                            int // Highest notified severity of the current incident; no downgrade spam.
	Last, WarningSince, CriticalSince, RecoverySince time.Time
	Frozen                                           bool
	Reference                                        store.CPUReference
	ReferenceAt                                      time.Time
	ReferenceHost                                    string
	ReferenceReady                                   bool
}

func (s *cpuState) resetHolds() {
	s.WarningSince = time.Time{}
	s.CriticalSince = time.Time{}
	s.RecoverySince = time.Time{}
}

type CPUStatus struct {
	Level                string    `json:"level"`
	MeasurementAvailable bool      `json:"measurement_available"`
	HistoryReady         bool      `json:"history_ready"`
	TypicalPercent       float64   `json:"typical_percent"`
	WarningThreshold     float64   `json:"warning_threshold"`
	CriticalThreshold    int       `json:"critical_threshold"`
	RecoveryThreshold    int       `json:"recovery_threshold"`
	HistorySamples       int       `json:"history_samples"`
	ReferenceAt          time.Time `json:"reference_at"`
	Frozen               bool      `json:"frozen"`
	Message              string    `json:"message"`
}

type cpuHistoryReader interface {
	CPUReference(context.Context, string, time.Time, float64) (store.CPUReference, error)
}

func enabledCPUKey(s sample) string {
	for _, r := range s.Rules {
		if r.Enabled && r.Metric == "cpu" {
			return "metric:" + r.ID
		}
	}
	return ""
}
func validCPU(m store.Metric, now time.Time) bool {
	return !missing(m, "cpu", now) && !math.IsNaN(m.CPUPercent) && !math.IsInf(m.CPUPercent, 0) && m.CPUPercent >= 0 && m.CPUPercent <= 100
}

// Runs only on the notification coordinator, never on the collector or login
// request. Reuses stored telemetry with one bounded query per five minutes.
func (e *Engine) refreshCPUReference(ctx context.Context, sample sample, now time.Time) {
	key := enabledCPUKey(sample)
	m := sample.Metric
	if !e.cfg.Enabled || key == "" || !validCPU(m, now) || missing(m, "host", now) || m.HostName == "" {
		return
	}
	c := &e.p.CPU
	if c.Host != m.HostName {
		c.Host = m.HostName
		c.ReferenceReady = false
		c.resetHolds()
		e.nextCPURefresh = time.Time{}
		e.dirty = true
	}
	// Old notification state is also an incident: do not relearn at migration.
	if c.Frozen || c.Level != cpuNormal || e.p.Alarms[key].Active || now.Before(e.nextCPURefresh) {
		return
	}
	e.nextCPURefresh = now.Add(cpuReferenceRefresh)
	reader, ok := e.db.(cpuHistoryReader)
	if !ok {
		e.cpuHistoryIssue = "CPU geçmişi kullanılamıyor; sabit eşik geçerli"
		return
	}
	queryCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	ref, err := reader.CPUReference(queryCtx, m.HostName, now, float64(e.cfg.CPU.Warning))
	if err != nil {
		e.cpuHistoryIssue = "CPU geçmişi okunamadı; geçerli önbellek veya sabit eşik kullanılıyor"
		return
	}
	cutoff := now.Add(-5 * time.Minute)
	ready := ref.Count >= 120 && ref.Last.Sub(ref.First) >= time.Hour &&
		!ref.Latest.Before(cutoff.Add(-90*time.Second)) && ref.Latest.Before(cutoff) &&
		!math.IsNaN(ref.Median) && !math.IsInf(ref.Median, 0) && ref.Median >= 0 && ref.Median < float64(e.cfg.CPU.Warning)
	c.Reference = ref
	c.ReferenceAt = now
	c.ReferenceHost = m.HostName
	c.ReferenceReady = ready
	e.cpuHistoryIssue = ""
	if !ready {
		e.cpuHistoryIssue = "Yeterli güncel CPU geçmişi yok; sabit eşik geçerli"
	}
	e.dirty = true
}

func (e *Engine) cpuReferenceUsable(now time.Time) bool {
	c := e.p.CPU
	return c.ReferenceReady && c.ReferenceHost == c.Host && !c.ReferenceAt.IsZero() && !c.ReferenceAt.After(now.Add(5*time.Second)) &&
		(c.Frozen || now.Sub(c.ReferenceAt) <= cpuReferenceMaxAge)
}
func (e *Engine) cpuWarningThreshold(now time.Time) float64 {
	threshold := float64(e.cfg.CPU.Warning)
	if e.cpuReferenceUsable(now) {
		threshold = math.Max(threshold, e.p.CPU.Reference.Median*float64(e.cfg.CPU.Multiplier))
	}
	return threshold
}
func (e *Engine) cpuStatus(now time.Time) CPUStatus {
	c := e.p.CPU
	level := "normal"
	switch c.Level {
	case cpuWarning:
		level = "warning"
	case cpuCritical:
		level = "critical"
	}
	ready := e.cpuReferenceUsable(now)
	message := e.cpuHistoryIssue
	if ready && c.Frozen {
		message = "Normal seviye yükseliş boyunca sabit tutuluyor"
	} else if ready && message == "" {
		message = "Son 24 saatin düşük yük medyanı"
	} else if !ready && message == "" {
		message = "Yeterli güncel CPU geçmişi yok; sabit eşik geçerli"
	}
	return CPUStatus{Level: level, MeasurementAvailable: e.cpuMeasurementAvailable && !c.Last.IsZero() && now.Sub(c.Last) <= 90*time.Second,
		HistoryReady: ready, TypicalPercent: c.Reference.Median, WarningThreshold: e.cpuWarningThreshold(now),
		CriticalThreshold: e.cfg.CPU.Critical, RecoveryThreshold: e.cfg.CPU.Recovery, HistorySamples: c.Reference.Count,
		ReferenceAt: c.ReferenceAt, Frozen: c.Frozen, Message: message}
}

func (e *Engine) evaluateCPU(m store.Metric, key string, now time.Time) {
	c := &e.p.CPU
	// Continue a previously delivered legacy CPU alarm without sending a duplicate.
	if c.Key == "" && e.p.Alarms[key].Active {
		c.Level = cpuCritical
		c.Frozen = true
	}
	c.Key = key
	e.cpuMeasurementAvailable = validCPU(m, now)
	if !e.cpuMeasurementAvailable {
		c.resetHolds()
		e.dirty = true
		return
	}
	if !m.Timestamp.After(c.Last) {
		return
	}
	if !c.Last.IsZero() && m.Timestamp.Sub(c.Last) > 90*time.Second {
		c.resetHolds()
	}
	if m.HostName != "" && !missing(m, "host", now) && c.Host != m.HostName {
		c.Host = m.HostName
		c.ReferenceReady = false
		c.resetHolds()
		e.nextCPURefresh = time.Time{}
	}
	c.Last = m.Timestamp
	value := m.CPUPercent
	// Freeze at the absolute floor, before either debounce has completed. A stale
	// cached reference must not become valid merely because we start freezing it.
	if value >= float64(e.cfg.CPU.Warning) && !c.Frozen {
		if !e.cpuReferenceUsable(now) {
			c.ReferenceReady = false
		}
		c.Frozen = true
	}
	warningThreshold := e.cpuWarningThreshold(now)
	warningDue := hold(&c.WarningSince, c.Level == cpuNormal && value >= warningThreshold, m.Timestamp, e.cfg.CPU.WarningHold)
	criticalDue := hold(&c.CriticalSince, c.Level < cpuCritical && value >= float64(e.cfg.CPU.Critical), m.Timestamp, e.cfg.CPU.CriticalHold)
	recoveryDue := hold(&c.RecoverySince, c.Level != cpuNormal && value <= float64(e.cfg.CPU.Recovery), m.Timestamp, e.cfg.CPU.RecoveryHold)
	switch {
	case criticalDue:
		text := fmt.Sprintf("CPU kritik: %s boyunca %%%d ve üzerinde; şu an %%%.1f.", holdLabel(e.cfg.CPU.CriticalHold), e.cfg.CPU.Critical, value)
		if e.cpuEvent(key, text, 3, now) {
			c.Level = cpuCritical
			c.resetHolds()
		}
	case warningDue:
		reference := "Geçmiş yetersiz; sabit eşik kullanıldı."
		if e.cpuReferenceUsable(now) {
			reference = fmt.Sprintf("Olağan düşük yük seviyesi %%%.1f; uyarı eşiği %%%.1f.", c.Reference.Median, warningThreshold)
		}
		text := fmt.Sprintf("CPU erken uyarı: %s boyunca yüksek; şu an %%%.1f. %s", holdLabel(e.cfg.CPU.WarningHold), value, reference)
		if e.cpuEvent(key, text, 2, now) {
			c.Level = cpuWarning
			c.WarningSince = time.Time{}
		}
	case recoveryDue:
		text := fmt.Sprintf("CPU normale döndü: %s boyunca %%%d ve altında; şu an %%%.1f.", holdLabel(e.cfg.CPU.RecoveryHold), e.cfg.CPU.Recovery, value)
		if e.cpuEvent(key, text, 2, now) {
			c.Level = cpuNormal
			c.resetHolds()
			c.Frozen = false
			e.nextCPURefresh = time.Time{}
		}
	}
	if c.Level == cpuNormal && value < float64(e.cfg.CPU.Warning) {
		c.Frozen = false
	}
	// Keep legacy state in sync for existing installations and disabled rules.
	if _, exists := e.p.Alarms[key]; exists || len(e.p.Alarms) < stateLimit {
		e.p.Alarms[key] = alarm{Active: c.Level != cpuNormal, Last: m.Timestamp}
	}
	e.dirty = true
}
func (e *Engine) cpuEvent(key, text string, priority int, now time.Time) bool {
	if !e.enqueue(key, "Sentinel · "+text, priority, now, 0) {
		return false
	}
	e.record(text, now)
	return true
}
func hold(since *time.Time, condition bool, at time.Time, duration time.Duration) bool {
	if !condition {
		*since = time.Time{}
		return false
	}
	if since.IsZero() {
		*since = at
		return false
	}
	return at.Sub(*since) >= duration
}
func holdLabel(d time.Duration) string {
	if d%time.Minute == 0 {
		return fmt.Sprintf("%d dakika", int(d/time.Minute))
	}
	return fmt.Sprintf("%d saniye", int(d/time.Second))
}
