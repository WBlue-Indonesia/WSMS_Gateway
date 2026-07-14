// Package dispatch is the delivery engine: it claims queued messages, routes them
// to a SIM, sends the command over the WS hub, and drives the lifecycle to a
// terminal state. Correctness centers on one invariant (amendment F1):
//
//	Once a send_command has been ENQUEUED to a device (message enters AWAITING_ACK),
//	the message is PINNED to that message_id+SIM and is NEVER re-routed to another
//	device/SIM. It can only terminate as SENT / DELIVERED / FAILED / SENT_UNCONFIRMED.
//	Re-queueing (which permits re-routing) happens ONLY before enqueue succeeds
//	(reserve failed, or the device was offline so the command never left the server).
//
// This is what prevents the double-send hole where an ack is lost, the message is
// re-queued, and a second device with no local ledger sends the SMS again.
package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/nizwar/wsms-gateway/server/internal/config"
	"github.com/nizwar/wsms-gateway/server/internal/fleet"
	"github.com/nizwar/wsms-gateway/server/internal/models"
	"github.com/nizwar/wsms-gateway/server/internal/router"
	"github.com/nizwar/wsms-gateway/server/internal/ws"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Waker revives an offline device via a push wake (implemented by internal/fcm) and
// carries the send command itself in a high-priority FCM data message (push-driven
// delivery — no persistent socket needed).
type Waker interface {
	Wake(ctx context.Context, deviceToken string) error
	SendData(ctx context.Context, deviceToken string, data map[string]string) error
}

type Dispatcher struct {
	db     *gorm.DB
	engine *router.Engine
	hub    *ws.Hub
	cfg    config.Config

	waker    Waker
	wmu      sync.Mutex
	lastWake map[string]time.Time
}

func New(db *gorm.DB, engine *router.Engine, hub *ws.Hub, cfg config.Config) *Dispatcher {
	d := &Dispatcher{db: db, engine: engine, hub: hub, cfg: cfg, lastWake: map[string]time.Time{}}
	hub.SetHandler(d.HandleFrame)
	return d
}

// SetWaker enables FCM wake of offline devices when work is waiting.
func (d *Dispatcher) SetWaker(w Waker) { d.waker = w }

// Run starts the worker pool and the reaper. Blocks until ctx is cancelled.
func (d *Dispatcher) Run(ctx context.Context) {
	for i := 0; i < d.cfg.DispatchWorkers; i++ {
		go d.worker(ctx, i)
	}
	go d.reaper(ctx)
	<-ctx.Done()
}

// worker repeatedly claims and dispatches one message.
func (d *Dispatcher) worker(ctx context.Context, id int) {
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			for d.dispatchOne(ctx) {
				// drain while work remains
			}
		}
	}
}

