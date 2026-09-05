package store

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestLoginFailuresAreAtomicAndExpire(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "attempts.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	const key = "ip:192.0.2.1"
	var workers sync.WaitGroup
	for i := 0; i < 5; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if err := db.RegisterLoginFailure(ctx, key, 5, 15*time.Minute); err != nil {
				t.Error(err)
			}
		}()
	}
	workers.Wait()
	remaining, err := db.LockoutRemaining(ctx, key)
	if err != nil || remaining < 14*time.Minute {
		t.Fatalf("lockout = %v, %v", remaining, err)
	}
	var count int
	if err := db.db.QueryRowContext(ctx, "SELECT fail_count FROM login_attempts WHERE identifier = ?", key).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 5 {
		t.Fatalf("concurrent failure count = %d, want 5", count)
	}
	if _, err := db.db.ExecContext(ctx, `UPDATE login_attempts SET last_attempt = now() - INTERVAL 16 MINUTE, locked_until = now() - INTERVAL 1 MINUTE WHERE identifier = ?`, key); err != nil {
		t.Fatal(err)
	}
	if err := db.RegisterLoginFailure(ctx, key, 5, 15*time.Minute); err != nil {
		t.Fatal(err)
	}
	if remaining, err := db.LockoutRemaining(ctx, key); err != nil || remaining != 0 {
		t.Fatalf("expired lockout immediately reactivated: %v, %v", remaining, err)
	}
	if err := db.db.QueryRowContext(ctx, "SELECT fail_count FROM login_attempts WHERE identifier = ?", key).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("fresh failure count = %d, want 1", count)
	}
}

func TestPruneRemovesOldLoginAttemptsAndPreservesActiveLockouts(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "prune.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, key := range []string{"old", "locked", "recent"} {
		if err := db.RegisterLoginFailure(ctx, key, 5, 15*time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.db.ExecContext(ctx, `UPDATE login_attempts SET last_attempt = now() - INTERVAL 2 DAY WHERE identifier IN ('old', 'locked')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, `UPDATE login_attempts SET locked_until = now() + INTERVAL 5 MINUTE WHERE identifier = 'locked'`); err != nil {
		t.Fatal(err)
	}
	if err := db.Prune(ctx); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"old", "locked", "recent"} {
		var count int
		if err := db.db.QueryRowContext(ctx, "SELECT count(*) FROM login_attempts WHERE identifier = ?", key).Scan(&count); err != nil {
			t.Fatal(err)
		}
		want := 1
		if key == "old" {
			want = 0
		}
		if count != want {
			t.Errorf("remaining rows for %s = %d, want %d", key, count, want)
		}
	}
}
