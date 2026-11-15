// Package metrics keeps the relay's counters and renders them in the
// Prometheus text exposition format. It has no dependencies: the relay needs
// a handful of counters, one histogram and a few gauges, not a client library.
//
// Every method is safe on a nil receiver and does nothing, so components that
// were not given a Set need no checks.
package metrics

import (
	"fmt"
	"io"
	"maps"
	"net/http"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const namespace = "mailgun_passport_"

// Set holds every metric the relay exports.
type Set struct {
	// Messages counts SMTP messages by outcome: accepted, invalid,
	// sender_rejected, temporary_failure or permanent_failure.
	Messages *Counter
	// MailgunRequests counts Mailgun API calls by HTTP status, or "error"
	// when Mailgun was not reached.
	MailgunRequests *Counter
	// MailgunDuration observes the duration of Mailgun API calls.
	MailgunDuration *Histogram
	// Connections counts SMTP connections: accepted or refused.
	Connections *Counter
	// ConnectionsOpen is the number of open SMTP connections.
	ConnectionsOpen *Gauge
	// AuthAttempts counts AUTH attempts: success, failure or blocked.
	AuthAttempts *Counter
	// RateLimited counts messages refused by SMTP_RATE_LIMIT_PER_MINUTE.
	RateLimited *Counter
	// Reloads counts SIGHUP reloads: success or failure.
	Reloads *Counter
	// QueueDeliveries counts delivery attempts of queued messages: delivered,
	// retry, failed or expired.
	QueueDeliveries *Counter

	mu      sync.Mutex
	metrics []metric
}

type metric interface {
	write(w io.Writer)
}

// New returns a Set with every metric registered.
func New(version, commit string) *Set {
	s := &Set{}
	s.Messages = s.counter("messages_total", "SMTP messages by outcome.", "result",
		"accepted", "invalid", "sender_rejected", "temporary_failure", "permanent_failure")
	s.MailgunRequests = s.counter("mailgun_requests_total",
		"Mailgun API calls by HTTP status, or error when Mailgun was not reached.", "code")
	s.MailgunDuration = s.histogram("mailgun_request_duration_seconds",
		"Duration of Mailgun API calls.", 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30)
	s.Connections = s.counter("smtp_connections_total", "SMTP connections.", "result", "accepted", "refused")
	s.ConnectionsOpen = s.gauge("smtp_connections_open", "Open SMTP connections.")
	s.AuthAttempts = s.counter("smtp_auth_attempts_total", "AUTH attempts.", "result",
		"success", "failure", "blocked")
	s.RateLimited = s.counter("smtp_rate_limited_total",
		"Messages refused by SMTP_RATE_LIMIT_PER_MINUTE.", "")
	s.Reloads = s.counter("config_reloads_total", "Configuration reloads on SIGHUP.", "result",
		"success", "failure")
	s.QueueDeliveries = s.counter("queue_deliveries_total",
		"Delivery attempts of queued messages.", "result", "delivered", "retry", "failed", "expired")

	s.add(buildInfo{version: version, commit: commit})
	start := float64(time.Now().UnixNano()) / 1e9
	s.GaugeFunc("process_start_time_seconds", "Start time of the process in Unix seconds.",
		func() float64 { return start })
	s.GaugeFunc("go_goroutines", "Number of goroutines.",
		func() float64 { return float64(runtime.NumGoroutine()) })
	s.GaugeFunc("go_memstats_alloc_bytes", "Bytes of allocated heap objects.", func() float64 {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		return float64(m.Alloc)
	})
	return s
}

func (s *Set) add(m metric) {
	s.mu.Lock()
	s.metrics = append(s.metrics, m)
	s.mu.Unlock()
}

func (s *Set) counter(name, help, label string, values ...string) *Counter {
	c := &Counter{name: namespace + name, help: help, label: label, vals: map[string]*atomic.Uint64{}}
	for _, v := range values {
		c.vals[v] = new(atomic.Uint64)
	}
	if label == "" {
		c.vals[""] = new(atomic.Uint64)
	}
	s.add(c)
	return c
}

func (s *Set) histogram(name, help string, buckets ...float64) *Histogram {
	h := &Histogram{name: namespace + name, help: help, buckets: buckets, counts: make([]uint64, len(buckets))}
	s.add(h)
	return h
}

func (s *Set) gauge(name, help string) *Gauge {
	g := &Gauge{name: namespace + name, help: help}
	s.add(g)
	return g
}

// GaugeFunc registers a gauge whose value is read when the metrics are
// scraped. Names outside the process_ and go_ families get the relay's prefix.
func (s *Set) GaugeFunc(name, help string, fn func() float64) {
	if s == nil {
		return
	}
	if !strings.HasPrefix(name, "process_") && !strings.HasPrefix(name, "go_") {
		name = namespace + name
	}
	s.add(&gaugeFunc{name: name, help: help, fn: fn})
}

// Write renders every metric in the Prometheus text format.
func (s *Set) Write(w io.Writer) {
	if s == nil {
		return
	}
	s.mu.Lock()
	metrics := append([]metric{}, s.metrics...)
	s.mu.Unlock()
	for _, m := range metrics {
		m.write(w)
	}
}

// Handler serves the metrics for a Prometheus scrape.
func (s *Set) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		s.Write(w)
	})
}

