// meshalerts_notify.go — TASK 3 delivery: Telegram bot + generic webhook.
// Both are plain HTTPS POSTs; the Telegram token/chat and the webhook
// URL are stored SEALED (AES-GCM via the per-install key, the same
// secretbox the pasarguard credentials use) and never logged.

package panel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"
)

var meshAlertHTTPClient = &http.Client{Timeout: 10 * time.Second}

// meshTelegramNotifier posts to the Bot API sendMessage.
type meshTelegramNotifier struct {
	token string
	chat  string
}

func (t *meshTelegramNotifier) Name() string { return "telegram" }

func (t *meshTelegramNotifier) Send(ev meshAlertEvent) error {
	msg := fmt.Sprintf("[Quantum %s] %s", ev.Sev, ev.Text)
	if ev.Recovery {
		msg = "[Quantum OK] " + ev.Text
	}
	body, _ := json.Marshal(map[string]string{
		"chat_id": t.chat, "text": msg,
	})
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", t.token)
	resp, err := meshAlertHTTPClient.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		log.Printf("[alerts] telegram send failed: %v", err)
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		log.Printf("[alerts] telegram returned %d", resp.StatusCode)
	}
	return nil
}

// meshWebhookNotifier posts the event as JSON {rule, target, severity, text,
// recovery, at} — a generic integration point (ntfy, Discord bridges,
// in-house receivers).
type meshWebhookNotifier struct {
	url string
}

func (w *meshWebhookNotifier) Name() string { return "webhook" }

func (w *meshWebhookNotifier) Send(ev meshAlertEvent) error {
	body, _ := json.Marshal(map[string]any{
		"rule": ev.Rule, "target": ev.Target, "severity": ev.Sev,
		"text": ev.Text, "recovery": ev.Recovery, "at": ev.Now.Unix(),
	})
	resp, err := meshAlertHTTPClient.Post(w.url, "application/json", bytes.NewReader(body))
	if err != nil {
		log.Printf("[alerts] webhook send failed: %v", err)
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		log.Printf("[alerts] webhook returned %d", resp.StatusCode)
	}
	return nil
}

// meshTestNotifier is used by the "send test notification" button.
type meshTestNotifier struct{ hooked *[]meshAlertEvent }

func (t *meshTestNotifier) Name() string { return "test" }
func (t *meshTestNotifier) Send(ev meshAlertEvent) error {
	*t.hooked = append(*t.hooked, ev)
	return nil
}
