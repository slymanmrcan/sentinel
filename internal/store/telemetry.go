package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

func (s *Store) InsertMetric(ctx context.Context, metric Metric) error {
	value := func(name string, raw any) any {
		for _, unavailable := range metric.Unavailable {
			if unavailable == name {
				return nil
			}
		}
		return raw
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO metrics (
			ts, cpu_percent, ram_percent, ram_used, ram_total,
			disk_percent, disk_used, disk_total, source, cpu_temp,
			load_1, load_5, load_15, processes,
			swap_percent, swap_used, swap_total,
			net_rx_bps, net_tx_bps, disk_read_bps, disk_write_bps
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		metric.Timestamp, value("cpu", metric.CPUPercent), value("memory", metric.RAMPercent), value("memory", metric.RAMUsed), value("memory", metric.RAMTotal),
		value("disk", metric.DiskPercent), value("disk", metric.DiskUsed), value("disk", metric.DiskTotal), metric.HostName, value("cpu_temp", metric.CPUTemp),
		value("load", metric.Load1), value("load", metric.Load5), value("load", metric.Load15), value("host", metric.Processes),
		value("swap", metric.SwapPercent), value("swap", metric.SwapUsed), value("swap", metric.SwapTotal),
		value("network", metric.NetRxBps), value("network", metric.NetTxBps), value("disk_io", metric.DiskReadBps), value("disk_io", metric.DiskWriteBps),
	)
	return err
}

func (s *Store) History(ctx context.Context, timeRange string) (metrics []Metric, err error) {
	bucket, window := "10 SECOND", "1 HOUR"
	switch timeRange {
	case "6h":
		bucket, window = "1 MINUTE", "6 HOUR"
	case "24h":
		bucket, window = "5 MINUTE", "24 HOUR"
	case "7d":
		bucket, window = "30 MINUTE", "7 DAY"
	}
	query := fmt.Sprintf(`
		SELECT time_bucket(INTERVAL '%s', ts) AS bucket_ts,
		       AVG(cpu_percent),
		       AVG(ram_percent),
		       AVG(disk_percent),
		       AVG(swap_percent),
		       AVG(cpu_temp),
		       AVG(load_1),
		       AVG(net_rx_bps),
		       AVG(net_tx_bps),
		       AVG(disk_read_bps),
		       AVG(disk_write_bps)
		FROM metrics
		WHERE ts > now() - INTERVAL %s
		GROUP BY bucket_ts
		ORDER BY bucket_ts ASC
	`, bucket, window)

	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer closeRows(rows, &err)

	metrics = make([]Metric, 0)
	for rows.Next() {
		var metric Metric
		var values [10]sql.NullFloat64
		args := []any{&metric.Timestamp}
		for i := range values {
			args = append(args, &values[i])
		}
		if err := rows.Scan(args...); err != nil {
			return nil, err
		}
		targets := []*float64{&metric.CPUPercent, &metric.RAMPercent, &metric.DiskPercent, &metric.SwapPercent, &metric.CPUTemp, &metric.Load1, &metric.NetRxBps, &metric.NetTxBps, &metric.DiskReadBps, &metric.DiskWriteBps}
		names := []string{"cpu", "memory", "disk", "swap", "cpu_temp", "load", "network", "network", "disk_io", "disk_io"}
		missing := make(map[string]bool)
		for i, value := range values {
			*targets[i] = value.Float64
			if !value.Valid && !missing[names[i]] {
				metric.Unavailable = append(metric.Unavailable, names[i])
				missing[names[i]] = true
			}
		}
		metrics = append(metrics, metric)
	}
	return metrics, rows.Err()
}

func (s *Store) InsertLog(ctx context.Context, entry LogEntry) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO logs (ts, level, message, source) VALUES (?, ?, ?, ?)`,
		entry.Timestamp, entry.Level, entry.Message, entry.Source,
	)
	return err
}

func (s *Store) Logs(ctx context.Context, level, query string, limit int) (entries []LogEntry, err error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if level == "" {
		level = "ALL"
	}
	pattern := "%" + strings.ToLower(query) + "%"
	rows, err := s.db.QueryContext(ctx, `
		SELECT ts, level, message, source
		FROM logs
		WHERE (level = ? OR ? = 'ALL')
		  AND (lower(message) LIKE ? OR lower(source) LIKE ? OR ? = '%%')
		ORDER BY ts DESC
		LIMIT ?
	`, level, level, pattern, pattern, pattern, limit)
	if err != nil {
		return nil, err
	}
	defer closeRows(rows, &err)

	entries = make([]LogEntry, 0)
	for rows.Next() {
		var entry LogEntry
		if err := rows.Scan(&entry.Timestamp, &entry.Level, &entry.Message, &entry.Source); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (s *Store) ClearLogs(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM logs`)
	return err
}