// dispatchOne claims a single QUEUED message and attempts to dispatch it.
// Returns true if it claimed a message (so the caller can loop for more).
func (d *Dispatcher) dispatchOne(ctx context.Context) bool {
	msg, ok := d.claim(ctx)
	if !ok {
		return false
	}

	// F7: drop if the message has effectively no life left — never deliver a stale OTP.
	if time.Until(msg.ExpiresAt) < d.cfg.MinRemainingTTL {
		d.terminate(ctx, msg, models.MsgExpired, models.EvExpired, "insufficient remaining TTL")
		return true
	}

	choice, err := d.engine.Route(ctx, &msg)
	if err != nil {
		if errors.Is(err, router.ErrNoCapacity) {
			// No SIM right now. Try to wake an offline device that could take it, then
			// put it back to QUEUED; the reaper expires it if TTL passes.
			d.wakeFor(ctx, &msg)
			d.setStatus(ctx, msg.ID, models.MsgQueued, "no capacity, awaiting SIM")
		} else {
			slog.Error("route error", "msg", msg.ID, "err", err)
			d.setStatus(ctx, msg.ID, models.MsgQueued, "route error")
		}
		return true
	}

	// Reserve succeeded. Record the SERVER-SIDE sent ledger BEFORE we touch the wire
	// (amendment F1): its presence blocks any cross-device re-send of this message_id.
	ledger := models.MessageSend{MessageID: msg.ID, SimID: choice.SimID, DeviceID: choice.DeviceID, DispatchedAt: time.Now()}
	if err := d.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&ledger).Error; err != nil {
		slog.Error("ledger insert", "msg", msg.ID, "err", err)
	}

	sc := ws.SendCommandData{
		MessageID:      msg.ID,
		Target:         msg.TargetMSISDN,
		Body:           msg.Body,
		SubscriptionID: choice.SubscriptionID,
		Encoding:       string(msg.Encoding),
		ExpiresAtMs:    msg.ExpiresAt.UnixMilli(),
	}

	if err := d.deliverCommand(ctx, choice.DeviceID, sc); err != nil {
		// Command never left the server → nothing was sent. Safe to release + re-queue
		// (this is the ONLY re-route path, and it is pre-enqueue).
		_ = d.engine.Release(ctx, choice.SimID, msg.Segments)
		d.db.WithContext(ctx).Delete(&models.MessageSend{}, "message_id = ?", msg.ID)
		d.setStatus(ctx, msg.ID, models.MsgQueued, "device unreachable at dispatch")
		return true
	}

	// Command is on the wire → PIN it. From here it can only reach a terminal state.
	op := choice.Operator
	d.db.WithContext(ctx).Model(&models.Message{}).Where("id = ?", msg.ID).Updates(map[string]any{
		"status":             models.MsgAwaitingAck,
		"assigned_sim_id":    choice.SimID,
		"assigned_device_id": choice.DeviceID,
		"assigned_operator":  op,
		"dispatched_at":      time.Now(),
		"attempts":           gorm.Expr("attempts + 1"),
		"updated_at":         time.Now(),
	})
	d.event(ctx, msg.ID, models.EvDispatched, map[string]any{
		"sim_id": choice.SimID, "device_id": choice.DeviceID, "operator": op, "on_net": choice.OnNet,
	})
	return true
}

// claim atomically moves one QUEUED, unexpired message to ROUTING and returns it.
func (d *Dispatcher) claim(ctx context.Context) (models.Message, bool) {
	var msg models.Message
	err := d.db.WithContext(ctx).Raw(`
UPDATE messages SET status = 'ROUTING', updated_at = now()
WHERE id = (
    SELECT id FROM messages
    WHERE status = 'QUEUED' AND expires_at > now()
    ORDER BY created_at
    FOR UPDATE SKIP LOCKED
    LIMIT 1
)
RETURNING *`).Scan(&msg).Error
	if err != nil || msg.ID == "" {
		return models.Message{}, false
	}
	return msg, true
}

// HandleFrame processes inbound device frames (send_ack, delivery_report, cancel_ack).
func (d *Dispatcher) HandleFrame(deviceID string, f *ws.Frame) {
	ctx := context.Background()
	switch f.Type {
	case ws.TypeSendAck:
		var a ws.SendAckData
		if f.Decode(&a) == nil {
			d.onSendAck(ctx, a)
		}
	case ws.TypeDeliveryReport:
		var dr ws.DeliveryReportData
		if f.Decode(&dr) == nil {
			d.onDeliveryReport(ctx, dr)
		}
	case ws.TypeCancelAck:
		var ca ws.CancelAckData
		if f.Decode(&ca) == nil {
			d.onCancelAck(ctx, ca)
		}
	case ws.TypeSimReport:
		var sr ws.SimReportData
		if f.Decode(&sr) == nil {
			if err := fleet.UpsertSims(d.db, deviceID, sr.Sims); err != nil {
				slog.Error("sim_report upsert", "device", deviceID, "err", err)
			}
			d.pushSimState(deviceID) // give the app its authoritative per-SIM quota/state
		}
	case ws.TypeSetQuota:
		var sq ws.SetQuotaData
		if f.Decode(&sq) == nil {
			if simID, clamped, ok := fleet.SetSimQuota(d.db, deviceID, sq.SubscriptionID, sq.DailyQuota); ok {
				d.db.Create(&models.AdminAudit{
					ID: models.NewID(), Actor: "device:" + deviceID, ActorRole: "device",
					Action: "sim.quota", TargetType: "sim", TargetID: simID,
					Reason: strconv.Itoa(clamped), CreatedAt: time.Now(),
				})
			}
			d.pushSimState(deviceID) // echo the authoritative (clamped) state back
		}
	case ws.TypeHello:
		var h ws.HelloData
		if f.Decode(&h) == nil && h.PushToken != "" {
			d.db.Model(&models.Device{}).Where("id = ?", deviceID).
				Update("push_token", h.PushToken)
		}
	case ws.TypeHeartbeat:
		// presence already refreshed by the read pump; nothing more to do here
	default:
		slog.Debug("unhandled frame", "type", f.Type, "device", deviceID)
	}
}

