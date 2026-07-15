// Package models holds the GORM models and the canonical enum vocabulary.
// It is the Go embodiment of docs/02-contract-protocol-schema.md as amended by
// docs/08-amendments.md. Enums are stored as text (amendment F10) to keep
// migrations painless; validity is enforced in Go via the Valid() methods.
package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// ---- Enums (canonical vocabulary) ------------------------------------------

type Operator string

const (
	OpTelkomsel Operator = "TELKOMSEL"
	OpIndosat   Operator = "INDOSAT"
	OpXL        Operator = "XL"
	OpAxis      Operator = "AXIS"
	OpTri       Operator = "TRI"
	OpSmartfren Operator = "SMARTFREN"
	OpUnknown   Operator = "UNKNOWN"
)

type Encoding string

const (
	EncGSM7 Encoding = "GSM7"
	EncUCS2 Encoding = "UCS2"
)

type DeviceStatus string

const (
	DevEnrolled DeviceStatus = "ENROLLED"
	DevOnline   DeviceStatus = "ONLINE"
	DevOffline  DeviceStatus = "OFFLINE"
	DevDisabled DeviceStatus = "DISABLED"
)

type SimStatus string

const (
	SimUnknown       SimStatus = "UNKNOWN"
	SimReady         SimStatus = "READY"
	SimAbsent        SimStatus = "ABSENT"
	SimDisabled      SimStatus = "DISABLED"
	SimQuotaExceeded SimStatus = "QUOTA_EXCEEDED"
	SimCooldown      SimStatus = "COOLDOWN"
)

// MessageStatus — state machine v2 (amendment F1 adds AWAITING_ACK, F4 adds SENT_UNCONFIRMED).
type MessageStatus string

const (
	MsgQueued          MessageStatus = "QUEUED"
	MsgRouting         MessageStatus = "ROUTING"
	MsgDispatched      MessageStatus = "DISPATCHED"
	MsgAwaitingAck     MessageStatus = "AWAITING_ACK"     // F1: command on the wire, ack not yet seen — AMBIGUOUS, never reroute
	MsgSent            MessageStatus = "SENT"             // left the radio, awaiting delivery report
	MsgSentUnconfirmed MessageStatus = "SENT_UNCONFIRMED" // F4: terminal, sent but no delivery report (normal on many IDN routes)
	MsgDelivered       MessageStatus = "DELIVERED"
	MsgFailed          MessageStatus = "FAILED"
	MsgExpired         MessageStatus = "EXPIRED"
	MsgCancelled       MessageStatus = "CANCELLED"
)

// RoutingPolicy — how strictly to honor on-net preference.
type RoutingPolicy string

const (
	PolicyOnNetPref   RoutingPolicy = "ON_NET_PREF"   // prefer same operator, fall back to any READY SIM (default)
	PolicyOnNetStrict RoutingPolicy = "ON_NET_STRICT" // only send from a same-operator SIM, else queue/expire
	PolicyAny         RoutingPolicy = "ANY"           // no operator preference, purely load-balanced
)

// EventType — message_events vocabulary.
type EventType string

const (
	EvSubmitted       EventType = "SUBMITTED"
	EvRouted          EventType = "ROUTED"
	EvDispatched      EventType = "DISPATCHED"
	EvAckAccepted     EventType = "ACK_ACCEPTED"
	EvAckRejected     EventType = "ACK_REJECTED"
	EvAckDuplicate    EventType = "ACK_DUPLICATE"
	EvSent            EventType = "SENT"
	EvSentUnconfirmed EventType = "SENT_UNCONFIRMED"
	EvDelivered       EventType = "DELIVERED"
	EvFailed          EventType = "FAILED"
	EvExpired         EventType = "EXPIRED"
	EvCancelled       EventType = "CANCELLED"
	EvRequeued        EventType = "REQUEUED"
)

// ---- Base structs ----------------------------------------------------------

