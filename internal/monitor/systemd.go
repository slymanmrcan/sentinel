package monitor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	systemCommandOutputLimit = 1 << 20
	systemdSnapshotTTL       = time.Minute
	systemctlTimeout         = 5 * time.Second
	journalTimeout           = 3 * time.Second
)

var ErrSystemdUnitNotConfigured = errors.New("systemd unit is not configured")

type SystemdSnapshot struct {
	Enabled   bool             `json:"enabled"`
	Available bool             `json:"available"`
	CheckedAt time.Time        `json:"checked_at"`
	Message   string           `json:"message,omitempty"`
	Services  []SystemdService `json:"services"`
}

type SystemdService struct {
	Unit          string     `json:"unit"`
	Description   string     `json:"description,omitempty"`
	LoadState     string     `json:"load_state"`
	ActiveState   string     `json:"active_state"`
	SubState      string     `json:"sub_state"`
	UnitFileState string     `json:"unit_file_state,omitempty"`
	RestartPolicy string     `json:"restart_policy,omitempty"`
	Result        string     `json:"result,omitempty"`
	Restarts      uint64     `json:"restarts"`
	ActiveSince   *time.Time `json:"active_since,omitempty"`
}

type SystemdLogsSnapshot struct {
	Unit      string         `json:"unit"`
	Available bool           `json:"available"`
	CheckedAt time.Time      `json:"checked_at"`
	Message   string         `json:"message,omitempty"`
	Logs      []JournalEntry `json:"logs"`
}

type JournalEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Priority  int       `json:"priority"`
	Message   string    `json:"message"`
}

type systemCommandRunner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

type execSystemCommandRunner struct {
	strictJournal bool
	hostRoot      string
}

type cappedBuffer struct {
	buffer bytes.Buffer
	limit  int
}

func (b *cappedBuffer) Write(data []byte) (int, error) {
	if b.buffer.Len() < b.limit {
		remaining := b.limit - b.buffer.Len()
		if remaining > len(data) {
			remaining = len(data)
		}
		_, _ = b.buffer.Write(data[:remaining])
	}
	return len(data), nil
}

func (r execSystemCommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Env = append(os.Environ(), "LC_ALL=C", "TZ=UTC")

	// Prefer the host bus beneath HOST_ROOT when it is visible. Otherwise
	// systemctl uses the runtime sockets mounted directly beneath /run.
	if r.hostRoot != "" {
		busPath := filepath.Join(r.hostRoot, "run/dbus/system_bus_socket")
		if _, err := os.Stat(busPath); err == nil {
			command.Env = append(command.Env, "DBUS_SYSTEM_BUS_ADDRESS=unix:path="+busPath)
		}
	}

	stdout := &cappedBuffer{limit: systemCommandOutputLimit}
	stderr := &cappedBuffer{limit: 64 << 10}
	command.Stdout = stdout
	command.Stderr = stderr

	if err := command.Run(); err != nil {
		message := strings.TrimSpace(stderr.buffer.String())
		if message != "" {
			return stdout.buffer.Bytes(), fmt.Errorf("%w: %s", err, message)
		}
		return stdout.buffer.Bytes(), err
	}
	if r.strictJournal && strings.TrimSpace(stderr.buffer.String()) != "" {
		return nil, errors.New("journal access could not be verified")
	}
	return stdout.buffer.Bytes(), nil
}

type SystemdMonitor struct {
	units    []string
	unitSet  map[string]struct{}
	logLines int
	hostRoot string
	runner   systemCommandRunner

	cacheMu  sync.Mutex
	cached   SystemdSnapshot
	cachedAt time.Time
}

func NewSystemdMonitor(units []string, logLines int, hostRoot string) *SystemdMonitor {
	if logLines <= 0 {
		logLines = 8
	}
	unitSet := make(map[string]struct{}, len(units))
	for _, unit := range units {
		unitSet[unit] = struct{}{}
	}
	return &SystemdMonitor{
		units:    append([]string(nil), units...),
		unitSet:  unitSet,
		logLines: logLines,
		hostRoot: hostRoot,
		runner:   execSystemCommandRunner{hostRoot: hostRoot},
	}
}

