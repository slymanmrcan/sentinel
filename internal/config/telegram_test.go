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
	if c.Timezone != "Europe/Istanbul" || len(c.Times) != 3 || c.Hold != 2*time.Minute || c.BruteThreshold != 5 {
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