func (s *Store) Baseline(ctx context.Context, metric string) (Baseline, error) {
	column := map[string]string{
		"cpu":    "cpu_percent",
		"memory": "ram_percent",
		"disk":   "disk_percent",
		"swap":   "swap_percent",
		"load":   "load_1",
	}[metric]
	if column == "" {
		return Baseline{}, fmt.Errorf("unsupported baseline metric %q", metric)
	}
	query := fmt.Sprintf(`
		SELECT COUNT(%s), COALESCE(AVG(%s), 0), COALESCE(STDDEV_SAMP(%s), 0)
		FROM metrics
		WHERE ts > now() - INTERVAL 1 HOUR
	`, column, column, column)
	var baseline Baseline
	baseline.Metric = metric
	err := s.db.QueryRowContext(ctx, query).Scan(&baseline.Count, &baseline.Mean, &baseline.StdDev)
	return baseline, err
}

func (s *Store) Baselines(ctx context.Context) ([]Baseline, error) {
	metrics := []string{"cpu", "memory", "disk", "swap", "load"}
	result := make([]Baseline, 0, len(metrics))
	for _, metric := range metrics {
		baseline, err := s.Baseline(ctx, metric)
		if err != nil {
			return nil, err
		}
		result = append(result, baseline)
	}
	return result, nil
}

func (s *Store) InsertAnomaly(ctx context.Context, anomaly Anomaly) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO anomalies (id, ts, metric, severity, value, baseline_mean, z_score, message)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, anomaly.ID, anomaly.Timestamp, anomaly.Metric, anomaly.Severity,
		anomaly.Value, anomaly.BaselineMean, anomaly.ZScore, anomaly.Message)
	return err
}

func (s *Store) Anomalies(ctx context.Context, limit int) (result []Anomaly, err error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, ts, metric, severity, value, baseline_mean, z_score, message
		FROM anomalies ORDER BY ts DESC LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer closeRows(rows, &err)
	result = make([]Anomaly, 0)
	for rows.Next() {
		var anomaly Anomaly
		if err := rows.Scan(&anomaly.ID, &anomaly.Timestamp, &anomaly.Metric,
			&anomaly.Severity, &anomaly.Value, &anomaly.BaselineMean,
			&anomaly.ZScore, &anomaly.Message); err != nil {
			return nil, err
		}
		result = append(result, anomaly)
	}
	return result, rows.Err()
}

func (s *Store) AlertRules(ctx context.Context) (result []AlertRule, err error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, metric, threshold, severity, enabled
		FROM alert_rules ORDER BY name
	`)
	if err != nil {
		return nil, err
	}
	defer closeRows(rows, &err)
	result = make([]AlertRule, 0)
	for rows.Next() {
		var rule AlertRule
		if err := rows.Scan(&rule.ID, &rule.Name, &rule.Metric, &rule.Threshold, &rule.Severity, &rule.Enabled); err != nil {
			return nil, err
		}
		result = append(result, rule)
	}
	return result, rows.Err()
}

func (s *Store) InsertAlertEvent(ctx context.Context, event AlertEvent) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO alert_events (id, ts, rule_id, rule_name, metric, value, severity, message)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, event.ID, event.Timestamp, event.RuleID, event.RuleName, event.Metric,
		event.Value, event.Severity, event.Message)
	return err
}

func (s *Store) AlertEvents(ctx context.Context, limit int) (result []AlertEvent, err error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, ts, rule_id, rule_name, metric, value, severity, message
		FROM alert_events ORDER BY ts DESC LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer closeRows(rows, &err)
	result = make([]AlertEvent, 0)
	for rows.Next() {
		var event AlertEvent
		if err := rows.Scan(&event.ID, &event.Timestamp, &event.RuleID, &event.RuleName,
			&event.Metric, &event.Value, &event.Severity, &event.Message); err != nil {
			return nil, err
		}
		result = append(result, event)
	}
	return result, rows.Err()
}

func closeRows(rows *sql.Rows, err *error) {
	*err = errors.Join(*err, rows.Close())
}

func (s *Store) Prune(ctx context.Context) error {
	statements := []string{
		`DELETE FROM metrics WHERE ts < now() - INTERVAL 30 DAY`,
		`DELETE FROM logs WHERE ts < now() - INTERVAL 7 DAY`,
		`DELETE FROM anomalies WHERE ts < now() - INTERVAL 30 DAY`,
		`DELETE FROM alert_events WHERE ts < now() - INTERVAL 30 DAY`,
		`DELETE FROM sessions WHERE expires_at <= now()`,
		`DELETE FROM login_attempts WHERE last_attempt < now() - INTERVAL 1 DAY AND locked_until <= now()`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
