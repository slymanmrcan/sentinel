package notify

import (
	"context"
	"time"
)

func (e *Engine) checkSSH(ctx context.Context, now time.Time) {
	if !e.loaded {
		return
	}
	batch := e.ssh.Read(ctx, e.p.SSHCursor, now)
	if batch.Cursor != e.p.SSHCursor {
		e.p.SSHCursor = batch.Cursor
		e.dirty = true
	}
	e.statusMu.Lock()
	e.status.SSH = batch.Status
	e.statusMu.Unlock()
	for _, f := range batch.Failures {
		e.failure(failure{At: f.At, IP: f.IP, Account: f.Account, Target: "SSH (isteğe bağlı journal takibi)"})
	}
}
