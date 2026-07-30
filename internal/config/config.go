package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	Port              string
	DBPath            string
	AdminLogin        string
	AdminPassword     string
	AdminName         string
	CookieSecure      bool
	SessionTTL        time.Duration
	TrustProxyHeaders bool
	AllowedOrigins    []string
	HostRoot          string
	HostSys           string
}

func Load() (Config, error) {
	_ = godotenv.Load()

	cookieSecure, err := envBool("AUTH_COOKIE_SECURE", false)
	if err != nil {
		return Config{}, err
	}
	allowedOrigins, err := parseOrigins(os.Getenv("AUTH_ALLOWED_ORIGINS"))
	if err != nil {
		return Config{}, err
	}
	trustProxyHeaders, err := envBool("TRUST_PROXY_HEADERS", false)
	if err != nil {
		return Config{}, err
	}

	sessionTTL := 12 * time.Hour
	if raw := strings.TrimSpace(os.Getenv("AUTH_SESSION_TTL")); raw != "" {
		sessionTTL, err = time.ParseDuration(raw)
		if err != nil || sessionTTL < 15*time.Minute || sessionTTL > 30*24*time.Hour {
			return Config{}, fmt.Errorf("AUTH_SESSION_TTL must be a duration between 15m and 720h")
		}
	}

	adminLogin := firstNonEmpty("ADMIN_EMAIL", "AUTH_USER")
	if adminLogin == "" {
		adminLogin = "admin@sentinel.local"
	}

	return Config{
		Port:              envOr("PORT", "8000"),
		DBPath:            envOr("DB_PATH", "metrics.db"),
		AdminLogin:        strings.ToLower(adminLogin),
		AdminPassword:     firstNonEmpty("ADMIN_PASSWORD", "AUTH_PASSWORD"),
		AdminName:         envOr("ADMIN_NAME", "Sentinel Admin"),
		CookieSecure:      cookieSecure,
		SessionTTL:        sessionTTL,
		TrustProxyHeaders: trustProxyHeaders,
		AllowedOrigins:    allowedOrigins,
		HostRoot:          strings.TrimSpace(os.Getenv("HOST_ROOT")),
		HostSys:           strings.TrimSpace(os.Getenv("HOST_SYS")),
	}, nil
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func firstNonEmpty(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

func envBool(key string, fallback bool) (bool, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false", key)
	}
	return value, nil
}

func parseOrigins(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	origins := make([]string, 0)
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		parsed, err := url.Parse(item)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
			parsed.Host == "" || parsed.User != nil ||
			(parsed.Path != "" && parsed.Path != "/") ||
			parsed.RawQuery != "" || parsed.Fragment != "" {
			return nil, fmt.Errorf("AUTH_ALLOWED_ORIGINS must contain comma-separated http(s) origins without paths")
		}
		origins = append(origins, strings.ToLower(parsed.Scheme+"://"+parsed.Host))
	}
	return origins, nil
}
