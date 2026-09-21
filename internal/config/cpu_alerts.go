package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// CPUAlerts affects Telegram notifications only; collection and existing panel
// alarm/anomaly history keep their current rules.
type CPUAlerts struct {
	Warning, Critical, Recovery, Multiplier int
	WarningHold, CriticalHold, RecoveryHold time.Duration
}

func DefaultCPUAlerts() CPUAlerts {
	return CPUAlerts{Warning: 35, Critical: 80, Recovery: 20, Multiplier: 3,
		WarningHold: 5 * time.Minute, CriticalHold: 2 * time.Minute, RecoveryHold: 3 * time.Minute}
}

func loadCPUAlerts() (CPUAlerts, error) {
	c := DefaultCPUAlerts()
	for _, field := range []struct {
		key      string
		dst      *int
		min, max int
	}{
		{"TELEGRAM_CPU_WARNING", &c.Warning, 1, 99},
		{"TELEGRAM_CPU_CRITICAL", &c.Critical, 2, 100},
		{"TELEGRAM_CPU_RECOVERY", &c.Recovery, 0, 98},
		{"TELEGRAM_CPU_MULTIPLIER", &c.Multiplier, 2, 10},
	} {
		v, err := envIntRange(field.key, *field.dst, field.min, field.max)
		if err != nil {
			return c, err
		}
		*field.dst = v
	}
	if c.Recovery >= c.Warning || c.Warning >= c.Critical {
		return c, fmt.Errorf("TELEGRAM_CPU_RECOVERY must be below WARNING, and WARNING below CRITICAL")
	}
	for _, field := range []struct {
		key string
		dst *time.Duration
	}{
		{"TELEGRAM_CPU_WARNING_HOLD", &c.WarningHold},
		{"TELEGRAM_CPU_CRITICAL_HOLD", &c.CriticalHold},
		{"TELEGRAM_CPU_RECOVERY_HOLD", &c.RecoveryHold},
	} {
		if raw := strings.TrimSpace(os.Getenv(field.key)); raw != "" {
			v, err := time.ParseDuration(raw)
			if err != nil || v < 30*time.Second || v > time.Hour {
				return c, fmt.Errorf("%s must be a duration between 30s and 1h", field.key)
			}
			*field.dst = v
		}
	}
	return c, nil
}
