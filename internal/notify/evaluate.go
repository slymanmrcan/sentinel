package notify

import (
	"context"
	"fmt"
	"math"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/slymanmrcan/sentinel/internal/monitor"
	"github.com/slymanmrcan/sentinel/internal/store"
)

func missing(m store.Metric, key string, now time.Time) bool {
	return m.Timestamp.IsZero() || now.Sub(m.Timestamp) > 90*time.Second || m.Timestamp.After(now.Add(5*time.Second)) || slices.Contains(m.Unavailable, "stale") || slices.Contains(m.Unavailable, key)
}
func (e *Engine) evaluate(s sample, now time.Time) {
	seen := make(map[string]bool)
	values := map[string]float64{"cpu": s.Metric.CPUPercent, "memory": s.Metric.RAMPercent, "swap": s.Metric.SwapPercent, "disk": s.Metric.DiskPercent}
	labels := map[string]string{"cpu": "CPU", "memory": "RAM", "swap": "Swap", "disk": "Disk /"}
	for _, r := range s.Rules {
		v, ok := values[r.Metric]
		if !r.Enabled || !ok {
			continue
		}
		seen["metric:"+r.ID] = true
		valid := !missing(s.Metric, r.Metric, now) && !math.IsNaN(v) && !math.IsInf(v, 0)
		e.transition("metric:"+r.ID, labels[r.Metric], valid, v >= r.Threshold, v <= math.Max(0, r.Threshold-float64(e.cfg.RecoveryMargin)), s.Metric.Timestamp, now, fmt.Sprintf("%.1f%% (alarm %.1f%%)", v, r.Threshold))
		if r.Metric == "disk" {
			for _, fs := range s.Metric.Filesystems {
				if fs.Mountpoint == "/" {
					continue
				}
				seen["mount:"+r.ID+":"+fs.Mountpoint] = true
				e.transition("mount:"+r.ID+":"+fs.Mountpoint, "Disk "+clip(fs.Mountpoint, 100), !missing(s.Metric, "filesystems", now) && fs.AvailableStats && !math.IsNaN(fs.UsedPercent) && !math.IsInf(fs.UsedPercent, 0), fs.UsedPercent >= r.Threshold, fs.UsedPercent <= math.Max(0, r.Threshold-float64(e.cfg.RecoveryMargin)), s.Metric.Timestamp, now, fmt.Sprintf("%.1f%%", fs.UsedPercent))
			}
		}
	}
	for key, a := range e.p.Alarms {
		if (strings.HasPrefix(key, "metric:") || strings.HasPrefix(key, "mount:")) && !seen[key] && !a.Pending.IsZero() {
			a.Pending = time.Time{}
			e.p.Alarms[key] = a
			e.dirty = true
		}
	}
}

func (e *Engine) transition(key, label string, valid, bad, good bool, at, now time.Time, detail string) {
	a, exists := e.p.Alarms[key]
	if !exists && len(e.p.Alarms) >= stateLimit {
		e.p.Dropped++
		e.dirty = true
		return
	}
	if !valid {
		if !a.Pending.IsZero() {
			a.Pending = time.Time{}
			e.p.Alarms[key] = a
			e.dirty = true
		}
		return
	}
	if !at.After(a.Last) {
		return
	} // A cached measurement cannot satisfy a hold twice.
	if !a.Last.IsZero() && at.Sub(a.Last) > maxGap(key) {
		a.Pending = time.Time{}
	}
	a.Last = at
	candidate := (!a.Active && bad) || (a.Active && good)
	if !candidate {
		a.Pending = time.Time{}
	} else if a.Pending.IsZero() {
		a.Pending = at
	} else if at.Sub(a.Pending) >= e.cfg.Hold {
		active := !a.Active
		text := label + ": sorun — " + detail
		if !active {
			text = label + ": normale döndü — " + detail
		}
		// A full queue must not silently consume a transition; retry next sample.
		if e.enqueue(key, "Sentinel · "+text, 2, now, 0) {
			a.Active = active
			a.Pending = time.Time{}
			e.record(text, now)
		}
	}
	e.p.Alarms[key] = a
	e.dirty = true
}

