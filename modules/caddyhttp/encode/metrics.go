package encode

import (
	"errors"
	"io"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/caddyserver/caddy/v2"
)

var encodeMetrics = struct {
	responses         *prometheus.CounterVec
	uncompressedBytes *prometheus.CounterVec
	compressedBytes   *prometheus.CounterVec
	bypassedBytes     *prometheus.CounterVec
}{
	responses: prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "caddy",
		Subsystem: "http",
		Name:      "compression_responses_total",
		Help:      "Number of responses that were compressed.",
	}, []string{"server", "encoding"}),
	uncompressedBytes: prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "caddy",
		Subsystem: "http",
		Name:      "compression_uncompressed_bytes_total",
		Help:      "Number of response body bytes fed into the compressor.",
	}, []string{"server", "encoding"}),
	compressedBytes: prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "caddy",
		Subsystem: "http",
		Name:      "compression_compressed_bytes_total",
		Help:      "Number of response body bytes produced by the compressor.",
	}, []string{"server", "encoding"}),
	bypassedBytes: prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "caddy",
		Subsystem: "http",
		Name:      "compression_bypassed_bytes_total",
		Help:      "Number of response body bytes passed through without compression.",
	}, []string{"server"}),
}

// encodeMetricVecs returns the metric vectors for encode module.
func encodeMetricVecs() []caddy.MetricVec {
	return []caddy.MetricVec{
		encodeMetrics.responses,
		encodeMetrics.uncompressedBytes,
		encodeMetrics.compressedBytes,
		encodeMetrics.bypassedBytes,
	}
}

// registerMetric registers c, ignoring the error if it is already registered.
func registerMetric(registry *prometheus.Registry, c prometheus.Collector) {
	if err := registry.Register(c); err != nil &&
		!errors.Is(err, prometheus.AlreadyRegisteredError{ExistingCollector: c, NewCollector: c}) {
		panic(err)
	}
}

func initEncodeMetrics(registry *prometheus.Registry) {
	for _, c := range encodeMetricVecs() {
		registerMetric(registry, c)
	}
}

// wrapper that counts bytes written into a Prometheus counter
type countingWriter struct {
	w io.Writer
	c prometheus.Counter
}

func (cw countingWriter) Write(p []byte) (int, error) {
	n, err := cw.w.Write(p)
	cw.c.Add(float64(n))
	return n, err
}
