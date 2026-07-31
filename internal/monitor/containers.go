package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/slymanmrcan/sentinel/internal/config"
)

const maxContainerResponseBytes = 8 << 20

type ContainerSnapshot struct {
	Enabled     bool              `json:"enabled"`
	Available   bool              `json:"available"`
	Message     string            `json:"message,omitempty"`
	CollectedAt *time.Time        `json:"collected_at,omitempty"`
	Containers  []ContainerMetric `json:"containers"`
}

type ContainerMetric struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	Image         string  `json:"image"`
	State         string  `json:"state"`
	Status        string  `json:"status"`
	CPUPercent    float64 `json:"cpu_percent"`
	MemoryUsed    uint64  `json:"memory_used"`
	MemoryLimit   uint64  `json:"memory_limit"`
	MemoryPercent float64 `json:"memory_percent"`
	NetRxBytes    uint64  `json:"net_rx_bytes"`
	NetTxBytes    uint64  `json:"net_tx_bytes"`
	PIDs          uint64  `json:"pids"`
}

type containerSource interface {
	Collect(context.Context) ([]ContainerMetric, error)
}

type dockerContainerSource struct {
	baseURL string
	client  *http.Client
}

type dockerContainer struct {
	ID     string   `json:"Id"`
	Names  []string `json:"Names"`
	Image  string   `json:"Image"`
	State  string   `json:"State"`
	Status string   `json:"Status"`
}

type dockerStats struct {
	CPUStats    dockerCPUStats    `json:"cpu_stats"`
	PreCPUStats dockerCPUStats    `json:"precpu_stats"`
	MemoryStats dockerMemoryStats `json:"memory_stats"`
	Networks    map[string]struct {
		RxBytes uint64 `json:"rx_bytes"`
		TxBytes uint64 `json:"tx_bytes"`
	} `json:"networks"`
	PIDsStats struct {
		Current uint64 `json:"current"`
	} `json:"pids_stats"`
}

type dockerCPUStats struct {
	CPUUsage struct {
		TotalUsage  uint64   `json:"total_usage"`
		PercpuUsage []uint64 `json:"percpu_usage"`
	} `json:"cpu_usage"`
	SystemCPUUsage uint64 `json:"system_cpu_usage"`
	OnlineCPUs     uint64 `json:"online_cpus"`
}

type dockerMemoryStats struct {
	Usage uint64            `json:"usage"`
	Limit uint64            `json:"limit"`
	Stats map[string]uint64 `json:"stats"`
}

func newContainerSource(cfg config.Config) (containerSource, bool) {
	if !cfg.ContainerMetrics {
		return nil, false
	}
	if cfg.ContainerAPIURL != "" {
		return &dockerContainerSource{
			baseURL: cfg.ContainerAPIURL,
			client:  &http.Client{Timeout: 8 * time.Second},
		}, true
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", cfg.DockerSocket)
		},
	}
	return &dockerContainerSource{
		baseURL: "http://docker",
		client:  &http.Client{Transport: transport, Timeout: 8 * time.Second},
	}, true
}

func (s *dockerContainerSource) Collect(ctx context.Context) ([]ContainerMetric, error) {
	var containers []dockerContainer
	if err := s.getJSON(ctx, "/containers/json?all=false", &containers); err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	if len(containers) > 64 {
		containers = containers[:64]
	}
	result := make([]ContainerMetric, len(containers))
	errorsByIndex := make([]error, len(containers))
	semaphore := make(chan struct{}, 8)
	var waitGroup sync.WaitGroup
	for index, container := range containers {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()
			var stats dockerStats
			path := "/containers/" + url.PathEscape(container.ID) + "/stats?stream=false"
			if err := s.getJSON(ctx, path, &stats); err != nil {
				errorsByIndex[index] = fmt.Errorf("stats for container %s: %w", shortContainerID(container.ID), err)
				return
			}
			result[index] = containerMetricFromDocker(container, stats)
		}()
	}
	waitGroup.Wait()
	if err := errors.Join(errorsByIndex...); err != nil {
		return nil, err
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].CPUPercent == result[j].CPUPercent {
			return result[i].Name < result[j].Name
		}
		return result[i].CPUPercent > result[j].CPUPercent
	})
	return result, nil
}

func (s *dockerContainerSource) getJSON(ctx context.Context, path string, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL+path, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	response, err := s.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return fmt.Errorf("Docker API returned %s", response.Status)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxContainerResponseBytes))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("Docker API returned multiple JSON values")
	}
	return nil
}

func containerMetricFromDocker(container dockerContainer, stats dockerStats) ContainerMetric {
	name := shortContainerID(container.ID)
	if len(container.Names) > 0 && strings.TrimPrefix(container.Names[0], "/") != "" {
		name = strings.TrimPrefix(container.Names[0], "/")
	}
	used := dockerMemoryUsage(stats.MemoryStats)
	var memoryPercent float64
	if stats.MemoryStats.Limit > 0 {
		memoryPercent = float64(used) / float64(stats.MemoryStats.Limit) * 100
	}
	var rxBytes, txBytes uint64
	for _, network := range stats.Networks {
		rxBytes += network.RxBytes
		txBytes += network.TxBytes
	}
	return ContainerMetric{
		ID:            shortContainerID(container.ID),
		Name:          name,
		Image:         container.Image,
		State:         container.State,
		Status:        container.Status,
		CPUPercent:    dockerCPUPercent(stats.CPUStats, stats.PreCPUStats),
		MemoryUsed:    used,
		MemoryLimit:   stats.MemoryStats.Limit,
		MemoryPercent: memoryPercent,
		NetRxBytes:    rxBytes,
		NetTxBytes:    txBytes,
		PIDs:          stats.PIDsStats.Current,
	}
}

func dockerCPUPercent(current, previous dockerCPUStats) float64 {
	if current.CPUUsage.TotalUsage < previous.CPUUsage.TotalUsage || current.SystemCPUUsage < previous.SystemCPUUsage {
		return 0
	}
	cpuDelta := current.CPUUsage.TotalUsage - previous.CPUUsage.TotalUsage
	systemDelta := current.SystemCPUUsage - previous.SystemCPUUsage
	if cpuDelta == 0 || systemDelta == 0 {
		return 0
	}
	cores := current.OnlineCPUs
	if cores == 0 {
		cores = uint64(len(current.CPUUsage.PercpuUsage))
	}
	if cores == 0 {
		cores = 1
	}
	return float64(cpuDelta) / float64(systemDelta) * float64(cores) * 100
}

func dockerMemoryUsage(memory dockerMemoryStats) uint64 {
	cache := memory.Stats["inactive_file"]
	if cache == 0 {
		cache = memory.Stats["total_inactive_file"]
	}
	if cache <= memory.Usage {
		return memory.Usage - cache
	}
	return memory.Usage
}

func shortContainerID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