func (d *Dispatcher) onSendAck(ctx context.Context, a ws.SendAckData) {
	switch a.Result {
	case ws.AckAccepted, ws.AckDuplicate:
		// Device will send / has already committed. Move to DISPATCHED and await delivery.
		// NEVER re-route from here.
		ev := models.EvAckAccepted
		if a.Result == ws.AckDuplicate {
			ev = models.EvAckDuplicate
		}
		d.advanceIfPinned(ctx, a.MessageID, models.MsgDispatched, ev, a.Reason)
	case ws.AckRejected:
		// Device proved the SMS never reached the radio (F5) → release + re-queue for reroute.
		d.rejectAndRequeue(ctx, a.MessageID, a.Reason)
	}
}

func (d *Dispatcher) onDeliveryReport(ctx context.Context, dr ws.DeliveryReportData) {
	switch dr.Status {
	case ws.DRSent:
		d.advanceIfPinned(ctx, dr.MessageID, models.MsgSent, models.EvSent, dr.Reason)
	case ws.DRDelivered:
		d.advanceIfPinned(ctx, dr.MessageID, models.MsgDelivered, models.EvDelivered, dr.Reason)
	case ws.DRFailed:
		// Terminal failure. Release the quota reserve but do NOT re-route (send may have
		// partially left the radio — safety over completeness).
		var msg models.Message
		if d.db.WithContext(ctx).First(&msg, "id = ?", dr.MessageID).Error == nil && msg.AssignedSimID != nil {
			_ = d.engine.Release(ctx, *msg.AssignedSimID, msg.Segments)
		}
		d.terminate(ctx, msg, models.MsgFailed, models.EvFailed, dr.Reason)
	}
}

func (d *Dispatcher) onCancelAck(ctx context.Context, ca ws.CancelAckData) {
	// F9: only honor a cancel that the device confirms happened before SmsManager.
	if ca.Result == "cancelled" {
		var msg models.Message
		if d.db.WithContext(ctx).First(&msg, "id = ?", ca.MessageID).Error == nil {
			if msg.AssignedSimID != nil {
				_ = d.engine.Release(ctx, *msg.AssignedSimID, msg.Segments)
			}
			d.terminate(ctx, msg, models.MsgCancelled, models.EvCancelled, "cancelled before send")
		}
	}
	// result "already_sent" → leave the message on its SENT/DELIVERED path (cancel_failed).
}

// advanceIfPinned moves a message forward only if it is still in a non-terminal, pinned
// state — protects against out-of-order/duplicate frames flipping a terminal message.
func (d *Dispatcher) advanceIfPinned(ctx context.Context, msgID string, to models.MessageStatus, ev models.EventType, reason string) {
	res := d.db.WithContext(ctx).Model(&models.Message{}).
		Where("id = ? AND status IN ?", msgID,
			[]models.MessageStatus{models.MsgAwaitingAck, models.MsgDispatched, models.MsgSent}).
		Updates(map[string]any{"status": to, "updated_at": time.Now(), "last_error": reason})
	if res.RowsAffected > 0 {
		d.event(ctx, msgID, ev, map[string]any{"reason": reason})
	}
}

