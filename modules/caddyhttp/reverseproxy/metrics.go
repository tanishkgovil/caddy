package reverseproxy

import (
	"errors"
	"runtime/debug"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/caddyserver/caddy/v2"
)

var reverseProxyMetrics = struct {
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
	dnsResolutionDuration    *prometheus.HistogramVec
	logger                   *zap.Logger
}{
	// Create the vecs at package init so callers can register
	// upstream labels with the metrics tracker
	upstreamsHealthy: prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "caddy",
		Subsystem: "reverse_proxy",
		Name:      "upstreams_healthy",
		Help:      "Health status of reverse proxy upstreams.",
	}, []string{"upstream"}),
	upstreamConnectDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "caddy",
		Subsystem: "reverse_proxy",
		Name:      "upstream_connect_duration_seconds",
		Help:      "Time spent connecting to upstreams, by status.",
		Buckets:   prometheus.DefBuckets,
	}, []string{"upstream", "status"}),
	upstreamResponseDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "caddy",
		Subsystem: "reverse_proxy",
		Name:      "upstream_response_duration_seconds",
		Help:      "Time spent waiting for a response from upstreams.",
		Buckets:   prometheus.DefBuckets,
	}, []string{"upstream"}),
	upstreamRequestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "caddy",
		Subsystem: "reverse_proxy",
		Name:      "upstream_requests_total",
		Help:      "Number of requests proxied to upstreams.",
	}, []string{"upstream"}),
	upstreamRequestsInFlight: prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "caddy",
		Subsystem: "reverse_proxy",
		Name:      "upstream_requests_in_flight",
		Help:      "Number of requests currently in flight to upstreams.",
	}, []string{"upstream"}),
	upstreamResponsesTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "caddy",
		Subsystem: "reverse_proxy",
		Name:      "upstream_responses_total",
		Help:      "Number of responses received from upstreams, by status code.",
	}, []string{"upstream", "code"}),
	upstreamCheckDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "caddy",
		Subsystem: "reverse_proxy",
		Name:      "upstream_check_duration_seconds",
		Help:      "Duration of active health checks to upstreams.",
		Buckets:   prometheus.DefBuckets,
	}, []string{"upstream"}),
	upstreamCheckFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "caddy",
		Subsystem: "reverse_proxy",
		Name:      "upstream_check_failures_total",
		Help:      "Number of failed active health checks to upstreams, by failure reason.",
	}, []string{"upstream", "reason"}),
	upstreamCheckUpDown: prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "caddy",
		Subsystem: "reverse_proxy",
		Name:      "upstream_check_up_down_total",
		Help:      "Number of upstream health state transitions from active health checks.",
	}, []string{"upstream"}),
	upstreamConnAttempts: prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "caddy",
		Subsystem: "reverse_proxy",
		Name:      "upstream_connection_attempts_total",
		Help:      "Number of attempts to establish a new connection to upstreams.",
	}, []string{"upstream"}),
	upstreamConnErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "caddy",
		Subsystem: "reverse_proxy",
		Name:      "upstream_connection_errors_total",
		Help:      "Number of failed attempts to establish a new connection to upstreams.",
	}, []string{"upstream"}),
	upstreamConnReuses: prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "caddy",
		Subsystem: "reverse_proxy",
		Name:      "upstream_connection_reuses_total",
		Help:      "Number of requests served on a reused connection to upstreams.",
	}, []string{"upstream"}),
	upstreamTLSDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "caddy",
		Subsystem: "reverse_proxy",
		Name:      "upstream_tls_handshake_duration_seconds",
		Help:      "Duration of TLS handshakes to upstreams, by status.",
		Buckets:   prometheus.DefBuckets,
	}, []string{"upstream", "status"}),
	upstreamTLSErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "caddy",
		Subsystem: "reverse_proxy",
		Name:      "upstream_tls_handshake_errors_total",
		Help:      "Number of failed TLS handshakes to upstreams.",
	}, []string{"upstream"}),
	upstreamTLSHandshakes: prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "caddy",
		Subsystem: "reverse_proxy",
		Name:      "upstream_tls_handshakes_total",
		Help:      "Counter of completed TLS handshakes to upstreams, by resumption status.",
	}, []string{"upstream", "resumed"}),
	upstreamDNSDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "caddy",
		Subsystem: "reverse_proxy",
		Name:      "upstream_dns_resolution_duration_seconds",
		Help:      "Duration of DNS resolution for upstreams, by status.",
		Buckets:   prometheus.DefBuckets,
	}, []string{"upstream", "status"}),
	upstreamDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "caddy",
		Subsystem: "reverse_proxy",
		Name:      "upstream_duration_seconds",
		Help:      "Total duration of the round trip to upstreams, by status.",
		Buckets:   prometheus.DefBuckets,
	}, []string{"upstream", "status"}),
	upstreamRetries: prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "caddy",
		Subsystem: "reverse_proxy",
		Name:      "upstream_retries_total",
		Help:      "Number of failed attempts to upstreams that triggered a retry.",
	}, []string{"upstream"}),
	upstreamResponseErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "caddy",
		Subsystem: "reverse_proxy",
		Name:      "upstream_response_errors_total",
		Help:      "Number of upstream request failures (excludes successful and retryable responses).",
	}, []string{"upstream"}),
	upstreamSentBytes: prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "caddy",
		Subsystem: "reverse_proxy",
		Name:      "upstream_sent_bytes_total",
		Help:      "Number of request body bytes sent to upstreams.",
	}, []string{"upstream"}),
	upstreamReceivedBytes: prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "caddy",
		Subsystem: "reverse_proxy",
		Name:      "upstream_received_bytes_total",
		Help:      "Number of response body bytes received from upstreams.",
	}, []string{"upstream"}),
	upstreamLastSession: prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "caddy",
		Subsystem: "reverse_proxy",
		Name:      "upstream_last_session_seconds",
		Help:      "Unix timestamp of the last request proxied to an upstream.",
	}, []string{"upstream"}),
	upstreamRedispatches: prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "caddy",
		Subsystem: "reverse_proxy",
		Name:      "upstream_redispatch_warnings_total",
		Help:      "Number of connection failures retried on a different upstream than the previous attempt.",
	}, []string{"upstream"}),
	upstreamClientAborts: prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "caddy",
		Subsystem: "reverse_proxy",
		Name:      "upstream_client_aborts_total",
		Help:      "Number of responses where the client disconnected before the full body was sent.",
	}, []string{"upstream"}),
	upstreamServerAborts: prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "caddy",
		Subsystem: "reverse_proxy",
		Name:      "upstream_server_aborts_total",
		Help:      "Number of responses where the upstream closed or reset the connection before the full body was received.",
	}, []string{"upstream"}),
	dnsResolutionDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "caddy",
		Subsystem: "reverse_proxy",
		Name:      "dns_resolution_duration_seconds",
		Help:      "Duration of DNS resolutions for dynamic upstream sources, by status.",
		Buckets:   prometheus.DefBuckets,
	}, []string{"source", "status"}),
}

