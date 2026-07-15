package router

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/nizwar/wsms-gateway/server/internal/models"
	"gorm.io/gorm"
)

// ErrNoCapacity means no eligible SIM could be atomically reserved for this message.
var ErrNoCapacity = errors.New("no eligible SIM available")

// ordering selects the ranking of eligible SIMs inside reserve.
type ordering int

const (
	orderLeastLoaded ordering = iota // spread load: least-used, healthiest, LRU first
	orderRandom                      // pick a random eligible SIM
)

// fallbackConfig is the off-net routing policy, cached atomically so a running
// dispatcher picks up admin changes without a per-message DB read.
type fallbackConfig struct {
	mode models.FallbackMode
	op   models.Operator
}

// Engine selects a SIM for a message and reserves its quota atomically.
type Engine struct {
	db       *gorm.DB
	prefixes map[string]models.Operator
	fb       atomic.Pointer[fallbackConfig]
}

func New(db *gorm.DB, prefixes map[string]models.Operator) *Engine {
	if prefixes == nil {
		prefixes = DefaultPrefixes
	}
	e := &Engine{db: db, prefixes: prefixes}
	e.fb.Store(&fallbackConfig{mode: models.FallbackLeastLoaded, op: models.OpUnknown})
	e.loadFallback()
	return e
}

// loadFallback reads the persisted routing fallback setting into the atomic cache.
func (e *Engine) loadFallback() {
	var s models.AppSettings
	if err := e.db.Where("id = ?", models.SettingsID).First(&s).Error; err == nil {
		e.fb.Store(&fallbackConfig{mode: s.FallbackMode, op: s.FallbackOperator})
	}
}

// SetFallback updates the in-memory routing fallback policy. Call it after persisting
// the change so the live dispatcher applies it immediately.
func (e *Engine) SetFallback(mode models.FallbackMode, op models.Operator) {
	e.fb.Store(&fallbackConfig{mode: mode, op: op})
}

// Fallback returns the current routing fallback policy (for the admin console).
func (e *Engine) Fallback() (models.FallbackMode, models.Operator) {
	c := e.fb.Load()
	return c.mode, c.op
}

// Detect resolves the operator of a canonical MSISDN using the engine's prefix table.
func (e *Engine) Detect(canonical string) models.Operator {
	return DetectOperator(canonical, e.prefixes)
}

// Choice is the result of a successful route: the reserved SIM's addressing info.
type Choice struct {
	SimID          string
	DeviceID       string
	Operator       models.Operator
	SubscriptionID int
	OnNet          bool // true if the reserved SIM matches the target operator
}

// Route picks a SIM for msg and atomically reserves msg.Segments against its quota.
// Two-pass per docs/03: on-net first (same operator), then random fallback across any
// READY SIM — unless policy forbids it. The reserve is a single conditional UPDATE so
// concurrent workers cannot both blow past a SIM's daily_quota or min_gap (amendment F2).
//
// On any downstream rejection/not-sent, the caller MUST call Release to roll the reserve back.
func (e *Engine) Route(ctx context.Context, msg *models.Message) (*Choice, error) {
	seg := msg.Segments
	if seg < 1 {
		seg = 1
	}

	tryOnNet := msg.RoutingPolicy != models.PolicyAny && msg.TargetOperator != models.OpUnknown
	tryFallback := msg.RoutingPolicy != models.PolicyOnNetStrict

	if tryOnNet {
		if c, err := e.reserve(ctx, []models.Operator{msg.TargetOperator}, seg, orderLeastLoaded); err == nil {
			c.OnNet = true
			return c, nil
		} else if !errors.Is(err, ErrNoCapacity) {
			return nil, err
		}
	}
	if tryFallback {
		fb := e.fb.Load()
		// Operator-configured default: prefer the chosen network for off-net numbers first.
		if fb.mode == models.FallbackDefaultOp && fb.op != models.OpUnknown {
			if c, err := e.reserve(ctx, []models.Operator{fb.op}, seg, orderLeastLoaded); err == nil {
				c.OnNet = c.Operator == msg.TargetOperator
				return c, nil
			} else if !errors.Is(err, ErrNoCapacity) {
				return nil, err
			}
		}
		// Last resort across any READY SIM. RANDOM shuffles; otherwise least-loaded.
		ord := orderLeastLoaded
		if fb.mode == models.FallbackRandom {
			ord = orderRandom
		}
		if c, err := e.reserve(ctx, nil, seg, ord); err == nil {
			c.OnNet = c.Operator == msg.TargetOperator
			return c, nil
		} else if !errors.Is(err, ErrNoCapacity) {
			return nil, err
		}
	}
	return nil, ErrNoCapacity
}

