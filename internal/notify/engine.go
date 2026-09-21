// Package notify consumes existing Sentinel telemetry; it does not collect host metrics.
package notify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/slymanmrcan/sentinel/internal/config"
	"github.com/slymanmrcan/sentinel/internal/monitor"
	"github.com/slymanmrcan/sentinel/internal/store"
)

const (
	stateKey     = "telegram.state.v1"
	queueLimit   = 128
	counterLimit = 1024
	stateLimit   = 256
)

type repository interface {
	Setting(context.Context, string) (string, bool, error)
	SetSetting(context.Context, string, string) error
}
type source interface {
	Current() store.Metric
	SystemServices(context.Context) monitor.SystemdSnapshot
}
type message struct {
	ID, Key, Text          string
	Priority               int
	Created, Expires, Next time.Time
	Attempts               int
	EventSeq               uint64
}
type alarm struct {
	Active        bool
	Pending, Last time.Time
}
type event struct {
	Seq  uint64
	At   time.Time
	Text string
}
type persisted struct {
	Queue                    []message
	Alarms                   map[string]alarm
	Events                   []event
	Sequence, SummarySeq     uint64
	LastSlot                 string
	SSHCursor                string
	LastSuccess, LastFailure time.Time
	LastError                string
	Dropped                  uint64
	NextSend                 time.Time
}
type sample struct {
	Metric store.Metric
	Rules  []store.AlertRule
}
type failure struct {
	At                  time.Time
	IP, Account, Target string
	Locked              bool
}
type bucket struct {
	Start, Last   time.Time
	Count, Locked int
	IPs           []string
	Reported      bool
}

type Status struct {
	Enabled      bool      `json:"enabled"`
	Pending      int       `json:"pending"`
	LastSuccess  time.Time `json:"last_success"`
	LastFailure  time.Time `json:"last_failure"`
	LastError    string    `json:"last_error"`
	StorageError string    `json:"storage_error"`
	Dropped      uint64    `json:"dropped"`
	SSH          string    `json:"ssh"`
}

type Engine struct {
	cfg           config.Telegram
	db            repository
	source        source
	sender        *telegramSender
	location      *time.Location
	samples       chan sample
	failures      chan failure
	tests         chan struct{}
	dropped       atomic.Uint64
	statusMu      sync.RWMutex
	status        Status
	p             persisted
	counters      map[string]*bucket
	dirty, loaded bool
	inflight      string
	lastTest      time.Time
	services      monitor.SystemdSnapshot
	ssh           *monitor.SSHJournal
}

func New(cfg config.Config, db repository, src source) *Engine {
	e := &Engine{cfg: cfg.Telegram, db: db, source: src, status: Status{Enabled: cfg.Telegram.Enabled, SSH: "Kapalı"}}
	if !e.cfg.Enabled {
		return e
	}
	e.location, _ = time.LoadLocation(e.cfg.Timezone)
	if e.location == nil {
		e.location = time.UTC
	}
	e.samples = make(chan sample, 2)
	e.failures = make(chan failure, 256)
	e.tests = make(chan struct{}, 1)
	e.sender = newSender(e.cfg.Token, e.cfg.ChatID)
	e.p.Alarms = make(map[string]alarm)
	e.counters = make(map[string]*bucket)
	if e.cfg.SSHEnabled {
		e.ssh = monitor.NewSSHJournal(cfg.HostRoot)
		e.status.SSH = "Erişim henüz doğrulanmadı"
	}
	return e
}

// Observe is installed before the collector starts. Never wait for IO here.
func (e *Engine) Observe(m store.Metric, rules []store.AlertRule) {
	if !e.cfg.Enabled {
		return
	}
	if len(e.samples) == cap(e.samples) {
		e.dropped.Add(1)
		return
	}
	// Host mount enumeration is already shared; bound retained notification data.
	m.Filesystems = append([]store.Filesystem(nil), m.Filesystems[:min(len(m.Filesystems), 128)]...)
	m.Unavailable = append([]string(nil), m.Unavailable...)
	rules = append([]store.AlertRule(nil), rules[:min(len(rules), 32)]...)
	select {
	case e.samples <- sample{m, rules}:
	default:
		e.dropped.Add(1)
	}
}

func (e *Engine) LoginFailure(ip, account string, locked bool) {
	if !e.cfg.Enabled {
		return
	}
	if len(e.failures) == cap(e.failures) {
		e.dropped.Add(1)
		return
	}
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		ip = "bilinmiyor"
	} else {
		ip = addr.Unmap().String()
	}
	digest := sha256.Sum256([]byte(account))
	f := failure{At: time.Now(), IP: ip, Account: hex.EncodeToString(digest[:8]), Target: "Sentinel paneli", Locked: locked}
	select {
	case e.failures <- f:
	default:
		e.dropped.Add(1)
	}
}

