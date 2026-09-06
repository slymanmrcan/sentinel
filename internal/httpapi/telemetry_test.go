package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/slymanmrcan/sentinel/internal/auth"
	"github.com/slymanmrcan/sentinel/internal/config"
	"github.com/slymanmrcan/sentinel/internal/monitor"
	"github.com/slymanmrcan/sentinel/internal/store"
)

func TestNormalizeLogInput(t *testing.T) {
	tests := []struct {
		name    string
		input   logInput
		want    logInput
		wantErr bool
	}{
		{name: "defaults", input: logInput{Message: " Backup done "}, want: logInput{Level: "INFO", Message: "Backup done", Source: "external"}},
		{name: "normalizes", input: logInput{Level: " warn ", Message: " Disk full ", Source: " worker-1 "}, want: logInput{Level: "WARN", Message: "Disk full", Source: "worker-1"}},
		{name: "unsafe source", input: logInput{Message: "x", Source: `<img onerror=alert(1)>`}, wantErr: true},
		{name: "oversized", input: logInput{Message: strings.Repeat("a", 4097)}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := normalizeLogInput(test.input)
			if test.wantErr {
				if err == nil {
					t.Fatal("normalizeLogInput() error = nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeLogInput() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("normalizeLogInput() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestHealthRejectsMissingCollection(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "health.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := &Server{store: db, collector: monitor.New(db, config.Config{})}
	w := httptest.NewRecorder()
	s.handleHealth(w, httptest.NewRequest("GET", "/healthz", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("health without any sample = %d, want 503", w.Code)
	}
}

func TestSystemServiceLogsEndpointRejectsUnconfiguredUnit(t *testing.T) {
	collector := monitor.New(nil, config.Config{SystemdUnits: []string{"ssh.service"}})
	server := &Server{collector: collector}
	request := httptest.NewRequest(http.MethodGet, "/api/system/services/docker.service/logs", nil)
	request = request.WithContext(context.Background())
	request.SetPathValue("unit", "docker.service")
	recorder := httptest.NewRecorder()

	server.handleSystemServiceLogs(recorder, request, auth.Principal{})
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusNotFound, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "not configured") {
		t.Fatalf("body = %q, want configured-unit rejection", recorder.Body.String())
	}
}
