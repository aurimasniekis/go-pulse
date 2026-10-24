package metrics

import (
	"context"
	"sync"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/aurimasniekis/go-pulse/internal/sysapi"
)

// CertExpiry is one certificate's expiry for the collector.
type CertExpiry struct {
	Name     string
	NotAfter time.Time
}

// Sources are read at scrape time; nil fields are skipped.
type Sources struct {
	Version       string
	Sys           *sysapi.Client
	JS            jetstream.JetStream
	NATSConnected func() bool
	TailSessions  func() int
	ActiveBans    func() int
	AlertsFiring  func() map[string]int // by severity
	Certs         func() []CertExpiry
}

// Collector exports gauges from Sources. NATS data is cached for a few seconds so concurrent scrapes share requests.
// One collector is registered on Registry; pulsed sets its sources with SetSources.
type Collector struct {
	smu sync.RWMutex
	s   Sources

	mu      sync.Mutex
	at      time.Time
	varz    *sysapi.Varz
	jsz     *sysapi.Jsz
	streams []*jetstream.StreamInfo
}

var current = &Collector{}

func init() { Registry.MustRegister(current) }

// SetSources points the registered collector at s (the last started app wins; tests may start several).
func SetSources(s Sources) {
	current.smu.Lock()
	current.s = s
	current.smu.Unlock()
	current.mu.Lock()
	current.at = time.Time{}
	current.mu.Unlock()
}

func (c *Collector) sources() Sources {
	c.smu.RLock()
	defer c.smu.RUnlock()
	return c.s
}

func d(name, help string, labels ...string) *prometheus.Desc {
	return prometheus.NewDesc(name, help, labels, nil)
}

var (
	dBuild        = d("pulse_build_info", "pulsed build information.", "version")
	dNATSConn     = d("pulse_nats_connected", "1 when pulsed's NATS connection is up.")
	dTail         = d("pulse_tail_sessions", "Open live-tail sessions.")
	dBans         = d("pulse_bans_active", "Active bans.")
	dAlerts       = d("pulse_alerts_firing", "Firing alerts, by severity.", "severity")
	dCert         = d("pulse_certificate_not_after_timestamp_seconds", "Certificate expiry (Unix seconds), by certificate.", "certificate")
	dNATSUp       = d("nats_up", "1 when the NATS server answered the monitoring request.")
	dNATSConns    = d("nats_connections", "Current client connections.")
	dNATSTotConns = d("nats_connections_total", "Connections since server start.")
	dNATSSubs     = d("nats_subscriptions", "Current subscriptions.")
	dNATSInMsgs   = d("nats_in_msgs_total", "Messages received by the server.")
	dNATSOutMsgs  = d("nats_out_msgs_total", "Messages sent by the server.")
	dNATSInBytes  = d("nats_in_bytes_total", "Bytes received by the server.")
	dNATSOutBytes = d("nats_out_bytes_total", "Bytes sent by the server.")
	dNATSSlow     = d("nats_slow_consumers_total", "Slow consumers since server start.")
	dNATSMem      = d("nats_mem_bytes", "NATS server resident memory.")
	dNATSCPU      = d("nats_cpu_percent", "NATS server CPU usage.")
	dJSStore      = d("nats_jetstream_storage_bytes", "JetStream file storage used.")
	dJSStoreMax   = d("nats_jetstream_storage_max_bytes", "JetStream file storage limit (0 = server default).")
	dJSMem        = d("nats_jetstream_memory_bytes", "JetStream memory storage used.")
	dJSStreams    = d("nats_jetstream_streams", "JetStream streams.")
	dJSConsumers  = d("nats_jetstream_consumers", "JetStream consumers.")
	dJSMsgs       = d("nats_jetstream_messages", "Messages stored in JetStream.")
	dJSAPI        = d("nats_jetstream_api_requests_total", "JetStream API requests.")
	dJSAPIErr     = d("nats_jetstream_api_errors_total", "JetStream API errors.")
	dStreamBytes  = d("pulse_stream_bytes", "Stream size in bytes.", "stream")
	dStreamMax    = d("pulse_stream_max_bytes", "Stream max_bytes (-1 = unlimited).", "stream")
	dStreamMsgs   = d("pulse_stream_messages", "Messages in the stream.", "stream")
	dStreamCons   = d("pulse_stream_consumers", "Consumers on the stream.", "stream")
	dStreamFirst  = d("pulse_stream_first_timestamp_seconds", "Timestamp of the oldest message (retention horizon).", "stream")
)

