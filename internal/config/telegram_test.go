package config

import (
	"strings"
	"testing"
	"time"
)

func TestTelegramConfiguration(t *testing.T) {
	t.Setenv("TELEGRAM_ENABLED", "true")
	t.Setenv("TELEGRAM_BOT_TOKEN", "123:secret")
	t.Setenv("TELEGRAM_CHAT_ID", "-123")
	c, err := loadTelegram()
	if err != nil {
		t.Fatal(err)
	}
	if c.Timezone != "Europe/Istanbul" || len(c.Times) != 3 || c.Hold != 2*time.Minute || c.BruteThreshold != 5 || c.CPU != DefaultCPUAlerts() {
		t.Fatalf("defaults %+v", c.Times)
	}
	t.Setenv("TELEGRAM_SUMMARY_TIMES", "09:00,21:00")
	c, err = loadTelegram()
	if err != nil || len(c.Times) != 2 {
		t.Fatal("two summaries")
	}
	for _, tc := range []struct{ key, value string }{{"TELEGRAM_SUMMARY_TIMES", "25:00"}, {"TELEGRAM_TIMEZONE", "invalid"}, {"TELEGRAM_ALERT_HOLD", "1s"}, {"TELEGRAM_BRUTE_THRESHOLD", "1"}, {"TELEGRAM_BOT_TOKEN", "secret-invalid-token"}} {
		t.Run(tc.key, func(t *testing.T) {
			t.Setenv(tc.key, tc.value)
			_, err := loadTelegram()
			if err == nil || strings.Contains(err.Error(), "secret") {
				t.Fatal("invalid config accepted or secret leaked")
			}
		})
	}
	t.Setenv("TELEGRAM_ENABLED", "false")
	t.Setenv("TELEGRAM_BOT_TOKEN", "bad-secret")
	if _, err := loadTelegram(); err != nil {
		t.Fatal("disabled validated unused credentials")
	}
}

func TestCPUAlertConfiguration(t *testing.T) {
	c, err := loadCPUAlerts()
	if err != nil || c != DefaultCPUAlerts() {
		t.Fatalf("CPU defaults %+v %v", c, err)
	}
	for _, tc := range []struct{ key, value string }{
		{"TELEGRAM_CPU_WARNING", "0"}, {"TELEGRAM_CPU_WARNING", "80"},
		{"TELEGRAM_CPU_CRITICAL", "35"}, {"TELEGRAM_CPU_RECOVERY", "35"},
		{"TELEGRAM_CPU_MULTIPLIER", "1"}, {"TELEGRAM_CPU_WARNING_HOLD", "10s"},
		{"TELEGRAM_CPU_CRITICAL_HOLD", "2h"}, {"TELEGRAM_CPU_RECOVERY_HOLD", "bad"},
	} {
		t.Run(tc.key+tc.value, func(t *testing.T) {
			t.Setenv(tc.key, tc.value)
			if _, err := loadCPUAlerts(); err == nil {
				t.Fatal("invalid CPU settings accepted")
			}
		})
	}
	t.Setenv("TELEGRAM_CPU_WARNING", "40")
	t.Setenv("TELEGRAM_CPU_WARNING_HOLD", "3m")
	t.Setenv("TELEGRAM_CPU_RECOVERY", "0")
	c, err = loadCPUAlerts()
	if err != nil || c.Warning != 40 || c.WarningHold != 3*time.Minute || c.Recovery != 0 {
		t.Fatalf("CPU overrides %+v %v", c, err)
	}
}
