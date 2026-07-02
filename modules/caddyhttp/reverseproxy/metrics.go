package reverseproxy

import (
	"errors"
	"runtime/debug"
	"slices"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/caddyserver/caddy/v2"
)

// reverseProxyMetricVecs holds the per-upstream metric vecs. Each vec's
// variable labels are its base labels and a union of custom label keys.
type reverseProxyMetricVecs struct {
	customKeys               []string
	upstreamsHealthy         *prometheus.GaugeVec
	upstreamConnectDuration  *prometheus.HistogramVec
	upstreamResponseDuration *prometheus.HistogramVec
	upstreamRequestsTotal    *prometheus.CounterVec
	upstreamRequestsInFlight *prometheus.GaugeVec
	upstreamResponsesTotal   *prometheus.CounterVec
	upstreamCheckDuration    *prometheus.HistogramVec
	upstreamCheckFailures    *prometheus.CounterVec
	upstreamCheckUpDown      *prometheus.CounterVec
	upstreamConnAttempts     *prometheus.CounterVec
	upstreamConnErrors       *prometheus.CounterVec
	upstreamConnReuses       *prometheus.CounterVec
	upstreamTLSDuration      *prometheus.HistogramVec
	upstreamTLSErrors        *prometheus.CounterVec
	upstreamTLSHandshakes    *prometheus.CounterVec
	upstreamDNSDuration      *prometheus.HistogramVec
	upstreamDuration         *prometheus.HistogramVec
	upstreamRetries          *prometheus.CounterVec
	upstreamResponseErrors   *prometheus.CounterVec
	upstreamSentBytes        *prometheus.CounterVec
	upstreamReceivedBytes    *prometheus.CounterVec
	upstreamLastSession      *prometheus.GaugeVec
	upstreamRedispatches     *prometheus.CounterVec
	upstreamClientAborts     *prometheus.CounterVec
	upstreamServerAborts     *prometheus.CounterVec
}

