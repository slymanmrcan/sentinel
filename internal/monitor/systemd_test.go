package monitor

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type systemRunnerFunc func(context.Context, string, ...string) ([]byte, error)

func (f systemRunnerFunc) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return f(ctx, name, args...)
}

func TestParseSystemctlShowPreservesConfiguredOrderAndMissingUnits(t *testing.T) {
	output := []byte("Id=ssh.service\nDescription=OpenSSH server\nLoadState=loaded\nActiveState=active\nSubState=running\nUnitFileState=enabled\nRestart=on-failure\nResult=success\nNRestarts=2\nActiveEnterTimestamp=Tue 2023-11-14 22:13:20 UTC\n")
	services := parseSystemctlShow(output, []string{"fail2ban.service", "ssh.service"})
	if len(services) != 2 || services[0].Unit != "fail2ban.service" || services[0].LoadState != "not-found" {
		t.Fatalf("missing service was not preserved: %#v", services)
	}
	if services[1].ActiveState != "active" || services[1].RestartPolicy != "on-failure" || services[1].Restarts != 2 || services[1].ActiveSince == nil {
		t.Fatalf("active service was not parsed: %#v", services[1])
	}
}

func TestParseJournalJSONNewestFirst(t *testing.T) {
	output := []byte("{\"__REALTIME_TIMESTAMP\":\"1700000000000000\",\"PRIORITY\":\"6\",\"MESSAGE\":\"started\"}\n{\"__REALTIME_TIMESTAMP\":\"1700000001000000\",\"PRIORITY\":\"3\",\"MESSAGE\":\"failed\"}\n")
	entries := parseJournalJSON(output)
	if len(entries) != 2 || entries[0].Message != "failed" || entries[0].Priority != 3 {
		t.Fatalf("journal entries = %#v", entries)
	}
	if entries[0].Timestamp != time.Unix(1700000001, 0) {
		t.Fatalf("timestamp = %v", entries[0].Timestamp)
	}
}

func TestSystemdSnapshotUsesCacheAndNeverReadsJournal(t *testing.T) {
	monitor := NewSystemdMonitor([]string{"fail2ban.service"}, 8, "")
	var calls atomic.Int32
	monitor.runner = systemRunnerFunc(func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name != "systemctl" {
			t.Fatalf("Snapshot invoked unexpected command %q", name)
		}
		calls.Add(1)
		return []byte("Id=fail2ban.service\nDescription=Fail2Ban\nLoadState=loaded\nActiveState=failed\nSubState=failed\nUnitFileState=enabled\nRestart=no\nResult=exit-code\nNRestarts=0\n"), nil
	})

	snapshot := monitor.Snapshot(context.Background())
	cached := monitor.Snapshot(context.Background())
	if !snapshot.Available || len(snapshot.Services) != 1 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	service := snapshot.Services[0]
	if service.ActiveState != "failed" || service.RestartPolicy != "no" {
		t.Fatalf("service = %#v", service)
	}
	if calls.Load() != 1 || cached.CheckedAt != snapshot.CheckedAt {
		t.Fatalf("cached snapshot caused %d calls; checked_at = %v, want %v", calls.Load(), cached.CheckedAt, snapshot.CheckedAt)
	}
	if systemdSnapshotTTL != time.Minute {
		t.Fatalf("systemdSnapshotTTL = %v, want 1m", systemdSnapshotTTL)
	}

	monitor.cachedAt = time.Now().Add(-systemdSnapshotTTL)
	_ = monitor.Snapshot(context.Background())
	if calls.Load() != 2 {
		t.Fatalf("expired cache caused %d calls, want 2", calls.Load())
	}
}

func TestSystemdSnapshotCoalescesConcurrentRefreshes(t *testing.T) {
	monitor := NewSystemdMonitor([]string{"ssh.service"}, 8, "")
	var calls atomic.Int32
	monitor.runner = systemRunnerFunc(func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name != "systemctl" {
			t.Fatalf("unexpected command %q", name)
		}
		calls.Add(1)
		time.Sleep(10 * time.Millisecond)
		return []byte("Id=ssh.service\nLoadState=loaded\nActiveState=active\nSubState=running\nNRestarts=0\n"), nil
	})

	var waitGroup sync.WaitGroup
	for range 8 {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			if snapshot := monitor.Snapshot(context.Background()); !snapshot.Available {
				t.Errorf("snapshot = %#v", snapshot)
			}
		}()
	}
	waitGroup.Wait()
	if calls.Load() != 1 {
		t.Fatalf("concurrent snapshots caused %d systemctl calls, want 1", calls.Load())
	}
}

func TestSystemdSnapshotIncludesCommandErrorDetails(t *testing.T) {
	monitor := NewSystemdMonitor([]string{"ssh.service"}, 8, "")
	monitor.runner = systemRunnerFunc(func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return nil, errors.New("exit status 1: failed to connect to bus")
	})

	snapshot := monitor.Snapshot(context.Background())
	if snapshot.Available || !strings.Contains(snapshot.Message, "failed to connect to bus") {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestSystemdLogsRejectsUnconfiguredUnitWithoutCommand(t *testing.T) {
	monitor := NewSystemdMonitor([]string{"ssh.service"}, 8, "/host/root")
	monitor.runner = systemRunnerFunc(func(_ context.Context, name string, _ ...string) ([]byte, error) {
		t.Fatalf("unconfigured unit invoked %q", name)
		return nil, nil
	})

	_, err := monitor.Logs(context.Background(), "docker.service")
	if !errors.Is(err, ErrSystemdUnitNotConfigured) {
		t.Fatalf("Logs() error = %v, want ErrSystemdUnitNotConfigured", err)
	}
}

func TestSystemdLogsReadsOnlyRequestedUnitAndPreservesCommandError(t *testing.T) {
	monitor := NewSystemdMonitor([]string{"ssh.service", "cron.service"}, 12, "/host/root")
	monitor.runner = systemRunnerFunc(func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "journalctl" {
			t.Fatalf("unexpected command %q", name)
		}
		joined := strings.Join(args, " ")
		for _, expected := range []string{"--lines=12", "--unit=cron.service", "--root=/host/root"} {
			if !strings.Contains(joined, expected) {
				t.Errorf("journalctl args %q do not contain %q", joined, expected)
			}
		}
		if strings.Contains(joined, "ssh.service") {
			t.Errorf("journalctl args unexpectedly contain another configured unit: %q", joined)
		}
		return nil, errors.New("exit status 1: permission denied by journal")
	})

	snapshot, err := monitor.Logs(context.Background(), "cron.service")
	if err != nil {
		t.Fatalf("Logs() error = %v", err)
	}
	if snapshot.Available || !strings.Contains(snapshot.Message, "permission denied by journal") {
		t.Fatalf("logs snapshot = %#v", snapshot)
	}
}