func (d *Dispatcher) rejectAndRequeue(ctx context.Context, msgID, reason string) {
	var msg models.Message
	if d.db.WithContext(ctx).First(&msg, "id = ?", msgID).Error != nil {
		return
	}
	if msg.AssignedSimID != nil {
		_ = d.engine.Release(ctx, *msg.AssignedSimID, msg.Segments)
	}
	d.db.WithContext(ctx).Delete(&models.MessageSend{}, "message_id = ?", msgID)
	next := models.MsgQueued
	errMsg := reason
	if msg.Attempts >= msg.MaxAttempts {
		next = models.MsgFailed
		errMsg = "max attempts exhausted after reject: " + reason
	}
	d.db.WithContext(ctx).Model(&models.Message{}).Where("id = ?", msgID).Updates(map[string]any{
		"status": next, "assigned_sim_id": nil, "assigned_device_id": nil,
		"last_error": errMsg, "updated_at": time.Now(),
	})
	d.event(ctx, msgID, models.EvAckRejected, map[string]any{"reason": reason, "next": next})
}

// reaper handles TTL expiry and the SENT_UNCONFIRMED transition (F4).
func (d *Dispatcher) reaper(ctx context.Context) {
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			now := time.Now()
			// 1. QUEUED/ROUTING past TTL never got dispatched → EXPIRED (safe: nothing sent).
			d.db.WithContext(ctx).Model(&models.Message{}).
				Where("status IN ? AND expires_at <= ?",
					[]models.MessageStatus{models.MsgQueued, models.MsgRouting}, now).
				Updates(map[string]any{"status": models.MsgExpired, "updated_at": now, "last_error": "ttl expired before dispatch"})

			// 2. Crash recovery: ROUTING stuck too long (worker died mid-claim) → back to QUEUED.
			d.db.WithContext(ctx).Model(&models.Message{}).
				Where("status = ? AND updated_at < ?", models.MsgRouting, now.Add(-2*time.Minute)).
				Updates(map[string]any{"status": models.MsgQueued, "updated_at": now})

			// 3. Pinned messages with no confirmation within DeliveryWait → SENT_UNCONFIRMED (F4).
			//    NEVER re-routed. This is the normal outcome on IDN routes that drop STATUS-REPORTs.
			cutoff := now.Add(-d.cfg.DeliveryWait)
			d.db.WithContext(ctx).Model(&models.Message{}).
				Where("status IN ? AND dispatched_at < ?",
					[]models.MessageStatus{models.MsgAwaitingAck, models.MsgDispatched, models.MsgSent}, cutoff).
				Updates(map[string]any{"status": models.MsgSentUnconfirmed, "updated_at": now, "last_error": "no delivery confirmation within window"})
		}
	}
}

// ---- helpers ----

func (d *Dispatcher) setStatus(ctx context.Context, msgID string, status models.MessageStatus, reason string) {
	d.db.WithContext(ctx).Model(&models.Message{}).Where("id = ?", msgID).
		Updates(map[string]any{"status": status, "updated_at": time.Now(), "last_error": reason})
}

func (d *Dispatcher) terminate(ctx context.Context, msg models.Message, status models.MessageStatus, ev models.EventType, reason string) {
	if msg.ID == "" {
		return
	}
	d.db.WithContext(ctx).Model(&models.Message{}).Where("id = ?", msg.ID).
		Updates(map[string]any{"status": status, "updated_at": time.Now(), "last_error": reason})
	d.event(ctx, msg.ID, ev, map[string]any{"reason": reason})
}

func (d *Dispatcher) event(ctx context.Context, msgID string, ev models.EventType, detail map[string]any) {
	var raw []byte
	if detail != nil {
		raw, _ = json.Marshal(detail)
	}
	d.db.WithContext(ctx).Create(&models.MessageEvent{
		ID: models.NewID(), MessageID: msgID, EventType: ev, Detail: raw, CreatedAt: time.Now(),
	})
}