func (e *Engine) checkServices(ctx context.Context, now time.Time) {
	if !e.loaded || e.source == nil {
		return
	}
	s := e.source.SystemServices(ctx)
	e.services = s
	for _, v := range s.Services {
		valid := s.Available && now.Sub(s.CheckedAt) <= 90*time.Second && v.LoadState == "loaded" && (v.ActiveState == "active" || v.ActiveState == "inactive" || v.ActiveState == "failed")
		e.transition("service:"+v.Unit, v.Unit, valid, v.ActiveState == "inactive" || v.ActiveState == "failed", v.ActiveState == "active", s.CheckedAt, now, "durum: "+v.ActiveState)
	}
	// Missing or unreadable units never generate recovery; reset pending holds.
	for key, a := range e.p.Alarms {
		if strings.HasPrefix(key, "service:") && (!s.Available || !slices.ContainsFunc(s.Services, func(v monitor.SystemdService) bool { return key == "service:"+v.Unit })) {
			a.Pending = time.Time{}
			e.p.Alarms[key] = a
			e.dirty = true
		}
	}
}

func maxGap(key string) time.Duration {
	if strings.HasPrefix(key, "service:") {
		return 150 * time.Second
	}
	return 90 * time.Second
}

func (e *Engine) failure(f failure) {
	if _, err := netip.ParseAddr(f.IP); err == nil {
		e.count("ip:"+f.Target+":"+f.IP, f, false)
	}
	e.count("account:"+f.Target+":"+f.Account, f, true)
}
func (e *Engine) count(key string, f failure, distributed bool) {
	b := e.counters[key]
	if b != nil && f.At.Sub(b.Start) >= e.cfg.BruteWindow {
		delete(e.counters, key)
		b = nil
	}
	if b == nil {
		if len(e.counters) >= counterLimit {
			e.p.Dropped++
			e.dirty = true
			return
		}
		b = &bucket{Start: f.At}
		e.counters[key] = b
	}
	b.Last = f.At
	b.Count = min(b.Count+1, 1_000_000)
	if f.Locked {
		b.Locked = min(b.Locked+1, 1_000_000)
	}
	if _, err := netip.ParseAddr(f.IP); err == nil && !slices.Contains(b.IPs, f.IP) && len(b.IPs) < 8 {
		b.IPs = append(b.IPs, f.IP)
	}
	if b.Reported || b.Count < e.cfg.BruteThreshold || (distributed && len(b.IPs) < 2) {
		return
	}
	kind := "aynı IP"
	if distributed {
		kind = "aynı hesap (kimlik izi " + f.Account + ") / farklı IP'ler"
	}
	text := fmt.Sprintf("%s: yoğun başarısız giriş (%s). %s–%s: %d deneme, %d kilitli ret. Kaynak IP (en çok 8): %s", f.Target, kind, b.Start.In(e.location).Format(time.RFC3339), f.At.In(e.location).Format(time.RFC3339), b.Count, b.Locked, strings.Join(b.IPs, ", "))
	if e.enqueue("brute:"+key, text, 2, f.At, 0) {
		b.Reported = true
		e.record(text, f.At)
	}
}
func (e *Engine) pruneCounters(now time.Time) {
	for k, b := range e.counters {
		if now.Sub(b.Start) >= e.cfg.BruteWindow {
			delete(e.counters, k)
		}
	}
}

