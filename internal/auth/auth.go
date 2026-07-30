package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/slymanmrcan/sentinel/internal/config"
	"github.com/slymanmrcan/sentinel/internal/store"
	"golang.org/x/crypto/bcrypt"
)

const (
	CookieName       = "sentinel_session"
	minPasswordRunes = 8
	maxPasswordBytes = 72
	maxLoginFailures = 5
	loginLockout     = 15 * time.Minute
)

var dummyHash = []byte("$2a$10$7EqJtq98hPqEX7fNZaFWoO5YtO6YQYQzUqFQfYyJqT0pM6GCrh/7y")

type Service struct {
	store *store.Store
	cfg   config.Config
}

type Principal struct {
	User      store.User
	CSRFToken string
	TokenHash string
}

type LoginResult struct {
	User      store.User `json:"user"`
	CSRFToken string     `json:"csrf_token"`
}

func New(ctx context.Context, dataStore *store.Store, cfg config.Config) (*Service, error) {
	service := &Service{store: dataStore, cfg: cfg}
	if err := service.bootstrapAdmin(ctx); err != nil {
		return nil, err
	}
	if err := dataStore.DeleteExpiredSessions(ctx); err != nil {
		return nil, fmt.Errorf("prune expired sessions: %w", err)
	}
	return service, nil
}

func (s *Service) bootstrapAdmin(ctx context.Context) error {
	count, err := s.store.UserCount(ctx)
	if err != nil {
		return fmt.Errorf("count users: %w", err)
	}
	if count == 0 && !validPasswordLength(s.cfg.AdminPassword) {
		return errors.New("first startup requires ADMIN_PASSWORD (or legacy AUTH_PASSWORD) with at least 8 characters and at most 72 bytes")
	}
	if count > 0 && s.cfg.AdminPassword == "" {
		return nil
	}
	if !validPasswordLength(s.cfg.AdminPassword) {
		return errors.New("configured ADMIN_PASSWORD (or legacy AUTH_PASSWORD) must have at least 8 characters and at most 72 bytes")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(s.cfg.AdminPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash bootstrap password: %w", err)
	}
	user := store.User{
		ID:           randomHex(16),
		Login:        strings.ToLower(strings.TrimSpace(s.cfg.AdminLogin)),
		Name:         strings.TrimSpace(s.cfg.AdminName),
		Role:         "admin",
		PasswordHash: string(hash),
		CreatedAt:    time.Now(),
	}
	if user.Login == "" {
		return errors.New("ADMIN_LOGIN, ADMIN_EMAIL, or AUTH_USER is required")
	}
	if count == 0 {
		if err := s.store.CreateUser(ctx, user); err != nil {
			return fmt.Errorf("create bootstrap admin: %w", err)
		}
		return nil
	}

	existing, err := s.store.UserByLogin(ctx, user.Login)
	if store.IsNotFound(err) {
		existing, err = s.store.FirstAdmin(ctx)
	}
	if err != nil {
		return fmt.Errorf("load bootstrap admin: %w", err)
	}

	passwordChanged := bcrypt.CompareHashAndPassword(
		[]byte(existing.PasswordHash),
		[]byte(s.cfg.AdminPassword),
	) != nil
	profileChanged := existing.Login != user.Login || existing.Name != user.Name
	if !passwordChanged && !profileChanged {
		return nil
	}
	if !passwordChanged {
		user.PasswordHash = existing.PasswordHash
	}
	if err := s.store.UpdateBootstrapAdmin(
		ctx,
		existing.ID,
		user.Login,
		user.Name,
		user.PasswordHash,
	); err != nil {
		return fmt.Errorf("synchronize bootstrap admin: %w", err)
	}
	if passwordChanged {
		if err := s.store.DeleteUserSessions(ctx, existing.ID); err != nil {
			return fmt.Errorf("revoke sessions after bootstrap password sync: %w", err)
		}
	}
	return nil
}

func (s *Service) Login(ctx context.Context, r *http.Request, login, password string) (LoginResult, string, error) {
	login = strings.ToLower(strings.TrimSpace(login))
	if login == "" || password == "" {
		return LoginResult{}, "", ErrInvalidCredentials
	}

	identifier := s.clientIP(r) + ":" + login
	remaining, err := s.store.LockoutRemaining(ctx, identifier)
	if err != nil {
		return LoginResult{}, "", fmt.Errorf("read login lockout: %w", err)
	}
	if remaining > 0 {
		return LoginResult{}, "", &LockoutError{Remaining: remaining}
	}

	user, lookupErr := s.store.UserByLogin(ctx, login)
	hash := dummyHash
	if lookupErr == nil {
		hash = []byte(user.PasswordHash)
	}
	if bcrypt.CompareHashAndPassword(hash, []byte(password)) != nil || lookupErr != nil {
		_ = s.store.RegisterLoginFailure(ctx, identifier, maxLoginFailures, loginLockout)
		return LoginResult{}, "", ErrInvalidCredentials
	}
	if err := s.store.ClearLoginFailures(ctx, identifier); err != nil {
		return LoginResult{}, "", fmt.Errorf("clear login failures: %w", err)
	}

	rawToken := randomHex(32)
	csrfToken := randomHex(32)
	session := store.Session{
		TokenHash: hashToken(rawToken),
		CSRFToken: csrfToken,
		User:      user,
		ExpiresAt: time.Now().Add(s.cfg.SessionTTL),
	}
	if err := s.store.CreateSession(ctx, session); err != nil {
		return LoginResult{}, "", fmt.Errorf("create session: %w", err)
	}
	return LoginResult{User: safeUser(user), CSRFToken: csrfToken}, rawToken, nil
}

func (s *Service) Authenticate(r *http.Request) (Principal, error) {
	cookie, err := r.Cookie(CookieName)
	if err != nil || cookie.Value == "" {
		return Principal{}, ErrUnauthenticated
	}
	tokenHash := hashToken(cookie.Value)
	session, err := s.store.SessionByTokenHash(r.Context(), tokenHash)
	if err != nil {
		return Principal{}, ErrUnauthenticated
	}
	return Principal{
		User:      safeUser(session.User),
		CSRFToken: session.CSRFToken,
		TokenHash: tokenHash,
	}, nil
}

func (s *Service) Logout(ctx context.Context, tokenHash string) error {
	if tokenHash == "" {
		return nil
	}
	return s.store.DeleteSession(ctx, tokenHash)
}

func (s *Service) ChangePassword(ctx context.Context, principal Principal, currentPassword, newPassword string) error {
	if !validPasswordLength(newPassword) {
		return ErrWeakPassword
	}
	user, err := s.store.UserByID(ctx, principal.User.ID)
	if err != nil {
		return ErrUnauthenticated
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(currentPassword)) != nil {
		return ErrInvalidCredentials
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash new password: %w", err)
	}
	if err := s.store.UpdatePassword(ctx, user.ID, string(hash)); err != nil {
		return fmt.Errorf("update password: %w", err)
	}
	return s.store.DeleteUserSessions(ctx, user.ID)
}

func (s *Service) SetSessionCookie(w http.ResponseWriter, rawToken string) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    rawToken,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.cfg.CookieSecure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(s.cfg.SessionTTL.Seconds()),
	})
}

