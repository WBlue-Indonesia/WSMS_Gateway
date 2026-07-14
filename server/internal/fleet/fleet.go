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
