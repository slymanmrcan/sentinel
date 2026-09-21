package monitor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestSSHJournalIncrementalBoundedSanitizedAndUnavailable(t *testing.T) {
	now := time.Now()
	line := fmt.Sprintf(`{"__CURSOR":"cursor2","__REALTIME_TIMESTAMP":"%d","_COMM":"sshd","MESSAGE":"Failed password for invalid user private-account from 192.0.2.10 port 42 ssh2"}`, now.UnixMicro())
	reader := NewSSHJournal("")
	var calls []string
	reader.runner = systemRunnerFunc(func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		return []byte(line), nil
	})
	first := reader.Read(context.Background(), "", now)
	if first.Cursor != "cursor2" || len(first.Failures) != 0 || !strings.Contains(calls[0], "--lines=1") {
		t.Fatal("initial historical replay")
	}
	next := reader.Read(context.Background(), "cursor1", now)
	if len(next.Failures) != 1 || next.Failures[0].IP != "192.0.2.10" || next.Failures[0].Account == "private-account" {
		t.Fatalf("bad sanitized failure: %+v", next)
	}
	for _, flag := range []string{"--after-cursor=cursor1", "--lines=201", "--since=@", "--system"} {
		if !strings.Contains(calls[1], flag) {
			t.Fatalf("missing bounded read flag %s", flag)
		}
	}
	reader.runner = systemRunnerFunc(func(context.Context, string, ...string) ([]byte, error) {
		return []byte(strings.Repeat(line+"\n", 201)), nil
	})
	batch := reader.Read(context.Background(), "cursor1", now)
	if len(batch.Failures) != 200 || !strings.Contains(batch.Status, "Kısmi") {
		t.Fatal("unbounded journal batch")
	}
	reader.runner = systemRunnerFunc(func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("permission denied sensitive raw output")
	})
	batch = reader.Read(context.Background(), "cursor1", now)
	if !strings.Contains(batch.Status, "Kullanılamıyor") || strings.Contains(batch.Status, "sensitive") || len(batch.Failures) != 0 {
		t.Fatal("access failure hidden/leaked")
	}
}
