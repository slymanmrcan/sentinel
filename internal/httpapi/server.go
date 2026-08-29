package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/slymanmrcan/sentinel/internal/auth"
	"github.com/slymanmrcan/sentinel/internal/config"
	"github.com/slymanmrcan/sentinel/internal/monitor"
	"github.com/slymanmrcan/sentinel/internal/store"
	webassets "github.com/slymanmrcan/sentinel/web"
)

const maxAuthBodyBytes = 4 << 10

type Server struct {
	cfg       config.Config
	store     *store.Store
	auth      *auth.Service
	collector *monitor.Collector
	handler   http.Handler
}

type authedHandler func(http.ResponseWriter, *http.Request, auth.Principal)

func New(cfg config.Config, dataStore *store.Store, authService *auth.Service, collector *monitor.Collector) *Server {
	server := &Server{
		cfg: cfg, store: dataStore, auth: authService, collector: collector,
	}
	server.handler = server.routes()
	return server
}

func (s *Server) HTTPServer() *http.Server {
	return &http.Server{
		Addr:              ":" + s.cfg.Port,
		Handler:           s.handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
}

func (s *Server) Handler() http.Handler {
	return s.handler
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)

	mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/auth/logout", s.requireAuth(s.handleLogout))
	mux.HandleFunc("GET /api/auth/me", s.requireAuth(s.handleMe))
	mux.HandleFunc("POST /api/auth/password", s.requireAuth(s.handleChangePassword))

	mux.HandleFunc("GET /api/metrics/realtime", s.requireAuth(s.handleRealtime))
	mux.HandleFunc("GET /api/metrics/history", s.requireAuth(s.handleHistory))
	mux.HandleFunc("GET /api/metrics/summary", s.requireAuth(s.handleSummary))
	mux.HandleFunc("GET /api/system/details", s.requireAuth(s.handleSystemDetails))
	mux.HandleFunc("GET /api/system/services", s.requireAuth(s.handleSystemServices))
	mux.HandleFunc("GET /api/containers", s.requireAuth(s.handleContainers))
	mux.HandleFunc("PUT /api/containers/settings", s.requireAuth(s.handleContainerSettings))
	mux.HandleFunc("GET /api/logs", s.requireAuth(s.handleLogs))
	mux.HandleFunc("POST /api/logs", s.requireAuth(s.handleLogs))
	mux.HandleFunc("DELETE /api/logs", s.requireAuth(s.handleLogs))
	mux.HandleFunc("GET /api/anomalies", s.requireAuth(s.handleAnomalies))
	mux.HandleFunc("GET /api/alerts/rules", s.requireAuth(s.handleAlertRules))
	mux.HandleFunc("GET /api/alerts/events", s.requireAuth(s.handleAlertEvents))
	mux.HandleFunc("GET /api/export/metrics.csv", s.requireAuth(s.handleExportMetrics))
	mux.HandleFunc("GET /api/export/anomalies.csv", s.requireAuth(s.handleExportAnomalies))
	mux.HandleFunc("GET /api/export/alerts.csv", s.requireAuth(s.handleExportAlerts))
	mux.HandleFunc("GET /api/export/logs.csv", s.requireAuth(s.handleExportLogs))

	mux.Handle("/", s.staticHandler())
	return securityHeaders(requestLogger(mux))
}

func (s *Server) requireAuth(next authedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, err := s.auth.Authenticate(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		if isWrite(r.Method) {
			if !s.auth.ClientOriginAllowed(r) {
				log.Printf("cross-origin request rejected: origin=%q host=%q trust_proxy_headers=%t",
					r.Header.Get("Origin"), r.Host, s.cfg.TrustProxyHeaders)
				writeError(w, http.StatusForbidden, "cross-origin request rejected")
				return
			}
			if !s.auth.ValidCSRF(principal, r.Header.Get("X-CSRF-Token")) {
				writeError(w, http.StatusForbidden, "invalid CSRF token")
				return
			}
		}
		next(w, r, principal)
	}
}

func isWrite(method string) bool {
	return method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
}

