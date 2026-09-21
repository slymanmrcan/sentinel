package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/slymanmrcan/sentinel/internal/auth"
	"github.com/slymanmrcan/sentinel/internal/config"
	"github.com/slymanmrcan/sentinel/internal/notify"
	"github.com/slymanmrcan/sentinel/internal/store"
)

func TestTelegramAPIAuthCSRFRoleAndNoCredentials(t *testing.T) {
	s, _ := loginTestServer(t, false)
	s.SetNotifications(notify.New(config.Config{Telegram: config.Telegram{Enabled: true, Token: "123:secret-token", ChatID: "987654321", Timezone: "UTC"}}, s.store, s.collector))
	ctx := context.Background()
	user, err := s.store.FirstAdmin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw := "telegram-session"
	hash := sha256.Sum256([]byte(raw))
	if err := s.store.CreateSession(ctx, store.Session{TokenHash: hex.EncodeToString(hash[:]), CSRFToken: "csrf", User: user, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, method, path, token, origin string
		cookie                            bool
		want                              int
	}{
		{"unauthenticated", "GET", "/api/notifications/telegram", "", "", false, 401},
		{"status", "GET", "/api/notifications/telegram", "", "", true, 200},
		{"missing csrf", "POST", "/api/notifications/telegram/test", "", "", true, 403},
		{"cross origin", "POST", "/api/notifications/telegram/test", "csrf", "https://evil.example", true, 403},
		{"admin test", "POST", "/api/notifications/telegram/test", "csrf", "", true, 202},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, nil)
			if tc.cookie {
				r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: raw})
			}
			r.Header.Set("X-CSRF-Token", tc.token)
			r.Header.Set("Origin", tc.origin)
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "secret-token") || strings.Contains(w.Body.String(), "987654321") {
				t.Fatal("credentials exposed")
			}
		})
	}
	w := httptest.NewRecorder()
	s.handleTelegramTest(w, httptest.NewRequest("POST", "/", nil), auth.Principal{User: store.User{Role: "viewer"}})
	if w.Code != 403 {
		t.Fatal("non-admin can test")
	}
}

func TestLoginObserverUsesTrustedClientIdentityAndLockout(t *testing.T) {
	for _, trusted := range []bool{false, true} {
		t.Run(fmt.Sprint(trusted), func(t *testing.T) {
			s, _ := loginTestServer(t, trusted)
			var ips []string
			var locked []bool
			s.auth.FailureObserver = func(ip, account string, lock bool) {
				ips = append(ips, ip)
				locked = append(locked, lock)
				if account != "admin" {
					t.Fatal("account not normalized")
				}
			}
			for i := 0; i < 6; i++ {
				r := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(`{"login":"ADMIN","password":"wrong-password"}`))
				r.RemoteAddr = "192.0.2.1:1234"
				r.Header.Set("X-Forwarded-For", "203.0.113.9, 198.51.100.1")
				s.Handler().ServeHTTP(httptest.NewRecorder(), r)
			}
			want := "192.0.2.1"
			if trusted {
				want = "198.51.100.1"
			}
			if len(ips) != 6 || !locked[5] {
				t.Fatalf("failures/lockout not observed %v %v", ips, locked)
			}
			for _, ip := range ips {
				if ip != want {
					t.Fatalf("spoofed identity %q want %q", ip, want)
				}
			}
		})
	}
}
