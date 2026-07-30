package config

import (
	"testing"
	"time"
)

func TestLoadDefaultsAndLegacyAuthFallback(t *testing.T) {
	t.Setenv("PORT", "")
	t.Setenv("DB_PATH", "")
	t.Setenv("ADMIN_LOGIN", "")
	t.Setenv("ADMIN_EMAIL", "")
	t.Setenv("ADMIN_PASSWORD", "")
	t.Setenv("AUTH_USER", "legacy-admin")
	t.Setenv("AUTH_PASSWORD", "legacy-password")
	t.Setenv("AUTH_COOKIE_SECURE", "true")
	t.Setenv("AUTH_SESSION_TTL", "2h")
	t.Setenv("AUTH_ALLOWED_ORIGINS", "https://monitor.example, http://localhost:8000/")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Port != "8000" || cfg.DBPath != "metrics.db" {
		t.Fatalf("unexpected defaults: %#v", cfg)
	}
	if cfg.AdminLogin != "legacy-admin" || cfg.AdminPassword != "legacy-password" {
		t.Fatalf("legacy auth fallback was not loaded: %#v", cfg)
	}
	if !cfg.CookieSecure || cfg.SessionTTL != 2*time.Hour {
		t.Fatalf("auth settings were not loaded: %#v", cfg)
	}
	if len(cfg.AllowedOrigins) != 2 ||
		cfg.AllowedOrigins[0] != "https://monitor.example" ||
		cfg.AllowedOrigins[1] != "http://localhost:8000" {
		t.Fatalf("allowed origins were not normalized: %#v", cfg.AllowedOrigins)
	}
}

func TestLoadRejectsUnsafeSessionTTL(t *testing.T) {
	t.Setenv("AUTH_SESSION_TTL", "5m")
	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want invalid duration error")
	}
}

func TestLoadRejectsAllowedOriginWithPath(t *testing.T) {
	t.Setenv("AUTH_ALLOWED_ORIGINS", "https://monitor.example/admin")
	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want invalid origin error")
	}
}