type Base struct {
	CreatedAt time.Time `gorm:"not null;default:now()" json:"created_at"`
	UpdatedAt time.Time `gorm:"not null;default:now()" json:"updated_at"`
}

type SoftBase struct {
	Base
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}

// NewID returns a time-ordered UUIDv7 string. Falls back to v4 if v7 fails.
func NewID() string {
	if id, err := uuid.NewV7(); err == nil {
		return id.String()
	}
	return uuid.NewString()
}

// ---- Tables ----------------------------------------------------------------

type Client struct {
	ID               string `gorm:"type:uuid;primaryKey" json:"id"`
	Name             string `gorm:"not null" json:"name"`
	Active           bool   `gorm:"not null;default:true" json:"active"`
	WebhookSecretEnc []byte `json:"-"` // AES-GCM ciphertext; signs outbound webhooks (F3-style reversible secret)
	SoftBase
}

// WebhookDelivery is one outbound status callback (at-least-once, retried with backoff).
// Unique per message so a message is notified exactly once at its terminal state.
type WebhookDelivery struct {
	ID            string        `gorm:"type:uuid;primaryKey" json:"id"`
	MessageID     string        `gorm:"type:uuid;uniqueIndex;not null" json:"message_id"`
	ClientID      string        `gorm:"type:uuid;index" json:"client_id"`
	URL           string        `gorm:"not null" json:"url"`
	Event         MessageStatus `gorm:"type:text;not null" json:"event"`
	Status        string        `gorm:"type:text;not null;default:'pending';index" json:"status"` // pending|sent|failed
	Attempts      int           `gorm:"not null;default:0" json:"attempts"`
	MaxAttempts   int           `gorm:"not null;default:6" json:"max_attempts"`
	LastCode      int           `json:"last_code"`
	LastError     string        `json:"last_error"`
	NextAttemptAt time.Time     `gorm:"index;not null" json:"next_attempt_at"`
	DeliveredAt   *time.Time    `json:"delivered_at,omitempty"`
	Base
}

// APIKey — bearer credential for a client. Secret is stored ONLY as an Argon2id hash.
// SigningSecretEnc is a SEPARATE, reversibly-encrypted secret used for optional inbound
// HMAC request signing (amendment F3 — the bearer hash cannot double as an HMAC key).
type APIKey struct {
	ID               string     `gorm:"type:uuid;primaryKey" json:"id"`
	ClientID         string     `gorm:"type:uuid;index;not null" json:"client_id"`
	Prefix           string     `gorm:"index;not null" json:"prefix"` // public lookup id, e.g. "wsms_ab12"
	Hash             string     `gorm:"not null" json:"-"`            // argon2id(secret)
	Scopes           string     `gorm:"not null" json:"scopes"`       // csv: messages:write,messages:read,...
	SigningSecretEnc []byte     `json:"-"`                            // AES-GCM ciphertext, null unless signing enabled
	Active           bool       `gorm:"not null;default:true" json:"active"`
	LastUsedAt       *time.Time `json:"last_used_at,omitempty"`
	SoftBase
}

type Device struct {
	ID         string       `gorm:"type:uuid;primaryKey" json:"id"`
	Name       string       `gorm:"not null" json:"name"`
	Status     DeviceStatus `gorm:"type:text;not null;default:'ENROLLED'" json:"status"`
	LastSeenAt *time.Time   `json:"last_seen_at,omitempty"`
	AppVersion string       `json:"app_version,omitempty"`
	PushToken  string       `json:"-"`                                     // FCM token for wake
	SecretHash string       `json:"-"`                                     // argon2id of the device's WS bearer secret (issued at enrollment)
	WakeMisses int          `gorm:"not null;default:0" json:"wake_misses"` // F6: consecutive failed wakes → "needs manual relaunch"
	SoftBase
}