// newReverseProxyMetricVecs returns a new reverseProxyMetricVecs with the
// given custom label keys.
func newReverseProxyMetricVecs(custom []string) *reverseProxyMetricVecs {
	with := func(base ...string) []string { return append(base, custom...) }
	return &reverseProxyMetricVecs{
		customKeys: custom,
		upstreamsHealthy: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "caddy",
			Subsystem: "reverse_proxy",
			Name:      "upstreams_healthy",
			Help:      "Health status of reverse proxy upstreams.",
		}, with("upstream")),
		upstreamConnectDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "caddy",
			Subsystem: "reverse_proxy",
			Name:      "upstream_connect_duration_seconds",
			Help:      "Time spent connecting to upstreams, by status.",
			Buckets:   prometheus.DefBuckets,
		}, with("upstream", "status")),
		upstreamResponseDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "caddy",
			Subsystem: "reverse_proxy",
			Name:      "upstream_response_duration_seconds",
			Help:      "Time spent waiting for a response from upstreams.",
			Buckets:   prometheus.DefBuckets,
		}, with("upstream")),
		upstreamRequestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "caddy",
			Subsystem: "reverse_proxy",
			Name:      "upstream_requests_total",
			Help:      "Number of requests proxied to upstreams.",
		}, with("upstream")),
		upstreamRequestsInFlight: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "caddy",
			Subsystem: "reverse_proxy",
			Name:      "upstream_requests_in_flight",
			Help:      "Number of requests currently in flight to upstreams.",
		}, with("upstream")),
		upstreamResponsesTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "caddy",
			Subsystem: "reverse_proxy",
			Name:      "upstream_responses_total",
			Help:      "Number of responses received from upstreams, by status code.",
		}, with("upstream", "code")),
		upstreamCheckDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "caddy",
			Subsystem: "reverse_proxy",
			Name:      "upstream_check_duration_seconds",
			Help:      "Duration of active health checks to upstreams.",
			Buckets:   prometheus.DefBuckets,
		}, with("upstream")),
		upstreamCheckFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "caddy",
			Subsystem: "reverse_proxy",
			Name:      "upstream_check_failures_total",
			Help:      "Number of failed active health checks to upstreams, by failure reason.",
		}, with("upstream", "reason")),
		upstreamCheckUpDown: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "caddy",
			Subsystem: "reverse_proxy",
			Name:      "upstream_check_up_down_total",
			Help:      "Number of upstream health state transitions from active health checks.",
		}, with("upstream")),
		upstreamConnAttempts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "caddy",
			Subsystem: "reverse_proxy",
			Name:      "upstream_connection_attempts_total",
			Help:      "Number of attempts to establish a new connection to upstreams.",
		}, with("upstream")),
		upstreamConnErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "caddy",
			Subsystem: "reverse_proxy",
			Name:      "upstream_connection_errors_total",
			Help:      "Number of failed attempts to establish a new connection to upstreams.",
		}, with("upstream")),
		upstreamConnReuses: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "caddy",
			Subsystem: "reverse_proxy",
			Name:      "upstream_connection_reuses_total",
			Help:      "Number of requests served on a reused connection to upstreams.",
		}, with("upstream")),
		upstreamTLSDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "caddy",
			Subsystem: "reverse_proxy",
			Name:      "upstream_tls_handshake_duration_seconds",
			Help:      "Duration of TLS handshakes to upstreams, by status.",
			Buckets:   prometheus.DefBuckets,
		}, with("upstream", "status")),
		upstreamTLSErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "caddy",
			Subsystem: "reverse_proxy",
			Name:      "upstream_tls_handshake_errors_total",
			Help:      "Number of failed TLS handshakes to upstreams.",
		}, with("upstream")),
		upstreamTLSHandshakes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "caddy",
			Subsystem: "reverse_proxy",
			Name:      "upstream_tls_handshakes_total",
			Help:      "Counter of completed TLS handshakes to upstreams, by resumption status.",
		}, with("upstream", "resumed")),
		upstreamDNSDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "caddy",
			Subsystem: "reverse_proxy",
			Name:      "upstream_dns_resolution_duration_seconds",
			Help:      "Duration of DNS resolution for upstreams, by status.",
			Buckets:   prometheus.DefBuckets,
		}, with("upstream", "status")),
		upstreamDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "caddy",
			Subsystem: "reverse_proxy",
			Name:      "upstream_duration_seconds",
			Help:      "Total duration of the round trip to upstreams, by status.",
			Buckets:   prometheus.DefBuckets,
		}, with("upstream", "status")),
		upstreamRetries: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "caddy",
			Subsystem: "reverse_proxy",
			Name:      "upstream_retries_total",
			Help:      "Number of failed attempts to upstreams that triggered a retry.",
		}, with("upstream")),
		upstreamResponseErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "caddy",
			Subsystem: "reverse_proxy",
			Name:      "upstream_response_errors_total",
			Help:      "Number of upstream request failures (excludes successful and retryable responses).",
		}, with("upstream")),
		upstreamSentBytes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "caddy",
			Subsystem: "reverse_proxy",
			Name:      "upstream_sent_bytes_total",
			Help:      "Number of request body bytes sent to upstreams.",
		}, with("upstream")),
		upstreamReceivedBytes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "caddy",
			Subsystem: "reverse_proxy",
			Name:      "upstream_received_bytes_total",
			Help:      "Number of response body bytes received from upstreams.",
		}, with("upstream")),
		upstreamLastSession: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "caddy",
			Subsystem: "reverse_proxy",
			Name:      "upstream_last_session_seconds",
			Help:      "Unix timestamp of the last request proxied to an upstream.",
		}, with("upstream")),
		upstreamRedispatches: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "caddy",
			Subsystem: "reverse_proxy",
			Name:      "upstream_redispatch_warnings_total",
			Help:      "Number of connection failures retried on a different upstream than the previous attempt.",
		}, with("upstream")),
		upstreamClientAborts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "caddy",
			Subsystem: "reverse_proxy",
			Name:      "upstream_client_aborts_total",
			Help:      "Number of responses where the client disconnected before the full body was sent.",
		}, with("upstream")),
		upstreamServerAborts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "caddy",
			Subsystem: "reverse_proxy",
			Name:      "upstream_server_aborts_total",
			Help:      "Number of responses where the upstream closed or reset the connection before the full body was received.",
		}, with("upstream")),
	}
}

// metricVecs returns the per-upstream vecs for tracker registration.
func (v *reverseProxyMetricVecs) metricVecs() []caddy.MetricVec {
	return []caddy.MetricVec{
		v.upstreamsHealthy,
		v.upstreamConnectDuration,
		v.upstreamResponseDuration,
		v.upstreamRequestsTotal,
		v.upstreamRequestsInFlight,
		v.upstreamResponsesTotal,
		v.upstreamCheckDuration,
		v.upstreamCheckFailures,
		v.upstreamCheckUpDown,
		v.upstreamConnAttempts,
		v.upstreamConnErrors,
		v.upstreamConnReuses,
		v.upstreamTLSDuration,
		v.upstreamTLSErrors,
		v.upstreamTLSHandshakes,
		v.upstreamDNSDuration,
		v.upstreamDuration,
		v.upstreamRetries,
		v.upstreamResponseErrors,
		v.upstreamSentBytes,
		v.upstreamReceivedBytes,
		v.upstreamLastSession,
		v.upstreamRedispatches,
		v.upstreamClientAborts,
		v.upstreamServerAborts,
	}
}