// reserve runs the atomic conditional UPDATE. operators==nil means "any operator";
// order selects how eligible SIMs are ranked (least-loaded vs. random).
func (e *Engine) reserve(ctx context.Context, operators []models.Operator, seg int, order ordering) (*Choice, error) {
	var opClause string
	args := []any{seg, seg} // sent_today+=seg, sent_window+=seg in SET
	// selection args start after the two SET args
	selArgs := []any{seg}
	if len(operators) > 0 {
		placeholders := make([]string, len(operators))
		for i, op := range operators {
			placeholders[i] = "?"
			selArgs = append(selArgs, string(op))
		}
		opClause = "AND s.operator IN (" + strings.Join(placeholders, ",") + ")"
	}

	// Ranking: least-loaded first (sent_window), then healthiest, then least-recently-used —
	// or a uniform random pick when the fallback policy is RANDOM. Either way, pacing
	// (min_gap_ms) and quota are enforced in the WHERE so a reserved SIM is always sendable.
	orderClause := "s.sent_window ASC, s.health_score DESC, s.last_used_at ASC NULLS FIRST"
	if order == orderRandom {
		orderClause = "random()"
	}
	sql := fmt.Sprintf(`
UPDATE sims SET
    sent_today  = sent_today + ?,
    sent_window = sent_window + ?,
    last_used_at = now(),
    updated_at   = now()
WHERE id = (
    SELECT s.id FROM sims s
    JOIN devices d ON d.id = s.device_id
    WHERE s.status = 'READY'
      AND d.status <> 'DISABLED'
      -- reachable = a live WS OR an FCM push token (push-driven delivery)
      AND (d.status = 'ONLINE' OR d.push_token <> '')
      AND s.deleted_at IS NULL AND d.deleted_at IS NULL
      AND (s.cooldown_until IS NULL OR s.cooldown_until < now())
      AND (s.last_used_at IS NULL OR s.last_used_at < now() - (s.min_gap_ms * interval '1 millisecond'))
      AND s.sent_today + ? <= s.daily_quota
      %s
    ORDER BY %s
    FOR UPDATE OF s SKIP LOCKED
    LIMIT 1
)
RETURNING id, device_id, operator, subscription_id`, opClause, orderClause)

	args = append(args, selArgs...)

	var c Choice
	row := struct {
		ID             string
		DeviceID       string
		Operator       string
		SubscriptionID int
	}{}
	tx := e.db.WithContext(ctx).Raw(sql, args...).Scan(&row)
	if tx.Error != nil {
		return nil, tx.Error
	}
	if row.ID == "" {
		return nil, ErrNoCapacity
	}
	c.SimID = row.ID
	c.DeviceID = row.DeviceID
	c.Operator = models.Operator(row.Operator)
	c.SubscriptionID = row.SubscriptionID
	return &c, nil
}

// Release rolls back a reserve when the send was rejected or never left the radio (F2/F5).
func (e *Engine) Release(ctx context.Context, simID string, seg int) error {
	if seg < 1 {
		seg = 1
	}
	return e.db.WithContext(ctx).Exec(`
UPDATE sims SET
    sent_today  = GREATEST(sent_today  - ?, 0),
    sent_window = GREATEST(sent_window - ?, 0),
    updated_at  = now()
WHERE id = ?`, seg, seg, simID).Error
}
