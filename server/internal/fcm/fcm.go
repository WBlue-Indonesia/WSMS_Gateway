// Package fcm sends Firebase Cloud Messaging high-priority "wake" data messages to
// revive an offline device that has queued work (docs/01 §7, docs/05 §4).
//
// It is OPTIONAL: if WSMS_FCM_CREDENTIALS is unset, New returns (nil, nil) and the
// server runs without wake — devices then rely solely on the foreground service +
// fast reconnect. FCM cannot wake a force-stopped process (amendment F6); such
// devices accumulate Device.WakeMisses and are surfaced in the admin fleet view.
package fcm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/nizwar/wsms-gateway/server/internal/config"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const scope = "https://www.googleapis.com/auth/firebase.messaging"

type Waker struct {
	projectID string
	ts        oauth2.TokenSource
	client    *http.Client
}

// New builds a Waker from a Firebase service-account JSON. Returns (nil, nil) when
// FCM is not configured (feature disabled).
func New(cfg config.Config) (*Waker, error) {
	if cfg.FCMCredentialsFile == "" {
		return nil, nil
	}
	data, err := os.ReadFile(cfg.FCMCredentialsFile)
	if err != nil {
		return nil, fmt.Errorf("read fcm credentials: %w", err)
	}
	jwtCfg, err := google.JWTConfigFromJSON(data, scope)
	if err != nil {
		return nil, fmt.Errorf("parse fcm credentials: %w", err)
	}
	project := cfg.FCMProjectID
	if project == "" {
		var sa struct {
			ProjectID string `json:"project_id"`
		}
		_ = json.Unmarshal(data, &sa)
		project = sa.ProjectID
	}
	if project == "" {
		return nil, fmt.Errorf("fcm project id unknown (set WSMS_FCM_PROJECT_ID)")
	}
	return &Waker{
		projectID: project,
		ts:        jwtCfg.TokenSource(context.Background()),
		client:    &http.Client{Timeout: 10 * time.Second},
	}, nil
}

// Wake sends a high-priority data message telling the device to reconnect its socket.
func (w *Waker) Wake(ctx context.Context, deviceToken string) error {
	return w.SendData(ctx, deviceToken, map[string]string{
		"type": "wake",
		"ts":   fmt.Sprintf("%d", time.Now().Unix()),
	})
}

// SendData delivers a high-priority FCM data message. FCM data values must be strings.
// This is the transport for the push-driven send model: the payload carries the full
// send command (message_id/target/body/subscription_id/...) so a frozen phone is woken
// by Play Services, sends the SMS, and confirms over REST — no persistent socket needed.
func (w *Waker) SendData(ctx context.Context, deviceToken string, data map[string]string) error {
	tok, err := w.ts.Token()
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]any{
		"message": map[string]any{
			"token": deviceToken,
			"android": map[string]any{
				"priority": "high",
			},
			"data": data,
		},
	})
	url := fmt.Sprintf("https://fcm.googleapis.com/v1/projects/%s/messages:send", w.projectID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("fcm send returned %d", resp.StatusCode)
	}
	return nil
}
