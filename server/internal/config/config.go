// Package config loads runtime configuration from the environment.
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	HTTPAddr        string
	DatabaseURL     string
	DispatchWorkers int
	DefaultTTL      time.Duration // amendment F7: short default so stale OTPs are not delivered
	AckTimeout      time.Duration // how long to wait for send_ack before treating as ambiguous
	DeliveryWait    time.Duration // how long to wait for a delivery report before SENT_UNCONFIRMED
	MinRemainingTTL time.Duration // F7: drop at dispatch if remaining TTL below this floor

	// MasterKey (32 bytes) encrypts stored signing/webhook secrets at rest (F3).
	MasterKey    [32]byte
	MasterKeyDev bool // true when a derived dev key is used (no WSMS_SECRET_KEY set)

	WebhookWorkers     int
	WebhookMaxAttempts int

	// Per-client submit rate limit (protects the fleet from a runaway client).
	RatePerSec float64
	RateBurst  int

	// RetentionDays purges messages/events/webhooks older than this (PII, docs/06 §2.6).
	RetentionDays int

	// FCM wake (optional): path to a Firebase service-account JSON + project id.
	FCMCredentialsFile string
	FCMProjectID       string

	// PublicURL is the externally-reachable base URL devices should connect to
	// (used in the pairing QR). If empty, it is derived from the admin request host.
	PublicURL string
}

// Load reads .env (if present) then the environment, applying sane defaults.
func Load() Config {
	_ = godotenv.Load()
	c := Config{
		HTTPAddr:           env("WSMS_HTTP_ADDR", ":8080"),
		DatabaseURL:        env("WSMS_DATABASE_URL", "postgres://wsms:wsms@localhost:5432/wsms?sslmode=disable"),
		DispatchWorkers:    envInt("WSMS_DISPATCH_WORKERS", 4),
		DefaultTTL:         envDur("WSMS_DEFAULT_TTL", 10*time.Minute),
		AckTimeout:         envDur("WSMS_ACK_TIMEOUT", 20*time.Second),
		DeliveryWait:       envDur("WSMS_DELIVERY_WAIT", 3*time.Minute),
		MinRemainingTTL:    envDur("WSMS_MIN_REMAINING_TTL", 30*time.Second),
		WebhookWorkers:     envInt("WSMS_WEBHOOK_WORKERS", 2),
		WebhookMaxAttempts: envInt("WSMS_WEBHOOK_MAX_ATTEMPTS", 6),
		RatePerSec:         envFloat("WSMS_RATE_PER_SEC", 5),
		RateBurst:          envInt("WSMS_RATE_BURST", 10),
		RetentionDays:      envInt("WSMS_MSG_RETENTION_DAYS", 30),
		FCMCredentialsFile: os.Getenv("WSMS_FCM_CREDENTIALS"),
		FCMProjectID:       os.Getenv("WSMS_FCM_PROJECT_ID"),
		PublicURL:          os.Getenv("WSMS_PUBLIC_URL"),
	}
	if raw := os.Getenv("WSMS_SECRET_KEY"); len(raw) == 64 {
		if b, err := hex.DecodeString(raw); err == nil {
			copy(c.MasterKey[:], b)
		} else {
			c.MasterKey = deriveDevKey()
			c.MasterKeyDev = true
		}
	} else {
		c.MasterKey = deriveDevKey()
		c.MasterKeyDev = true
	}
	return c
}

// deriveDevKey produces a deterministic insecure key for local dev when
// WSMS_SECRET_KEY is not set. Secrets encrypted with it will not decrypt after a
// key change, so production MUST set WSMS_SECRET_KEY (64 hex chars).
func deriveDevKey() [32]byte {
	return sha256.Sum256([]byte("wsms-dev-insecure-master-key"))
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envFloat(k string, def float64) float64 {
	if v := os.Getenv(k); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

func envInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envDur(k string, def time.Duration) time.Duration {
	if v := os.Getenv(k); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
