package ws

import (
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	writeWait      = 10 * time.Second
	pongWait       = 60 * time.Second
	pingPeriod     = (pongWait * 9) / 10
	maxMessageSize = 32 * 1024
	sendBuffer     = 64
)

// Conn wraps one device WebSocket with independent read and write pumps.
type Conn struct {
	deviceID string
	ws       *websocket.Conn
	hub      *Hub
	send     chan []byte
	closeOne sync.Once
}

// Serve upgrades an authenticated device connection and runs its pumps. The caller
// (api layer) has already validated the device token and resolved deviceID.
func Serve(hub *Hub, wsConn *websocket.Conn, deviceID string) {
	c := &Conn{
		deviceID: deviceID,
		ws:       wsConn,
		hub:      hub,
		send:     make(chan []byte, sendBuffer),
	}
	hub.register(c)
	go c.writePump()
	c.readPump() // blocks until the socket dies
}

func (c *Conn) enqueue(b []byte) error {
	select {
	case c.send <- b:
		return nil
	default:
		// slow/backed-up device: drop it so a full buffer can't wedge the hub.
		c.close()
		return ErrDeviceOffline
	}
}

func (c *Conn) close() {
	c.closeOne.Do(func() {
		close(c.send)
		_ = c.ws.Close()
	})
}

func (c *Conn) readPump() {
	defer func() {
		c.hub.unregister(c)
		c.close()
	}()
	c.ws.SetReadLimit(maxMessageSize)
	_ = c.ws.SetReadDeadline(time.Now().Add(pongWait))
	c.ws.SetPongHandler(func(string) error {
		return c.ws.SetReadDeadline(time.Now().Add(pongWait))
	})
	for {
		_, raw, err := c.ws.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				slog.Debug("ws read closed", "device", c.deviceID, "err", err)
			}
			return
		}
		var f Frame
		if err := json.Unmarshal(raw, &f); err != nil {
			slog.Warn("bad frame", "device", c.deviceID, "err", err)
			continue
		}
		// Application-level heartbeat also refreshes the read deadline.
		if f.Type == TypeHeartbeat {
			_ = c.ws.SetReadDeadline(time.Now().Add(pongWait))
		}
		if c.hub.handler != nil {
			c.hub.handler(c.deviceID, &f)
		}
	}
}

func (c *Conn) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer ticker.Stop()
	for {
		select {
		case b, ok := <-c.send:
			_ = c.ws.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				_ = c.ws.WriteMessage(websocket.CloseMessage, nil)
				return
			}
			if err := c.ws.WriteMessage(websocket.TextMessage, b); err != nil {
				return
			}
		case <-ticker.C:
			_ = c.ws.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.ws.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
