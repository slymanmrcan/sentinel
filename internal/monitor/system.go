package monitor

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v3/host"
	gnet "github.com/shirou/gopsutil/v3/net"
	"github.com/shirou/gopsutil/v3/process"
)

type SystemDetails struct {
	CheckedAt      time.Time     `json:"checked_at"`
	Unavailable    []string      `json:"unavailable,omitempty"`
	KernelVersion  string        `json:"kernel_version"`
	RebootRequired bool          `json:"reboot_required"`
	Processes      []ProcessInfo `json:"processes"`
	ListeningPorts []PortInfo    `json:"listening_ports"`
}

type ProcessInfo struct {
	PID     int32   `json:"pid"`
	Name    string  `json:"name"`
	CPU     float64 `json:"cpu"`
	Memory  float64 `json:"memory"`
	Command string  `json:"command"`
}

type PortInfo struct {
	Port uint32 `json:"port"`
	PID  int32  `json:"pid"`
	Name string `json:"name"`
}

type processSample struct {
	at       time.Time
	cpuTotal float64
}

func (c *Collector) SystemDetails() SystemDetails {
	c.detailsMu.Lock()
	defer c.detailsMu.Unlock()
	if !c.details.CheckedAt.IsZero() && time.Since(c.details.CheckedAt) < time.Minute {
		return cloneSystemDetails(c.details)
	}
	checkedAt := time.Now()
	kernel := "Unknown"
	if stat, err := host.Info(); err == nil {
		kernel = stat.KernelVersion
	}
	ports, portErr := c.listeningPorts()
	processes, processErr := c.processesNow()
	c.details = SystemDetails{
		CheckedAt:      checkedAt,
		KernelVersion:  kernel,
		RebootRequired: c.rebootRequired(),
		Processes:      processes,
		ListeningPorts: ports,
	}
	if portErr != nil {
		c.details.Unavailable = append(c.details.Unavailable, "listening_ports")
	}
	if processErr != nil {
		c.details.Unavailable = append(c.details.Unavailable, "processes")
	}
	return cloneSystemDetails(c.details)
}

func cloneSystemDetails(details SystemDetails) SystemDetails {
	details.Processes = append([]ProcessInfo{}, details.Processes...)
	details.ListeningPorts = append([]PortInfo{}, details.ListeningPorts...)
	details.Unavailable = append([]string(nil), details.Unavailable...)
	return details
}

func (c *Collector) hostOS(fallback string) string {
	if c.cfg.HostRoot == "" {
		return fallback
	}
	for _, path := range []string{
		c.cfg.HostRoot + "/etc/os-release",
		c.cfg.HostRoot + "/usr/lib/os-release",
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, key := range []string{"PRETTY_NAME=", "NAME="} {
			for _, line := range strings.Split(string(data), "\n") {
				if strings.HasPrefix(line, key) {
					return strings.Trim(strings.TrimPrefix(line, key), `"'`)
				}
			}
		}
	}
	return fallback
}

func (c *Collector) cpuTemperature() (float64, bool) {
	bases := []string{"/sys/class/thermal", "/host/sys/class/thermal"}
	if c.cfg.HostSys != "" {
		bases = append([]string{c.cfg.HostSys + "/class/thermal"}, bases...)
	}
	for _, base := range bases {
		if value, ok := temperatureFromThermalRoot(base); ok {
			return value, true
		}
	}
	if temperatures, err := host.SensorsTemperatures(); err == nil {
		for _, temperature := range temperatures {
			key := strings.ToLower(temperature.SensorKey)
			if strings.Contains(key, "cpu") || strings.Contains(key, "core") {
				if temperature.Temperature > 0 && temperature.Temperature < 120 {
					return temperature.Temperature, true
				}
			}
		}
	}
	return 0, false
}

func temperatureFromThermalRoot(root string) (float64, bool) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0, false
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "thermal_zone") {
			continue
		}
		zone := fmt.Sprintf("%s/%s", root, entry.Name())
		typeData, err := os.ReadFile(zone + "/type")
		if err != nil || !isCPUThermalType(string(typeData)) {
			continue
		}
		tempData, err := os.ReadFile(zone + "/temp")
		if err != nil {
			continue
		}
		var milliDegrees float64
		if _, err := fmt.Sscanf(strings.TrimSpace(string(tempData)), "%f", &milliDegrees); err != nil {
			continue
		}
		value := milliDegrees / 1000
		if value > 0 && value < 120 {
			return value, true
		}
	}
	return 0, false
}

func isCPUThermalType(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	for _, token := range []string{"cpu", "core", "package", "x86_pkg", "k10temp", "tctl", "soc_thermal"} {
		if strings.Contains(value, token) {
			return true
		}
	}
	return false
}

func (c *Collector) rebootRequired() bool {
	_, err := os.Stat(c.cfg.HostRoot + "/var/run/reboot-required")
	return err == nil
}

func (c *Collector) processesNow() ([]ProcessInfo, error) {
	c.processMu.Lock()
	defer c.processMu.Unlock()

	list, err := process.Processes()
	if err != nil {
		return []ProcessInfo{}, err
	}
	now := time.Now()
	current := make(map[int32]processSample, len(list))
	result := make([]ProcessInfo, 0, len(list))
	for _, item := range list {
		name, err := item.Name()
		if err != nil {
			continue
		}
		memory, _ := item.MemoryPercent()
		cpuTimes, timeErr := item.Times()
		cpuPercent := float64(0)
		if timeErr == nil {
			current[item.Pid] = processSample{at: now, cpuTotal: cpuTimes.Total()}
			if previous, ok := c.processes[item.Pid]; ok {
				cpuPercent = processCPUPercent(cpuTimes.Total(), previous.cpuTotal, now.Sub(previous.at).Seconds())
			}
		}
		command, _ := item.Cmdline()
		if len(command) > 120 {
			command = command[:120] + "…"
		}
		result = append(result, ProcessInfo{
			PID: item.Pid, Name: name, CPU: cpuPercent,
			Memory: float64(memory), Command: command,
		})
	}
	c.processes = current
	sort.Slice(result, func(i, j int) bool { return result[i].Memory > result[j].Memory })
	if len(result) > 15 {
		result = result[:15]
	}
	return result, nil
}

func processCPUPercent(current, previous, elapsedSeconds float64) float64 {
	if current < previous || elapsedSeconds <= 0 {
		return 0
	}
	return (current - previous) / elapsedSeconds * 100
}

func (c *Collector) listeningPorts() ([]PortInfo, error) {
	if c.cfg.HostProc != "" {
		return readHostListeningPorts(c.cfg.HostProc)
	}
	connections, err := gnet.Connections("tcp")
	if err != nil {
		return []PortInfo{}, err
	}
	result := make([]PortInfo, 0)
	seen := make(map[uint32]bool)
	for _, connection := range connections {
		if connection.Status != "LISTEN" || seen[connection.Laddr.Port] {
			continue
		}
		seen[connection.Laddr.Port] = true
		name := "Unknown"
		if connection.Pid > 0 {
			if item, err := process.NewProcess(connection.Pid); err == nil {
				if processName, err := item.Name(); err == nil {
					name = processName
				}
			}
		}
		result = append(result, PortInfo{
			Port: connection.Laddr.Port, PID: connection.Pid, Name: name,
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Port < result[j].Port })
	return result, nil
}

func randomID() string {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(buffer)
}
