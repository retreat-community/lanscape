package server

import (
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	"github.com/retreat-community/lanscape/internal/topo"
)

// Metrics exported on /metrics.
type Metrics struct {
	Registry    *prometheus.Registry
	agentUp     *prometheus.GaugeVec
	pathBPS     *prometheus.GaugeVec
	pathRTT     *prometheus.GaugeVec
	pathVerdict *prometheus.GaugeVec
	pathLoss    *prometheus.GaugeVec
	runs        *prometheus.CounterVec
	runSeconds  *prometheus.HistogramVec
	problems    *prometheus.GaugeVec
	devices     prometheus.Gauge
	monUp       *prometheus.GaugeVec
	monLatency  *prometheus.GaugeVec
	notifyFail  *prometheus.CounterVec
}

var verdictValue = map[string]float64{topo.Green: 0, topo.None: 1, topo.Yellow: 2, topo.Purple: 3, topo.Red: 4}

// NewMetrics registers all collectors.
func NewMetrics() *Metrics {
	r := prometheus.NewRegistry()
	r.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	m := &Metrics{Registry: r,
		agentUp: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "lanscape_agent_up",
			Help: "Agent control channel connected (1) or not (0)."}, []string{"agent", "name", "kind"}),
		pathBPS: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "lanscape_path_throughput_bits_per_second",
			Help: "Best TCP throughput of the last run per path."}, []string{"segment", "src", "src_if", "dst", "dst_if"}),
		pathRTT: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "lanscape_path_rtt_seconds",
			Help: "Average ICMP RTT of the last run per path."}, []string{"segment", "src", "src_if", "dst", "dst_if"}),
		pathVerdict: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "lanscape_path_verdict",
			Help: "Verdict per path: 0 green, 1 none, 2 yellow, 3 purple (CPU-bound), 4 red."},
			[]string{"segment", "src", "src_if", "dst", "dst_if"}),
		pathLoss: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "lanscape_path_loss_ratio",
			Help: "ICMP packet loss ratio of the last run per path."}, []string{"segment", "src", "src_if", "dst", "dst_if"}),
		runs: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "lanscape_runs_total",
			Help: "Finished runs."}, []string{"kind", "status"}),
		runSeconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "lanscape_run_duration_seconds",
			Help: "Run duration.", Buckets: prometheus.ExponentialBuckets(5, 2, 10)}, []string{"kind"}),
		problems: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "lanscape_problems",
			Help: "Problems found by the last run by kind."}, []string{"kind"}),
		devices: prometheus.NewGauge(prometheus.GaugeOpts{Name: "lanscape_devices",
			Help: "Known devices (agents and discovered)."}),
		monUp: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "lanscape_monitor_up",
			Help: "Monitor status: 1 up, 0.5 degraded, 0 down."}, []string{"monitor", "name"}),
		monLatency: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "lanscape_monitor_latency_seconds",
			Help: "Response time of the last check."}, []string{"monitor", "name"}),
		notifyFail: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "lanscape_notification_failures_total",
			Help: "Failed notification deliveries by channel type."}, []string{"type"}),
	}
	r.MustRegister(m.agentUp, m.pathBPS, m.pathRTT, m.pathVerdict, m.pathLoss, m.runs, m.runSeconds, m.problems, m.devices,
		m.monUp, m.monLatency, m.notifyFail)
	return m
}

// SetAgent updates the agent gauge.
func (m *Metrics) SetAgent(id, name, kind string, up bool) {
	v := 0.0
	if up {
		v = 1
	}
	m.agentUp.WithLabelValues(id, name, kind).Set(v)
}

// ObserveReport exports the paths of a finished run.
func (m *Metrics) ObserveReport(rep *Report) {
	if rep.Kind != "full" && rep.Kind != "reachability" {
		return
	}
	m.pathBPS.Reset()
	m.pathRTT.Reset()
	m.pathVerdict.Reset()
	m.pathLoss.Reset()
	for _, p := range rep.Paths {
		l := []string{p.SegID, p.Src, p.SrcIf, p.Dst, p.DstIf}
		if p.BestBPS > 0 {
			m.pathBPS.WithLabelValues(l...).Set(float64(p.BestBPS))
		}
		if p.Ping != nil && p.Ping.Recv > 0 {
			m.pathRTT.WithLabelValues(l...).Set(float64(p.Ping.RTTAvgUS) / 1e6)
			m.pathLoss.WithLabelValues(l...).Set(float64(p.Ping.Sent-p.Ping.Recv) / float64(p.Ping.Sent))
		}
		m.pathVerdict.WithLabelValues(l...).Set(verdictValue[p.Verdict])
	}
	m.problems.Reset()
	for _, pr := range rep.Problems {
		m.problems.WithLabelValues(pr.Kind).Inc()
	}
	m.runs.WithLabelValues(rep.Kind, rep.Status).Inc()
	if rep.Finished > rep.Started {
		m.runSeconds.WithLabelValues(rep.Kind).Observe(float64(rep.Finished-rep.Started) / 1000)
	}
}

// SetMonitor exports the last check of a monitor.
func (m *Metrics) SetMonitor(id int64, name, status string, latencyMS float64) {
	l := []string{strconv.FormatInt(id, 10), name}
	switch status {
	case "up":
		m.monUp.WithLabelValues(l...).Set(1)
	case "degraded":
		m.monUp.WithLabelValues(l...).Set(0.5)
	case "down":
		m.monUp.WithLabelValues(l...).Set(0)
	default:
		return
	}
	m.monLatency.WithLabelValues(l...).Set(latencyMS / 1000)
}

// DeleteMonitor removes the series of a deleted monitor.
func (m *Metrics) DeleteMonitor(id int64, name string) {
	l := []string{strconv.FormatInt(id, 10), name}
	m.monUp.DeleteLabelValues(l...)
	m.monLatency.DeleteLabelValues(l...)
}

// NotifyFailed counts a failed notification.
func (m *Metrics) NotifyFailed(typ string) { m.notifyFail.WithLabelValues(typ).Inc() }