func (c *Collector) SystemServices(ctx context.Context) SystemdSnapshot {
	return c.systemd.Snapshot(ctx)
}

func (c *Collector) SystemServiceLogs(ctx context.Context, unit string) (SystemdLogsSnapshot, error) {
	return c.systemd.Logs(ctx, unit)
}

func (m *SystemdMonitor) Snapshot(ctx context.Context) SystemdSnapshot {
	checkedAt := time.Now()
	if len(m.units) == 0 {
		return SystemdSnapshot{
			Enabled:   false,
			Available: false,
			CheckedAt: checkedAt,
			Services:  []SystemdService{},
			Message:   "No systemd services are configured",
		}
	}

	// Keep the mutex for the whole refresh so concurrent requests share one
	// systemctl invocation and then receive the same cached snapshot.
	m.cacheMu.Lock()
	defer m.cacheMu.Unlock()
	if !m.cachedAt.IsZero() && time.Since(m.cachedAt) < systemdSnapshotTTL {
		return cloneSystemdSnapshot(m.cached)
	}

	snapshot := m.collect(ctx)
	m.cached = cloneSystemdSnapshot(snapshot)
	m.cachedAt = time.Now()
	return snapshot
}

func (m *SystemdMonitor) collect(ctx context.Context) SystemdSnapshot {
	checkedAt := time.Now()
	commandCtx, cancel := context.WithTimeout(ctx, systemctlTimeout)
	defer cancel()

	args := []string{
		"show",
		"--no-pager",
		"--property=Id,Description,LoadState,ActiveState,SubState,UnitFileState,Restart,Result,NRestarts,ActiveEnterTimestamp",
		"--",
	}
	args = append(args, m.units...)
	output, err := m.runner.Run(commandCtx, "systemctl", args...)
	if err != nil && !bytes.Contains(output, []byte("LoadState=")) {
		message := "systemctl failed: " + cleanCommandError(err)
		if errors.Is(commandCtx.Err(), context.DeadlineExceeded) {
			message = "systemctl timed out: " + cleanCommandError(err)
		}
		return SystemdSnapshot{
			Enabled:   true,
			Available: false,
			CheckedAt: checkedAt,
			Services:  []SystemdService{},
			Message:   message,
		}
	}

	snapshot := SystemdSnapshot{
		Enabled:   true,
		Available: true,
		CheckedAt: checkedAt,
		Services:  parseSystemctlShow(output, m.units),
	}
	if err != nil {
		snapshot.Message = "systemctl partial failure: " + cleanCommandError(err)
	}
	return snapshot
}

func (m *SystemdMonitor) Logs(ctx context.Context, unit string) (SystemdLogsSnapshot, error) {
	if _, ok := m.unitSet[unit]; !ok {
		return SystemdLogsSnapshot{}, fmt.Errorf("%w: %s", ErrSystemdUnitNotConfigured, unit)
	}

	checkedAt := time.Now()
	commandCtx, cancel := context.WithTimeout(ctx, journalTimeout)
	defer cancel()

	args := []string{
		"--no-pager",
		"--quiet",
		"--output=json",
		"--output-fields=__REALTIME_TIMESTAMP,PRIORITY,MESSAGE",
		"--lines=" + strconv.Itoa(m.logLines),
		"--unit=" + unit,
	}
	if m.hostRoot != "" {
		args = append(args, "--root="+m.hostRoot)
	}

	output, err := m.runner.Run(commandCtx, "journalctl", args...)
	if err != nil {
		message := "journalctl failed: " + cleanCommandError(err)
		if errors.Is(commandCtx.Err(), context.DeadlineExceeded) {
			message = "journalctl timed out: " + cleanCommandError(err)
		}
		return SystemdLogsSnapshot{
			Unit:      unit,
			Available: false,
			CheckedAt: checkedAt,
			Logs:      []JournalEntry{},
			Message:   message,
		}, nil
	}

	return SystemdLogsSnapshot{
		Unit:      unit,
		Available: true,
		CheckedAt: checkedAt,
		Logs:      parseJournalJSON(output),
	}, nil
}

