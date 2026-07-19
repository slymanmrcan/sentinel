package main

import (
	"strings"
	"testing"
)

func TestNormalizeLogInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   logInput
		want    logInput
		wantErr bool
	}{
		{
			name:  "defaults optional fields",
			input: logInput{Message: "Backup completed"},
			want:  logInput{Level: "INFO", Message: "Backup completed", Source: "external"},
		},
		{
			name:  "normalizes level and whitespace",
			input: logInput{Level: " warn ", Message: " Disk is filling ", Source: " backup-worker "},
			want:  logInput{Level: "WARN", Message: "Disk is filling", Source: "backup-worker"},
		},
		{
			name:    "rejects unsupported level",
			input:   logInput{Level: "DEBUG", Message: "Debug data"},
			wantErr: true,
		},
		{
			name:    "rejects empty message",
			input:   logInput{Message: " \n "},
			wantErr: true,
		},
		{
			name:    "rejects unsafe source",
			input:   logInput{Message: "Attack", Source: `<img src=x onerror=alert(1)>`},
			wantErr: true,
		},
		{
			name:    "rejects oversized message",
			input:   logInput{Message: strings.Repeat("a", 4097)},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := normalizeLogInput(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("normalizeLogInput() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeLogInput() unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("normalizeLogInput() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestCredentialsMatch(t *testing.T) {
	t.Parallel()

	if !credentialsMatch("correct horse", "correct horse") {
		t.Fatal("credentialsMatch() rejected equal values")
	}
	if credentialsMatch("correct horse", "wrong horse") {
		t.Fatal("credentialsMatch() accepted different values")
	}
}
