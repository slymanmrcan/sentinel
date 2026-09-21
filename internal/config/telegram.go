package config

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
	_ "time/tzdata"
)

// Telegram credentials never belong in API representations or logs.
type Telegram struct {
	Enabled        bool
	Token          string `json:"-"`
	ChatID         string `json:"-"`
	Timezone       string
	Times          []string
	Hold           time.Duration
	RecoveryMargin int
	BruteWindow    time.Duration
	BruteThreshold int
	SSHEnabled     bool
}

func loadTelegram() (Telegram, error) {
	c := Telegram{Timezone: "Europe/Istanbul", Times: []string{"09:00", "15:00", "21:00"}, Hold: 2 * time.Minute, RecoveryMargin: 5, BruteWindow: 5 * time.Minute, BruteThreshold: 5}
	var err error
	c.Enabled, err = envBool("TELEGRAM_ENABLED", false)
	if err != nil || !c.Enabled {
		return c, err
	}
	c.Token, c.ChatID = strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN")), strings.TrimSpace(os.Getenv("TELEGRAM_CHAT_ID"))
	if !regexp.MustCompile(`^[0-9]+:[A-Za-z0-9_-]+$`).MatchString(c.Token) || !regexp.MustCompile(`^-?[0-9]+$`).MatchString(c.ChatID) {
		return c, fmt.Errorf("TELEGRAM_BOT_TOKEN and numeric TELEGRAM_CHAT_ID are required")
	}
	c.Timezone = envOr("TELEGRAM_TIMEZONE", c.Timezone)
	if _, err = time.LoadLocation(c.Timezone); err != nil {
		return c, fmt.Errorf("TELEGRAM_TIMEZONE must be a valid IANA timezone")
	}
	if raw, ok := os.LookupEnv("TELEGRAM_SUMMARY_TIMES"); ok {
		c.Times = splitCSV(raw)
		if len(c.Times) > 6 {
			return c, fmt.Errorf("TELEGRAM_SUMMARY_TIMES allows at most 6 times")
		}
		for _, v := range c.Times {
			if t, e := time.Parse("15:04", v); e != nil || t.Format("15:04") != v {
				return c, fmt.Errorf("TELEGRAM_SUMMARY_TIMES must use HH:MM")
			}
		}
		sort.Strings(c.Times)
	}
	for _, p := range []struct {
		key      string
		dst      *time.Duration
		min, max time.Duration
	}{
		{"TELEGRAM_ALERT_HOLD", &c.Hold, 30 * time.Second, time.Hour},
		{"TELEGRAM_BRUTE_WINDOW", &c.BruteWindow, time.Minute, time.Hour},
	} {
		if raw := strings.TrimSpace(os.Getenv(p.key)); raw != "" {
			v, e := time.ParseDuration(raw)
			if e != nil || v < p.min || v > p.max {
				return c, fmt.Errorf("%s duration out of range", p.key)
			}
			*p.dst = v
		}
	}
	c.RecoveryMargin, err = envIntRange("TELEGRAM_RECOVERY_MARGIN", 5, 1, 30)
	if err != nil {
		return c, err
	}
	c.BruteThreshold, err = envIntRange("TELEGRAM_BRUTE_THRESHOLD", 5, 3, 1000)
	if err != nil {
		return c, err
	}
	c.SSHEnabled, err = envBool("TELEGRAM_SSH_ENABLED", false)
	return c, err
}
