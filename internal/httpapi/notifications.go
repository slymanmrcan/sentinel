package httpapi

import (
	"github.com/slymanmrcan/sentinel/internal/auth"
	"github.com/slymanmrcan/sentinel/internal/notify"
	"net/http"
)

// SetNotifications is called only during app construction, before serving.
func (s *Server) SetNotifications(e *notify.Engine) { s.notifications = e }
func (s *Server) handleTelegramStatus(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	status := notify.Status{SSH: "Kapalı"}
	if s.notifications != nil {
		status = s.notifications.Status()
	}
	writeJSON(w, http.StatusOK, status)
}
func (s *Server) handleTelegramTest(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	if p.User.Role != "admin" {
		writeError(w, http.StatusForbidden, "Yönetici yetkisi gerekli")
		return
	}
	if s.notifications == nil || !s.notifications.Test() {
		writeError(w, http.StatusServiceUnavailable, "Telegram kapalı veya bildirim kuyruğu dolu")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"message": "Test isteği alındı; en fazla dakikada bir işlenir. Teslimat durumunu aşağıdan izleyin."})
}
