package config

import (
	"fmt"
	"net/url"
	"os"
	"regexp"
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
	NetworkInterfaces []string
	ContainerMetrics  bool
	ContainerInterval time.Duration
	ContainerAPIURL   string
	DockerSocket      string
	SystemdUnits      []string
	SystemdLogLines   int
}

var systemdUnitPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.@:-]{0,126}\.service$`)

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
	containerMetrics, err := envBool("CONTAINER_METRICS_ENABLED", false)
	if err != nil {
		return Config{}, err
	}
	containerAPIURL, err := parseContainerAPIURL(os.Getenv("CONTAINER_API_URL"))
	if err != nil {
		return Config{}, err
	}
	containerInterval, err := parseContainerInterval(os.Getenv("CONTAINER_COLLECTION_INTERVAL"))
	if err != nil {
		return Config{}, err
	}
	systemdUnits, err := parseSystemdUnits(os.Getenv("SYSTEMD_UNITS"))
	if err != nil {
		return Config{}, err
	}
	systemdLogLines, err := envIntRange("SYSTEMD_LOG_LINES", 8, 1, 50)
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

	adminLogin := firstNonEmpty("ADMIN_LOGIN", "ADMIN_EMAIL", "AUTH_USER")
	if adminLogin == "" {
		adminLogin = "admin"
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
		NetworkInterfaces: splitCSV(os.Getenv("NETWORK_INTERFACES")),
		ContainerMetrics:  containerMetrics,
		ContainerInterval: containerInterval,
		ContainerAPIURL:   containerAPIURL,
		DockerSocket:      envOr("DOCKER_SOCKET", "/var/run/docker.sock"),
		SystemdUnits:      systemdUnits,
		SystemdLogLines:   systemdLogLines,
	}, nil
}

func parseSystemdUnits(raw string) ([]string, error) {
	units := splitCSV(raw)
	if len(units) > 20 {
		return nil, fmt.Errorf("SYSTEMD_UNITS must contain at most 20 services")
	}
	for _, unit := range units {
		if !systemdUnitPattern.MatchString(unit) {
			return nil, fmt.Errorf("SYSTEMD_UNITS contains invalid service name %q", unit)
		}
	}
	return units, nil
}

func envIntRange(key string, fallback, minimum, maximum int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < minimum || value > maximum {
		return 0, fmt.Errorf("%s must be between %d and %d", key, minimum, maximum)
	}
	return value, nil
}

func parseContainerInterval(raw string) (time.Duration, error) {
	if strings.TrimSpace(raw) == "" {
		return 30 * time.Second, nil
	}
	interval, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("CONTAINER_COLLECTION_INTERVAL must be one of 15s, 30s, 45s, 1m, or 2m")
	}
	switch interval {
	case 15 * time.Second, 30 * time.Second, 45 * time.Second, time.Minute, 2 * time.Minute:
		return interval, nil
	default:
		return 0, fmt.Errorf("CONTAINER_COLLECTION_INTERVAL must be one of 15s, 30s, 45s, 1m, or 2m")
	}
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

func parseContainerAPIURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("CONTAINER_API_URL must be an http(s) URL without credentials, query, or fragment")
	}
	return strings.TrimRight(raw, "/"), nil
}

func splitCSV(raw string) []string {
	result := make([]string, 0)
	seen := make(map[string]bool)
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		result = append(result, item)
	}
	return result
}
