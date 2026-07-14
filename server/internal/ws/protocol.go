// Package ws implements the device<->server WebSocket protocol (docs/02 §C).
package ws

import "encoding/json"

// Frame is the envelope for every message in both directions.
type Frame struct {
	Type string          `json:"type"`
	ID   string          `json:"id"`             // per-frame ULID/uuid for correlation
	TS   int64           `json:"ts"`             // unix epoch millis
	Data json.RawMessage `json:"data,omitempty"` // type-specific payload
}

// Device -> server frame types.
const (
	TypeHello          = "hello"           // handshake after auth: app version, device info
	TypeSimReport      = "sim_report"      // full list of active SIMs
	TypeHeartbeat      = "heartbeat"       // presence keepalive
	TypeSendAck        = "send_ack"        // accepted | rejected | duplicate for a send_command
	TypeDeliveryReport = "delivery_report" // sent | sent_unconfirmed | delivered | failed
	TypeCancelAck      = "cancel_ack"      // response to a cancel command (F9)
	TypeSetQuota       = "set_quota"       // device adjusts a SIM's daily quota (segments/day)
)

// Server -> device frame types.
const (
	TypeSendCommand = "send_command"
	TypeCancel      = "cancel"
	TypeConfig      = "config"
	TypePing        = "ping"
	TypePong        = "pong"
	TypeSimState    = "sim_state" // authoritative per-SIM state (operator, status, quota, sent)
)

// ---- Payloads ----

type HelloData struct {
	AppVersion string `json:"app_version"`
	OS         string `json:"os"`
	Model      string `json:"model"`
	PushToken  string `json:"push_token,omitempty"` // FCM token for wake (docs/01 §7)
}

type SimInfo struct {
	SubscriptionID int    `json:"subscription_id"`
	Slot           int    `json:"slot"`
	CarrierName    string `json:"carrier_name"`
	MCC            string `json:"mcc,omitempty"`
	MNC            string `json:"mnc,omitempty"`
	Number         string `json:"number,omitempty"` // often empty on Android
}

type SimReportData struct {
	Sims []SimInfo `json:"sims"`
}

// SendCommandData is what the server tells a device to send.
type SendCommandData struct {
	MessageID      string `json:"message_id"`
	Target         string `json:"target"` // canonical +62
	Body           string `json:"body"`
	SubscriptionID int    `json:"subscription_id"` // which local SIM to bind SmsManager to
	Encoding       string `json:"encoding"`
	ExpiresAtMs    int64  `json:"expires_at_ms"` // device drops if past this (F7)
}

// SendAck: accepted = will send; rejected = never reached radio, safe to reroute (F5);
// duplicate = message_id already committed on this device.
type SendAckData struct {
	MessageID string `json:"message_id"`
	Result    string `json:"result"` // "accepted" | "rejected" | "duplicate"
	Reason    string `json:"reason,omitempty"`
}

const (
	AckAccepted  = "accepted"
	AckRejected  = "rejected"
	AckDuplicate = "duplicate"
)

// DeliveryReportData carries the radio/network outcome.
type DeliveryReportData struct {
	MessageID string `json:"message_id"`
	Status    string `json:"status"` // "sent" | "delivered" | "failed"
	Reason    string `json:"reason,omitempty"`
}

const (
	DRSent      = "sent"
	DRDelivered = "delivered"
	DRFailed    = "failed"
)

// SimState is the server's authoritative view of one SIM, pushed to the owning device
// so the app can show operator/status/quota and let the operator adjust the quota. It is
// keyed to the device by subscription_id (the only id the phone knows); sim_id is the
// server UUID, echoed for reference.
type SimState struct {
	SimID          string `json:"sim_id"`
	SubscriptionID int    `json:"subscription_id"`
	Slot           int    `json:"slot"`
	Operator       string `json:"operator"`
	MSISDN         string `json:"msisdn,omitempty"`
	Status         string `json:"status"`
	DailyQuota     int    `json:"daily_quota"`
	SentToday      int    `json:"sent_today"`
	HealthScore    int    `json:"health_score"`
}

type SimStateData struct {
	Sims []SimState `json:"sims"`
}

// SetQuotaData is a device-initiated change to a SIM's daily quota. The server resolves
// the SIM by (device_id, subscription_id) and clamps the value.
type SetQuotaData struct {
	SubscriptionID int `json:"subscription_id"`
	DailyQuota     int `json:"daily_quota"`
}

type CancelData struct {
	MessageID string `json:"message_id"`
}

type CancelAckData struct {
	MessageID string `json:"message_id"`
	Result    string `json:"result"` // "cancelled" | "already_sent"
}

// Encode builds a Frame with a JSON-marshaled payload.
func Encode(typ, id string, tsMs int64, payload any) (*Frame, error) {
	var raw json.RawMessage
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		raw = b
	}
	return &Frame{Type: typ, ID: id, TS: tsMs, Data: raw}, nil
}

// Decode unmarshals a frame's Data into v.
func (f *Frame) Decode(v any) error {
	if len(f.Data) == 0 {
		return nil
	}
	return json.Unmarshal(f.Data, v)
}