func header(w io.Writer, name, help, kind string) {
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, kind)
}

var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

// Counter is a monotonically increasing count, split by one label.
type Counter struct {
	name, help, label string

	mu   sync.RWMutex
	vals map[string]*atomic.Uint64
}

// Inc adds one to the series with the given label value.
func (c *Counter) Inc(value string) { c.Add(value, 1) }

// Add adds n to the series with the given label value. A counter without a
// label ignores value.
func (c *Counter) Add(value string, n uint64) {
	if c == nil {
		return
	}
	if c.label == "" {
		value = ""
	}
	c.mu.RLock()
	v := c.vals[value]
	c.mu.RUnlock()
	if v == nil {
		c.mu.Lock()
		if v = c.vals[value]; v == nil {
			v = new(atomic.Uint64)
			c.vals[value] = v
		}
		c.mu.Unlock()
	}
	v.Add(n)
}

// Value returns the current count of a series.
func (c *Counter) Value(value string) uint64 {
	if c == nil {
		return 0
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if v := c.vals[value]; v != nil {
		return v.Load()
	}
	return 0
}

func (c *Counter) write(w io.Writer) {
	header(w, c.name, c.help, "counter")
	c.mu.RLock()
	defer c.mu.RUnlock()
	// Sorted, so the output does not change between scrapes.
	for _, k := range slices.Sorted(maps.Keys(c.vals)) {
		if c.label == "" {
			fmt.Fprintf(w, "%s %d\n", c.name, c.vals[k].Load())
			continue
		}
		fmt.Fprintf(w, "%s{%s=\"%s\"} %d\n", c.name, c.label, labelEscaper.Replace(k), c.vals[k].Load())
	}
}

// Gauge is a value that goes up and down.
type Gauge struct {
	name, help string
	v          atomic.Int64
}

func (g *Gauge) Add(n int64) {
	if g != nil {
		g.v.Add(n)
	}
}

func (g *Gauge) Value() int64 {
	if g == nil {
		return 0
	}
	return g.v.Load()
}

func (g *Gauge) write(w io.Writer) {
	header(w, g.name, g.help, "gauge")
	fmt.Fprintf(w, "%s %d\n", g.name, g.v.Load())
}

type gaugeFunc struct {
	name, help string
	fn         func() float64
}

func (g *gaugeFunc) write(w io.Writer) {
	header(w, g.name, g.help, "gauge")
	fmt.Fprintf(w, "%s %s\n", g.name, formatFloat(g.fn()))
}

// Histogram counts observations in cumulative buckets.
type Histogram struct {
	name, help string
	buckets    []float64

	mu     sync.Mutex
	counts []uint64 // per bucket, not cumulative
	count  uint64
	sum    float64
}

// Observe records one value, in seconds for durations.
func (h *Histogram) Observe(v float64) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.count++
	h.sum += v
	for i, le := range h.buckets {
		if v <= le {
			h.counts[i]++
			break
		}
	}
}

// Count returns the number of observations.
func (h *Histogram) Count() uint64 {
	if h == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.count
}

func (h *Histogram) write(w io.Writer) {
	header(w, h.name, h.help, "histogram")
	h.mu.Lock()
	defer h.mu.Unlock()
	var cum uint64
	for i, le := range h.buckets {
		cum += h.counts[i]
		fmt.Fprintf(w, "%s_bucket{le=\"%s\"} %d\n", h.name, formatFloat(le), cum)
	}
	fmt.Fprintf(w, "%s_bucket{le=\"+Inf\"} %d\n", h.name, h.count)
	fmt.Fprintf(w, "%s_sum %s\n%s_count %d\n", h.name, formatFloat(h.sum), h.name, h.count)
}

type buildInfo struct{ version, commit string }

func (b buildInfo) write(w io.Writer) {
	header(w, namespace+"build_info", "Version and commit of the running relay.", "gauge")
	fmt.Fprintf(w, "%sbuild_info{version=\"%s\",commit=\"%s\"} 1\n", namespace,
		labelEscaper.Replace(b.version), labelEscaper.Replace(b.commit))
}

func formatFloat(f float64) string {
	return strconv.FormatFloat(f, 'g', -1, 64)
}
