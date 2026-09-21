package store

import (
	"context"
	"database/sql"
	"time"
)

// CPUReference describes normal-load history for one host. Missing, invalid and
// high-load samples are never learned as normal. The current five-minute alert
// candidate is also excluded, even on first startup without a saved reference.
type CPUReference struct {
	Median              float64
	Count               int
	First, Last, Latest time.Time
}

func (s *Store) CPUReference(ctx context.Context, host string, now time.Time, warningFloor float64) (CPUReference, error) {
	cutoff := now.Add(-5 * time.Minute)
	var result CPUReference
	var median sql.NullFloat64
	var first, last, latest sql.NullTime
	err := s.db.QueryRowContext(ctx, `
  SELECT COUNT(*) FILTER (WHERE cpu_percent < ?),
         MEDIAN(cpu_percent) FILTER (WHERE cpu_percent < ?),
         MIN(ts) FILTER (WHERE cpu_percent < ?),
         MAX(ts) FILTER (WHERE cpu_percent < ?), MAX(ts)
  FROM metrics
  WHERE source = ? AND ts >= ? AND ts < ?
    AND cpu_percent >= 0 AND cpu_percent <= 100
 `, warningFloor, warningFloor, warningFloor, warningFloor,
		host, now.Add(-24*time.Hour), cutoff).Scan(&result.Count, &median, &first, &last, &latest)
	if err != nil {
		return CPUReference{}, err
	}
	result.Median = median.Float64
	result.First, result.Last, result.Latest = first.Time, last.Time, latest.Time
	return result, nil
}
