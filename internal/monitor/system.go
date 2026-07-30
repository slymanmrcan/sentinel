package monitor

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/shirou/gopsutil/v3/host"
	gnet "github.com/shirou/gopsutil/v3/net"
	"github.com/shirou/gopsutil/v3/process"
)

type SystemDetails struct {
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

func (c *Collector) SystemDetails() SystemDetails {
	kernel := "Unknown"
	if stat, err := host.Info(); err == nil {
		kernel = stat.KernelVersion
	}
	return SystemDetails{
		KernelVersion:  kernel,
		RebootRequired: c.rebootRequired(),
		Processes:      processes(),
		ListeningPorts: listeningPorts(),
	}
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

func (c *Collector) cpuTemperature() float64 {
	bases := []string{"/sys/class/thermal", "/host/sys/class/thermal"}
	if c.cfg.HostSys != "" {
		bases = append([]string{c.cfg.HostSys + "/class/thermal"}, bases...)
	}
	for _, base := range bases {
		entries, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !strings.HasPrefix(entry.Name(), "thermal_zone") {
				continue
			}
			data, err := os.ReadFile(fmt.Sprintf("%s/%s/temp", base, entry.Name()))
			if err != nil {
				continue
			}
			var milliDegrees float64
			if _, err := fmt.Sscanf(strings.TrimSpace(string(data)), "%f", &milliDegrees); err == nil {
				value := milliDegrees / 1000
				if value > 0 && value < 120 {
					return value
				}
			}
		}
	}
	if temperatures, err := host.SensorsTemperatures(); err == nil {
		for _, temperature := range temperatures {
			key := strings.ToLower(temperature.SensorKey)
			if strings.Contains(key, "cpu") || strings.Contains(key, "core") {
				return temperature.Temperature
			}
		}
	}
	return 0
}

func (c *Collector) rebootRequired() bool {
	_, err := os.Stat(c.cfg.HostRoot + "/var/run/reboot-required")
	return err == nil
}

func processes() []ProcessInfo {
	list, err := process.Processes()
	if err != nil {
		return []ProcessInfo{}
	}
	result := make([]ProcessInfo, 0, len(list))
	for _, item := range list {
		name, err := item.Name()
		if err != nil {
			continue
		}
		memory, _ := item.MemoryPercent()
		cpuPercent, _ := item.CPUPercent()
		command, _ := item.Cmdline()
		if len(command) > 120 {
			command = command[:120] + "…"
		}
		result = append(result, ProcessInfo{
			PID: item.Pid, Name: name, CPU: cpuPercent,
			Memory: float64(memory), Command: command,
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Memory > result[j].Memory })
	if len(result) > 15 {
		result = result[:15]
	}
	return result
}

func listeningPorts() []PortInfo {
	connections, err := gnet.Connections("tcp")
	if err != nil {
		return []PortInfo{}
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
	return result
}

func randomID() string {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(buffer)
}
