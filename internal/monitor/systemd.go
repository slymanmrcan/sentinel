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

	// UI daha sık çağırsa bile gerçek systemctl/journalctl
	// en fazla 30 saniyede bir çalışır.
	systemdSnapshotTTL = 30 * time.Second

	systemctlTimeout = 5 * time.Second
	journalTimeout   = 3 * time.Second
)

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

func (r execSystemCommandRunner) Run(
	ctx context.Context,
	name string,
	args ...string,
) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)

	command.Env = append(
		os.Environ(),
		"LC_ALL=C",
		"TZ=UTC",
	)

	// Sentinel container içinden host systemd bus'a bağlanacaksa
	// HOST_ROOT=/host altında socket'i arıyoruz.
	if r.hostRoot != "" {
		busPath := filepath.Join(
			r.hostRoot,
			"run/dbus/system_bus_socket",
		)

		if _, err := os.Stat(busPath); err == nil {
			command.Env = append(
				command.Env,
				"DBUS_SYSTEM_BUS_ADDRESS=unix:path="+busPath,
			)
		}
	}

	stdout := &cappedBuffer{
		limit: systemCommandOutputLimit,
	}

	stderr := &cappedBuffer{
		limit: 64 << 10,
	}

	command.Stdout = stdout
	command.Stderr = stderr

	err := command.Run()
	if err != nil {
		message := strings.TrimSpace(stderr.buffer.String())

		if message != "" {
			return stdout.buffer.Bytes(),
				fmt.Errorf("%w: %s", err, message)
		}

		return stdout.buffer.Bytes(), err
	}

	return stdout.buffer.Bytes(), nil
}

type SystemdMonitor struct {
	units    []string
	logLines int
	hostRoot string
	runner   systemCommandRunner

	cacheMu  sync.Mutex
	cached   SystemdSnapshot
	cachedAt time.Time
}

func NewSystemdMonitor(
	units []string,
	logLines int,
	hostRoot string,
) *SystemdMonitor {
	if logLines <= 0 {
		logLines = 8
	}

	return &SystemdMonitor{
		units: append([]string(nil), units...),

		logLines: logLines,
		hostRoot: hostRoot,

		runner: execSystemCommandRunner{
			hostRoot: hostRoot,
		},
	}
}

func (c *Collector) SystemServices(
	ctx context.Context,
) SystemdSnapshot {
	return c.systemd.Snapshot(ctx)
}

func (m *SystemdMonitor) Snapshot(
	ctx context.Context,
) SystemdSnapshot {
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

	/*
		Mutex'i tüm refresh boyunca tutuyoruz.

		Böylece örneğin frontend aynı anda 3 request gönderirse:

			request 1 -> systemctl çalıştırır
			request 2 -> bekler
			request 3 -> bekler

		sonra diğerleri cache'i kullanır.

		3 ayrı systemctl/journalctl zinciri oluşmaz.
	*/
	m.cacheMu.Lock()
	defer m.cacheMu.Unlock()

	if !m.cachedAt.IsZero() &&
		time.Since(m.cachedAt) < systemdSnapshotTTL {

		return cloneSystemdSnapshot(m.cached)
	}

	snapshot := m.collect(ctx)

	m.cached = cloneSystemdSnapshot(snapshot)
	m.cachedAt = time.Now()

	return snapshot
}

func (m *SystemdMonitor) collect(
	ctx context.Context,
) SystemdSnapshot {
	checkedAt := time.Now()

	commandCtx, cancel := context.WithTimeout(
		ctx,
		systemctlTimeout,
	)

	args := []string{
		"show",
		"--no-pager",
		"--property=" +
			"Id," +
			"Description," +
			"LoadState," +
			"ActiveState," +
			"SubState," +
			"UnitFileState," +
			"Restart," +
			"Result," +
			"NRestarts," +
			"ActiveEnterTimestamp",
		"--",
	}

	args = append(args, m.units...)

	output, err := m.runner.Run(
		commandCtx,
		"systemctl",
		args...,
	)

	commandTimedOut := errors.Is(
		commandCtx.Err(),
		context.DeadlineExceeded,
	)

	cancel()

	if err != nil &&
		!bytes.Contains(output, []byte("LoadState=")) {

		message := "systemctl failed: " + cleanCommandError(err)

		if commandTimedOut {
			message = "systemd status check timed out"
		}

		return SystemdSnapshot{
			Enabled:   true,
			Available: false,
			CheckedAt: checkedAt,
			Services:  []SystemdService{},
			Message:   message,
		}
	}

	services := parseSystemctlShow(
		output,
		m.units,
	)

	/*
		Journal command'ları systemctl'in context'ini paylaşmıyor.

		Her servisin kendi timeout'u var.
	*/
	for index := range services {
		logs, available := m.journal(
			ctx,
			services[index].Unit,
		)

		services[index].Logs = logs
		services[index].JournalReady = available
	}

	return SystemdSnapshot{
		Enabled:   true,
		Available: true,
		CheckedAt: checkedAt,
		Services:  services,
	}
}