// Sim — one physical SIM in a device slot. sim_id (this ID) is the stable cross-system
// key; SubscriptionID is device-local and only echoed back in send_command.
type Sim struct {
	ID             string     `gorm:"type:uuid;primaryKey" json:"id"`
	DeviceID       string     `gorm:"type:uuid;index;not null" json:"device_id"`
	Slot           int        `gorm:"not null" json:"slot"`
	SubscriptionID int        `gorm:"not null" json:"subscription_id"` // Android SubscriptionInfo id, device-local
	Operator       Operator   `gorm:"type:text;index;not null;default:'UNKNOWN'" json:"operator"`
	OperatorLocked bool       `gorm:"not null;default:false" json:"operator_locked"` // manual override wins over carrierName
	MSISDN         string     `json:"msisdn,omitempty"`                              // canonical +62, often null from Android
	Status         SimStatus  `gorm:"type:text;not null;default:'UNKNOWN'" json:"status"`
	DailyQuota     int        `gorm:"not null;default:200" json:"daily_quota"`  // segments/day
	SentToday      int        `gorm:"not null;default:0" json:"sent_today"`     // segments (single unit — amendment F2)
	SentWindow     int        `gorm:"not null;default:0" json:"sent_window"`    // segments in the current rate window
	MinGapMs       int        `gorm:"not null;default:8000" json:"min_gap_ms"`  // pacing floor between sends
	HealthScore    int        `gorm:"not null;default:100" json:"health_score"` // 0..100
	CooldownUntil  *time.Time `json:"cooldown_until,omitempty"`
	LastUsedAt     *time.Time `json:"last_used_at,omitempty"`
	SoftBase
}

type Message struct {
	ID               string         `gorm:"type:uuid;primaryKey" json:"id"`
	ClientID         string         `gorm:"type:uuid;index;not null" json:"client_id"`
	TargetMSISDN     string         `gorm:"index;not null" json:"target_msisdn"` // canonical +62
	TargetOperator   Operator       `gorm:"type:text;index;not null" json:"target_operator"`
	Body             string         `gorm:"not null" json:"body"`
	Encoding         Encoding       `gorm:"type:text;not null" json:"encoding"`
	Segments         int            `gorm:"not null" json:"segments"`
	Status           MessageStatus  `gorm:"type:text;index;not null;default:'QUEUED'" json:"status"`
	RoutingPolicy    RoutingPolicy  `gorm:"type:text;not null;default:'ON_NET_PREF'" json:"routing_policy"`
	AssignedSimID    *string        `gorm:"type:uuid;index" json:"assigned_sim_id,omitempty"`
	AssignedDeviceID *string        `gorm:"type:uuid" json:"assigned_device_id,omitempty"`
	AssignedOperator *Operator      `gorm:"type:text" json:"assigned_operator,omitempty"`
	DedupKey         *string        `gorm:"index" json:"dedup_key,omitempty"` // unique per client (composite index below)
	PayloadHash      string         `json:"-"`                                // sha256 of canonical payload — F12 conflict detection
	Attempts         int            `gorm:"not null;default:0" json:"attempts"`
	MaxAttempts      int            `gorm:"not null;default:3" json:"max_attempts"`
	CallbackURL      string         `json:"callback_url,omitempty"`
	LastError        string         `json:"last_error,omitempty"`
	ExpiresAt        time.Time      `gorm:"index;not null" json:"expires_at"`
	DispatchedAt     *time.Time     `json:"dispatched_at,omitempty"`
	Meta             datatypes.JSON `json:"meta,omitempty"`
	Base
}

// MessageEvent — append-only lifecycle log.
type MessageEvent struct {
	ID        string         `gorm:"type:uuid;primaryKey" json:"id"`
	MessageID string         `gorm:"type:uuid;index;not null" json:"message_id"`
	EventType EventType      `gorm:"type:text;not null" json:"event_type"`
	Detail    datatypes.JSON `json:"detail,omitempty"`
	CreatedAt time.Time      `gorm:"not null;default:now();index" json:"created_at"`
}

