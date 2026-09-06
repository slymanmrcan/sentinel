package httpapi

import (
	"encoding/csv"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/slymanmrcan/sentinel/internal/auth"
	"github.com/slymanmrcan/sentinel/internal/monitor"
	"github.com/slymanmrcan/sentinel/internal/store"
)

const maxLogBodyBytes = 16 << 10

var logSourcePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/ -]{0,63}$`)

type logInput struct {
	Level   string `json:"level"`
	Message string `json:"message"`
	Source  string `json:"source"`
}

func (s *Server) handleRealtime(w http.ResponseWriter, _ *http.Request, _ auth.Principal) {
	writeJSON(w, http.StatusOK, s.collector.Current())
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request, _ auth.Principal) {
	metrics, err := s.store.History(r.Context(), r.URL.Query().Get("range"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load metric history")
		return
	}
	writeJSON(w, http.StatusOK, metrics)
}

func (s *Server) handleSummary(w http.ResponseWriter, r *http.Request, _ auth.Principal) {
	baselines, err := s.store.Baselines(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load statistical baseline")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"baselines": baselines})
}

func (s *Server) handleSystemDetails(w http.ResponseWriter, _ *http.Request, _ auth.Principal) {
	writeJSON(w, http.StatusOK, s.collector.SystemDetails())
}

func (s *Server) handleSystemServices(w http.ResponseWriter, r *http.Request, _ auth.Principal) {
	writeJSON(w, http.StatusOK, s.collector.SystemServices(r.Context()))
}

func (s *Server) handleSystemServiceLogs(w http.ResponseWriter, r *http.Request, _ auth.Principal) {
	logs, err := s.collector.SystemServiceLogs(r.Context(), r.PathValue("unit"))
	if err != nil {
		if errors.Is(err, monitor.ErrSystemdUnitNotConfigured) {
			writeError(w, http.StatusNotFound, "systemd unit is not configured")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load systemd journal")
		return
	}
	writeJSON(w, http.StatusOK, logs)
}

func (s *Server) handleContainers(w http.ResponseWriter, _ *http.Request, _ auth.Principal) {
	writeJSON(w, http.StatusOK, s.collector.Containers())
}

type containerSettingsInput struct {
	IntervalSeconds int `json:"interval_seconds"`
}

func (s *Server) handleContainerSettings(w http.ResponseWriter, r *http.Request, _ auth.Principal) {
	var input containerSettingsInput
	if err := decodeJSON(w, r, maxLogBodyBytes, &input); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.collector.SetContainerInterval(r.Context(), input.IntervalSeconds); err != nil {
		if errors.Is(err, monitor.ErrInvalidContainerInterval) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to save container interval")
		return
	}
	s.log(r.Context(), "INFO", fmt.Sprintf("Container collection interval changed to %ds", input.IntervalSeconds), "monitor")
	writeJSON(w, http.StatusOK, s.collector.Containers())
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request, _ auth.Principal) {
	switch r.Method {
	case http.MethodGet:
		entries, err := s.store.Logs(r.Context(),
			strings.ToUpper(r.URL.Query().Get("level")),
			r.URL.Query().Get("query"), 100)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to load events")
			return
		}
		writeJSON(w, http.StatusOK, entries)
	case http.MethodPost:
		var entry logInput
		if err := decodeJSON(w, r, maxLogBodyBytes, &entry); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		normalized, err := normalizeLogInput(entry)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.log(r.Context(), normalized.Level, normalized.Message, normalized.Source)
		w.WriteHeader(http.StatusCreated)
	case http.MethodDelete:
		if err := s.store.ClearLogs(r.Context()); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to clear events")
			return
		}
		s.log(r.Context(), "INFO", "Event history cleared by user", "system")
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func normalizeLogInput(entry logInput) (logInput, error) {
	entry.Level = strings.ToUpper(strings.TrimSpace(entry.Level))
	entry.Message = strings.TrimSpace(entry.Message)
	entry.Source = strings.TrimSpace(entry.Source)
	if entry.Level == "" {
		entry.Level = "INFO"
	}
	switch entry.Level {
	case "INFO", "WARN", "ERROR":
	default:
		return logInput{}, fmt.Errorf("level must be INFO, WARN, or ERROR")
	}
	if entry.Message == "" {
		return logInput{}, fmt.Errorf("message is required")
	}
	if utf8.RuneCountInString(entry.Message) > 4096 {
		return logInput{}, fmt.Errorf("message must be at most 4096 characters")
	}
	if entry.Source == "" {
		entry.Source = "external"
	}
	if !logSourcePattern.MatchString(entry.Source) {
		return logInput{}, fmt.Errorf("source contains unsupported characters")
	}
	return entry, nil
}

func (s *Server) handleAnomalies(w http.ResponseWriter, r *http.Request, _ auth.Principal) {
	anomalies, err := s.store.Anomalies(r.Context(), 100)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load anomalies")
		return
	}
	writeJSON(w, http.StatusOK, anomalies)
}

func (s *Server) handleAlertRules(w http.ResponseWriter, r *http.Request, _ auth.Principal) {
	rules, err := s.store.AlertRules(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load alert rules")
		return
	}
	writeJSON(w, http.StatusOK, rules)
}

func (s *Server) handleAlertEvents(w http.ResponseWriter, r *http.Request, _ auth.Principal) {
	events, err := s.store.AlertEvents(r.Context(), 100)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load alert events")
		return
	}
	writeJSON(w, http.StatusOK, events)
}

func (s *Server) handleExportMetrics(w http.ResponseWriter, r *http.Request, _ auth.Principal) {
	metrics, err := s.store.History(r.Context(), "7d")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to export metrics")
		return
	}
	writer := csvResponse(w, "sentinel-metrics.csv",
		[]string{"timestamp", "cpu_percent", "memory_percent", "disk_percent", "swap_percent", "load_1", "net_rx_bps", "net_tx_bps"})
	for _, metric := range metrics {
		_ = writer.Write([]string{
			metric.Timestamp.Format(time.RFC3339), metricNumber(metric, "cpu", metric.CPUPercent), metricNumber(metric, "memory", metric.RAMPercent),
			metricNumber(metric, "disk", metric.DiskPercent), metricNumber(metric, "swap", metric.SwapPercent), metricNumber(metric, "load", metric.Load1),
			metricNumber(metric, "network", metric.NetRxBps), metricNumber(metric, "network", metric.NetTxBps),
		})
	}
	writer.Flush()
}

func (s *Server) handleExportAnomalies(w http.ResponseWriter, r *http.Request, _ auth.Principal) {
	anomalies, err := s.store.Anomalies(r.Context(), 500)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to export anomalies")
		return
	}
	writer := csvResponse(w, "sentinel-anomalies.csv",
		[]string{"timestamp", "metric", "severity", "value", "baseline_mean", "z_score", "message"})
	for _, anomaly := range anomalies {
		_ = writer.Write([]string{
			anomaly.Timestamp.Format(time.RFC3339), anomaly.Metric, anomaly.Severity,
			number(anomaly.Value), number(anomaly.BaselineMean), number(anomaly.ZScore), anomaly.Message,
		})
	}
	writer.Flush()
}

func (s *Server) handleExportAlerts(w http.ResponseWriter, r *http.Request, _ auth.Principal) {
	events, err := s.store.AlertEvents(r.Context(), 500)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to export alerts")
		return
	}
	writer := csvResponse(w, "sentinel-alerts.csv",
		[]string{"timestamp", "rule", "metric", "severity", "value", "message"})
	for _, event := range events {
		_ = writer.Write([]string{
			event.Timestamp.Format(time.RFC3339), event.RuleName, event.Metric,
			event.Severity, number(event.Value), event.Message,
		})
	}
	writer.Flush()
}

func (s *Server) handleExportLogs(w http.ResponseWriter, r *http.Request, _ auth.Principal) {
	entries, err := s.store.Logs(r.Context(), "ALL", "", 500)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to export events")
		return
	}
	writer := csvResponse(w, "sentinel-events.csv",
		[]string{"timestamp", "level", "source", "message"})
	for _, entry := range entries {
		_ = writer.Write([]string{
			entry.Timestamp.Format(time.RFC3339), entry.Level, entry.Source, entry.Message,
		})
	}
	writer.Flush()
}

func csvResponse(w http.ResponseWriter, filename string, header []string) *csv.Writer {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	writer := csv.NewWriter(w)
	if err := writer.Write(header); err != nil {
		log.Printf("CSV header write failed: %v", err)
	}
	return writer
}

func number(value float64) string {
	return strconv.FormatFloat(value, 'f', 4, 64)
}

func metricNumber(metric store.Metric, field string, value float64) string {
	for _, missing := range metric.Unavailable {
		if field == missing {
			return ""
		}
	}
	return number(value)
}
