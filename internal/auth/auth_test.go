package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/slymanmrcan/sentinel/internal/config"
	"github.com/slymanmrcan/sentinel/internal/store"
)

func TestSafeUserRemovesPasswordHash(t *testing.T) {
	user := store.User{ID: "u1", Login: "admin", PasswordHash: "secret"}
	safe := safeUser(user)
	if safe.PasswordHash != "" {
		t.Fatal("safeUser retained password hash")
	}
	if safe.ID != user.ID || safe.Login != user.Login {
		t.Fatal("safeUser changed public fields")
	}
}

func TestHashTokenIsStableAndDoesNotReturnRawToken(t *testing.T) {
	first := hashToken("session-token")
	second := hashToken("session-token")
	if first != second {
		t.Fatal("hashToken is not stable")
	}
	if first == "session-token" {
		t.Fatal("hashToken returned raw token")
	}
}

func TestCSRFUsesSessionBoundToken(t *testing.T) {
	service := &Service{}
	principal := Principal{CSRFToken: "expected-token"}
	if !service.ValidCSRF(principal, "expected-token") {
		t.Fatal("ValidCSRF rejected the session token")
	}
	if service.ValidCSRF(principal, "different-token") {
		t.Fatal("ValidCSRF accepted a different token")
	}
}

func TestOriginHeadersAreIgnoredUnlessProxyTrustIsEnabled(t *testing.T) {
	request := httptest.NewRequest("POST", "http://sentinel.local/api/logs", nil)
	request.Host = "sentinel.local"
	request.Header.Set("Origin", "https://monitor.example")
	request.Header.Set("X-Forwarded-Host", "monitor.example")
	request.Header.Set("X-Forwarded-Proto", "https")

	untrusted := &Service{cfg: config.Config{}}
	if untrusted.ClientOriginAllowed(request) {
		t.Fatal("ClientOriginAllowed trusted forwarded headers by default")
	}

	trusted := &Service{cfg: config.Config{TrustProxyHeaders: true}}
	if !trusted.ClientOriginAllowed(request) {
		t.Fatal("ClientOriginAllowed rejected configured trusted proxy headers")
	}
}

func TestOriginAllowsTLSTerminationWhenPublicHostIsPreserved(t *testing.T) {
	request := httptest.NewRequest("POST", "http://monitor.example/api/auth/login", nil)
	request.Host = "monitor.example"
	request.Header.Set("Origin", "https://monitor.example")

	service := &Service{cfg: config.Config{CookieSecure: false, TrustProxyHeaders: false}}
	if !service.ClientOriginAllowed(request) {
		t.Fatal("ClientOriginAllowed coupled origin validation to the cookie secure setting")
	}
}

func TestExplicitAllowedOriginSupportsRewrittenProxyHost(t *testing.T) {
	request := httptest.NewRequest("POST", "http://sentinel:8000/api/auth/login", nil)
	request.Host = "sentinel:8000"
	request.Header.Set("Origin", "https://monitor.example")

	service := &Service{cfg: config.Config{AllowedOrigins: []string{"https://monitor.example"}}}
	if !service.ClientOriginAllowed(request) {
		t.Fatal("ClientOriginAllowed rejected an explicitly allowed public origin")
	}
}

func TestPasswordLength(t *testing.T) {
	tests := []struct {
		password string
		want     bool
	}{
		{password: "1234567", want: false},
		{password: "12345678", want: true},
		{password: "güçlü-şifre", want: true},
		{password: string(make([]byte, 73)), want: false},
	}
	for _, test := range tests {
		if got := validPasswordLength(test.password); got != test.want {
			t.Fatalf("validPasswordLength(%q) = %t, want %t", test.password, got, test.want)
		}
	}
}

func TestSessionCookieSecurityAttributes(t *testing.T) {
	service := &Service{cfg: config.Config{CookieSecure: true, SessionTTL: time.Hour}}
	recorder := httptest.NewRecorder()
	service.SetSessionCookie(recorder, "opaque-token")

	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookie count = %d, want 1", len(cookies))
	}
	cookie := cookies[0]
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("unexpected cookie security attributes: %#v", cookie)
	}
}
