package store

import (
	"context"
	"math"
	"path/filepath"
	"testing"
	"time"
)

func cpuTestStore(t testing.TB) *Store {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "cpu.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
func TestCPUReferenceFiltersHostGapsInvalidAndHighLoad(t *testing.T) {
	db := cpuTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	for _, v := range []struct {
		ago         time.Duration
		value       float64
		host        string
		unavailable bool
	}{
		{6 * time.Minute, 10, "a", false}, {10 * time.Minute, 0, "a", false}, {12 * time.Minute, 5, "a", false},
		{2 * time.Hour, 10, "a", false}, {20 * time.Hour, 12, "a", false}, {22 * time.Hour, 34, "a", false},
		{8 * time.Minute, 35, "a", false}, {9 * time.Minute, 95, "a", false},
		{5*time.Minute + time.Second, 2, "b", false}, {5*time.Minute + time.Second, 0, "a", true},
		{6 * time.Minute, -1, "a", false}, {6 * time.Minute, 101, "a", false},
		{6 * time.Minute, math.NaN(), "a", false}, {6 * time.Minute, math.Inf(1), "a", false},
		{25 * time.Hour, 0, "a", false}, {time.Minute, 0, "a", false}, {-time.Minute, 0, "a", false},
	} {
		m := Metric{Timestamp: now.Add(-v.ago), CPUPercent: v.value, HostName: v.host}
		if v.unavailable {
			m.Unavailable = []string{"cpu"}
		}
		if err := db.InsertMetric(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	ref, err := db.CPUReference(ctx, "a", now, 35)
	if err != nil {
		t.Fatal(err)
	}
	if ref.Count != 6 || ref.Median != 10 || !ref.First.Equal(now.Add(-22*time.Hour)) || !ref.Last.Equal(now.Add(-6*time.Minute)) || !ref.Latest.Equal(now.Add(-6*time.Minute)) {
		t.Fatalf("unexpected reference: %+v", ref)
	}
	empty, err := db.CPUReference(ctx, "missing", now, 35)
	if err != nil || empty.Count != 0 || !empty.First.IsZero() || !empty.Latest.IsZero() {
		t.Fatalf("missing data not empty: %+v %v", empty, err)
	}
}
func BenchmarkCPUReference24Hours(b *testing.B) {
	db := cpuTestStore(b)
	ctx := context.Background()
	now := time.Now()
	_, err := db.db.ExecContext(ctx, `INSERT INTO metrics(ts,cpu_percent,source)
 SELECT ?::TIMESTAMPTZ - i * INTERVAL '30 SECONDS',
 CASE WHEN i % 100 = 0 THEN 90 ELSE 10 + i % 7 END, 'bench'
 FROM range(86400) AS t(i)`, now)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ref, err := db.CPUReference(ctx, "bench", now, 35)
		if err != nil || ref.Count < 120 {
			b.Fatalf("query: %+v %v", ref, err)
		}
	}
}