// Describe implements prometheus.Collector (unchecked: label sets depend on runtime data).
func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	for _, x := range []*prometheus.Desc{dBuild, dNATSConn, dTail, dBans, dAlerts, dCert, dNATSUp, dNATSConns, dNATSTotConns,
		dNATSSubs, dNATSInMsgs, dNATSOutMsgs, dNATSInBytes, dNATSOutBytes, dNATSSlow, dNATSMem, dNATSCPU, dJSStore, dJSStoreMax,
		dJSMem, dJSStreams, dJSConsumers, dJSMsgs, dJSAPI, dJSAPIErr, dStreamBytes, dStreamMax, dStreamMsgs, dStreamCons, dStreamFirst} {
		ch <- x
	}
}

const cacheFor = 5 * time.Second

func (c *Collector) refresh() (*sysapi.Varz, *sysapi.Jsz, []*jetstream.StreamInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.at) < cacheFor {
		return c.varz, c.jsz, c.streams
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	src := c.sources()
	c.varz, c.jsz, c.streams = nil, nil, nil
	if src.Sys != nil {
		if v, err := src.Sys.Varz(ctx); err == nil {
			c.varz = v
		}
		if j, err := src.Sys.Jsz(ctx); err == nil {
			c.jsz = j
		}
	}
	if src.JS != nil {
		l := src.JS.ListStreams(ctx)
		for si := range l.Info() {
			c.streams = append(c.streams, si)
		}
	}
	c.at = time.Now()
	return c.varz, c.jsz, c.streams
}

func gauge(ch chan<- prometheus.Metric, desc *prometheus.Desc, v float64, labels ...string) {
	ch <- prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, v, labels...)
}

func counter(ch chan<- prometheus.Metric, desc *prometheus.Desc, v float64, labels ...string) {
	ch <- prometheus.MustNewConstMetric(desc, prometheus.CounterValue, v, labels...)
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// Collect implements prometheus.Collector.
func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	s := c.sources()
	if s.Version != "" {
		gauge(ch, dBuild, 1, s.Version)
	}
	if s.NATSConnected != nil {
		gauge(ch, dNATSConn, b2f(s.NATSConnected()))
	}
	if s.TailSessions != nil {
		gauge(ch, dTail, float64(s.TailSessions()))
	}
	if s.ActiveBans != nil {
		gauge(ch, dBans, float64(s.ActiveBans()))
	}
	if s.AlertsFiring != nil {
		m := s.AlertsFiring()
		for _, sev := range []string{"info", "warning", "critical"} {
			gauge(ch, dAlerts, float64(m[sev]), sev)
		}
	}
	if s.Certs != nil {
		for _, ce := range s.Certs() {
			gauge(ch, dCert, float64(ce.NotAfter.Unix()), ce.Name)
		}
	}
	if s.Sys == nil && s.JS == nil {
		return
	}
	varz, jsz, streams := c.refresh()
	gauge(ch, dNATSUp, b2f(varz != nil))
	if varz != nil {
		gauge(ch, dNATSConns, float64(varz.Connections))
		counter(ch, dNATSTotConns, float64(varz.TotalConns))
		gauge(ch, dNATSSubs, float64(varz.Subscriptions))
		counter(ch, dNATSInMsgs, float64(varz.InMsgs))
		counter(ch, dNATSOutMsgs, float64(varz.OutMsgs))
		counter(ch, dNATSInBytes, float64(varz.InBytes))
		counter(ch, dNATSOutBytes, float64(varz.OutBytes))
		counter(ch, dNATSSlow, float64(varz.SlowConsumers))
		gauge(ch, dNATSMem, float64(varz.Mem))
		gauge(ch, dNATSCPU, varz.CPU)
	}
	if jsz != nil {
		gauge(ch, dJSStore, float64(jsz.Store))
		gauge(ch, dJSStoreMax, float64(jsz.Config.MaxStore))
		gauge(ch, dJSMem, float64(jsz.Memory))
		gauge(ch, dJSStreams, float64(jsz.Streams))
		gauge(ch, dJSConsumers, float64(jsz.Consumers))
		gauge(ch, dJSMsgs, float64(jsz.Messages))
		counter(ch, dJSAPI, float64(jsz.API.Total))
		counter(ch, dJSAPIErr, float64(jsz.API.Errors))
	}
	for _, si := range streams {
		n := si.Config.Name
		gauge(ch, dStreamBytes, float64(si.State.Bytes), n)
		gauge(ch, dStreamMax, float64(si.Config.MaxBytes), n)
		gauge(ch, dStreamMsgs, float64(si.State.Msgs), n)
		gauge(ch, dStreamCons, float64(si.State.Consumers), n)
		if !si.State.FirstTime.IsZero() && si.State.Msgs > 0 {
			gauge(ch, dStreamFirst, float64(si.State.FirstTime.Unix()), n)
		}
	}
}
