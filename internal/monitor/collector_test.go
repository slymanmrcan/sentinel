package monitor

import (
	"testing"

	gnet "github.com/shirou/gopsutil/v3/net"
)

func TestRateHandlesDeltasAndCounterReset(t *testing.T) {
	if got := rate(250, 100, 5); got != 30 {
		t.Fatalf("rate() = %v, want 30", got)
	}
	if got := rate(10, 100, 5); got != 0 {
		t.Fatalf("rate() after counter reset = %v, want 0", got)
	}
	if got := rate(100, 100, 0); got != 0 {
		t.Fatalf("rate() with zero elapsed time = %v, want 0", got)
	}
}

func TestSelectedNetworkCounters(t *testing.T) {
	counters := []gnet.IOCountersStat{
		{Name: "eth0", BytesRecv: 100, BytesSent: 40},
		{Name: "docker0", BytesRecv: 500, BytesSent: 300},
		{Name: "tailscale0", BytesRecv: 25, BytesSent: 10},
	}
	received, sent, found := selectedNetworkCounters(counters, []string{"eth0", "tailscale0"})
	if received != 125 || sent != 50 || !found {
		t.Fatalf("selectedNetworkCounters() = (%d, %d, %t), want (125, 50, true)", received, sent, found)
	}
	if _, _, found := selectedNetworkCounters(counters, []string{"missing0"}); found {
		t.Fatal("selectedNetworkCounters() reported a missing interface as found")
	}
}

func TestHasUnavailable(t *testing.T) {
	if !hasUnavailable([]string{"network", "cpu"}, "cpu", "memory") {
		t.Fatal("hasUnavailable() did not find cpu")
	}
	if hasUnavailable([]string{"network"}, "cpu", "memory") {
		t.Fatal("hasUnavailable() returned true for unrelated fields")
	}
}

func TestProcessCPUPercentUsesSamplingWindow(t *testing.T) {
	if got := processCPUPercent(14, 10, 2); got != 200 {
		t.Fatalf("processCPUPercent() = %v, want 200", got)
	}
	if got := processCPUPercent(2, 10, 2); got != 0 {
		t.Fatalf("processCPUPercent() after reset = %v, want 0", got)
	}
}
