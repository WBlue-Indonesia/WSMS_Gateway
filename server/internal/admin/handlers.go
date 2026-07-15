package admin

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nizwar/wsms-gateway/server/internal/models"
	"github.com/nizwar/wsms-gateway/server/internal/router"
	"github.com/nizwar/wsms-gateway/server/internal/secret"
	"github.com/nizwar/wsms-gateway/server/internal/smstext"
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

	// Fleet quota utilization: segments sent today vs total daily quota across live SIMs.
	var quotaSent, quotaTotal int64
	s.db.Model(&models.Sim{}).Where("status <> ?", models.SimAbsent).
		Select("COALESCE(sum(sent_today),0)").Scan(&quotaSent)
	s.db.Model(&models.Sim{}).Where("status <> ?", models.SimAbsent).
		Select("COALESCE(sum(daily_quota),0)").Scan(&quotaTotal)

	// 7-day message volume sparkline (fills gaps with zero so the line is continuous).
	series := make([]int, 7)
	{
		type dayRow struct {
			Day time.Time
			N   int64
		}
		var rows []dayRow
		s.db.Model(&models.Message{}).
			Select("date_trunc('day', created_at) as day, count(*) as n").
			Where("created_at > ?", time.Now().AddDate(0, 0, -6).Truncate(24*time.Hour)).
			Group("day").Order("day").Scan(&rows)
		byDay := map[string]int64{}
		for _, r := range rows {
			byDay[r.Day.Local().Format("2006-01-02")] = r.N
		}
		today := time.Now()
		for i := 0; i < 7; i++ {
			d := today.AddDate(0, 0, -6+i).Format("2006-01-02")
			series[i] = int(byDay[d])
		}
	}

	// Status funnel (only non-zero), in lifecycle order, for a share-of-total bar list.
	order := []models.MessageStatus{
		models.MsgDelivered, models.MsgSent, models.MsgSentUnconfirmed, models.MsgAwaitingAck,
		models.MsgDispatched, models.MsgRouting, models.MsgQueued, models.MsgFailed,
		models.MsgExpired, models.MsgCancelled,
	}
	var statusBars []kv
	for _, st := range order {
		if n := statusMap[string(st)]; n > 0 {
			statusBars = append(statusBars, kv{K: string(st), N: n})
		}
	}

	// Bar-scaling maxima.
	var opMax, opReadyMax int64 = 1, 1
	for _, r := range operatorRows {
		if r.N > opMax {
			opMax = r.N
		}
	}
	for _, r := range opReady {
		if r.N > opReadyMax {
			opReadyMax = r.N
		}
	}

	// Devices needing attention: offline with a live SIM history, or repeated wake misses.
	var attention int64
	s.db.Model(&models.Device{}).Where("status <> ? AND (status = ? OR wake_misses > 0)",
		models.DevDisabled, models.DevOffline).Count(&attention)

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
		"QuotaSent":     quotaSent,
		"QuotaTotal":    quotaTotal,
		"Series":        series,
		"StatusMap":     statusMap,
		"StatusBars":    statusBars,
		"Operators":     operatorRows,
		"OpMax":         opMax,
		"OpReady":       opReady,
		"OpReadyMax":    opReadyMax,
		"Attention":     attention,
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

// composePage renders the "send SMS from the console" form.
func (s *Server) composePage(c *gin.Context) {
	renderPage(c, "compose", gin.H{"Error": c.Query("error"), "Sent": c.Query("sent")})
}

// sendCompose queues a message submitted by an operator from the admin console. It runs
// the same normalization/operator-detection/segmentation as the public API and attributes
// the send to a dedicated "admin-console" client.
func (s *Server) sendCompose(c *gin.Context) {
	if r := s.role(c); r != "owner" && r != "operator" {
		c.String(http.StatusForbidden, "not permitted")
		return
	}
	to := c.PostForm("to")
	body := c.PostForm("message")
	canonical, ok := router.NormalizeMSISDN(to)
	if !ok || body == "" {
		c.Redirect(http.StatusSeeOther, "/admin/compose?error="+url.QueryEscape("invalid number or empty message"))
		return
	}
	policy := models.RoutingPolicy(c.PostForm("routing_policy"))
	switch policy {
	case models.PolicyOnNetPref, models.PolicyOnNetStrict, models.PolicyAny:
	default:
		policy = models.PolicyOnNetPref
	}
	ttl := s.cfg.DefaultTTL
	if v := c.PostForm("ttl_seconds"); v != "" {
		if n, err := time.ParseDuration(v + "s"); err == nil && n > 0 {
			ttl = n
		}
	}

	enc, segs := smstext.Analyze(body)
	op := s.engine.Detect(canonical)
	msg := models.Message{
		ID: models.NewID(), ClientID: s.adminClientID(), TargetMSISDN: canonical,
		TargetOperator: op, Body: body, Encoding: models.Encoding(enc), Segments: segs,
		Status: models.MsgQueued, RoutingPolicy: policy, MaxAttempts: 3,
		ExpiresAt: time.Now().Add(ttl),
	}
	if err := s.db.Create(&msg).Error; err != nil {
		c.Redirect(http.StatusSeeOther, "/admin/compose?error="+url.QueryEscape("could not queue"))
		return
	}
	s.db.Create(&models.MessageEvent{ID: models.NewID(), MessageID: msg.ID, EventType: models.EvSubmitted, CreatedAt: time.Now()})
	s.audit(c, "message.send", "message", msg.ID, string(op))
	c.Redirect(http.StatusSeeOther, "/admin/compose?sent="+url.QueryEscape(string(op)))
}

