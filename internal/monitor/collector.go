package monitor

import (
	"context"
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v3/load"
	"github.com/shirou/gopsutil/v3/mem"
	gnet "github.com/shirou/gopsutil/v3/net"
	"github.com/slymanmrcan/sentinel/internal/config"
	"github.com/slymanmrcan/sentinel/internal/store"
)

type Collector struct {
	store   *store.Store
	cfg     config.Config
	current store.Metric
	mu      sync.RWMutex

	throughputMu sync.Mutex
	lastSample   ioSample
	processMu    sync.Mutex
	processes    map[int32]processSample
	containerMu  sync.RWMutex
	containers   ContainerSnapshot
	containerSrc containerSource
	systemd      *SystemdMonitor
	intervalMu   sync.RWMutex
	interval     time.Duration
	intervalSet  chan struct{}
	lastAnomaly  map[string]time.Time
	lastAlert    map[string]time.Time
}

type ioSample struct {
	at           time.Time
	netRx, netTx uint64
	diskRead     uint64
	diskWrite    uint64
}

func New(dataStore *store.Store, cfg config.Config) *Collector {
	containerSrc, containerEnabled := newContainerSource(cfg)
	interval := cfg.ContainerInterval
	if interval == 0 {
		interval = 30 * time.Second
	}
	return &Collector{
		store:        dataStore,
		cfg:          cfg,
		processes:    make(map[int32]processSample),
		containerSrc: containerSrc,
		systemd:      NewSystemdMonitor(cfg.SystemdUnits, cfg.SystemdLogLines, cfg.HostRoot),
		interval:     interval,
		intervalSet:  make(chan struct{}, 1),
		containers: ContainerSnapshot{
			Enabled: containerEnabled, Message: "Container metrics are disabled",
			IntervalSec: int(interval / time.Second), Containers: []ContainerMetric{},
		},
		lastAnomaly: make(map[string]time.Time),
		lastAlert:   make(map[string]time.Time),
	}
}

func (c *Collector) Start(ctx context.Context) {
	c.collect(ctx)
	c.collectContainers(ctx)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	containerTimer := time.NewTimer(c.ContainerInterval())
	defer containerTimer.Stop()
	pruneTicker := time.NewTicker(time.Hour)
	defer pruneTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.collect(ctx)
		case <-containerTimer.C:
			c.collectContainers(ctx)
			containerTimer.Reset(c.ContainerInterval())
		case <-c.intervalSet:
			if !containerTimer.Stop() {
				select {
				case <-containerTimer.C:
				default:
				}
			}
			containerTimer.Reset(c.ContainerInterval())
		case <-pruneTicker.C:
			if err := c.store.Prune(ctx); err != nil {
				log.Printf("telemetry prune failed: %v", err)
			}
		}
	}
}

func (c *Collector) Containers() ContainerSnapshot {
	c.containerMu.RLock()
	defer c.containerMu.RUnlock()
	snapshot := c.containers
	snapshot.IntervalSec = int(c.ContainerInterval() / time.Second)
	snapshot.Containers = make([]ContainerMetric, len(c.containers.Containers))
	copy(snapshot.Containers, c.containers.Containers)
	return snapshot
}

func (c *Collector) collectContainers(ctx context.Context) {
	if c.containerSrc == nil {
		return
	}
	collectCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	containers, err := c.containerSrc.Collect(collectCtx)
	collectedAt := time.Now()
	snapshot := ContainerSnapshot{
		Enabled: true, Available: err == nil, CollectedAt: &collectedAt,
		IntervalSec: int(c.ContainerInterval() / time.Second), Containers: containers,
	}
	if containers == nil {
		snapshot.Containers = []ContainerMetric{}
	}
	if err != nil {
		snapshot.Message = "Docker metrics are currently unavailable"
		log.Printf("container metric collection failed: %v", err)
	}
	c.containerMu.Lock()
	c.containers = snapshot
	c.containerMu.Unlock()
}