// wakeFor pushes an FCM wake to offline devices that could serve this message.
// Throttled per device; increments Device.WakeMisses (reset on reconnect) so the
// admin can flag chronically-unwakeable (force-stopped) phones (amendment F6).
func (d *Dispatcher) wakeFor(ctx context.Context, msg *models.Message) {
	if d.waker == nil {
		return
	}
	type cand struct {
		ID        string
		PushToken string
	}
	q := d.db.WithContext(ctx).Table("devices d").
		Select("DISTINCT d.id, d.push_token").
		Joins("JOIN sims s ON s.device_id = d.id").
		Where("d.status <> ? AND d.push_token <> '' AND s.status = ? AND s.deleted_at IS NULL AND d.deleted_at IS NULL",
			models.DevDisabled, models.SimReady)
	if msg.RoutingPolicy == models.PolicyOnNetStrict {
		q = q.Where("s.operator = ?", msg.TargetOperator)
	}
	var cands []cand
	q.Limit(5).Scan(&cands)

	for _, c := range cands {
		if d.hub.Online(c.ID) || d.recentlyWoke(c.ID) {
			continue
		}
		if err := d.waker.Wake(ctx, c.PushToken); err != nil {
			slog.Warn("fcm wake failed", "device", c.ID, "err", err)
		}
		d.db.WithContext(ctx).Model(&models.Device{}).Where("id = ?", c.ID).
			Update("wake_misses", gorm.Expr("wake_misses + 1"))
	}
}

func (d *Dispatcher) recentlyWoke(deviceID string) bool {
	d.wmu.Lock()
	defer d.wmu.Unlock()
	if t, ok := d.lastWake[deviceID]; ok && time.Since(t) < 30*time.Second {
		return true
	}
	d.lastWake[deviceID] = time.Now()
	return false
}

// pushSimState sends the device its authoritative per-SIM state (operator, status,
// quota, sent-today) so the app can show and adjust quota. Best-effort — a no-op if the
// device is offline.
func (d *Dispatcher) pushSimState(deviceID string) {
	states, err := fleet.DeviceSimStates(d.db, deviceID)
	if err != nil {
		slog.Error("sim_state build", "device", deviceID, "err", err)
		return
	}
	f, err := ws.Encode(ws.TypeSimState, models.NewID(), nowMs(), ws.SimStateData{Sims: states})
	if err != nil {
		return
	}
	_ = d.hub.SendTo(deviceID, f)
}

// deliverCommand sends the send_command to the chosen device. It prefers FCM — a
// high-priority data message wakes even a frozen app; the device sends the SMS and
// confirms over REST, so no persistent socket is needed. If the device has no push
// token but holds a live WS, it falls back to the socket.
func (d *Dispatcher) deliverCommand(ctx context.Context, deviceID string, sc ws.SendCommandData) error {
	if d.waker != nil {
		var dev models.Device
		if err := d.db.WithContext(ctx).Select("push_token").First(&dev, "id = ?", deviceID).Error; err == nil && dev.PushToken != "" {
			return d.waker.SendData(ctx, dev.PushToken, map[string]string{
				"type":            "send",
				"message_id":      sc.MessageID,
				"target":          sc.Target,
				"body":            sc.Body,
				"subscription_id": strconv.Itoa(sc.SubscriptionID),
				"encoding":        sc.Encoding,
				"expires_at_ms":   strconv.FormatInt(sc.ExpiresAtMs, 10),
			})
		}
	}
	cmd, err := ws.Encode(ws.TypeSendCommand, models.NewID(), nowMs(), sc)
	if err != nil {
		return err
	}
	return d.hub.SendTo(deviceID, cmd)
}

// HandleDeviceAck / HandleDeviceDelivery let the REST device endpoints reuse the exact
// same lifecycle logic as the WS path (F1/F5/F9 invariants preserved).
func (d *Dispatcher) HandleDeviceAck(ctx context.Context, a ws.SendAckData) { d.onSendAck(ctx, a) }

func (d *Dispatcher) HandleDeviceDelivery(ctx context.Context, dr ws.DeliveryReportData) {
	d.onDeliveryReport(ctx, dr)
}

func nowMs() int64 { return time.Now().UnixMilli() }