func (s *Server) staticHandler() http.Handler {
	fileServer := http.FileServer(http.FS(webassets.Assets))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login", "/login/":
			if _, err := s.auth.Authenticate(r); err == nil {
				http.Redirect(w, r, "/", http.StatusFound)
				return
			}
			clone := r.Clone(r.Context())
			clone.URL.Path = "/login.html"
			fileServer.ServeHTTP(w, clone)
			return
		case "/", "/index.html":
			if _, err := s.auth.Authenticate(r); err != nil {
				http.Redirect(w, r, "/login", http.StatusFound)
				return
			}
		}
		fileServer.ServeHTTP(w, r)
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.store.Health(ctx); err != nil {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("OK"))
}

type loginRequest struct {
	Login    string `json:"login"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.auth.ClientOriginAllowed(r) {
		log.Printf("cross-origin login rejected: origin=%q host=%q trust_proxy_headers=%t",
			r.Header.Get("Origin"), r.Host, s.cfg.TrustProxyHeaders)
		writeError(w, http.StatusForbidden, "cross-origin request rejected")
		return
	}
	var payload loginRequest
	if err := decodeJSON(w, r, maxAuthBodyBytes, &payload); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	login := payload.Login
	if login == "" {
		login = payload.Email
	}
	result, rawToken, err := s.auth.Login(r.Context(), r, login, payload.Password)
	if err != nil {
		var lockout *auth.LockoutError
		switch {
		case errors.As(err, &lockout):
			writeError(w, http.StatusTooManyRequests, lockout.Error())
		case errors.Is(err, auth.ErrInvalidCredentials):
			s.log(r.Context(), "WARN", "Failed sign-in attempt", "auth")
			writeError(w, http.StatusUnauthorized, "invalid login or password")
		default:
			log.Printf("login failed: %v", err)
			writeError(w, http.StatusInternalServerError, "sign-in failed")
		}
		return
	}
	s.auth.SetSessionCookie(w, rawToken)
	s.log(r.Context(), "INFO", fmt.Sprintf("User %s signed in", result.User.Login), "auth")
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request, principal auth.Principal) {
	if err := s.auth.Logout(r.Context(), principal.TokenHash); err != nil {
		log.Printf("logout session delete failed: %v", err)
	}
	s.auth.ClearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleMe(w http.ResponseWriter, _ *http.Request, principal auth.Principal) {
	writeJSON(w, http.StatusOK, map[string]any{
		"user": principal.User, "csrf_token": principal.CSRFToken,
	})
}

type passwordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request, principal auth.Principal) {
	var payload passwordRequest
	if err := decodeJSON(w, r, maxAuthBodyBytes, &payload); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.auth.ChangePassword(r.Context(), principal, payload.CurrentPassword, payload.NewPassword); err != nil {
		switch {
		case errors.Is(err, auth.ErrWeakPassword):
			writeError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, auth.ErrInvalidCredentials):
			writeError(w, http.StatusUnauthorized, "current password is incorrect")
		default:
			log.Printf("password change failed: %v", err)
			writeError(w, http.StatusInternalServerError, "password change failed")
		}
		return
	}
	s.auth.ClearSessionCookie(w)
	s.log(r.Context(), "INFO", fmt.Sprintf("Password changed for %s", principal.User.Login), "auth")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, maxBytes int64, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("invalid request payload")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain a single JSON object")
	}
	return nil
}

func (s *Server) log(ctx context.Context, level, message, source string) {
	consoleMessage := strings.NewReplacer("\r", `\r`, "\n", `\n`).Replace(message)
	log.Printf("[%s] [%s] %s", level, source, consoleMessage)
	if err := s.store.InsertLog(ctx, store.LogEntry{
		Timestamp: time.Now(), Level: level, Message: message, Source: source,
	}); err != nil {
		log.Printf("database log insert failed: %v", err)
	}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		log.Printf("JSON response failed: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		if r.URL.Path == "/" || r.URL.Path == "/login" || strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("[%s] %s %s - %v", r.Method, r.URL.Path, r.RemoteAddr, time.Since(start))
	})
}

var _ fs.FS = webassets.Assets
