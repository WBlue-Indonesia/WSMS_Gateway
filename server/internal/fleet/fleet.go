// Package fleet holds device/SIM lifecycle helpers shared by the API (enrollment)
// and the dispatcher (sim_report handling).
package fleet

import (
	"time"

	"github.com/nizwar/wsms-gateway/server/internal/models"
	"github.com/nizwar/wsms-gateway/server/internal/router"
	"github.com/nizwar/wsms-gateway/server/internal/ws"
	"gorm.io/gorm"
)

// UpsertSims reconciles the reported SIM set for a device: it creates/updates the
// reported SIMs (keyed by device_id+subscription_id) and marks any previously-known
// SIMs that are no longer present as ABSENT. Operator is derived from carrierName
// unless the SIM has a manual OperatorLocked override (docs/03 §6).
func UpsertSims(db *gorm.DB, deviceID string, report []ws.SimInfo) error {
	seen := make(map[int]bool, len(report))
	for _, si := range report {
		seen[si.SubscriptionID] = true
		var sim models.Sim
		err := db.Where("device_id = ? AND subscription_id = ?", deviceID, si.SubscriptionID).First(&sim).Error

		op := router.CarrierToOperator(si.CarrierName)
		msisdn := ""
		if n, ok := router.NormalizeMSISDN(si.Number); ok {
			msisdn = n
		}

		if err == gorm.ErrRecordNotFound {
			sim = models.Sim{
				ID:             models.NewID(),
				DeviceID:       deviceID,
				Slot:           si.Slot,
				SubscriptionID: si.SubscriptionID,
				Operator:       op,
				MSISDN:         msisdn,
				Status:         models.SimReady,
				DailyQuota:     200,
				MinGapMs:       8000,
				HealthScore:    100,
			}
			if err := db.Create(&sim).Error; err != nil {
				return err
			}
			continue
		} else if err != nil {
			return err
		}

		updates := map[string]any{
			"slot":       si.Slot,
			"status":     models.SimReady,
			"updated_at": time.Now(),
		}
		if !sim.OperatorLocked {
			updates["operator"] = op
		}
		if msisdn != "" {
			updates["msisdn"] = msisdn
		}
		if err := db.Model(&models.Sim{}).Where("id = ?", sim.ID).Updates(updates).Error; err != nil {
			return err
		}
	}

	// SIMs previously known for this device but absent from the report → ABSENT.
	var known []models.Sim
	db.Where("device_id = ?", deviceID).Find(&known)
	for _, s := range known {
		if !seen[s.SubscriptionID] && s.Status != models.SimAbsent {
			db.Model(&models.Sim{}).Where("id = ?", s.ID).
				Updates(map[string]any{"status": models.SimAbsent, "updated_at": time.Now()})
		}
	}
	return nil
}

// MaxDailyQuota is the sanity ceiling for a SIM's daily quota (segments/day). Shared by
// the admin console and the device-initiated set_quota path.
const MaxDailyQuota = 100000

// DeviceSimStates returns the current server-side state of every (non-deleted) SIM for a
// device, for pushing back to the phone as a ws.SimState frame.
func DeviceSimStates(db *gorm.DB, deviceID string) ([]ws.SimState, error) {
	var sims []models.Sim
	if err := db.Where("device_id = ?", deviceID).Order("slot").Find(&sims).Error; err != nil {
		return nil, err
	}
	out := make([]ws.SimState, 0, len(sims))
	for _, s := range sims {
		out = append(out, ws.SimState{
			SimID:          s.ID,
			SubscriptionID: s.SubscriptionID,
			Slot:           s.Slot,
			Operator:       string(s.Operator),
			MSISDN:         s.MSISDN,
			Status:         string(s.Status),
			DailyQuota:     s.DailyQuota,
			SentToday:      s.SentToday,
			HealthScore:    s.HealthScore,
		})
	}
	return out, nil
}

// SetSimQuota applies a device-initiated daily-quota change, resolving the SIM by
// (device_id, subscription_id) and clamping to [0, MaxDailyQuota]. Returns the SIM's
// server id and the clamped value so the caller can audit it. ok=false if no such SIM.
func SetSimQuota(db *gorm.DB, deviceID string, subID, quota int) (simID string, clamped int, ok bool) {
	if quota < 0 {
		quota = 0
	}
	if quota > MaxDailyQuota {
		quota = MaxDailyQuota
	}
	var sim models.Sim
	if db.Where("device_id = ? AND subscription_id = ?", deviceID, subID).First(&sim).Error != nil {
		return "", 0, false
	}
	db.Model(&models.Sim{}).Where("id = ?", sim.ID).
		Updates(map[string]any{"daily_quota": quota, "updated_at": time.Now()})
	return sim.ID, quota, true
}
