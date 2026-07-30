package httpapi

import (
	"strings"
	"testing"
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
