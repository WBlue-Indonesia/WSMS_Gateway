// Package config loads runtime configuration from the environment.
package config

import (
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
}

// Load reads .env (if present) then the environment, applying sane defaults.
func Load() Config {
	_ = godotenv.Load()
	return Config{
		HTTPAddr:        env("WSMS_HTTP_ADDR", ":8080"),
		DatabaseURL:     env("WSMS_DATABASE_URL", "postgres://wsms:wsms@localhost:5432/wsms?sslmode=disable"),
		DispatchWorkers: envInt("WSMS_DISPATCH_WORKERS", 4),
		DefaultTTL:      envDur("WSMS_DEFAULT_TTL", 10*time.Minute),
		AckTimeout:      envDur("WSMS_ACK_TIMEOUT", 20*time.Second),
		DeliveryWait:    envDur("WSMS_DELIVERY_WAIT", 3*time.Minute),
		MinRemainingTTL: envDur("WSMS_MIN_REMAINING_TTL", 30*time.Second),
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
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
