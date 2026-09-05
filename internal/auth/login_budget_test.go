package auth

import (
	"context"
	"errors"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestLoginBudgetBoundsBurstAndRefills(t *testing.T) {
	var budget loginBudget
	now := time.Now()
	for i := 0; i < loginBurst; i++ {
		if wait := budget.take(now); wait != 0 {
			t.Fatalf("burst attempt %d rejected: %v", i, wait)
		}
	}
	if wait := budget.take(now); wait != loginRefillInterval {
		t.Fatalf("exhausted budget retry = %v, want %v", wait, loginRefillInterval)
	}
	if wait := budget.take(now.Add(time.Second)); wait != 2*time.Second {
		t.Fatalf("partial refill retry = %v, want 2s", wait)
	}
	if wait := budget.take(now.Add(loginRefillInterval)); wait != 0 {
		t.Fatalf("refilled token rejected: %v", wait)
	}
	if wait := budget.take(now.Add(loginRefillInterval)); wait != loginRefillInterval {
		t.Fatalf("refill admitted more than one attempt: %v", wait)
	}
}

func TestConcurrentLoginsAreRejectedBeforeDatabaseWork(t *testing.T) {
	// A nil store makes any accidental database work fail the test.
	service := &Service{}
	service.loginMu.Lock()
	defer service.loginMu.Unlock()
	var workers sync.WaitGroup
	for i := 0; i < 20; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			_, _, err := service.Login(context.Background(), httptest.NewRequest("POST", "/api/auth/login", nil), "admin", "password")
			var limit *LockoutError
			if !errors.As(err, &limit) || limit.RetryAfterSeconds() != 1 {
				t.Errorf("concurrent login error = %v, want short rate limit", err)
			}
		}()
	}
	workers.Wait()
}

func TestClientIPUsesOnlyNearestTrustedProxyEntry(t *testing.T) {
	cases := []struct {
		name, remote string
		forwarded    []string
		trust        bool
		want         string
	}{
		{"untrusted", "192.0.2.1:5", []string{"198.51.100.1"}, false, "192.0.2.1"},
		{"npm appends", "172.18.0.2:5", []string{"198.51.100.1, 192.0.2.1"}, true, "192.0.2.1"},
		{"multiple headers", "172.18.0.2:5", []string{"198.51.100.1", "192.0.2.1"}, true, "192.0.2.1"},
		{"invalid last entry", "172.18.0.2:5", []string{"198.51.100.1, invalid"}, true, "172.18.0.2"},
		{"missing header", "172.18.0.2:5", nil, true, "172.18.0.2"},
		{"ipv6", "[2001:0db8:0:0::1]:5", nil, false, "2001:db8::1"},
		{"mapped ipv4", "172.18.0.2:5", []string{"::ffff:192.0.2.1"}, true, "192.0.2.1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			service := &Service{}
			service.cfg.TrustProxyHeaders = tc.trust
			request := httptest.NewRequest("POST", "/api/auth/login", nil)
			request.RemoteAddr = tc.remote
			for _, value := range tc.forwarded {
				request.Header.Add("X-Forwarded-For", value)
			}
			if got := service.clientIP(request); got != tc.want {
				t.Fatalf("client IP = %q, want %q", got, tc.want)
			}
		})
	}
}
