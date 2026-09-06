package monitor

import (
	gnet "github.com/shirou/gopsutil/v3/net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/slymanmrcan/sentinel/internal/config"
)

func TestLinuxHostNamespaceIntegration(t *testing.T) {
	if runtime.GOOS != "linux" || os.Getenv("SENTINEL_LINUX_TEST") != "1" {
		t.Skip("requires isolated network and host PID namespaces")
	}
	c := New(nil, config.Config{HostProc: "/proc"})
	host, err := c.networkCounters()
	if err != nil {
		t.Fatal(err)
	}
	local, err := gnet.IOCounters(true)
	if err != nil {
		t.Fatal(err)
	}
	localNames := make(map[string]bool)
	for _, item := range local {
		localNames[item.Name] = true
	}
	foundHostOnly := false
	for _, item := range host {
		if !localNames[item.Name] {
			foundHostOnly = true
		}
	}
	if !foundHostOnly {
		t.Fatal("host interfaces were not distinguished from the isolated container")
	}
	if _, err := c.listeningPorts(); err != nil {
		t.Fatal(err)
	}
	t.Logf("host interfaces: %d; container interfaces: %d; host TCP table readable", len(host), len(local))
}

func TestHostNetworkUsesPIDOneInsteadOfSelfNamespace(t *testing.T) {
	root := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("net/dev", "header\nheader\n eth0: 999 0 0 0 0 0 0 0 999 0 0 0 0 0 0 0\n")
	write("1/net/dev", "header\nheader\n ens3: 100 0 0 0 0 0 0 0 200 0 0 0 0 0 0 0\n")
	write("1/net/tcp", "header\n 0: 00000000:0016 00000000:0000 0A 00000000:00000000 00:00000000 00000000 0 0 123\n")
	write("net/tcp", "header\n 0: 00000000:1F40 00000000:0000 0A 00000000:00000000 00:00000000 00000000 0 0 456\n")
	write("42/comm", "sshd\n")
	if err := os.MkdirAll(filepath.Join(root, "42/fd"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("socket:[123]", filepath.Join(root, "42/fd/3")); err != nil {
		t.Fatal(err)
	}
	c := New(nil, config.Config{HostProc: root})
	counters, err := c.networkCounters()
	if err != nil || len(counters) != 1 || counters[0].Name != "ens3" || counters[0].BytesRecv != 100 {
		t.Fatalf("wrong network namespace: %+v %v", counters, err)
	}
	ports, err := c.listeningPorts()
	if err != nil || len(ports) != 1 || ports[0].Port != 22 || ports[0].PID != 42 || ports[0].Name != "sshd" {
		t.Fatalf("wrong listener namespace: %+v %v", ports, err)
	}
	if err := os.Remove(filepath.Join(root, "1/net/dev")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.networkCounters(); err == nil {
		t.Fatal("missing host data silently fell back to container data")
	}
}

func TestSystemDetailsSharesSnapshotAndReturnsCopies(t *testing.T) {
	c := New(nil, config.Config{})
	c.details = SystemDetails{CheckedAt: time.Now(), KernelVersion: "cached", Processes: []ProcessInfo{{PID: 123}}, ListeningPorts: []PortInfo{{Port: 22}}}
	first := c.SystemDetails()
	first.Processes[0].PID = 456
	first.ListeningPorts[0].Port = 80
	second := c.SystemDetails()
	if second.KernelVersion != "cached" || second.Processes[0].PID != 123 || second.ListeningPorts[0].Port != 22 {
		t.Fatalf("cache was refreshed or mutated: %+v", second)
	}
}
