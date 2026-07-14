// Package webhook delivers outbound status callbacks to clients. A message is
// notified exactly once when it reaches a terminal state; deliveries retry with
// exponential backoff and are HMAC-signed with the client's webhook secret.
package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"time"

	"github.com/nizwar/wsms-gateway/server/internal/config"
	"github.com/nizwar/wsms-gateway/server/internal/models"
	"github.com/nizwar/wsms-gateway/server/internal/secret"
	"gorm.io/gorm"
)

// terminalStates trigger a webhook. SENT is intermediate and intentionally excluded;
// SENT_UNCONFIRMED (F4) IS terminal and means "left the phone, no delivery report".
var terminalStates = []models.MessageStatus{
	models.MsgDelivered, models.MsgFailed, models.MsgExpired,
	models.MsgCancelled, models.MsgSentUnconfirmed,
}

type Worker struct {
	db     *gorm.DB
	cfg    config.Config
	client *http.Client
}

func New(db *gorm.DB, cfg config.Config) *Worker {
	return &Worker{db: db, cfg: cfg, client: &http.Client{Timeout: 10 * time.Second}}
}

func (w *Worker) Run(ctx context.Context) {
	tick := time.NewTicker(3 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			w.enqueue(ctx)
			w.deliverBatch(ctx)
		}
	}
}

// enqueue creates a pending delivery for any terminal message with a callback URL
// that has not been enqueued yet (unique on message_id makes this idempotent).
func (w *Worker) enqueue(ctx context.Context) {
	var msgs []models.Message
	w.db.WithContext(ctx).
		Where("status IN ? AND callback_url <> '' AND id NOT IN (SELECT message_id FROM webhook_deliveries)", terminalStates).
		Limit(200).Find(&msgs)
	for _, m := range msgs {
		wd := models.WebhookDelivery{
			ID: models.NewID(), MessageID: m.ID, ClientID: m.ClientID, URL: m.CallbackURL,
			Event: m.Status, Status: "pending", MaxAttempts: w.cfg.WebhookMaxAttempts,
			NextAttemptAt: time.Now(),
		}
		w.db.WithContext(ctx).Create(&wd)
	}
}

func (w *Worker) deliverBatch(ctx context.Context) {
	var pending []models.WebhookDelivery
	w.db.WithContext(ctx).
		Where("status = 'pending' AND next_attempt_at <= now()").
		Order("next_attempt_at").Limit(50).Find(&pending)
	for i := range pending {
		w.deliverOne(ctx, &pending[i])
	}
}

// Payload is the webhook body.
type Payload struct {
	Event          string    `json:"event"`
	MessageID      string    `json:"message_id"`
	Status         string    `json:"status"`
	TargetOperator string    `json:"target_operator"`
	Segments       int       `json:"segments"`
	Attempts       int       `json:"attempts"`
	LastError      string    `json:"last_error,omitempty"`
	Timestamp      time.Time `json:"timestamp"`
}

func (w *Worker) deliverOne(ctx context.Context, wd *models.WebhookDelivery) {
	var msg models.Message
	if err := w.db.WithContext(ctx).First(&msg, "id = ?", wd.MessageID).Error; err != nil {
		return
	}
	body, _ := json.Marshal(Payload{
		Event: string(wd.Event), MessageID: msg.ID, Status: string(msg.Status),
		TargetOperator: string(msg.TargetOperator), Segments: msg.Segments,
		Attempts: msg.Attempts, LastError: msg.LastError, Timestamp: time.Now().UTC(),
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, wd.URL, bytes.NewReader(body))
	if err != nil {
		w.fail(ctx, wd, 0, err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "wsms-gateway/webhook")
	req.Header.Set("X-WSMS-Event", string(wd.Event))
	ts := fmt.Sprintf("%d", time.Now().Unix())
	req.Header.Set("X-WSMS-Timestamp", ts)
	if sig := w.sign(wd.ClientID, ts, body); sig != "" {
		req.Header.Set("X-WSMS-Signature", "sha256="+sig)
	}

	resp, err := w.client.Do(req)
	if err != nil {
		w.fail(ctx, wd, 0, err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		now := time.Now()
		w.db.WithContext(ctx).Model(&models.WebhookDelivery{}).Where("id = ?", wd.ID).
			Updates(map[string]any{"status": "sent", "attempts": wd.Attempts + 1, "last_code": resp.StatusCode, "delivered_at": now, "updated_at": now})
		return
	}
	w.fail(ctx, wd, resp.StatusCode, "non-2xx response")
}

func (w *Worker) fail(ctx context.Context, wd *models.WebhookDelivery, code int, reason string) {
	attempts := wd.Attempts + 1
	upd := map[string]any{"attempts": attempts, "last_code": code, "last_error": reason, "updated_at": time.Now()}
	if attempts >= wd.MaxAttempts {
		upd["status"] = "failed"
	} else {
		backoff := time.Duration(math.Min(float64(5*time.Minute), float64(time.Second)*math.Pow(2, float64(attempts))))
		upd["next_attempt_at"] = time.Now().Add(backoff)
	}
	w.db.WithContext(ctx).Model(&models.WebhookDelivery{}).Where("id = ?", wd.ID).Updates(upd)
}

// sign HMACs the payload with the client's webhook secret (decrypted from storage).
// Returns "" if the client has no webhook secret configured (unsigned delivery).
func (w *Worker) sign(clientID, ts string, body []byte) string {
	var cl models.Client
	if w.db.First(&cl, "id = ?", clientID).Error != nil || len(cl.WebhookSecretEnc) == 0 {
		return ""
	}
	key, err := secret.Open(w.cfg.MasterKey[:], cl.WebhookSecretEnc)
	if err != nil {
		return ""
	}
	return secret.HMACSHA256Hex(key, []byte(ts+"."+string(body)))
}