// adminClientID returns (creating if needed) the client that owns console-sent messages.
func (s *Server) adminClientID() string {
	var cl models.Client
	if err := s.db.Where("name = ?", "admin-console").First(&cl).Error; err == nil {
		return cl.ID
	}
	cl = models.Client{ID: models.NewID(), Name: "admin-console", Active: true}
	s.db.Create(&cl)
	return cl.ID
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

// deviceView is a device plus its SIMs and a distinct-operator summary, shared by the
// fleet list and the per-device detail fragment.
type deviceView struct {
	D      models.Device
	Online bool
	Sims   []models.Sim
	Ops    []models.Operator // distinct operators among its SIMs (for the summary line)
}

// deviceViews builds the fleet view. id=="" loads all devices; otherwise just that one.
func (s *Server) deviceViews(id string) []deviceView {
	var devices []models.Device
	q := s.db.Order("name")
	if id != "" {
		q = q.Where("id = ?", id)
	}
	q.Find(&devices)
	views := make([]deviceView, 0, len(devices))
	for _, d := range devices {
		var sims []models.Sim
		s.db.Where("device_id = ?", d.ID).Order("slot").Find(&sims)
		ops := make([]models.Operator, 0, len(sims))
		seen := map[models.Operator]bool{}
		for _, sm := range sims {
			if sm.Operator != "" && !seen[sm.Operator] {
				seen[sm.Operator] = true
				ops = append(ops, sm.Operator)
			}
		}
		views = append(views, deviceView{D: d, Online: s.hub.Online(d.ID), Sims: sims, Ops: ops})
	}
	return views
}

func (s *Server) fleet(c *gin.Context) {
	renderPage(c, "fleet", gin.H{"Devices": s.deviceViews(""), "CanMutate": s.canMutate(c)})
}

// fleetDetail renders one device's SIMs and controls into the drawer / bottom-sheet.
func (s *Server) fleetDetail(c *gin.Context) {
	views := s.deviceViews(c.Param("id"))
	if len(views) == 0 {
		c.String(http.StatusNotFound, "device not found")
		return
	}
	renderFragment(c, "fleet_detail", gin.H{"Dev": views[0], "CanMutate": s.canMutate(c)})
}

// ---- Enrollment ----

func (s *Server) enrollmentPage(c *gin.Context) {
	var tokens []models.EnrollmentToken
	s.db.Order("created_at desc").Limit(50).Find(&tokens)
	data := gin.H{"Tokens": tokens, "CanMutate": s.canMutate(c)}
	// The just-issued token is fetched from the one-time flash (the redirect carried only
	// an opaque id) so the plaintext pairing token never lands in the URL/history/logs.
	if it, ok := s.popFlash(c.Query("issued")); ok {
		data["Issued"] = it.secret
		_, qr := pairQR(s.baseURL(c), it.secret)
		data["QR"] = qr
	}
	renderPage(c, "enrollment", data)
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
	fid := s.putFlash("enroll", et.Label, plain)
	c.Redirect(http.StatusSeeOther, "/admin/enrollment?issued="+fid)
}

// deleteEnrollment removes an enrollment token. If the token already paired a device,
// that device is unlinked too: its live WebSocket is force-closed and the row is
// soft-deleted, so the phone disconnects immediately and cannot reconnect.
func (s *Server) deleteEnrollment(c *gin.Context) {
	if r := s.role(c); r != "owner" && r != "operator" {
		c.String(http.StatusForbidden, "not permitted")
		return
	}
	var et models.EnrollmentToken
	if s.db.First(&et, "id = ?", c.Param("id")).Error != nil {
		c.String(http.StatusNotFound, "not found")
		return
	}
	if et.DeviceID != nil && *et.DeviceID != "" {
		// unlinkDevice also deletes this token (device_id match), so the phone drops now.
		s.unlinkDevice(*et.DeviceID)
		s.audit(c, "device.unlink", "device", *et.DeviceID, "via enrollment delete")
	} else {
		s.db.Delete(&models.EnrollmentToken{}, "id = ?", et.ID)
	}
	s.audit(c, "enroll.token.delete", "enrollment_token", et.ID, et.Label)
	c.Redirect(http.StatusSeeOther, "/admin/enrollment")
}

// ---- Clients & API keys ----

// knownScopes is the fixed menu the "new key" form offers (checkbox list).
var knownScopes = []string{"messages:write", "messages:read", "devices:read", "sims:read", "admin"}

func (s *Server) clientsPage(c *gin.Context) {
	var clients []models.Client
	s.db.Order("name").Find(&clients)
	type row struct {
		Client        models.Client
		Keys          []models.APIKey
		WebhookSecret bool
	}
	rows := make([]row, 0, len(clients))
	for _, cl := range clients {
		var keys []models.APIKey
		s.db.Where("client_id = ?", cl.ID).Order("created_at").Find(&keys)
		rows = append(rows, row{Client: cl, Keys: keys, WebhookSecret: len(cl.WebhookSecretEnc) > 0})
	}
	data := gin.H{
		"Clients":   rows,
		"Scopes":    knownScopes,
		"CanMutate": s.canMutate(c),
		"Error":     c.Query("error"),
	}
	// One-time secret reveal: the redirect carried only an opaque id (never the secret).
	if it, ok := s.popFlash(c.Query("revealed")); ok {
		data["Reveal"] = it.secret
		data["RevealKind"] = it.kind
		data["RevealLabel"] = it.label
	}
	renderPage(c, "clients", data)
}

// createClient adds a new API caller (no keys yet).
func (s *Server) createClient(c *gin.Context) {
	if !s.canMutate(c) {
		c.String(http.StatusForbidden, "not permitted")
		return
	}
	name := strings.TrimSpace(c.PostForm("name"))
	if name == "" {
		c.Redirect(http.StatusSeeOther, "/admin/clients?error="+url.QueryEscape("client name required"))
		return
	}
	cl := models.Client{ID: models.NewID(), Name: name, Active: true}
	if err := s.db.Create(&cl).Error; err != nil {
		c.Redirect(http.StatusSeeOther, "/admin/clients?error="+url.QueryEscape("could not create client"))
		return
	}
	s.audit(c, "client.create", "client", cl.ID, name)
	c.Redirect(http.StatusSeeOther, "/admin/clients")
}

// toggleClient flips a client between active and disabled. A disabled client's keys
// stay on file but every request is rejected (clientAuth filters active=true).
func (s *Server) toggleClient(c *gin.Context) {
	if !s.canMutate(c) {
		c.String(http.StatusForbidden, "not permitted")
		return
	}
	var cl models.Client
	if s.db.First(&cl, "id = ?", c.Param("id")).Error != nil {
		c.String(http.StatusNotFound, "not found")
		return
	}
	next := !cl.Active
	s.db.Model(&models.Client{}).Where("id = ?", cl.ID).
		Update("active", next)
	act := "client.disable"
	if next {
		act = "client.enable"
	}
	s.audit(c, act, "client", cl.ID, cl.Name)
	c.Redirect(http.StatusSeeOther, "/admin/clients")
}

// deleteClient soft-deletes a client and revokes all its keys.
func (s *Server) deleteClient(c *gin.Context) {
	if !s.canMutate(c) {
		c.String(http.StatusForbidden, "not permitted")
		return
	}
	id := c.Param("id")
	s.db.Where("client_id = ?", id).Delete(&models.APIKey{})
	s.db.Delete(&models.Client{}, "id = ?", id)
	s.audit(c, "client.delete", "client", id, "")
	c.Redirect(http.StatusSeeOther, "/admin/clients")
}

// createKey mints a bearer API key for a client and reveals the full token ONCE via a
// one-time flash (never the URL). Only the Argon2id hash of the secret is stored.
func (s *Server) createKey(c *gin.Context) {
	if !s.canMutate(c) {
		c.String(http.StatusForbidden, "not permitted")
		return
	}
	clientID := c.Param("id")
	var cl models.Client
	if s.db.First(&cl, "id = ?", clientID).Error != nil {
		c.String(http.StatusNotFound, "client not found")
		return
	}
	// Collect selected scopes from the checkbox list.
	var scopes []string
	for _, sc := range knownScopes {
		if c.PostForm("scope_"+strings.ReplaceAll(sc, ":", "_")) != "" {
			scopes = append(scopes, sc)
		}
	}
	if len(scopes) == 0 {
		c.Redirect(http.StatusSeeOther, "/admin/clients?error="+url.QueryEscape("select at least one scope"))
		return
	}

	// Prefix is the public lookup id auth resolves on, so it must be unique among live
	// keys. Collisions are astronomically unlikely, but retry rather than risk a silent
	// ambiguous-auth key.
	var prefix string
	for i := 0; i < 6; i++ {
		p, _ := secret.RandomToken(4)
		var n int64
		s.db.Model(&models.APIKey{}).Where("prefix = ?", p).Count(&n)
		if n == 0 {
			prefix = p
			break
		}
	}
	if prefix == "" {
		c.Redirect(http.StatusSeeOther, "/admin/clients?error="+url.QueryEscape("could not allocate key prefix"))
		return
	}
	sec, _ := secret.RandomToken(32)
	hash, err := secret.Hash(sec)
	if err != nil {
		c.Redirect(http.StatusSeeOther, "/admin/clients?error="+url.QueryEscape("could not mint key"))
		return
	}
	key := models.APIKey{
		ID: models.NewID(), ClientID: clientID, Prefix: prefix, Hash: hash,
		Scopes: strings.Join(scopes, ","), Active: true,
	}
	if err := s.db.Create(&key).Error; err != nil {
		c.Redirect(http.StatusSeeOther, "/admin/clients?error="+url.QueryEscape("could not store key"))
		return
	}
	token := "wsms_" + prefix + "." + sec
	s.audit(c, "apikey.create", "api_key", key.ID, cl.Name)
	fid := s.putFlash("apikey", cl.Name+" · wsms_"+prefix, token)
	c.Redirect(http.StatusSeeOther, "/admin/clients?revealed="+fid)
}

// revokeKey soft-deletes an API key (its bearer secret stops working immediately).
func (s *Server) revokeKey(c *gin.Context) {
	if !s.canMutate(c) {
		c.String(http.StatusForbidden, "not permitted")
		return
	}
	id := c.Param("id")
	s.db.Delete(&models.APIKey{}, "id = ?", id)
	s.audit(c, "apikey.revoke", "api_key", id, "")
	c.Redirect(http.StatusSeeOther, "/admin/clients")
}

// rotateWebhookSecret generates a new webhook signing secret for a client, stores it
// AES-GCM-encrypted, and reveals the plaintext once via a one-time flash (docs/06 §1.8).
func (s *Server) rotateWebhookSecret(c *gin.Context) {
	if !s.canMutate(c) {
		c.String(http.StatusForbidden, "not permitted")
		return
	}
	var cl models.Client
	if s.db.First(&cl, "id = ?", c.Param("id")).Error != nil {
		c.String(http.StatusNotFound, "not found")
		return
	}
	plain, _ := secretToken()
	enc, err := sealSecret(s.cfg.MasterKey[:], plain)
	if err != nil {
		c.String(http.StatusInternalServerError, "seal failed")
		return
	}
	if s.db.Model(&models.Client{}).Where("id = ?", cl.ID).
		Update("webhook_secret_enc", enc).Error != nil {
		c.String(http.StatusInternalServerError, "store failed")
		return
	}
	s.audit(c, "webhook.secret.rotate", "client", cl.ID, cl.Name)
	fid := s.putFlash("webhook", cl.Name, plain)
	c.Redirect(http.StatusSeeOther, "/admin/clients?revealed="+fid)
}

// enableKeySigning gives an API key a separate signing secret (encrypted at rest) so
// inbound requests can be HMAC-verified (amendment F3). Revealed once via flash.
func (s *Server) enableKeySigning(c *gin.Context) {
	if !s.canMutate(c) {
		c.String(http.StatusForbidden, "not permitted")
		return
	}
	var key models.APIKey
	if s.db.First(&key, "id = ?", c.Param("id")).Error != nil {
		c.String(http.StatusNotFound, "not found")
		return
	}
	plain, _ := secretToken()
	enc, err := sealSecret(s.cfg.MasterKey[:], plain)
	if err != nil {
		c.String(http.StatusInternalServerError, "seal failed")
		return
	}
	if s.db.Model(&models.APIKey{}).Where("id = ?", key.ID).
		Update("signing_secret_enc", enc).Error != nil {
		c.String(http.StatusInternalServerError, "store failed")
		return
	}
	s.audit(c, "apikey.signing.enable", "api_key", key.ID, "wsms_"+key.Prefix)
	fid := s.putFlash("signing", "wsms_"+key.Prefix, plain)
	c.Redirect(http.StatusSeeOther, "/admin/clients?revealed="+fid)
}
