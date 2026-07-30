package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "github.com/duckdb/duckdb-go/v2"
)

type Store struct {
	db *sql.DB
}

type User struct {
	ID           string    `json:"id"`
	Login        string    `json:"email"`
	Name         string    `json:"name"`
	Role         string    `json:"role"`
	PasswordHash string    `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
}

type Session struct {
	TokenHash string
	CSRFToken string
	User      User
	ExpiresAt time.Time
}

type Metric struct {
	Timestamp    time.Time `json:"ts"`
	CPUPercent   float64   `json:"cpu_percent"`
	CPUCores     int       `json:"cpu_cores,omitempty"`
	CPUModel     string    `json:"cpu_model,omitempty"`
	CPUTemp      float64   `json:"cpu_temp"`
	Load1        float64   `json:"load_1"`
	Load5        float64   `json:"load_5,omitempty"`
	Load15       float64   `json:"load_15,omitempty"`
	RAMPercent   float64   `json:"ram_percent"`
	RAMUsed      uint64    `json:"ram_used,omitempty"`
	RAMTotal     uint64    `json:"ram_total,omitempty"`
	SwapPercent  float64   `json:"swap_percent"`
	SwapUsed     uint64    `json:"swap_used,omitempty"`
	SwapTotal    uint64    `json:"swap_total,omitempty"`
	DiskPercent  float64   `json:"disk_percent"`
	DiskUsed     uint64    `json:"disk_used,omitempty"`
	DiskTotal    uint64    `json:"disk_total,omitempty"`
	NetRxBps     float64   `json:"net_rx_bps"`
	NetTxBps     float64   `json:"net_tx_bps"`
	NetRxTotal   uint64    `json:"net_rx_total,omitempty"`
	NetTxTotal   uint64    `json:"net_tx_total,omitempty"`
	DiskReadBps  float64   `json:"disk_read_bps"`
	DiskWriteBps float64   `json:"disk_write_bps"`
	OS           string    `json:"os,omitempty"`
	HostName     string    `json:"host_name,omitempty"`
	Uptime       uint64    `json:"uptime,omitempty"`
	Processes    uint64    `json:"processes,omitempty"`
}

type LogEntry struct {
	Timestamp time.Time `json:"ts"`
	Level     string    `json:"level"`
	Message   string    `json:"message"`
	Source    string    `json:"source"`
}

type Anomaly struct {
	ID           string    `json:"id"`
	Timestamp    time.Time `json:"ts"`
	Metric       string    `json:"metric"`
	Severity     string    `json:"severity"`
	Value        float64   `json:"value"`
	BaselineMean float64   `json:"baseline_mean"`
	ZScore       float64   `json:"z_score"`
	Message      string    `json:"message"`
}

type AlertRule struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Metric    string  `json:"metric"`
	Threshold float64 `json:"threshold"`
	Severity  string  `json:"severity"`
	Enabled   bool    `json:"enabled"`
}

type AlertEvent struct {
	ID        string    `json:"id"`
	Timestamp time.Time `json:"ts"`
	RuleID    string    `json:"rule_id"`
	RuleName  string    `json:"rule_name"`
	Metric    string    `json:"metric"`
	Value     float64   `json:"value"`
	Severity  string    `json:"severity"`
	Message   string    `json:"message"`
}

type Baseline struct {
	Metric string  `json:"metric"`
	Mean   float64 `json:"mean"`
	StdDev float64 `json:"stddev"`
	Count  int     `json:"count"`
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("duckdb", path)
	if err != nil {
		return nil, fmt.Errorf("open DuckDB: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	store := &Store{db: db}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping DuckDB: %w", err)
	}
	if err := store.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) Health(ctx context.Context) error {
	var one int
	if err := s.db.QueryRowContext(ctx, `SELECT 1`).Scan(&one); err != nil {
		return err
	}
	if one != 1 {
		return errors.New("unexpected database health result")
	}
	return nil
}

func (s *Store) migrate(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS metrics (
			ts TIMESTAMPTZ,
			cpu_percent DOUBLE,
			ram_percent DOUBLE,
			ram_used UBIGINT,
			ram_total UBIGINT,
			disk_percent DOUBLE,
			disk_used UBIGINT,
			disk_total UBIGINT,
			source VARCHAR,
			cpu_temp DOUBLE,
			load_1 DOUBLE,
			load_5 DOUBLE,
			load_15 DOUBLE,
			processes UBIGINT,
			swap_percent DOUBLE,
			swap_used UBIGINT,
			swap_total UBIGINT,
			net_rx_bps DOUBLE,
			net_tx_bps DOUBLE,
			disk_read_bps DOUBLE,
			disk_write_bps DOUBLE
		)`,
		`ALTER TABLE metrics ADD COLUMN IF NOT EXISTS cpu_temp DOUBLE`,
		`ALTER TABLE metrics ADD COLUMN IF NOT EXISTS load_1 DOUBLE`,
		`ALTER TABLE metrics ADD COLUMN IF NOT EXISTS load_5 DOUBLE`,
		`ALTER TABLE metrics ADD COLUMN IF NOT EXISTS load_15 DOUBLE`,
		`ALTER TABLE metrics ADD COLUMN IF NOT EXISTS processes UBIGINT`,
		`ALTER TABLE metrics ADD COLUMN IF NOT EXISTS swap_percent DOUBLE`,
		`ALTER TABLE metrics ADD COLUMN IF NOT EXISTS swap_used UBIGINT`,
		`ALTER TABLE metrics ADD COLUMN IF NOT EXISTS swap_total UBIGINT`,
		`ALTER TABLE metrics ADD COLUMN IF NOT EXISTS net_rx_bps DOUBLE`,
		`ALTER TABLE metrics ADD COLUMN IF NOT EXISTS net_tx_bps DOUBLE`,
		`ALTER TABLE metrics ADD COLUMN IF NOT EXISTS disk_read_bps DOUBLE`,
		`ALTER TABLE metrics ADD COLUMN IF NOT EXISTS disk_write_bps DOUBLE`,
		`CREATE TABLE IF NOT EXISTS logs (
			ts TIMESTAMPTZ,
			level VARCHAR,
			message VARCHAR,
			source VARCHAR
		)`,
		`CREATE TABLE IF NOT EXISTS users (
			id VARCHAR PRIMARY KEY,
			email VARCHAR UNIQUE,
			password_hash VARCHAR,
			name VARCHAR,
			role VARCHAR,
			created_at TIMESTAMPTZ
		)`,
		`CREATE TABLE IF NOT EXISTS sessions (
			token_hash VARCHAR PRIMARY KEY,
			user_id VARCHAR,
			csrf_token VARCHAR,
			expires_at TIMESTAMPTZ,
			created_at TIMESTAMPTZ
		)`,
		`CREATE TABLE IF NOT EXISTS login_attempts (
			identifier VARCHAR PRIMARY KEY,
			fail_count INTEGER,
			locked_until TIMESTAMPTZ,
			last_attempt TIMESTAMPTZ
		)`,
		`CREATE TABLE IF NOT EXISTS anomalies (
			id VARCHAR PRIMARY KEY,
			ts TIMESTAMPTZ,
			metric VARCHAR,
			severity VARCHAR,
			value DOUBLE,
			baseline_mean DOUBLE,
			z_score DOUBLE,
			message VARCHAR
		)`,
		`CREATE TABLE IF NOT EXISTS alert_rules (
			id VARCHAR PRIMARY KEY,
			name VARCHAR,
			metric VARCHAR,
			threshold DOUBLE,
			severity VARCHAR,
			enabled BOOLEAN
		)`,
		`CREATE TABLE IF NOT EXISTS alert_events (
			id VARCHAR PRIMARY KEY,
			ts TIMESTAMPTZ,
			rule_id VARCHAR,
			rule_name VARCHAR,
			metric VARCHAR,
			value DOUBLE,
			severity VARCHAR,
			message VARCHAR
		)`,
	}

	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("database migration failed: %w", err)
		}
	}
	return s.seedAlertRules(ctx)
}

func (s *Store) seedAlertRules(ctx context.Context) error {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM alert_rules`).Scan(&count); err != nil {
		return fmt.Errorf("count alert rules: %w", err)
	}
	if count > 0 {
		return nil
	}
	rules := []AlertRule{
		{ID: "cpu-high", Name: "CPU saturation", Metric: "cpu", Threshold: 90, Severity: "critical", Enabled: true},
		{ID: "memory-high", Name: "Memory pressure", Metric: "memory", Threshold: 90, Severity: "critical", Enabled: true},
		{ID: "disk-high", Name: "Disk capacity", Metric: "disk", Threshold: 85, Severity: "warning", Enabled: true},
	}
	for _, rule := range rules {
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO alert_rules (id, name, metric, threshold, severity, enabled) VALUES (?, ?, ?, ?, ?, ?)`,
			rule.ID, rule.Name, rule.Metric, rule.Threshold, rule.Severity, rule.Enabled,
		); err != nil {
			return fmt.Errorf("seed alert rule: %w", err)
		}
	}
	return nil
}
