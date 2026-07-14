// Package maintenance runs periodic housekeeping: PII retention purge (docs/06 §2.6).
package maintenance

import (
	"context"
	"log/slog"
	"time"

	"github.com/nizwar/wsms-gateway/server/internal/config"
	"gorm.io/gorm"
)

type Retention struct {
	db  *gorm.DB
	cfg config.Config
}

func New(db *gorm.DB, cfg config.Config) *Retention {
	return &Retention{db: db, cfg: cfg}
}

// Run purges message content past the retention window every 6h. Admin audit rows
// are intentionally NOT purged here — they are kept longer for compliance.
func (r *Retention) Run(ctx context.Context) {
	if r.cfg.RetentionDays <= 0 {
		slog.Info("retention purge disabled")
		return
	}
	r.purge(ctx) // once at startup
	t := time.NewTicker(6 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.purge(ctx)
		}
	}
}

func (r *Retention) purge(ctx context.Context) {
	cutoff := time.Now().AddDate(0, 0, -r.cfg.RetentionDays)
	// Children first (no hard FKs, but keep it clean), then messages.
	r.db.WithContext(ctx).Exec(
		"DELETE FROM message_events WHERE message_id IN (SELECT id FROM messages WHERE created_at < ?)", cutoff)
	r.db.WithContext(ctx).Exec(
		"DELETE FROM webhook_deliveries WHERE message_id IN (SELECT id FROM messages WHERE created_at < ?)", cutoff)
	r.db.WithContext(ctx).Exec(
		"DELETE FROM message_sends WHERE message_id IN (SELECT id FROM messages WHERE created_at < ?)", cutoff)
	res := r.db.WithContext(ctx).Exec("DELETE FROM messages WHERE created_at < ?", cutoff)
	if res.RowsAffected > 0 {
		slog.Info("retention purge", "deleted_messages", res.RowsAffected, "older_than", cutoff.Format(time.DateOnly))
	}
}
