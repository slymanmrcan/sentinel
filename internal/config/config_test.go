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
	t.Setenv("NETWORK_INTERFACES", "eth0, tailscale0, eth0")
	t.Setenv("CONTAINER_METRICS_ENABLED", "true")
	t.Setenv("CONTAINER_COLLECTION_INTERVAL", "45s")
	t.Setenv("CONTAINER_API_URL", "http://docker-proxy:2375/")
	t.Setenv("DOCKER_SOCKET", "/run/user/1000/docker.sock")
	t.Setenv("SYSTEMD_UNITS", "fail2ban.service, ssh.service, fail2ban.service")
	t.Setenv("SYSTEMD_LOG_LINES", "12")

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
	if len(cfg.NetworkInterfaces) != 2 || cfg.NetworkInterfaces[0] != "eth0" || cfg.NetworkInterfaces[1] != "tailscale0" {
		t.Fatalf("network interfaces were not normalized: %#v", cfg.NetworkInterfaces)
	}
	if !cfg.ContainerMetrics || cfg.ContainerInterval != 45*time.Second || cfg.ContainerAPIURL != "http://docker-proxy:2375" || cfg.DockerSocket != "/run/user/1000/docker.sock" {
		t.Fatalf("container settings were not loaded: %#v", cfg)
	}
	if len(cfg.SystemdUnits) != 2 || cfg.SystemdUnits[0] != "fail2ban.service" || cfg.SystemdUnits[1] != "ssh.service" || cfg.SystemdLogLines != 12 {
		t.Fatalf("systemd settings were not loaded: %#v", cfg)
	}
}

func TestLoadRejectsInvalidSystemdUnit(t *testing.T) {
	t.Setenv("SYSTEMD_UNITS", "--all.service")
	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want invalid systemd unit error")
	}
}

func TestLoadRejectsSystemdLogLineLimit(t *testing.T) {
	t.Setenv("SYSTEMD_LOG_LINES", "1000")
	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want invalid systemd log line limit error")
	}
}

func TestLoadRejectsUnsupportedContainerInterval(t *testing.T) {
	t.Setenv("CONTAINER_COLLECTION_INTERVAL", "20s")
	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want unsupported container interval error")
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

func TestLoadRejectsUnsafeContainerAPIURL(t *testing.T) {
	t.Setenv("CONTAINER_API_URL", "http://user:pass@docker.example:2375")
	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want invalid container API URL error")
	}
}