func (s *Service) ClearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.cfg.CookieSecure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
}

func (s *Service) ValidCSRF(principal Principal, provided string) bool {
	expected := []byte(principal.CSRFToken)
	actual := []byte(provided)
	if len(expected) == 0 || len(expected) != len(actual) {
		return false
	}
	return subtle.ConstantTimeCompare(expected, actual) == 1
}

func (s *Service) ClientOriginAllowed(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	parsedOrigin, err := url.Parse(origin)
	if err != nil || (parsedOrigin.Scheme != "http" && parsedOrigin.Scheme != "https") ||
		parsedOrigin.Host == "" || parsedOrigin.User != nil ||
		(parsedOrigin.Path != "" && parsedOrigin.Path != "/") ||
		parsedOrigin.RawQuery != "" || parsedOrigin.Fragment != "" {
		return false
	}
	normalizedOrigin := strings.ToLower(parsedOrigin.Scheme + "://" + parsedOrigin.Host)
	for _, allowed := range s.cfg.AllowedOrigins {
		if normalizedOrigin == allowed {
			return true
		}
	}

	expectedScheme := "http"
	if r.TLS != nil {
		expectedScheme = "https"
	}
	expectedHost := r.Host
	if s.cfg.TrustProxyHeaders {
		if forwardedHost := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Host"), ",")[0]); forwardedHost != "" {
			expectedHost = forwardedHost
		}
		if forwardedProto := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]); forwardedProto != "" {
			expectedScheme = forwardedProto
		}
		return strings.EqualFold(normalizedOrigin, expectedScheme+"://"+expectedHost)
	}

	if !strings.EqualFold(parsedOrigin.Host, expectedHost) {
		return false
	}
	// A TLS-terminating reverse proxy may preserve Host while the upstream
	// request itself is plain HTTP. In that case host equality still blocks a
	// cross-site browser request without trusting spoofable proxy headers.
	return r.TLS == nil || strings.EqualFold(parsedOrigin.Scheme, expectedScheme)
}

func (s *Service) clientIP(r *http.Request) string {
	if s.cfg.TrustProxyHeaders {
		if forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0]); forwarded != "" {
			return forwarded
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

func safeUser(user store.User) store.User {
	user.PasswordHash = ""
	return user
}

func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func validPasswordLength(password string) bool {
	return utf8.RuneCountInString(password) >= minPasswordRunes && len(password) <= maxPasswordBytes
}

func randomHex(bytes int) string {
	buffer := make([]byte, bytes)
	if _, err := rand.Read(buffer); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(buffer)
}

var (
	ErrUnauthenticated    = errors.New("authentication required")
	ErrInvalidCredentials = errors.New("invalid login or password")
	ErrWeakPassword       = errors.New("new password must be at least 8 characters and at most 72 bytes")
)

type LockoutError struct {
	Remaining time.Duration
}

func (e *LockoutError) Error() string {
	return fmt.Sprintf("too many failed attempts; try again in %d minute(s)", int(e.Remaining.Minutes())+1)
}
