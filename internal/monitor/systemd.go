package monitor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const systemCommandOutputLimit = 1 << 20

type SystemdSnapshot struct {
	Enabled   bool             `json:"enabled"`
	Available bool             `json:"available"`
	CheckedAt time.Time        `json:"checked_at"`
	Message   string           `json:"message,omitempty"`
	Services  []SystemdService `json:"services"`
}

type SystemdService struct {
	Unit          string         `json:"unit"`
	Description   string         `json:"description,omitempty"`
	LoadState     string         `json:"load_state"`
	ActiveState   string         `json:"active_state"`
	SubState      string         `json:"sub_state"`
	UnitFileState string         `json:"unit_file_state,omitempty"`
	RestartPolicy string         `json:"restart_policy,omitempty"`
	Result        string         `json:"result,omitempty"`
	Restarts      uint64         `json:"restarts"`
	ActiveSince   *time.Time     `json:"active_since,omitempty"`
	JournalReady  bool           `json:"journal_available"`
	Logs          []JournalEntry `json:"logs"`
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
	hostRoot string
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
	if r.hostRoot != "" {
		busPath := filepath.Join(r.hostRoot, "run/dbus/system_bus_socket")
		if _, err := os.Stat(busPath); err == nil {
			command.Env = append(command.Env, "DBUS_SYSTEM_BUS_ADDRESS=unix:path="+busPath)
		}
	}
	output := &cappedBuffer{limit: systemCommandOutputLimit}
	command.Stdout = output
	command.Stderr = &cappedBuffer{limit: 64 << 10}
	err := command.Run()
	return output.buffer.Bytes(), err
}

type SystemdMonitor struct {
	units    []string
	logLines int
	hostRoot string
	runner   systemCommandRunner
}

func NewSystemdMonitor(units []string, logLines int, hostRoot string) *SystemdMonitor {
	if logLines <= 0 {
		logLines = 8
	}
	return &SystemdMonitor{
		units: append([]string(nil), units...), logLines: logLines, hostRoot: hostRoot,
		runner: execSystemCommandRunner{hostRoot: hostRoot},
	}
}

func (c *Collector) SystemServices(ctx context.Context) SystemdSnapshot {
	return c.systemd.Snapshot(ctx)
}

func (m *SystemdMonitor) Snapshot(ctx context.Context) SystemdSnapshot {
	checkedAt := time.Now()
	if len(m.units) == 0 {
		return SystemdSnapshot{
			Enabled: false, CheckedAt: checkedAt, Services: []SystemdService{},
			Message: "No systemd services are configured",
		}
	}

	commandCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	args := []string{
		"show", "--no-pager",
		"--property=Id,Description,LoadState,ActiveState,SubState,UnitFileState,Restart,Result,NRestarts,ActiveEnterTimestamp",
		"--",
	}
	args = append(args, m.units...)
	output, err := m.runner.Run(commandCtx, "systemctl", args...)
	if err != nil && !bytes.Contains(output, []byte("LoadState=")) {
		message := "systemctl is unavailable or the system bus cannot be reached"
		if errors.Is(commandCtx.Err(), context.DeadlineExceeded) {
			message = "systemd status check timed out"
		}
		return SystemdSnapshot{
			Enabled: true, CheckedAt: checkedAt, Services: []SystemdService{}, Message: message,
		}
	}

	services := parseSystemctlShow(output, m.units)
	for index := range services {
		services[index].Logs, services[index].JournalReady = m.journal(commandCtx, services[index].Unit)
	}
	return SystemdSnapshot{
		Enabled: true, Available: true, CheckedAt: checkedAt, Services: services,
	}
}

func (m *SystemdMonitor) journal(ctx context.Context, unit string) ([]JournalEntry, bool) {
	args := []string{
		"--no-pager", "--quiet", "--output=json",
		"--output-fields=__REALTIME_TIMESTAMP,PRIORITY,MESSAGE",
		"--lines=" + strconv.Itoa(m.logLines), "--unit=" + unit,
	}
	if m.hostRoot != "" {
		args = append(args, "--root="+m.hostRoot)
	}
	output, err := m.runner.Run(ctx, "journalctl", args...)
	if err != nil {
		return []JournalEntry{}, false
	}
	return parseJournalJSON(output), true
}

func parseSystemctlShow(output []byte, requested []string) []SystemdService {
	byUnit := make(map[string]SystemdService, len(requested))
	for _, block := range strings.Split(strings.TrimSpace(string(output)), "\n\n") {
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
			Unit: unit, Description: properties["Description"], LoadState: properties["LoadState"],
			ActiveState: properties["ActiveState"], SubState: properties["SubState"],
			UnitFileState: properties["UnitFileState"], RestartPolicy: properties["Restart"],
			Result: properties["Result"], Logs: []JournalEntry{},
		}
		service.Restarts, _ = strconv.ParseUint(properties["NRestarts"], 10, 64)
		if activeSince, err := time.Parse("Mon 2006-01-02 15:04:05 MST", properties["ActiveEnterTimestamp"]); err == nil {
			service.ActiveSince = &activeSince
		}
		byUnit[unit] = service
	}

	services := make([]SystemdService, 0, len(requested))
	for _, unit := range requested {
		service, found := byUnit[unit]
		if !found {
			service = SystemdService{
				Unit: unit, LoadState: "not-found", ActiveState: "unknown", SubState: "unknown",
				Logs: []JournalEntry{},
			}
		}
		services = append(services, service)
	}
	return services
}

func parseJournalJSON(output []byte) []JournalEntry {
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
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
		entries = append(entries, JournalEntry{
			Timestamp: time.UnixMicro(micros), Priority: priority, Message: message,
		})
	}
	for left, right := 0, len(entries)-1; left < right; left, right = left+1, right-1 {
		entries[left], entries[right] = entries[right], entries[left]
	}
	return entries
}

func jsonString(raw json.RawMessage) string {
	var value string
	if err := json.Unmarshal(raw, &value); err == nil {
		return value
	}
	return ""
}
