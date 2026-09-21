package notify

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/slymanmrcan/sentinel/internal/store"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestTelegramResponsesAndSafeErrors(t *testing.T) {
	for _, tc := range []struct {
		name          string
		code          int
		body          string
		ok, permanent bool
		after         time.Duration
	}{
		{"success", 200, `{"ok":true}`, true, false, 0},
		{"rate limit", 429, `{"ok":false,"parameters":{"retry_after":73}}`, false, false, 73 * time.Second},
		{"bad token", 401, `{"ok":false,"description":"TOKEN_MUST_NOT_LEAK"}`, false, true, 0},
		{"bad chat", 400, `{"ok":false}`, false, true, 0},
		{"server down", 503, `unavailable`, false, false, 0},
		{"invalid response", 200, `{`, false, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sender := newSender("123:secret", "42")
			sender.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.Method != "POST" {
					t.Fatal("not POST")
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if len([]rune(body["text"].(string))) > messageLimit || body["chat_id"] != "42" {
					t.Fatal("payload limits")
				}
				return &http.Response{StatusCode: tc.code, Body: io.NopCloser(strings.NewReader(tc.body)), Header: make(http.Header)}, nil
			})
			result := sender.send(context.Background(), message{ID: "1", Text: strings.Repeat("ğ", 5000)})
			if result.OK != tc.ok || result.Permanent != tc.permanent || result.After != tc.after {
				t.Fatalf("result %+v", result)
			}
			if strings.Contains(result.Error, "secret") || strings.Contains(result.Error, "TOKEN") {
				t.Fatal("secret leaked")
			}
		})
	}
	sender := newSender("123:secret", "42")
	sender.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("https://api.telegram.org/bot123:secret/sendMessage")
	})
	r := sender.send(context.Background(), message{})
	if r.OK || r.Permanent || strings.Contains(r.Error, "secret") || strings.Contains(r.Error, "http") {
		t.Fatalf("unsafe network error: %+v", r)
	}
}
func TestRetryBackoffGlobal429AndPermanentDiscard(t *testing.T) {
	e := testEngine()
	e.enqueue("one", "one", 2, epoch, 0)
	e.enqueue("two", "two", 2, epoch, 0)
	id := e.p.Queue[0].ID
	e.p.Queue[0].Attempts = 1
	e.complete(delivery{ID: id, After: 73 * time.Second, Error: "429"}, epoch)
	if e.p.NextSend != epoch.Add(73*time.Second) || e.p.Queue[0].Next != e.p.NextSend {
		t.Fatal("429 not global")
	}
	e.p.Queue[0].Attempts = 2
	e.complete(delivery{ID: id, Error: "timeout"}, epoch)
	if e.p.Queue[0].Next != epoch.Add(20*time.Second) {
		t.Fatal("no exponential backoff")
	}
	e.complete(delivery{ID: id, Permanent: true, Error: "rejected"}, epoch)
	if len(e.p.Queue) != 1 || e.p.LastFailure != epoch {
		t.Fatal("permanent error retried")
	}
	e.p.Queue[0].Attempts = 6
	e.complete(delivery{ID: e.p.Queue[0].ID, Error: "timeout"}, epoch)
	if len(e.p.Queue) != 0 {
		t.Fatal("infinite retries")
	}
}
func TestDeliveryWorkerDoesNotBlockIngressAndRestartRetainsPending(t *testing.T) {
	e := testEngine()
	e.cfg.Times = nil
	started := make(chan struct{})
	db := e.db
	e.sender.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		close(started)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); e.Run(ctx) }()
	if !e.Test() {
		t.Fatal("test not accepted")
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("worker did not start")
	}
	// A network request stays blocked while producers continue with bounded work.
	for i := 0; i < 1000; i++ {
		e.LoginFailure("192.0.2.1", "admin", false)
		e.Observe(store.Metric{}, nil)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("worker shutdown blocked")
	}
	restarted := New(testConfig(), db, &fakeSource{})
	if !restarted.load(context.Background()) || len(restarted.p.Queue) == 0 || restarted.p.Queue[0].Attempts != 1 {
		t.Fatal("pending attempt lost on restart")
	}
}

func TestFinal429StillDelaysOtherMessages(t *testing.T) {
	e := testEngine()
	e.enqueue("one", "one", 2, epoch, 0)
	e.p.Queue[0].Attempts = 6
	e.complete(delivery{ID: e.p.Queue[0].ID, After: time.Hour, Error: "429"}, epoch)
	if len(e.p.Queue) != 0 || e.p.NextSend != epoch.Add(time.Hour) {
		t.Fatal("terminal attempt bypassed global retry_after")
	}
}

func TestSenderTimeoutIsBoundedAndSanitized(t *testing.T) {
	sender := newSender("123:private", "42")
	sender.client.Timeout = 10 * time.Millisecond
	sender.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() })
	start := time.Now()
	result := sender.send(context.Background(), message{Text: "test"})
	if result.OK || result.Permanent || time.Since(start) > time.Second || strings.Contains(result.Error, "private") {
		t.Fatalf("timeout result %+v", result)
	}
}