// MessageSend — SERVER-SIDE sent ledger (amendment F1). One row proves a given message_id
// was committed to a specific device+SIM; its existence blocks any cross-device re-send.
type MessageSend struct {
	MessageID    string    `gorm:"type:uuid;primaryKey" json:"message_id"`
	SimID        string    `gorm:"type:uuid;not null" json:"sim_id"`
	DeviceID     string    `gorm:"type:uuid;not null" json:"device_id"`
	DispatchedAt time.Time `gorm:"not null;default:now()" json:"dispatched_at"`
}

type OperatorPrefix struct {
	Prefix   string   `gorm:"primaryKey" json:"prefix"` // "0812"
	Operator Operator `gorm:"type:text;not null" json:"operator"`
}

type EnrollmentToken struct {
	ID        string     `gorm:"type:uuid;primaryKey" json:"id"`
	TokenHash string     `gorm:"index;not null" json:"-"` // sha256(token)
	Label     string     `json:"label,omitempty"`
	Used      bool       `gorm:"not null;default:false" json:"used"`
	DeviceID  *string    `gorm:"type:uuid" json:"device_id,omitempty"`
	ExpiresAt time.Time  `gorm:"not null" json:"expires_at"`
	UsedAt    *time.Time `json:"used_at,omitempty"`
	Base
}

// AdminUser is a human operator of the admin console (distinct from client API keys).
// Roles: owner | operator | support | readonly (docs/07 §3).
type AdminUser struct {
	ID           string     `gorm:"type:uuid;primaryKey" json:"id"`
	Username     string     `gorm:"uniqueIndex;not null" json:"username"`
	PasswordHash string     `gorm:"not null" json:"-"`
	Role         string     `gorm:"not null;default:'readonly'" json:"role"`
	Active       bool       `gorm:"not null;default:true" json:"active"`
	LastLoginAt  *time.Time `json:"last_login_at,omitempty"`
	SoftBase
}

// AdminSession is a persisted admin console login. Stored in the DB (not in memory) so
// sessions survive server restarts/redeploys and a user can be logged in from any number
// of devices/places at once — each login is its own row, keyed by the token's hash.
type AdminSession struct {
	TokenHash string    `gorm:"primaryKey"` // sha256(session token); the plaintext lives only in the cookie
	UserID    string    `gorm:"type:uuid;index;not null" json:"user_id"`
	Username  string    `gorm:"not null" json:"username"`
	Role      string    `gorm:"not null" json:"role"`
	SourceIP  string    `json:"source_ip"`
	UserAgent string    `json:"user_agent"`
	ExpiresAt time.Time `gorm:"index;not null" json:"expires_at"`
	CreatedAt time.Time `gorm:"not null;default:now()" json:"created_at"`
}

// AdminAudit is the administrative audit trail — a superset of docs/06 §1.9 that adds
// actor_role and reason (docs/07 §8). Records privileged actions and PII reveals.
type AdminAudit struct {
	ID         string         `gorm:"type:uuid;primaryKey" json:"id"`
	Actor      string         `gorm:"not null" json:"actor"`
	ActorRole  string         `json:"actor_role"`
	Action     string         `gorm:"not null;index" json:"action"`
	TargetType string         `json:"target_type"`
	TargetID   string         `json:"target_id"`
	Reason     string         `json:"reason"`
	Before     datatypes.JSON `json:"before,omitempty"`
	After      datatypes.JSON `json:"after,omitempty"`
	SourceIP   string         `json:"source_ip"`
	CreatedAt  time.Time      `gorm:"not null;default:now();index" json:"created_at"`
}

// AllModels lists every table for AutoMigrate.
func AllModels() []any {
	return []any{
		&Client{}, &APIKey{}, &Device{}, &Sim{}, &Message{}, &MessageEvent{},
		&MessageSend{}, &OperatorPrefix{}, &EnrollmentToken{},
		&AdminUser{}, &AdminSession{}, &AdminAudit{}, &WebhookDelivery{},
	}
}
