package ws

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/nizwar/wsms-gateway/server/internal/models"
	"gorm.io/gorm"
)

// ErrDeviceOffline is returned by SendTo when the target device has no live connection.
var ErrDeviceOffline = errors.New("device offline")

// FrameHandler processes an inbound frame from a device. Set by the dispatcher.
type FrameHandler func(deviceID string, f *Frame)

// Hub is the registry of live device connections. It owns presence: a device is
// ONLINE exactly while it holds a registered connection here. The routing engine's
// reserve query filters on devices.status='ONLINE', so presence must be accurate.
type Hub struct {
	db      *gorm.DB
	mu      sync.RWMutex
	conns   map[string]*Conn // deviceID -> conn
	handler FrameHandler
}

func NewHub(db *gorm.DB) *Hub {
	return &Hub{db: db, conns: make(map[string]*Conn)}
}

func (h *Hub) SetHandler(fn FrameHandler) { h.handler = fn }

// register marks a device online and replaces any stale connection.
func (h *Hub) register(c *Conn) {
	h.mu.Lock()
	if old, ok := h.conns[c.deviceID]; ok && old != c {
		old.close() // supersede: one live connection per device
	}
	h.conns[c.deviceID] = c
	h.mu.Unlock()
	h.setDeviceStatus(c.deviceID, models.DevOnline)
}

// unregister marks a device offline, but only if the connection being removed is
// still the current one (avoids a superseded conn flipping a fresh one offline).
func (h *Hub) unregister(c *Conn) {
	h.mu.Lock()
	if cur, ok := h.conns[c.deviceID]; ok && cur == c {
		delete(h.conns, c.deviceID)
		h.mu.Unlock()
		h.setDeviceStatus(c.deviceID, models.DevOffline)
		return
	}
	h.mu.Unlock()
}

func (h *Hub) setDeviceStatus(deviceID string, status models.DeviceStatus) {
	now := time.Now()
	upd := map[string]any{"status": status, "last_seen_at": now, "updated_at": now}
	if status == models.DevOnline {
		upd["wake_misses"] = 0 // reconnected → clear the force-stop suspicion (F6)
	}
	h.db.Model(&models.Device{}).
		Where("id = ? AND status <> ?", deviceID, models.DevDisabled).
		Updates(upd)
}

// SendTo delivers a frame to a device. Returns ErrDeviceOffline if not connected.
func (h *Hub) SendTo(deviceID string, f *Frame) error {
	h.mu.RLock()
	c, ok := h.conns[deviceID]
	h.mu.RUnlock()
	if !ok {
		return ErrDeviceOffline
	}
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	return c.enqueue(b)
}

// Disconnect force-closes a device's live WebSocket, if any, and drops it from the
// registry. Used when a device is unlinked/deleted so the phone loses its session
// immediately rather than at the next ping timeout. It removes the conn from the map
// itself (so the closed conn's own unregister becomes a no-op) and does NOT flip the
// device to OFFLINE — the caller is deleting/disabling the row, which supersedes status.
func (h *Hub) Disconnect(deviceID string) {
	h.mu.Lock()
	c, ok := h.conns[deviceID]
	if ok {
		delete(h.conns, deviceID)
	}
	h.mu.Unlock()
	if ok {
		c.close()
	}
}

// Online reports whether a device currently holds a live connection.
func (h *Hub) Online(deviceID string) bool {
	h.mu.RLock()
	_, ok := h.conns[deviceID]
	h.mu.RUnlock()
	return ok
}

// OnlineCount returns the number of live device connections.
func (h *Hub) OnlineCount() int {
	h.mu.RLock()
	n := len(h.conns)
	h.mu.RUnlock()
	return n
}

// Shutdown closes all connections (graceful stop).
func (h *Hub) Shutdown(_ context.Context) {
	h.mu.Lock()
	for _, c := range h.conns {
		c.close()
	}
	h.conns = make(map[string]*Conn)
	h.mu.Unlock()
}
