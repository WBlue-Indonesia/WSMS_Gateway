package admin

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nizwar/wsms-gateway/server/internal/models"
)

// ---- Overview ----

func (s *Server) overview(c *gin.Context) {
	since := time.Now().Add(-24 * time.Hour)

	var totalDevices, totalSims, readySims int64
	s.db.Model(&models.Device{}).Count(&totalDevices)
	s.db.Model(&models.Sim{}).Count(&totalSims)
	s.db.Model(&models.Sim{}).Where("status = ?", models.SimReady).Count(&readySims)

	type kv struct {
		K string
		N int64
	}
	var statusRows []kv
	s.db.Model(&models.Message{}).Select("status as k, count(*) as n").
		Where("created_at > ?", since).Group("status").Scan(&statusRows)
	statusMap := map[string]int64{}
	var total24 int64
	for _, r := range statusRows {
		statusMap[r.K] = r.N
		total24 += r.N
	}
	delivered := statusMap["DELIVERED"]
	failed := statusMap["FAILED"] + statusMap["EXPIRED"]
	queueDepth := statusMap["QUEUED"] + statusMap["ROUTING"] + statusMap["DISPATCHED"] + statusMap["AWAITING_ACK"]

	var operatorRows []kv
	s.db.Model(&models.Message{}).Select("target_operator as k, count(*) as n").
		Where("created_at > ?", since).Group("target_operator").Order("n desc").Scan(&operatorRows)

	// On-net vs fallback among assigned messages.
	var onNet, fallback int64
	s.db.Model(&models.Message{}).
		Where("assigned_operator IS NOT NULL AND assigned_operator = target_operator AND created_at > ?", since).Count(&onNet)
	s.db.Model(&models.Message{}).
		Where("assigned_operator IS NOT NULL AND assigned_operator <> target_operator AND created_at > ?", since).Count(&fallback)

	var segToday int64
	s.db.Model(&models.Sim{}).Select("COALESCE(sum(sent_today),0)").Scan(&segToday)

	// per-operator ready SIMs
	var opReady []kv
	s.db.Model(&models.Sim{}).Select("operator as k, count(*) as n").
		Joins("JOIN devices d ON d.id = sims.device_id").
		Where("sims.status = ? AND d.status = ?", models.SimReady, models.DevOnline).
		Group("operator").Scan(&opReady)

	renderPage(c, "overview", gin.H{
		"OnlineDevices": s.hub.OnlineCount(),
		"TotalDevices":  totalDevices,
		"ReadySims":     readySims,
		"TotalSims":     totalSims,
		"QueueDepth":    queueDepth,
		"Delivered":     delivered,
		"Failed":        failed,
		"Total24":       total24,
		"SuccessNum":    delivered,
		"SuccessDen":    delivered + failed,
		"OnNet":         onNet,
		"Fallback":      fallback,
		"SegToday":      segToday,
		"StatusMap":     statusMap,
		"Operators":     operatorRows,
		"OpReady":       opReady,
	})
}

// ---- Messages ----

func (s *Server) messagesPage(c *gin.Context) {
	renderPage(c, "messages", gin.H{
		"Messages": s.queryMessages(c),
		"Q":        c.Query("q"),
		"Status":   c.Query("status"),
		"Operator": c.Query("operator"),
	})
}

func (s *Server) messagesRows(c *gin.Context) {
	renderFragment(c, "messages_rows", gin.H{"Messages": s.queryMessages(c), "Role": s.role(c)})
}

func (s *Server) queryMessages(c *gin.Context) []models.Message {
	q := s.db.Model(&models.Message{}).Order("created_at desc").Limit(100)
	if v := c.Query("status"); v != "" {
		q = q.Where("status = ?", v)
	}
	if v := c.Query("operator"); v != "" {
		q = q.Where("target_operator = ?", v)
	}
	if v := c.Query("q"); v != "" {
		q = q.Where("target_msisdn ILIKE ? OR id::text ILIKE ? OR dedup_key ILIKE ?", "%"+v+"%", "%"+v+"%", "%"+v+"%")
	}
	var msgs []models.Message
	q.Find(&msgs)
	return msgs
}

func (s *Server) messageDetail(c *gin.Context) {
	var msg models.Message
	if s.db.First(&msg, "id = ?", c.Param("id")).Error != nil {
		c.String(http.StatusNotFound, "not found")
		return
	}
	var events []models.MessageEvent
	s.db.Where("message_id = ?", msg.ID).Order("created_at").Find(&events)
	renderFragment(c, "message_detail", gin.H{"M": msg, "Events": events, "Role": s.role(c)})
}

// unmaskMSISDN reveals a full number, gated by role and written to the audit log (docs/07 §6.4).
func (s *Server) unmaskMSISDN(c *gin.Context) {
	if !canUnmask(s.role(c)) {
		c.String(http.StatusForbidden, "not permitted")
		return
	}
	var msg models.Message
	if s.db.First(&msg, "id = ?", c.Param("id")).Error != nil {
		c.String(http.StatusNotFound, "not found")
		return
	}
	s.audit(c, "pii.unmask.msisdn", "message", msg.ID, c.PostForm("reason"))
	renderFragment(c, "unmask", gin.H{"MSISDN": msg.TargetMSISDN})
}

// ---- Fleet ----

func (s *Server) fleet(c *gin.Context) {
	var devices []models.Device
	s.db.Order("name").Find(&devices)
	type deviceView struct {
		D      models.Device
		Online bool
		Sims   []models.Sim
	}
	views := make([]deviceView, 0, len(devices))
	for _, d := range devices {
		var sims []models.Sim
		s.db.Where("device_id = ?", d.ID).Order("slot").Find(&sims)
		views = append(views, deviceView{D: d, Online: s.hub.Online(d.ID), Sims: sims})
	}
	renderPage(c, "fleet", gin.H{"Devices": views})
}

// ---- Enrollment ----

func (s *Server) enrollmentPage(c *gin.Context) {
	var tokens []models.EnrollmentToken
	s.db.Order("created_at desc").Limit(50).Find(&tokens)
	renderPage(c, "enrollment", gin.H{"Tokens": tokens, "Issued": c.Query("issued")})
}

func (s *Server) issueEnrollment(c *gin.Context) {
	if r := s.role(c); r != "owner" && r != "operator" {
		c.String(http.StatusForbidden, "not permitted")
		return
	}
	plain, hash := newEnrollToken()
	et := models.EnrollmentToken{
		ID: models.NewID(), TokenHash: hash, Label: c.PostForm("label"),
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}
	if err := s.db.Create(&et).Error; err != nil {
		c.String(http.StatusInternalServerError, "could not issue")
		return
	}
	s.audit(c, "enroll.token.create", "enrollment_token", et.ID, et.Label)
	c.Redirect(http.StatusSeeOther, "/admin/enrollment?issued="+plain)
}

// ---- Clients ----

func (s *Server) clientsPage(c *gin.Context) {
	var clients []models.Client
	s.db.Order("name").Find(&clients)
	type row struct {
		Client models.Client
		Keys   []models.APIKey
	}
	rows := make([]row, 0, len(clients))
	for _, cl := range clients {
		var keys []models.APIKey
		s.db.Where("client_id = ?", cl.ID).Find(&keys)
		rows = append(rows, row{Client: cl, Keys: keys})
	}
	renderPage(c, "clients", gin.H{"Clients": rows})
}