func (c *Collector) LoadSettings(ctx context.Context) error {
	raw, found, err := c.store.Setting(ctx, containerIntervalSettingKey)
	if err != nil || !found {
		return err
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || !validContainerInterval(seconds) {
		return fmt.Errorf("load saved container interval %q: %w", raw, ErrInvalidContainerInterval)
	}
	c.setContainerInterval(time.Duration(seconds) * time.Second)
	return nil
}

func (c *Collector) SetContainerInterval(ctx context.Context, seconds int) error {
	if !validContainerInterval(seconds) {
		return ErrInvalidContainerInterval
	}
	if err := c.store.SetSetting(ctx, containerIntervalSettingKey, strconv.Itoa(seconds)); err != nil {
		return err
	}
	c.setContainerInterval(time.Duration(seconds) * time.Second)
	return nil
}

func (c *Collector) ContainerInterval() time.Duration {
	c.intervalMu.RLock()
	defer c.intervalMu.RUnlock()
	return c.interval
}

func (c *Collector) setContainerInterval(interval time.Duration) {
	c.intervalMu.Lock()
	c.interval = interval
	c.intervalMu.Unlock()
	select {
	case c.intervalSet <- struct{}{}:
	default:
	}
}

func (c *Collector) Current() store.Metric {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.current
}

func (c *Collector) collect(ctx context.Context) {
	metric := c.snapshot()
	c.mu.Lock()
	c.current = metric
	c.mu.Unlock()

	if hasUnavailable(metric.Unavailable, "cpu", "memory", "disk", "swap", "load") {
		log.Printf("metric history sample skipped because a baseline field is unavailable: %s", strings.Join(metric.Unavailable, ","))
		return
	}
	if err := c.store.InsertMetric(ctx, metric); err != nil {
		log.Printf("metric insert failed: %v", err)
		return
	}
	c.evaluateAnomalies(ctx, metric)
	c.evaluateAlerts(ctx, metric)
}

func (c *Collector) snapshot() store.Metric {
	unavailable := make([]string, 0)
	cpuValues, cpuErr := cpu.Percent(0, false)
	cpuPercent := firstFloat(cpuValues)
	if cpuErr != nil || len(cpuValues) == 0 {
		unavailable = append(unavailable, "cpu")
	}
	cpuCores, _ := cpu.Counts(true)
	cpuModel := "Unknown CPU"
	if info, err := cpu.Info(); err == nil && len(info) > 0 {
		cpuModel = info[0].ModelName
	}

	var ramPercent float64
	var ramUsed, ramTotal uint64
	if stat, err := mem.VirtualMemory(); err == nil && stat != nil {
		ramPercent, ramUsed, ramTotal = stat.UsedPercent, stat.Used, stat.Total
	} else {
		unavailable = append(unavailable, "memory")
	}

	var swapPercent float64
	var swapUsed, swapTotal uint64
	if stat, err := mem.SwapMemory(); err == nil && stat != nil {
		swapPercent, swapUsed, swapTotal = stat.UsedPercent, stat.Used, stat.Total
	} else {
		unavailable = append(unavailable, "swap")
	}

	diskPath := c.cfg.HostRoot
	if diskPath == "" {
		diskPath = "/"
	}
	var diskPercent float64
	var diskUsed, diskTotal uint64
	if stat, err := disk.Usage(diskPath); err == nil && stat != nil {
		diskPercent, diskUsed, diskTotal = stat.UsedPercent, stat.Used, stat.Total
	} else {
		unavailable = append(unavailable, "disk")
	}

	hostName, osName := "sentinel", "Unknown OS"
	var uptime, processes uint64
	if stat, err := host.Info(); err == nil && stat != nil {
		hostName = stat.Hostname
		osName = strings.TrimSpace(stat.OS + " " + stat.Platform)
		uptime, processes = stat.Uptime, stat.Procs
	} else {
		unavailable = append(unavailable, "host")
	}
	osName = c.hostOS(osName)

	var load1, load5, load15 float64
	if stat, err := load.Avg(); err == nil && stat != nil {
		load1, load5, load15 = stat.Load1, stat.Load5, stat.Load15
	} else {
		unavailable = append(unavailable, "load")
	}
	netRxBps, netTxBps, netRxTotal, netTxTotal, diskReadBps, diskWriteBps, throughputUnavailable := c.sampleThroughput()
	unavailable = append(unavailable, throughputUnavailable...)
	cpuTemp, tempAvailable := c.cpuTemperature()
	if !tempAvailable {
		unavailable = append(unavailable, "cpu_temp")
	}

	return store.Metric{
		Timestamp:    time.Now(),
		CPUPercent:   cpuPercent,
		CPUCores:     cpuCores,
		CPUModel:     cpuModel,
		CPUTemp:      cpuTemp,
		Load1:        load1,
		Load5:        load5,
		Load15:       load15,
		RAMPercent:   ramPercent,
		RAMUsed:      ramUsed,
		RAMTotal:     ramTotal,
		SwapPercent:  swapPercent,
		SwapUsed:     swapUsed,
		SwapTotal:    swapTotal,
		DiskPercent:  diskPercent,
		DiskUsed:     diskUsed,
		DiskTotal:    diskTotal,
		NetRxBps:     netRxBps,
		NetTxBps:     netTxBps,
		NetRxTotal:   netRxTotal,
		NetTxTotal:   netTxTotal,
		DiskReadBps:  diskReadBps,
		DiskWriteBps: diskWriteBps,
		OS:           osName,
		HostName:     hostName,
		Uptime:       uptime,
		Processes:    processes,
		Unavailable:  unavailable,
	}
}

func firstFloat(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	return values[0]
}

func (c *Collector) sampleThroughput() (float64, float64, uint64, uint64, float64, float64, []string) {
	var netRx, netTx, diskRead, diskWrite uint64
	unavailable := make([]string, 0, 2)
	if counters, err := gnet.IOCounters(len(c.cfg.NetworkInterfaces) > 0); err == nil && len(counters) > 0 {
		var found bool
		netRx, netTx, found = selectedNetworkCounters(counters, c.cfg.NetworkInterfaces)
		if !found {
			unavailable = append(unavailable, "network")
		}
	} else {
		unavailable = append(unavailable, "network")
	}
	if counters, err := disk.IOCounters(); err == nil && len(counters) > 0 {
		for _, counter := range counters {
			diskRead += counter.ReadBytes
			diskWrite += counter.WriteBytes
		}
	} else {
		unavailable = append(unavailable, "disk_io")
	}

	c.throughputMu.Lock()
	defer c.throughputMu.Unlock()
	now := time.Now()
	current := ioSample{at: now, netRx: netRx, netTx: netTx, diskRead: diskRead, diskWrite: diskWrite}
	previous := c.lastSample
	c.lastSample = current
	if previous.at.IsZero() {
		return 0, 0, netRx, netTx, 0, 0, unavailable
	}
	elapsed := now.Sub(previous.at).Seconds()
	if elapsed <= 0 {
		return 0, 0, netRx, netTx, 0, 0, unavailable
	}
	return rate(netRx, previous.netRx, elapsed),
		rate(netTx, previous.netTx, elapsed),
		netRx,
		netTx,
		rate(diskRead, previous.diskRead, elapsed),
		rate(diskWrite, previous.diskWrite, elapsed),
		unavailable
}

func selectedNetworkCounters(counters []gnet.IOCountersStat, selected []string) (uint64, uint64, bool) {
	if len(selected) == 0 {
		if len(counters) == 0 {
			return 0, 0, false
		}
		return counters[0].BytesRecv, counters[0].BytesSent, true
	}
	wanted := make(map[string]bool, len(selected))
	for _, name := range selected {
		wanted[name] = true
	}
	var received, sent uint64
	found := false
	for _, counter := range counters {
		if wanted[counter.Name] {
			found = true
			received += counter.BytesRecv
			sent += counter.BytesSent
		}
	}
	return received, sent, found
}

func hasUnavailable(unavailable []string, names ...string) bool {
	lookup := make(map[string]bool, len(unavailable))
	for _, name := range unavailable {
		lookup[name] = true
	}
	for _, name := range names {
		if lookup[name] {
			return true
		}
	}
	return false
}

func rate(current, previous uint64, seconds float64) float64 {
	if current < previous || seconds <= 0 {
		return 0
	}
	return float64(current-previous) / seconds
}

func (c *Collector) evaluateAnomalies(ctx context.Context, metric store.Metric) {
	values := map[string]float64{
		"cpu": metric.CPUPercent, "memory": metric.RAMPercent,
		"disk": metric.DiskPercent, "swap": metric.SwapPercent,
		"load": metric.Load1,
	}
	for name, value := range values {
		baseline, err := c.store.Baseline(ctx, name)
		if err != nil || baseline.Count < 12 || baseline.StdDev < 0.01 {
			continue
		}
		zScore := (value - baseline.Mean) / baseline.StdDev
		if math.Abs(zScore) < 3 || time.Since(c.lastAnomaly[name]) < 10*time.Minute {
			continue
		}
		severity := "warning"
		if math.Abs(zScore) >= 5 {
			severity = "critical"
		}
		anomaly := store.Anomaly{
			ID:           randomID(),
			Timestamp:    metric.Timestamp,
			Metric:       name,
			Severity:     severity,
			Value:        value,
			BaselineMean: baseline.Mean,
			ZScore:       zScore,
			Message:      fmt.Sprintf("%s value %.2f is unusual for the current baseline", metricLabel(name), value),
		}
		if err := c.store.InsertAnomaly(ctx, anomaly); err == nil {
			c.lastAnomaly[name] = time.Now()
			c.log(ctx, "WARN", anomaly.Message, "anomaly")
		}
	}
}

func metricLabel(metric string) string {
	switch metric {
	case "cpu":
		return "CPU"
	case "memory":
		return "Memory"
	case "disk":
		return "Disk"
	case "swap":
		return "Swap"
	case "load":
		return "Load"
	default:
		return metric
	}
}

func (c *Collector) evaluateAlerts(ctx context.Context, metric store.Metric) {
	rules, err := c.store.AlertRules(ctx)
	if err != nil {
		return
	}
	values := map[string]float64{
		"cpu": metric.CPUPercent, "memory": metric.RAMPercent,
		"disk": metric.DiskPercent, "swap": metric.SwapPercent,
	}
	for _, rule := range rules {
		value := values[rule.Metric]
		if !rule.Enabled || value < rule.Threshold || time.Since(c.lastAlert[rule.ID]) < 10*time.Minute {
			continue
		}
		event := store.AlertEvent{
			ID:        randomID(),
			Timestamp: metric.Timestamp,
			RuleID:    rule.ID,
			RuleName:  rule.Name,
			Metric:    rule.Metric,
			Value:     value,
			Severity:  rule.Severity,
			Message:   fmt.Sprintf("%s: %.1f%% exceeds %.1f%%", rule.Name, value, rule.Threshold),
		}
		if err := c.store.InsertAlertEvent(ctx, event); err == nil {
			c.lastAlert[rule.ID] = time.Now()
			c.log(ctx, "WARN", event.Message, "alerts")
		}
	}
}

func (c *Collector) log(ctx context.Context, level, message, source string) {
	log.Printf("[%s] [%s] %s", level, source, message)
	_ = c.store.InsertLog(ctx, store.LogEntry{
		Timestamp: time.Now(), Level: level, Message: message, Source: source,
	})
}
