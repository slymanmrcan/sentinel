package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/slymanmrcan/sentinel/internal/auth"
	"github.com/slymanmrcan/sentinel/internal/config"
	"github.com/slymanmrcan/sentinel/internal/monitor"
	"github.com/slymanmrcan/sentinel/internal/store"
)

func loginTestServer(t *testing.T, trustProxy bool) (*Server, config.Config) {
	t.Helper()
	cfg := config.Config{AdminLogin: "admin", AdminPassword: "test-password", SessionTTL: time.Hour, TrustProxyHeaders: trustProxy}
	db, err := store.Open(filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	service, err := auth.New(context.Background(), db, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return New(cfg, db, service, monitor.New(db, cfg)), cfg
}

func TestLoginLimitsFailuresAcrossUsernames(t *testing.T) {
	server, cfg := loginTestServer(t, false)
	for attempt := 0; attempt < 6; attempt++ {
		request := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(
			fmt.Sprintf(`{"login":"unknown-%d","password":"wrong-password"}`, attempt)))
		request.RemoteAddr = "192.0.2.1:1234"
		request.Header.Set("X-Forwarded-For", fmt.Sprintf("198.51.100.%d", attempt+1))
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		want := http.StatusUnauthorized
		if attempt == 5 {
			want = http.StatusTooManyRequests
		}
		if response.Code != want {
			t.Fatalf("attempt %d with a new username: status %d, want %d", attempt+1, response.Code, want)
		}
		if want == http.StatusTooManyRequests {
			seconds, err := strconv.Atoi(response.Header().Get("Retry-After"))
			if err != nil || seconds < 890 || seconds > 900 {
				t.Fatalf("Retry-After = %q, want approximately 15 minutes", response.Header().Get("Retry-After"))
			}
		}
	}
	// A fresh auth service retains the persisted IP lockout.
	restarted, err := auth.New(context.Background(), server.store, cfg)
	if err != nil {
		t.Fatal(err)
	}
	server = New(cfg, server.store, restarted, server.collector)
	request := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"login":"admin","password":"test-password"}`))
	request.RemoteAddr = "192.0.2.1:9000"
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("locked IP after restart: got %d, want 429", response.Code)
	}
	request = httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"login":"admin","password":"test-password"}`))
	request.RemoteAddr = "192.0.2.2:1234"
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("another IP with correct credentials: got %d, want 200", response.Code)
	}
}

func TestLoginBehindNPMIgnoresForgedForwardedPrefix(t *testing.T) {
	server, _ := loginTestServer(t, true)
	for attempt := 0; attempt < 6; attempt++ {
		request := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"login":"admin","password":"wrong-password"}`))
		request.RemoteAddr = "172.18.0.2:1234"
		request.Header.Set("X-Forwarded-For", fmt.Sprintf("198.51.100.%d, 192.0.2.1", attempt+1))
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		want := http.StatusUnauthorized
		if attempt == 5 {
			want = http.StatusTooManyRequests
		}
		if response.Code != want {
			t.Fatalf("attempt %d: got %d, want %d", attempt+1, response.Code, want)
		}
	}
	request := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"login":"admin","password":"test-password"}`))
	request.RemoteAddr = "172.18.0.2:1234"
	request.Header.Set("X-Forwarded-For", "192.0.2.2")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("another user behind the same NPM: got %d, want 200", response.Code)
	}
}