func (e *Engine) Test() bool {
	if !e.cfg.Enabled {
		return false
	}
	select {
	case e.tests <- struct{}{}:
		return true
	default:
		return false
	}
}
func (e *Engine) Status() Status {
	e.statusMu.RLock()
	defer e.statusMu.RUnlock()
	s := e.status
	s.Dropped += e.dropped.Load()
	return s
}
func (e *Engine) publish() {
	e.statusMu.Lock()
	defer e.statusMu.Unlock()
	e.status.Pending = len(e.p.Queue)
	e.status.LastSuccess = e.p.LastSuccess
	e.status.LastFailure = e.p.LastFailure
	e.status.LastError = e.p.LastError
	e.status.Dropped = e.p.Dropped
}
func (e *Engine) storageError(failed bool) {
	e.statusMu.Lock()
	defer e.statusMu.Unlock()
	if failed {
		if e.status.StorageError == "" {
			log.Print("Telegram outbox depolaması kullanılamıyor; izleme devam ediyor")
		}
		e.status.StorageError = "Outbox depolaması kullanılamıyor; gönderim bekletiliyor"
	} else {
		e.status.StorageError = ""
	}
}
func (e *Engine) load(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	raw, found, err := e.db.Setting(ctx, stateKey)
	if err == nil && found {
		var p persisted
		if len(raw) > 2<<20 {
			err = fmt.Errorf("oversized state")
		} else {
			err = json.Unmarshal([]byte(raw), &p)
		}
		if err == nil && (len(p.Queue) > queueLimit || len(p.Alarms) > stateLimit || len(p.Events) > 50) {
			err = fmt.Errorf("invalid state bounds")
		}
		if err == nil {
			e.p = p
			if e.p.Alarms == nil {
				e.p.Alarms = make(map[string]alarm)
			}
			// Require a fresh continuous hold after any restart, but retain active alarms.
			for k, a := range e.p.Alarms {
				a.Pending = time.Time{}
				e.p.Alarms[k] = a
			}
		}
	}
	e.storageError(err != nil)
	e.loaded = err == nil
	return e.loaded
}
func (e *Engine) save(ctx context.Context) bool {
	if !e.dirty {
		return true
	}
	raw, err := json.Marshal(e.p)
	if err == nil {
		writeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		err = e.db.SetSetting(writeCtx, stateKey, string(raw))
		cancel()
	}
	if err != nil && ctx.Err() != nil {
		return false
	} // Shutdown cancellation is not a storage outage.
	e.storageError(err != nil)
	if err == nil {
		e.dirty = false
	}
	return err == nil
}

