// Package store opens the database, runs migrations, and seeds reference data.
package store

import (
	"encoding/hex"
	"time"

	"github.com/nizwar/wsms-gateway/server/internal/models"
	"github.com/nizwar/wsms-gateway/server/internal/router"
	"github.com/nizwar/wsms-gateway/server/internal/secret"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Open connects to Postgres, auto-migrates the schema, and seeds the operator
// prefix table. AutoMigrate is used for dev convenience; docs/04 ships SQL
// migrations for production (amendment F10 on native-enum caveats does not apply
// here because enums are modeled as text).
func Open(dsn string, debug bool) (*gorm.DB, error) {
	lvl := logger.Warn
	if debug {
		lvl = logger.Info
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(lvl),
	})
	if err != nil {
		return nil, err
	}
	if err := db.AutoMigrate(models.AllModels()...); err != nil {
		return nil, err
	}
	if err := ensureDedupIndex(db); err != nil {
		return nil, err
	}
	if err := seedPrefixes(db); err != nil {
		return nil, err
	}
	if err := ensureSettings(db); err != nil {
		return nil, err
	}
	return db, nil
}

// ensureSettings creates the singleton settings row (id=SettingsID) with defaults if absent.
func ensureSettings(db *gorm.DB) error {
	row := models.AppSettings{
		ID: models.SettingsID, FallbackMode: models.FallbackLeastLoaded, FallbackOperator: models.OpUnknown,
	}
	return db.Where(models.AppSettings{ID: models.SettingsID}).FirstOrCreate(&row).Error
}

// ensureDedupIndex enforces per-client idempotency: dedup_key unique within a client.
func ensureDedupIndex(db *gorm.DB) error {
	return db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS ux_messages_client_dedup
		ON messages (client_id, dedup_key) WHERE dedup_key IS NOT NULL`).Error
}

// seedPrefixes inserts any missing rows from the default prefix table (idempotent).
func seedPrefixes(db *gorm.DB) error {
	for prefix, op := range router.DefaultPrefixes {
		row := models.OperatorPrefix{Prefix: prefix, Operator: op}
		if err := db.Where(models.OperatorPrefix{Prefix: prefix}).
			FirstOrCreate(&row).Error; err != nil {
			return err
		}
	}
	return nil
}

// BootstrapClient creates a default client + API key if no clients exist, returning
// the plaintext token to log ONCE. Token format: wsms_<prefix>.<secret>.
func BootstrapClient(db *gorm.DB) (token string, created bool, err error) {
	var n int64
	if err = db.Model(&models.Client{}).Count(&n).Error; err != nil || n > 0 {
		return "", false, err
	}
	prefixBytes := make([]byte, 4)
	if s, e := secret.RandomToken(4); e == nil {
		copy(prefixBytes, []byte(s))
	}
	prefix := hex.EncodeToString(prefixBytes)
	sec, err := secret.RandomToken(32)
	if err != nil {
		return "", false, err
	}
	hash, err := secret.Hash(sec)
	if err != nil {
		return "", false, err
	}
	client := models.Client{ID: models.NewID(), Name: "default", Active: true}
	if err = db.Create(&client).Error; err != nil {
		return "", false, err
	}
	key := models.APIKey{
		ID: models.NewID(), ClientID: client.ID, Prefix: prefix, Hash: hash,
		Scopes: "messages:write,messages:read,devices:read,sims:read,admin", Active: true,
	}
	if err = db.Create(&key).Error; err != nil {
		return "", false, err
	}
	return "wsms_" + prefix + "." + sec, true, nil
}

// BootstrapEnrollmentToken creates a single-use device enrollment token valid 24h,
// returning the plaintext to log ONCE (only if no unused tokens currently exist).
func BootstrapEnrollmentToken(db *gorm.DB) (token string, created bool, err error) {
	var n int64
	if err = db.Model(&models.EnrollmentToken{}).
		Where("used = false AND expires_at > now()").Count(&n).Error; err != nil || n > 0 {
		return "", false, err
	}
	plain, err := secret.RandomToken(24)
	if err != nil {
		return "", false, err
	}
	et := models.EnrollmentToken{
		ID: models.NewID(), TokenHash: secret.SHA256Hex(plain), Label: "bootstrap",
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}
	if err = db.Create(&et).Error; err != nil {
		return "", false, err
	}
	return plain, true, nil
}

// LoadPrefixes reads the (possibly operator-edited) prefix table from the DB.
func LoadPrefixes(db *gorm.DB) (map[string]models.Operator, error) {
	var rows []models.OperatorPrefix
	if err := db.Find(&rows).Error; err != nil {
		return nil, err
	}
	m := make(map[string]models.Operator, len(rows))
	for _, r := range rows {
		m[r.Prefix] = r.Operator
	}
	return m, nil
}
