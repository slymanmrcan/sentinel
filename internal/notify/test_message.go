package notify

import (
	"context"
	"errors"

	"github.com/slymanmrcan/sentinel/internal/config"
)

// SendTest checks delivery using the production transport without opening the
// database or starting collectors. It sends once and waits for Telegram's reply.
// Config must have been validated by config.Load.
func SendTest(ctx context.Context, cfg config.Telegram) error {
	if !cfg.Enabled {
		return errors.New("Telegram kapalı: TELEGRAM_ENABLED=true ayarlayın ve servisi yeniden başlatın")
	}
	sender := newSender(cfg.Token, cfg.ChatID)
	defer sender.client.CloseIdleConnections()
	result := sender.send(ctx, message{Text: "Sentinel: Telegram test bildirimi. Bağlantı testi başarılı."})
	if !result.OK {
		return errors.New(result.Error)
	}
	return nil
}
