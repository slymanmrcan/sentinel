package auth

import "time"

const (
	loginBurst          = 10
	loginRefillInterval = 3 * time.Second
)

// loginBudget bounds password work even when callers rotate source IPs.
// It uses constant memory and is guarded by Service.loginMu. The budget resets
// on restart; the longer per-IP failure lockout is stored in the database.
type loginBudget struct {
	updated time.Time
	tokens  float64
}

func (b *loginBudget) take(now time.Time) time.Duration {
	if b.updated.IsZero() {
		b.tokens = loginBurst
	} else {
		elapsed := max(0, now.Sub(b.updated).Seconds())
		b.tokens = min(loginBurst, b.tokens+elapsed/loginRefillInterval.Seconds())
	}
	b.updated = now
	if b.tokens < 1 {
		return max(time.Nanosecond, time.Duration((1-b.tokens)*float64(loginRefillInterval)))
	}
	b.tokens--
	return 0
}