func (e *Engine) Run(ctx context.Context) {
	if !e.cfg.Enabled {
		return
	}
	jobs := make(chan message)
	results := make(chan delivery, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case m := <-jobs:
				r := e.sender.send(ctx, m)
				select {
				case results <- r:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	defer func() { <-done; e.sender.client.CloseIdleConnections() }()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	serviceTick := time.NewTicker(time.Minute)
	defer serviceTick.Stop()
	e.load(ctx)
	// Shared cache, independent of browser presence. Calls are bounded by systemctl timeout.
	e.checkServices(ctx, time.Now())
	if e.ssh != nil {
		e.checkSSH(ctx, time.Now())
	}
	for {
		select {
		case <-ctx.Done():
			if e.loaded {
				saveCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				e.save(saveCtx)
				cancel()
			}
			return
		case s := <-e.samples:
			if e.loaded {
				e.evaluate(s, time.Now())
			}
		case f := <-e.failures:
			if e.loaded {
				e.failure(f)
			}
		case <-e.tests:
			now := time.Now()
			if e.loaded && now.Sub(e.lastTest) >= time.Minute {
				e.enqueue("test", "Sentinel: Telegram test bildirimi.", 1, now, 0)
				e.lastTest = now
			}
		case r := <-results:
			e.complete(r, time.Now())
			e.inflight = ""
		case now := <-serviceTick.C:
			e.checkServices(ctx, now)
			if e.ssh != nil {
				e.checkSSH(ctx, now)
			}
			e.pruneCounters(now)
		case now := <-tick.C:
			if !e.loaded {
				if !e.load(ctx) {
					continue
				}
			}
			e.prune(now)
			e.schedule(now)
			if n := e.dropped.Swap(0); n > 0 {
				e.p.Dropped += n
				e.dirty = true
			}
			saved := e.save(ctx)
			if saved && e.inflight == "" && !now.Before(e.p.NextSend) {
				if m, ok := e.next(now); ok {
					// Persist the attempt and global rate limit before the network request.
					e.p.NextSend = now.Add(3 * time.Second)
					for i := range e.p.Queue {
						if e.p.Queue[i].ID == m.ID {
							e.p.Queue[i].Attempts++
							e.p.Queue[i].Next = now.Add(30 * time.Second)
							m = e.p.Queue[i]
							break
						}
					}
					e.dirty = true
					if e.save(ctx) {
						select {
						case jobs <- m:
							e.inflight = m.ID
						case <-ctx.Done():
							return
						}
					}
				}
			}
			e.publish()
		}
	}
}

func (e *Engine) enqueue(key, text string, priority int, now time.Time, seq uint64) bool {
	text = clip(text, messageLimit)
	for i, m := range e.p.Queue {
		if m.Key == key {
			if m.ID == e.inflight {
				return false
			}
			m.Text = clip(text+"\nTekrarlanan olaylar birleştirildi.", messageLimit)
			m.EventSeq = max(m.EventSeq, seq)
			// Keep original TTL/retry budget, but order the latest transition last.
			e.p.Queue = append(e.p.Queue[:i], e.p.Queue[i+1:]...)
			e.p.Queue = append(e.p.Queue, m)
			e.dirty = true
			return true
		}
	}
	if len(e.p.Queue) >= queueLimit {
		victim := -1
		for i, m := range e.p.Queue {
			if m.ID != e.inflight && m.Priority < priority {
				victim = i
				break
			}
		}
		e.p.Dropped++
		e.dirty = true
		if victim < 0 {
			return false
		}
		e.p.Queue = append(e.p.Queue[:victim], e.p.Queue[victim+1:]...)
	}
	ttl := 24 * time.Hour
	if priority == 0 {
		ttl = 10 * time.Minute
	}
	e.p.Sequence++
	e.p.Queue = append(e.p.Queue, message{ID: fmt.Sprint(e.p.Sequence), Key: key, Text: text, Priority: priority, Created: now, Expires: now.Add(ttl), Next: now, EventSeq: seq})
	e.dirty = true
	return true
}
func (e *Engine) record(text string, now time.Time) {
	e.p.Sequence++
	e.p.Events = append(e.p.Events, event{e.p.Sequence, now, clip(text, 300)})
	if len(e.p.Events) > 50 {
		e.p.Events = e.p.Events[len(e.p.Events)-50:]
	}
	e.dirty = true
}
func (e *Engine) next(now time.Time) (message, bool) {
	var selected message
	found := false
	for _, m := range e.p.Queue {
		if now.Before(m.Next) || !now.Before(m.Expires) || m.Attempts >= 6 {
			continue
		}
		if !found || m.Priority > selected.Priority {
			selected = m
			found = true
		}
	}
	return selected, found
}
func (e *Engine) prune(now time.Time) {
	q := e.p.Queue[:0]
	for _, m := range e.p.Queue {
		if m.ID != e.inflight && (!now.Before(m.Expires) || m.Attempts >= 6) {
			e.p.Dropped++
			e.dirty = true
			continue
		}
		q = append(q, m)
	}
	e.p.Queue = q
	events := e.p.Events[:0]
	for _, v := range e.p.Events {
		if now.Sub(v.At) <= 7*24*time.Hour {
			events = append(events, v)
		} else {
			e.dirty = true
		}
	}
	e.p.Events = events
	for k, a := range e.p.Alarms {
		if !a.Last.IsZero() && now.Sub(a.Last) > 7*24*time.Hour {
			delete(e.p.Alarms, k)
			e.dirty = true
		}
	}
}
func (e *Engine) complete(r delivery, now time.Time) {
	for i := range e.p.Queue {
		m := &e.p.Queue[i]
		if m.ID != r.ID {
			continue
		}
		if r.OK {
			e.p.LastSuccess = now
			if m.Priority == 0 {
				e.p.SummarySeq = max(e.p.SummarySeq, m.EventSeq)
			}
		} else {
			e.p.LastFailure = now
			e.p.LastError = r.Error
			log.Printf("Telegram: %s", r.Error)
		}
		if r.After > 0 {
			e.p.NextSend = maxTime(e.p.NextSend, now.Add(r.After))
		}
		if r.OK || r.Permanent || m.Attempts >= 6 {
			if !r.OK {
				e.p.Dropped++
			}
			e.p.Queue = append(e.p.Queue[:i], e.p.Queue[i+1:]...)
		} else {
			backoff := time.Duration(1<<min(m.Attempts, 8)) * 5 * time.Second
			m.Next = now.Add(max(backoff, r.After))
		}
		e.dirty = true
		return
	}
}
func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}