func (e *Engine) schedule(now time.Time) {
	local := now.In(e.location)
	for _, clock := range e.cfg.Times {
		parsed, _ := time.Parse("15:04", clock)
		slot := time.Date(local.Year(), local.Month(), local.Day(), parsed.Hour(), parsed.Minute(), 0, 0, e.location)
		if slot.Format("15:04") != clock {
			continue
		} // Skip nonexistent local times at DST spring-forward.
		key := slot.Format("2006-01-02 15:04")
		// Only a two-minute current window is eligible: no startup catch-up burst.
		if now.Before(slot) || now.Sub(slot) >= 2*time.Minute || key <= e.p.LastSlot {
			continue
		}
		if e.enqueue("summary", e.summary(now), 0, now, e.p.Sequence) {
			e.p.LastSlot = key
			e.dirty = true
		}
	}
}
func (e *Engine) summary(now time.Time) string {
	m := e.source.Current()
	lines := []string{"Sentinel durum özeti · " + now.In(e.location).Format("02.01 15:04 MST")}
	if missing(m, "host", now) {
		lines = append(lines, "Sunucu / uptime: eski veya alınamıyor")
	} else {
		lines = append(lines, fmt.Sprintf("%s · uptime %s", clip(m.HostName, 100), (time.Duration(m.Uptime)*time.Second).String()))
	}
	lines = append(lines, "Ölçüm: "+m.Timestamp.In(e.location).Format(time.RFC3339))
	for _, v := range []struct {
		key, label string
		value      float64
	}{{"cpu", "CPU", m.CPUPercent}, {"memory", "RAM", m.RAMPercent}, {"swap", "Swap", m.SwapPercent}, {"disk", "Disk /", m.DiskPercent}} {
		text := fmt.Sprintf("%s: %.1f%%", v.label, v.value)
		if missing(m, v.key, now) || math.IsNaN(v.value) || math.IsInf(v.value, 0) {
			text = v.label + ": eski veya alınamıyor"
		}
		lines = append(lines, text)
	}
	// Read only the existing cache; the engine refreshes it independently each minute.
	s := e.services
	if !s.Enabled {
		lines = append(lines, "Servis takibi: yapılandırılmadı")
	} else if !s.Available || now.Sub(s.CheckedAt) > 90*time.Second {
		lines = append(lines, "Servisler: eski veya alınamıyor")
	} else {
		working, problems, unknown := 0, 0, 0
		for _, v := range s.Services {
			if v.LoadState != "loaded" {
				unknown++
			} else if v.ActiveState == "active" {
				working++
			} else if v.ActiveState == "inactive" || v.ActiveState == "failed" {
				problems++
			} else {
				unknown++
			}
		}
		lines = append(lines, fmt.Sprintf("Servisler: %d çalışıyor, %d sorun, %d alınamıyor", working, problems, unknown))
		for _, v := range s.Services {
			state := v.ActiveState
			if v.LoadState != "loaded" || (state != "active" && state != "inactive" && state != "failed") {
				state = "alınamıyor"
			}
			if state != "active" {
				lines = append(lines, clip(v.Unit, 64)+": "+state)
			}
		}
	}
	if e.cfg.SSHEnabled {
		lines = append(lines, "SSH takibi: "+e.Status().SSH)
	}
	if slices.Contains(m.Unavailable, "filesystems") {
		lines = append(lines, "Disk listesi: eksik / alınamıyor")
	}
	for _, fs := range m.Filesystems[:min(len(m.Filesystems), 128)] {
		if fs.Mountpoint == "/" {
			continue
		}
		text := fmt.Sprintf("Disk %s: %.1f%%", clip(fs.Mountpoint, 80), fs.UsedPercent)
		if !fs.AvailableStats || missing(m, "filesystems", now) || math.IsNaN(fs.UsedPercent) || math.IsInf(fs.UsedPercent, 0) {
			text = "Disk " + clip(fs.Mountpoint, 80) + ": eski veya alınamıyor"
		}
		lines = append(lines, text)
	}
	// Reserve space for significant events even on hosts with many mounts.
	text := clip(strings.Join(lines, "\n"), 2400)
	text += "\nSon başarılı özetten sonraki olaylar (son 7 gün, en çok 50 kayıt):"
	count := 0
	for _, v := range e.p.Events {
		if v.Seq > e.p.SummarySeq {
			count++
		}
	}
	if count == 0 {
		text += " yok"
	} else {
		text += fmt.Sprintf(" %d\n", count)
		for i := len(e.p.Events) - 1; i >= 0; i-- {
			v := e.p.Events[i]
			if v.Seq > e.p.SummarySeq {
				text += v.At.In(e.location).Format("02.01 15:04") + " " + v.Text + "\n"
				if len([]rune(text)) > messageLimit-100 {
					break
				}
			}
		}
	}
	if e.p.Dropped > 0 {
		text += fmt.Sprintf("\nKapasite/süre/teslimat nedeniyle atlanan: %d", e.p.Dropped)
	}
	return clip(text, messageLimit)
}
