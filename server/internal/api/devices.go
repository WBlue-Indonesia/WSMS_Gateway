package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/nizwar/wsms-gateway/server/internal/models"
)

func (s *Server) listDevices(c *gin.Context) {
	var devices []models.Device
	s.db.Order("name").Find(&devices)
	// annotate live presence from the hub (authoritative for "online right now")
	out := make([]gin.H, 0, len(devices))
	for _, d := range devices {
		out = append(out, gin.H{"device": d, "online": s.hub.Online(d.ID)})
	}
	c.JSON(http.StatusOK, gin.H{"devices": out, "online_count": s.hub.OnlineCount()})
}

func (s *Server) listSims(c *gin.Context) {
	var sims []models.Sim
	s.db.Order("device_id, slot").Find(&sims)

	// F14: expose per-operator on-net readiness so ON_NET_STRICT clients can pre-check.
	if c.Query("on_net_ready") == "true" {
		type row struct {
			Operator string
			N        int64
		}
		var rows []row
		s.db.Model(&models.Sim{}).
			Select("operator, count(*) as n").
			Joins("JOIN devices d ON d.id = sims.device_id").
			Where("sims.status = ? AND d.status = ?", models.SimReady, models.DevOnline).
			Group("operator").Scan(&rows)
		ready := gin.H{}
		for _, r := range rows {
			ready[r.Operator] = r.N
		}
		c.JSON(http.StatusOK, gin.H{"sims": sims, "on_net_ready": ready})
		return
	}
	c.JSON(http.StatusOK, gin.H{"sims": sims})
}