// activeVecs holds the current per-upstream vecs. Atomic so during reloads,
// background goroutines can read the metrics.
var (
	activeVecs  atomic.Pointer[reverseProxyMetricVecs]
	currentKeys []string
)

var dnsResolutionDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
	Namespace: "caddy",
	Subsystem: "reverse_proxy",
	Name:      "dns_resolution_duration_seconds",
	Help:      "Duration of DNS resolutions for dynamic upstream sources, by status.",
	Buckets:   prometheus.DefBuckets,
}, []string{"source", "status"})

var metricsLogger *zap.Logger

func init() {
	activeVecs.Store(newReverseProxyMetricVecs(nil))
}

// currentVecs returns the active per-upstream vecs.
func currentVecs() *reverseProxyMetricVecs {
	return activeVecs.Load()
}

// selectReverseProxyVecs rebuilds the vecs if keys changed, registers them on
// registry, and returns them.
func selectReverseProxyVecs(keys []string, registry *prometheus.Registry) *reverseProxyMetricVecs {
	vecs := activeVecs.Load()
	if !slices.Equal(currentKeys, keys) {
		vecs = newReverseProxyMetricVecs(keys)
		activeVecs.Store(vecs)
		currentKeys = keys
	}
	for _, vec := range vecs.metricVecs() {
		registerMetric(registry, vec)
	}
	registerMetric(registry, dnsResolutionDuration)
	return vecs
}

// resolveCustomLabels resolves the given custom label map against the
// current vecs' custom keys.
func (v *reverseProxyMetricVecs) resolveCustomLabels(ml map[string]string, repl *caddy.Replacer) prometheus.Labels {
	if len(v.customKeys) == 0 {
		return nil
	}
	out := make(prometheus.Labels, len(v.customKeys))
	for _, k := range v.customKeys {
		if tmpl, ok := ml[k]; ok {
			out[k] = repl.ReplaceAll(tmpl, "")
		} else {
			out[k] = ""
		}
	}
	return out
}

// upstreamMetrics holds the current vecs and custom labels
// for a specific request to an upstream.
type upstreamMetrics struct {
	vecs   *reverseProxyMetricVecs
	custom prometheus.Labels
}

// newUpstreamMetrics resolves h's custom labels for the current vecs
// and returns an upstreamMetrics for a request to an upstream.
func newUpstreamMetrics(h *Handler, repl *caddy.Replacer) upstreamMetrics {
	vecs := currentVecs()
	return upstreamMetrics{vecs: vecs, custom: vecs.resolveCustomLabels(h.MetricLabels, repl)}
}

// labels returns the merged labels for this upstreamMetrics.
func (um upstreamMetrics) labels(base prometheus.Labels) prometheus.Labels {
	return mergeLabels(um.custom, base)
}

// mergeLabels clones custom and adds the base labels to it.
func mergeLabels(custom, base prometheus.Labels) prometheus.Labels {
	out := make(prometheus.Labels, len(custom)+len(base))
	for k, v := range custom {
		out[k] = v
	}
	for k, v := range base {
		out[k] = v
	}
	return out
}

// registerMetric registers c, ignoring the error if it is already registered.
func registerMetric(registry *prometheus.Registry, c prometheus.Collector) {
	if err := registry.Register(c); err != nil &&
		!errors.Is(err, prometheus.AlreadyRegisteredError{ExistingCollector: c, NewCollector: c}) {
		panic(err)
	}
}

type metricsUpstreamsHealthyUpdater struct {
	handler *Handler
}

func newMetricsUpstreamsHealthyUpdater(handler *Handler) *metricsUpstreamsHealthyUpdater {
	return &metricsUpstreamsHealthyUpdater{handler}
}

func (m *metricsUpstreamsHealthyUpdater) init() {
	go func() {
		defer func() {
			if err := recover(); err != nil {
				if c := metricsLogger.Check(zapcore.ErrorLevel, "upstreams healthy metrics updater panicked"); c != nil {
					c.Write(
						zap.Any("error", err),
						zap.ByteString("stack", debug.Stack()),
					)
				}
			}
		}()

		m.update()

		ticker := time.NewTicker(10 * time.Second)
		for {
			select {
			case <-ticker.C:
				m.update()
			case <-m.handler.ctx.Done():
				ticker.Stop()
				return
			}
		}
	}()
}

func (m *metricsUpstreamsHealthyUpdater) update() {
	vecs := currentVecs()
	custom := vecs.resolveCustomLabels(m.handler.MetricLabels, caddy.NewReplacer())
	for _, upstream := range m.handler.Upstreams {
		labels := mergeLabels(custom, prometheus.Labels{"upstream": upstream.Dial})

		gaugeValue := 0.0
		if upstream.Healthy() {
			gaugeValue = 1.0
		}

		vecs.upstreamsHealthy.With(labels).Set(gaugeValue)
	}
}
