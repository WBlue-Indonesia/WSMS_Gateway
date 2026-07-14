// Package metrics exposes Prometheus gauges computed from the DB + WS hub at scrape
// time (docs/06 §3.4). A pull-at-scrape collector avoids threading counters through
// every package; the values are always consistent with the database.
package metrics

import (
	"github.com/nizwar/wsms-gateway/server/internal/models"
	"github.com/nizwar/wsms-gateway/server/internal/ws"
	"github.com/prometheus/client_golang/prometheus"
	"gorm.io/gorm"
)

type Collector struct {
	db  *gorm.DB
	hub *ws.Hub

	devicesOnline *prometheus.Desc
	simsReady     *prometheus.Desc
	simsTotal     *prometheus.Desc
	queueDepth    *prometheus.Desc
	messages24h   *prometheus.Desc
	simSentToday  *prometheus.Desc
	webhookByStat *prometheus.Desc
}

func New(db *gorm.DB, hub *ws.Hub) *Collector {
	return &Collector{
		db:  db,
		hub: hub,
		devicesOnline: prometheus.NewDesc("wsms_devices_online", "Devices with a live WebSocket connection.", nil, nil),
		simsReady:     prometheus.NewDesc("wsms_sims_ready", "SIMs in READY status.", nil, nil),
		simsTotal:     prometheus.NewDesc("wsms_sims_total", "Total SIMs known.", nil, nil),
		queueDepth:    prometheus.NewDesc("wsms_queue_depth", "Messages currently in each non-terminal status.", []string{"status"}, nil),
		messages24h:   prometheus.NewDesc("wsms_messages_24h", "Messages created in the last 24h by terminal-ish status.", []string{"status"}, nil),
		simSentToday:  prometheus.NewDesc("wsms_sim_sent_today", "Segments sent today per SIM.", []string{"sim", "operator"}, nil),
		webhookByStat: prometheus.NewDesc("wsms_webhook_deliveries", "Webhook deliveries by status.", []string{"status"}, nil),
	}
}

func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.devicesOnline
	ch <- c.simsReady
	ch <- c.simsTotal
	ch <- c.queueDepth
	ch <- c.messages24h
	ch <- c.simSentToday
	ch <- c.webhookByStat
}

func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	ch <- prometheus.MustNewConstMetric(c.devicesOnline, prometheus.GaugeValue, float64(c.hub.OnlineCount()))

	var ready, total int64
	c.db.Model(&models.Sim{}).Where("status = ?", models.SimReady).Count(&ready)
	c.db.Model(&models.Sim{}).Count(&total)
	ch <- prometheus.MustNewConstMetric(c.simsReady, prometheus.GaugeValue, float64(ready))
	ch <- prometheus.MustNewConstMetric(c.simsTotal, prometheus.GaugeValue, float64(total))

	type kv struct {
		K string
		N int64
	}
	var q []kv
	c.db.Model(&models.Message{}).Select("status as k, count(*) as n").
		Where("status IN ?", []models.MessageStatus{models.MsgQueued, models.MsgRouting, models.MsgDispatched, models.MsgAwaitingAck}).
		Group("status").Scan(&q)
	for _, r := range q {
		ch <- prometheus.MustNewConstMetric(c.queueDepth, prometheus.GaugeValue, float64(r.N), r.K)
	}

	var m []kv
	c.db.Model(&models.Message{}).Select("status as k, count(*) as n").
		Where("created_at > now() - interval '24 hours'").Group("status").Scan(&m)
	for _, r := range m {
		ch <- prometheus.MustNewConstMetric(c.messages24h, prometheus.GaugeValue, float64(r.N), r.K)
	}

	type simrow struct {
		ID        string
		Operator  string
		SentToday int
	}
	var sims []simrow
	c.db.Model(&models.Sim{}).Select("id, operator, sent_today").Scan(&sims)
	for _, s := range sims {
		ch <- prometheus.MustNewConstMetric(c.simSentToday, prometheus.GaugeValue, float64(s.SentToday), s.ID[:8], s.Operator)
	}

	var wh []kv
	c.db.Model(&models.WebhookDelivery{}).Select("status as k, count(*) as n").Group("status").Scan(&wh)
	for _, r := range wh {
		ch <- prometheus.MustNewConstMetric(c.webhookByStat, prometheus.GaugeValue, float64(r.N), r.K)
	}
}