func (m *SystemdMonitor) journal(
	ctx context.Context,
	unit string,
) ([]JournalEntry, bool) {
	commandCtx, cancel := context.WithTimeout(
		ctx,
		journalTimeout,
	)
	defer cancel()

	args := []string{
		"--no-pager",
		"--quiet",
		"--output=json",
		"--output-fields=__REALTIME_TIMESTAMP,PRIORITY,MESSAGE",
		"--lines=" + strconv.Itoa(m.logLines),
		"--unit=" + unit,
	}

	/*
		HOST_ROOT=/host ise host'un journal database'ini oku.

		Ör:
			/host/var/log/journal
			/host/run/log/journal
	*/
	if m.hostRoot != "" {
		args = append(
			args,
			"--root="+m.hostRoot,
		)
	}

	output, err := m.runner.Run(
		commandCtx,
		"journalctl",
		args...,
	)

	if err != nil {
		return []JournalEntry{}, false
	}

	return parseJournalJSON(output), true
}

func parseSystemctlShow(
	output []byte,
	requested []string,
) []SystemdService {
	byUnit := make(
		map[string]SystemdService,
		len(requested),
	)

	content := strings.TrimSpace(string(output))

	if content != "" {
		for _, block := range strings.Split(
			content,
			"\n\n",
		) {
			properties := make(map[string]string)

			for _, line := range strings.Split(
				block,
				"\n",
			) {
				key, value, found := strings.Cut(
					line,
					"=",
				)

				if found {
					properties[key] = value
				}
			}

			unit := properties["Id"]
			if unit == "" {
				continue
			}

			service := SystemdService{
				Unit: unit,

				Description: properties["Description"],

				LoadState: properties["LoadState"],

				ActiveState: properties["ActiveState"],

				SubState: properties["SubState"],

				UnitFileState: properties["UnitFileState"],

				RestartPolicy: properties["Restart"],

				Result: properties["Result"],

				Logs: []JournalEntry{},
			}

			service.Restarts, _ = strconv.ParseUint(
				properties["NRestarts"],
				10,
				64,
			)

			if activeSince, ok := parseSystemdTime(
				properties["ActiveEnterTimestamp"],
			); ok {
				service.ActiveSince = &activeSince
			}

			byUnit[unit] = service
		}
	}

	services := make(
		[]SystemdService,
		0,
		len(requested),
	)

	for _, unit := range requested {
		service, found := byUnit[unit]

		if !found {
			service = SystemdService{
				Unit: unit,

				LoadState: "not-found",

				ActiveState: "unknown",

				SubState: "unknown",

				Logs: []JournalEntry{},
			}
		}

		services = append(
			services,
			service,
		)
	}

	return services
}

func parseSystemdTime(
	value string,
) (time.Time, bool) {
	value = strings.TrimSpace(value)

	if value == "" ||
		value == "n/a" ||
		value == "0" {

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
		parsed, err := time.Parse(
			layout,
			value,
		)

		if err == nil {
			return parsed, true
		}
	}

	return time.Time{}, false
}

func parseJournalJSON(
	output []byte,
) []JournalEntry {
	content := strings.TrimSpace(
		string(output),
	)

	if content == "" {
		return []JournalEntry{}
	}

	lines := strings.Split(
		content,
		"\n",
	)

	entries := make(
		[]JournalEntry,
		0,
		len(lines),
	)

	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}

		var raw map[string]json.RawMessage

		if err := json.Unmarshal(
			[]byte(line),
			&raw,
		); err != nil {
			continue
		}

		message := jsonString(
			raw["MESSAGE"],
		)

		if message == "" {
			continue
		}

		if len(message) > 1000 {
			message =
				message[:1000] + "…"
		}

		micros, _ := strconv.ParseInt(
			jsonString(
				raw["__REALTIME_TIMESTAMP"],
			),
			10,
			64,
		)

		priority, _ := strconv.Atoi(
			jsonString(
				raw["PRIORITY"],
			),
		)

		entry := JournalEntry{
			Priority: priority,
			Message:  message,
		}

		if micros > 0 {
			entry.Timestamp = time.UnixMicro(micros)
		}

		entries = append(
			entries,
			entry,
		)
	}

	/*
		journalctl en yeniyi sona verebiliyor;
		UI'da newest-first istiyorsak ters çeviriyoruz.
	*/
	for left, right := 0, len(entries)-1; left < right; left, right = left+1, right-1 {

		entries[left],
			entries[right] =
			entries[right],
			entries[left]
	}

	return entries
}

func jsonString(
	raw json.RawMessage,
) string {
	if len(raw) == 0 {
		return ""
	}

	var value string

	if err := json.Unmarshal(
		raw,
		&value,
	); err == nil {
		return value
	}

	/*
		systemd journal bazı alanları JSON number
		olarak döndürebilir.
	*/
	var number json.Number

	if err := json.Unmarshal(
		raw,
		&number,
	); err == nil {
		return number.String()
	}

	return ""
}

func cleanCommandError(
	err error,
) string {
	if err == nil {
		return ""
	}

	message := strings.TrimSpace(
		err.Error(),
	)

	const maxLength = 500

	if len(message) > maxLength {
		message =
			message[:maxLength] + "…"
	}

	return message
}

func cloneSystemdSnapshot(
	source SystemdSnapshot,
) SystemdSnapshot {
	result := source

	result.Services = make(
		[]SystemdService,
		len(source.Services),
	)

	for index := range source.Services {
		result.Services[index] =
			source.Services[index]

		result.Services[index].Logs =
			append(
				[]JournalEntry(nil),
				source.Services[index].Logs...,
			)
	}

	return result
}
