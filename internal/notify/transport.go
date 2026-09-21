package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const messageLimit = 3500

type delivery struct {
	ID            string
	OK, Permanent bool
	After         time.Duration
	Error         string
}

type telegramSender struct {
	client           *http.Client
	endpoint, chatID string
}

func newSender(token, chatID string) *telegramSender {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 2
	transport.MaxIdleConnsPerHost = 1
	transport.MaxConnsPerHost = 1
	return &telegramSender{client: &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, endpoint: "https://api.telegram.org/bot" + token + "/sendMessage", chatID: chatID}
}

func (s *telegramSender) send(ctx context.Context, m message) delivery {
	result := delivery{ID: m.ID}
	body, _ := json.Marshal(map[string]any{"chat_id": s.chatID, "text": clip(m.Text, messageLimit), "link_preview_options": map[string]bool{"is_disabled": true}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(body))
	if err != nil {
		result.Permanent = true
		result.Error = "Telegram isteği oluşturulamadı"
		return result
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := s.client.Do(req)
	if err != nil {
		result.Error = "Telegram bağlantısı başarısız veya zaman aşımı (teslimat belirsiz)"
		return result
	}
	defer func() { _ = response.Body.Close() }()
	var payload struct {
		OK         bool `json:"ok"`
		Code       int  `json:"error_code"`
		Parameters struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	err = json.NewDecoder(io.LimitReader(response.Body, 16<<10)).Decode(&payload)
	if err == nil && response.StatusCode == 200 && payload.OK {
		result.OK = true
		return result
	}
	code := response.StatusCode
	if payload.Code != 0 {
		code = payload.Code
	}
	result.Error = fmt.Sprintf("Telegram gönderimi reddedildi (kod %d)", code)
	if code == 429 {
		result.Error = "Telegram hız sınırı (429)"
		result.After = time.Duration(min(max(payload.Parameters.RetryAfter, 1), 86400)) * time.Second
	} else if code >= 400 && code < 500 {
		result.Permanent = true
	}
	return result
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
