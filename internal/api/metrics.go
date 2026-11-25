package api

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
)

// metrics keeps the counters the /metrics endpoint exports. Prometheus text
// format is written by hand so the server keeps zero dependencies.
type metrics struct {
	mu     sync.Mutex
	counts map[string]int64
}

func newMetrics() *metrics { return &metrics{counts: map[string]int64{}} }

func (m *metrics) observe(method, route string, status int) {
	key := method + "\x00" + route + "\x00" + itoa(status)
	m.mu.Lock()
	m.counts[key]++
	m.mu.Unlock()
}

// statusRecorder captures the status code for the counter without changing the
// handler contract.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(b)
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

	s.metrics.mu.Lock()
	keys := make([]string, 0, len(s.metrics.counts))
	for k := range s.metrics.counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("# HELP waveycord_http_requests_total HTTP requests by method, route and status.\n")
	b.WriteString("# TYPE waveycord_http_requests_total counter\n")
	for _, k := range keys {
		parts := strings.Split(k, "\x00")
		fmt.Fprintf(&b, "waveycord_http_requests_total{method=%q,route=%q,status=%q} %d\n",
			parts[0], parts[1], parts[2], s.metrics.counts[k])
	}
	s.metrics.mu.Unlock()

	stats := s.Store.DB.Stats()
	fmt.Fprintf(&b, "# HELP waveycord_db_open_connections Open database connections.\n")
	fmt.Fprintf(&b, "# TYPE waveycord_db_open_connections gauge\nwaveycord_db_open_connections %d\n", stats.OpenConnections)
	fmt.Fprintf(&b, "# HELP waveycord_db_wait_seconds_total Total time blocked waiting for a connection.\n")
	fmt.Fprintf(&b, "# TYPE waveycord_db_wait_seconds_total counter\nwaveycord_db_wait_seconds_total %f\n", stats.WaitDuration.Seconds())
	fmt.Fprintf(&b, "# HELP waveycord_gateway_connections Live gateway connections.\n")
	fmt.Fprintf(&b, "# TYPE waveycord_gateway_connections gauge\nwaveycord_gateway_connections %d\n", s.Hub.ConnectionCount())
	fmt.Fprintf(&b, "# HELP waveycord_gateway_dispatches_total Gateway events fanned out.\n")
	fmt.Fprintf(&b, "# TYPE waveycord_gateway_dispatches_total counter\nwaveycord_gateway_dispatches_total %d\n", s.Hub.DispatchCount())
	if s.voice != nil {
		sessions, rtp, speaking := s.voice.Stats()
		fmt.Fprintf(&b, "# HELP waveycord_voice_sessions Authenticated voice gateway connections.\n")
		fmt.Fprintf(&b, "# TYPE waveycord_voice_sessions gauge\nwaveycord_voice_sessions %d\n", sessions)
		fmt.Fprintf(&b, "# HELP waveycord_voice_rtp_packets_total RTP packets observed on the UDP socket.\n")
		fmt.Fprintf(&b, "# TYPE waveycord_voice_rtp_packets_total counter\nwaveycord_voice_rtp_packets_total %d\n", rtp)
		fmt.Fprintf(&b, "# HELP waveycord_voice_speaking_total Speaking events received.\n")
		fmt.Fprintf(&b, "# TYPE waveycord_voice_speaking_total counter\nwaveycord_voice_speaking_total %d\n", speaking)
		fmt.Fprintf(&b, "# HELP waveycord_voice_rtp_forwarded_total RTP packets forwarded to other connections.\n")
		fmt.Fprintf(&b, "# TYPE waveycord_voice_rtp_forwarded_total counter\nwaveycord_voice_rtp_forwarded_total %d\n", s.voice.Forwarded())
	}
	_, _ = w.Write([]byte(b.String()))
}