func parseSystemctlShow(output []byte, requested []string) []SystemdService {
	byUnit := make(map[string]SystemdService, len(requested))
	content := strings.TrimSpace(string(output))
	if content != "" {
		for _, block := range strings.Split(content, "\n\n") {
			properties := make(map[string]string)
			for _, line := range strings.Split(block, "\n") {
				key, value, found := strings.Cut(line, "=")
				if found {
					properties[key] = value
				}
			}

			unit := properties["Id"]
			if unit == "" {
				continue
			}
			service := SystemdService{
				Unit:          unit,
				Description:   properties["Description"],
				LoadState:     properties["LoadState"],
				ActiveState:   properties["ActiveState"],
				SubState:      properties["SubState"],
				UnitFileState: properties["UnitFileState"],
				RestartPolicy: properties["Restart"],
				Result:        properties["Result"],
			}
			service.Restarts, _ = strconv.ParseUint(properties["NRestarts"], 10, 64)
			if activeSince, ok := parseSystemdTime(properties["ActiveEnterTimestamp"]); ok {
				service.ActiveSince = &activeSince
			}
			byUnit[unit] = service
		}
	}

	services := make([]SystemdService, 0, len(requested))
	for _, unit := range requested {
		service, found := byUnit[unit]
		if !found {
			service = SystemdService{
				Unit:        unit,
				LoadState:   "not-found",
				ActiveState: "unknown",
				SubState:    "unknown",
			}
		}
		services = append(services, service)
	}
	return services
}

func parseSystemdTime(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" || value == "n/a" || value == "0" {
		return time.Time{}, false
	}
	layouts := []string{
		"Mon 2006-01-02 15:04:05 MST",
		"Mon 2006-01-02 15:04:05 -0700",
		"Mon 2006-01-02 15:04:05 -07",
		"Mon 2006-01-02 15:04:05 -07:00",
		"2006-01-02 15:04:05 MST",
		"2006-01-02 15:04:05 -0700",
		"2006-01-02 15:04:05 -07:00",
	}
	for _, layout := range layouts {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

func parseJournalJSON(output []byte) []JournalEntry {
	content := strings.TrimSpace(string(output))
	if content == "" {
		return []JournalEntry{}
	}

	lines := strings.Split(content, "\n")
	entries := make([]JournalEntry, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}
		message := jsonString(raw["MESSAGE"])
		if message == "" {
			continue
		}
		if len(message) > 1000 {
			message = message[:1000] + "…"
		}
		micros, _ := strconv.ParseInt(jsonString(raw["__REALTIME_TIMESTAMP"]), 10, 64)
		priority, _ := strconv.Atoi(jsonString(raw["PRIORITY"]))
		entry := JournalEntry{Priority: priority, Message: message}
		if micros > 0 {
			entry.Timestamp = time.UnixMicro(micros)
		}
		entries = append(entries, entry)
	}

	for left, right := 0, len(entries)-1; left < right; left, right = left+1, right-1 {
		entries[left], entries[right] = entries[right], entries[left]
	}
	return entries
}

func jsonString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err == nil {
		return value
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err == nil {
		return number.String()
	}
	return ""
}

func cleanCommandError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.TrimSpace(err.Error())
	const maxLength = 500
	if len(message) > maxLength {
		message = message[:maxLength] + "…"
	}
	return message
}

func cloneSystemdSnapshot(source SystemdSnapshot) SystemdSnapshot {
	result := source
	result.Services = append([]SystemdService(nil), source.Services...)
	return result
}
