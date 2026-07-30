package monitor

import (
	"context"
	"fmt"
	"log"
	"math"
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
	return &Collector{
		store:       dataStore,
		cfg:         cfg,
		lastAnomaly: make(map[string]time.Time),
		lastAlert:   make(map[string]time.Time),
	}
}

func (c *Collector) Start(ctx context.Context) {
	c.collect(ctx)
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	pruneTicker := time.NewTicker(time.Hour)
	defer pruneTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.collect(ctx)
		case <-pruneTicker.C:
			if err := c.store.Prune(ctx); err != nil {
				log.Printf("telemetry prune failed: %v", err)
			}
		}
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

	if err := c.store.InsertMetric(ctx, metric); err != nil {
		log.Printf("metric insert failed: %v", err)
		return
	}
	c.evaluateAnomalies(ctx, metric)
	c.evaluateAlerts(ctx, metric)
}

func (c *Collector) snapshot() store.Metric {
	cpuPercent := firstFloat(cpu.Percent(0, false))
	cpuCores, _ := cpu.Counts(true)
	cpuModel := "Unknown CPU"
	if info, err := cpu.Info(); err == nil && len(info) > 0 {
		cpuModel = info[0].ModelName
	}

	var ramPercent float64
	var ramUsed, ramTotal uint64
	if stat, _ := mem.VirtualMemory(); stat != nil {
		ramPercent, ramUsed, ramTotal = stat.UsedPercent, stat.Used, stat.Total
	}

	var swapPercent float64
	var swapUsed, swapTotal uint64
	if stat, _ := mem.SwapMemory(); stat != nil {
		swapPercent, swapUsed, swapTotal = stat.UsedPercent, stat.Used, stat.Total
	}

	diskPath := c.cfg.HostRoot
	if diskPath == "" {
		diskPath = "/"
	}
	var diskPercent float64
	var diskUsed, diskTotal uint64
	if stat, _ := disk.Usage(diskPath); stat != nil {
		diskPercent, diskUsed, diskTotal = stat.UsedPercent, stat.Used, stat.Total
	}

	hostName, osName := "sentinel", "Unknown OS"
	var uptime, processes uint64
	if stat, _ := host.Info(); stat != nil {
		hostName = stat.Hostname
		osName = strings.TrimSpace(stat.OS + " " + stat.Platform)
		uptime, processes = stat.Uptime, stat.Procs
	}
	osName = c.hostOS(osName)

	var load1, load5, load15 float64
	if stat, _ := load.Avg(); stat != nil {
		load1, load5, load15 = stat.Load1, stat.Load5, stat.Load15
	}
	netRxBps, netTxBps, netRxTotal, netTxTotal, diskReadBps, diskWriteBps := c.sampleThroughput()

	return store.Metric{
		Timestamp:    time.Now(),
		CPUPercent:   cpuPercent,
		CPUCores:     cpuCores,
		CPUModel:     cpuModel,
		CPUTemp:      c.cpuTemperature(),
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
	}
}

func firstFloat(values []float64, _ error) float64 {
	if len(values) == 0 {
		return 0
	}
	return values[0]
}

func (c *Collector) sampleThroughput() (float64, float64, uint64, uint64, float64, float64) {
	var netRx, netTx, diskRead, diskWrite uint64
	if counters, err := gnet.IOCounters(false); err == nil && len(counters) > 0 {
		netRx, netTx = counters[0].BytesRecv, counters[0].BytesSent
	}
	if counters, err := disk.IOCounters(); err == nil {
		for _, counter := range counters {
			diskRead += counter.ReadBytes
			diskWrite += counter.WriteBytes
		}
	}

	c.throughputMu.Lock()
	defer c.throughputMu.Unlock()
	now := time.Now()
	current := ioSample{at: now, netRx: netRx, netTx: netTx, diskRead: diskRead, diskWrite: diskWrite}
	previous := c.lastSample
	c.lastSample = current
	if previous.at.IsZero() {
		return 0, 0, netRx, netTx, 0, 0
	}
	elapsed := now.Sub(previous.at).Seconds()
	if elapsed <= 0 {
		return 0, 0, netRx, netTx, 0, 0
	}
	return rate(netRx, previous.netRx, elapsed),
		rate(netTx, previous.netTx, elapsed),
		netRx,
		netTx,
		rate(diskRead, previous.diskRead, elapsed),
		rate(diskWrite, previous.diskWrite, elapsed)
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
