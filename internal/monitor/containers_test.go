package monitor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestDockerContainerSourceCollectsCPUAndMemory(t *testing.T) {
	statsCalls := 0
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body := ""
		switch request.URL.Path {
		case "/containers/json":
			body = `[{"Id":"1234567890abcdef","Names":["/api"],"Image":"example/api:latest","State":"running","Status":"Up 2 hours"}]`
		case "/containers/1234567890abcdef/stats":
			if request.URL.Query().Get("stream") != "false" || request.URL.Query().Get("one-shot") != "true" {
				return nil, fmt.Errorf("unexpected stats query: %s", request.URL.RawQuery)
			}
			statsCalls++
			cpuTotal, systemTotal := 100, 1000
			if statsCalls > 1 {
				cpuTotal, systemTotal = 300, 2000
			}
			body = fmt.Sprintf(`{
				"cpu_stats":{"cpu_usage":{"total_usage":%d},"system_cpu_usage":%d,"online_cpus":2},
				"precpu_stats":{},
				"memory_stats":{"usage":1000,"limit":2000,"stats":{"inactive_file":200}},
				"networks":{"eth0":{"rx_bytes":500,"tx_bytes":250}},
				"pids_stats":{"current":7}
			}`, cpuTotal, systemTotal)
		default:
			return &http.Response{StatusCode: http.StatusNotFound, Status: "404 Not Found", Body: io.NopCloser(bytes.NewBufferString("not found")), Header: make(http.Header)}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(bytes.NewBufferString(body)), Header: http.Header{"Content-Type": []string{"application/json"}}}, nil
	})

	source := &dockerContainerSource{baseURL: "http://docker", client: &http.Client{Transport: transport, Timeout: time.Second}}
	firstMetrics, err := source.Collect(context.Background())
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if len(firstMetrics) != 1 || firstMetrics[0].CPUPercent != 0 {
		t.Fatalf("first sample CPU = %#v, want zero until a previous sample exists", firstMetrics)
	}
	metrics, err := source.Collect(context.Background())
	if err != nil {
		t.Fatalf("second Collect() error = %v", err)
	}
	if len(metrics) != 1 {
		t.Fatalf("metric count = %d, want 1", len(metrics))
	}
	metric := metrics[0]
	if metric.ID != "1234567890ab" || metric.Name != "api" || metric.CPUPercent != 40 {
		t.Fatalf("unexpected container identity/CPU: %#v", metric)
	}
	if metric.MemoryUsed != 800 || metric.MemoryPercent != 40 || metric.NetRxBytes != 500 || metric.NetTxBytes != 250 || metric.PIDs != 7 {
		t.Fatalf("unexpected container stats: %#v", metric)
	}
}

func TestDockerMemoryUsageSupportsCgroupV1CacheField(t *testing.T) {
	memory := dockerMemoryStats{Usage: 4096, Limit: 8192, Stats: map[string]uint64{"total_inactive_file": 1024}}
	if got := dockerMemoryUsage(memory); got != 3072 {
		t.Fatalf("dockerMemoryUsage() = %d, want 3072", got)
	}
}

func TestDockerCPUPercentHandlesCounterReset(t *testing.T) {
	current := dockerCPUStats{}
	previous := dockerCPUStats{}
	current.CPUUsage.TotalUsage = 10
	previous.CPUUsage.TotalUsage = 20
	if got := dockerCPUPercent(current, previous); got != 0 {
		t.Fatalf("dockerCPUPercent() after reset = %v, want 0", got)
	}
}

func TestDockerStoppedContainersRemainVisible(t *testing.T) {
	source := &dockerContainerSource{baseURL: "http://docker", client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/containers/json" {
			t.Errorf("stats requested for stopped container: %s", r.URL.Path)
		}
		body := `[]`
		if r.URL.Query().Get("all") == "true" {
			body = `[{"Id":"stopped","Names":["/api"],"State":"exited","Status":"Exited (1)"}]`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewBufferString(body))}, nil
	})}}
	metrics, err := source.Collect(context.Background())
	if err != nil || len(metrics) != 1 || metrics[0].State != "exited" {
		t.Fatalf("stopped workload disappeared: metrics=%v err=%v", metrics, err)
	}
}

func TestDockerPartialFailureKeepsSuccessfulContainers(t *testing.T) {
	source := &dockerContainerSource{baseURL: "http://docker", client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := `{ "memory_stats": {"usage":100,"limit":200} }`
		if r.URL.Path == "/containers/json" {
			body = `[{"Id":"good","State":"running"},{"Id":"bad","State":"running"}]`
		}
		if r.URL.Path == "/containers/bad/stats" {
			return nil, fmt.Errorf("container disappeared during sampling")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewBufferString(body))}, nil
	})}}
	metrics, _ := source.Collect(context.Background())
	if len(metrics) != 2 {
		t.Fatalf("one failed stats call removed workloads: got %d, want 2", len(metrics))
	}
	for _, metric := range metrics {
		if metric.ID == "good" && metric.MemoryUsed != 100 {
			t.Fatalf("successful stats lost: %v", metric)
		}
	}
}

func TestDockerStatsBudgetNeverHidesInventory(t *testing.T) {
	var statsCalls atomic.Int32
	containers := make([]dockerContainer, 65)
	for i := range containers {
		containers[i] = dockerContainer{ID: fmt.Sprintf("container-%03d", i), State: "running"}
	}
	encoded, err := json.Marshal(containers)
	if err != nil {
		t.Fatal(err)
	}
	source := &dockerContainerSource{baseURL: "http://docker", client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := []byte(`{}`)
		if r.URL.Path == "/containers/json" {
			body = encoded
		} else {
			statsCalls.Add(1)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body))}, nil
	})}}
	metrics, err := source.Collect(context.Background())
	if err == nil || len(metrics) != 65 || statsCalls.Load() != 64 {
		t.Fatalf("budget discarded state or exceeded stats limit: count=%d calls=%d err=%v", len(metrics), statsCalls.Load(), err)
	}
	unavailable := 0
	for _, metric := range metrics {
		if !metric.StatsAvailable {
			unavailable++
		}
	}
	if unavailable != 1 {
		t.Fatalf("unavailable stats=%d, want 1", unavailable)
	}
}
