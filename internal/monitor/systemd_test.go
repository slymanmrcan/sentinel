package monitor

import (
	"context"
	"errors"
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

func TestSystemdSnapshotKeepsStatusWhenJournalIsUnavailable(t *testing.T) {
	monitor := &SystemdMonitor{
		units: []string{"fail2ban.service"}, logLines: 8,
		runner: systemRunnerFunc(func(_ context.Context, name string, _ ...string) ([]byte, error) {
			switch name {
			case "systemctl":
				return []byte("Id=fail2ban.service\nDescription=Fail2Ban\nLoadState=loaded\nActiveState=failed\nSubState=failed\nUnitFileState=enabled\nRestart=no\nResult=exit-code\nNRestarts=0\n"), nil
			case "journalctl":
				return nil, errors.New("permission denied")
			default:
				t.Fatalf("unexpected command %q", name)
				return nil, nil
			}
		}),
	}

	snapshot := monitor.Snapshot(context.Background())
	if !snapshot.Available || len(snapshot.Services) != 1 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	service := snapshot.Services[0]
	if service.ActiveState != "failed" || service.RestartPolicy != "no" || service.JournalReady {
		t.Fatalf("service = %#v", service)
	}
}
