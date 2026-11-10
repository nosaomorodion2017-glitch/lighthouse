// Package obs provides a small, dependency-free Prometheus metrics registry.
package obs

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"sync"
	"time"
)

// Metrics is safe for concurrent use.
type Metrics struct {
	mu       sync.Mutex
	requests map[reqKey]uint64
	durSum   map[string]float64
	durCount map[string]uint64
	counters map[string]uint64  // full series name, e.g. `x_total{a="b"}`
	gauges   map[string]float64 // full series name
}

type reqKey struct {
	route string
	code  int
}

// New returns an empty registry.
func New() *Metrics {
	return &Metrics{
		requests: map[reqKey]uint64{},
		durSum:   map[string]float64{},
		durCount: map[string]uint64{},
		counters: map[string]uint64{},
		gauges:   map[string]float64{},
	}
}

// ObserveRequest records one HTTP request.
func (m *Metrics) ObserveRequest(route string, code int, d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests[reqKey{route, code}]++
	m.durSum[route] += d.Seconds()
	m.durCount[route]++
}

// Inc increments a counter series.
func (m *Metrics) Inc(series string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.counters[series]++
}

// SetGauge sets a gauge series.
func (m *Metrics) SetGauge(series string, v float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gauges[series] = v
}

// WriteTo writes all series in Prometheus text exposition format, sorted so
// output is stable.
func (m *Metrics) WriteTo(w io.Writer) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cw := &countWriter{w: w}

	fmt.Fprintln(cw, "# HELP http_requests_total HTTP requests by route and status code.")
	fmt.Fprintln(cw, "# TYPE http_requests_total counter")
	keys := make([]reqKey, 0, len(m.requests))
	for k := range m.requests {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].route != keys[j].route {
			return keys[i].route < keys[j].route
		}
		return keys[i].code < keys[j].code
	})
	for _, k := range keys {
		fmt.Fprintf(cw, "http_requests_total{route=%s,code=\"%d\"} %d\n", strconv.Quote(k.route), k.code, m.requests[k])
	}

	fmt.Fprintln(cw, "# HELP http_request_duration_seconds Total time spent serving requests by route.")
	fmt.Fprintln(cw, "# TYPE http_request_duration_seconds summary")
	for _, r := range sortedKeys(m.durSum) {
		fmt.Fprintf(cw, "http_request_duration_seconds_sum{route=%s} %g\n", strconv.Quote(r), m.durSum[r])
		fmt.Fprintf(cw, "http_request_duration_seconds_count{route=%s} %d\n", strconv.Quote(r), m.durCount[r])
	}

	for _, s := range sortedKeys(m.counters) {
		fmt.Fprintf(cw, "%s %d\n", s, m.counters[s])
	}
	for _, s := range sortedKeys(m.gauges) {
		fmt.Fprintf(cw, "%s %g\n", s, m.gauges[s])
	}
	return cw.n, cw.err
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

type countWriter struct {
	w   io.Writer
	n   int64
	err error
}

func (c *countWriter) Write(p []byte) (int, error) {
	if c.err != nil {
		return 0, c.err
	}
	n, err := c.w.Write(p)
	c.n += int64(n)
	c.err = err
	return n, err
}