// upstreamMetricVecs returns the per-upstream metric vectors.
func upstreamMetricVecs() []caddy.MetricVec {
	return []caddy.MetricVec{
		reverseProxyMetrics.upstreamsHealthy,
		reverseProxyMetrics.upstreamConnectDuration,
		reverseProxyMetrics.upstreamResponseDuration,
		reverseProxyMetrics.upstreamRequestsTotal,
		reverseProxyMetrics.upstreamRequestsInFlight,
		reverseProxyMetrics.upstreamResponsesTotal,
		reverseProxyMetrics.upstreamCheckDuration,
		reverseProxyMetrics.upstreamCheckFailures,
		reverseProxyMetrics.upstreamCheckUpDown,
		reverseProxyMetrics.upstreamConnAttempts,
		reverseProxyMetrics.upstreamConnErrors,
		reverseProxyMetrics.upstreamConnReuses,
		reverseProxyMetrics.upstreamTLSDuration,
		reverseProxyMetrics.upstreamTLSErrors,
		reverseProxyMetrics.upstreamTLSHandshakes,
		reverseProxyMetrics.upstreamDNSDuration,
		reverseProxyMetrics.upstreamDuration,
		reverseProxyMetrics.upstreamRetries,
		reverseProxyMetrics.upstreamResponseErrors,
		reverseProxyMetrics.upstreamSentBytes,
		reverseProxyMetrics.upstreamReceivedBytes,
		reverseProxyMetrics.upstreamLastSession,
		reverseProxyMetrics.upstreamRedispatches,
		reverseProxyMetrics.upstreamClientAborts,
		reverseProxyMetrics.upstreamServerAborts,
	}
}

// registerMetric registers c, ignoring the error if it is already registered.
func registerMetric(registry *prometheus.Registry, c prometheus.Collector) {
	if err := registry.Register(c); err != nil &&
		!errors.Is(err, prometheus.AlreadyRegisteredError{ExistingCollector: c, NewCollector: c}) {
		panic(err)
	}
}

func initReverseProxyMetrics(handler *Handler, registry *prometheus.Registry) {
	for _, vec := range upstreamMetricVecs() {
		registerMetric(registry, vec)
	}
	registerMetric(registry, reverseProxyMetrics.dnsResolutionDuration)

	reverseProxyMetrics.logger = handler.logger.Named("reverse_proxy.metrics")
}

type metricsUpstreamsHealthyUpdater struct {
	handler *Handler
}

func newMetricsUpstreamsHealthyUpdater(handler *Handler, ctx caddy.Context) *metricsUpstreamsHealthyUpdater {
	initReverseProxyMetrics(handler, ctx.GetMetricsRegistry())
	reverseProxyMetrics.upstreamsHealthy.Reset()

	return &metricsUpstreamsHealthyUpdater{handler}
}

func (m *metricsUpstreamsHealthyUpdater) init() {
	go func() {
		defer func() {
			if err := recover(); err != nil {
				if c := reverseProxyMetrics.logger.Check(zapcore.ErrorLevel, "upstreams healthy metrics updater panicked"); c != nil {
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
	for _, upstream := range m.handler.Upstreams {
		labels := prometheus.Labels{"upstream": upstream.Dial}

		gaugeValue := 0.0
		if upstream.Healthy() {
			gaugeValue = 1.0
		}

		reverseProxyMetrics.upstreamsHealthy.With(labels).Set(gaugeValue)
	}
}
