package caddyhttp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	otelprom "go.opentelemetry.io/contrib/bridges/prometheus"
	"go.opentelemetry.io/contrib/exporters/autoexport"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"

	"github.com/caddyserver/caddy/v2"
	caddymetrics "github.com/caddyserver/caddy/v2/internal/metrics"
)

// Metrics configures metrics observations.
// EXPERIMENTAL and subject to change or removal.
//
// Example configuration:
//
//	{
//		"apps": {
//			"http": {
//				"metrics": {
//					"per_host": true,
//					"observe_catchall_hosts": false
//				},
//				"servers": {
//					"srv0": {
//						"routes": [{
//							"match": [{"host": ["example.com", "www.example.com"]}],
//							"handle": [{"handler": "static_response", "body": "Hello"}]
//						}]
//					}
//				}
//			}
//		}
//	}
//
// In this configuration:
// - Requests to example.com and www.example.com get individual host labels
// - All other hosts (e.g., attacker.com) are aggregated under "_other" label
// - This prevents unlimited cardinality from arbitrary Host headers
type Metrics struct {
	// Enable per-host metrics. Enabling this option may
	// incur high-memory consumption, depending on the number of hosts
	// managed by Caddy.
	//
	// CARDINALITY PROTECTION: To prevent unbounded cardinality attacks,
	// only explicitly configured hosts (via host matchers) are allowed
	// by default. Other hosts are aggregated under the "_other" label.
	// See AllowCatchAllHosts to change this behavior.
	PerHost bool `json:"per_host,omitempty"`

	// Allow metrics for catch-all hosts (hosts without explicit configuration).
	// When false (default), only hosts explicitly configured via host matchers
	// will get individual metrics labels. All other hosts will be aggregated
	// under the "_other" label to prevent cardinality explosion.
	//
	// This is automatically enabled for HTTPS servers (since certificates provide
	// some protection against unbounded cardinality), but disabled for HTTP servers
	// by default to prevent cardinality attacks from arbitrary Host headers.
	//
	// Set to true to allow all hosts to get individual metrics (NOT RECOMMENDED
	// for production environments exposed to the internet).
	ObserveCatchallHosts bool `json:"observe_catchall_hosts,omitempty"`

	// Enable pushing metrics via OTLP in addition to the existing Prometheus
	// scrape endpoints. When set, a PeriodicReader is attached to the shared
	// Prometheus registry (via a Prometheus -> OpenTelemetry bridge), and the
	// exporter is autoconfigured from the standard OTEL_* environment
	// variables (OTEL_EXPORTER_OTLP_ENDPOINT, OTEL_EXPORTER_OTLP_PROTOCOL,
	// OTEL_METRICS_EXPORTER, ...). Set OTEL_METRICS_EXPORTER=none or simply
	// keep this field false to disable OTLP export.
	OTLP bool `json:"otlp,omitempty"`

	allowedHosts   map[string]struct{}
	hasHTTPSServer bool
	meterProvider  *sdkmetric.MeterProvider
	vecs           *httpMetricVecs
}

// httpMetricVecs holds the package-level metrics for the HTTP app.
type httpMetricVecs struct {
	requestInFlight  *prometheus.GaugeVec
	requestErrors    *prometheus.CounterVec
	requestCount     *prometheus.CounterVec
	requestDuration  *prometheus.HistogramVec
	requestSize      *prometheus.HistogramVec
	responseSize     *prometheus.HistogramVec
	responseDuration *prometheus.HistogramVec
}

// metricVecs returns all MetricVecs contained in this struct.
func (v *httpMetricVecs) metricVecs() []caddy.MetricVec {
	return []caddy.MetricVec{
		v.requestInFlight.MetricVec,
		v.requestErrors.MetricVec,
		v.requestCount.MetricVec,
		v.requestDuration.MetricVec,
		v.requestSize.MetricVec,
		v.responseSize.MetricVec,
		v.responseDuration.MetricVec,
	}
}

func newHTTPMetricVecs(perHost bool) *httpMetricVecs {
	base := []string{"server", "handler"}
	withCode := []string{"server", "handler", "code", "method"}
	if perHost {
		base = append(base, "host")
		withCode = append(withCode, "host")
	}
	return &httpMetricVecs{
		requestInFlight: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "caddy", Subsystem: "http",
			Name: "requests_in_flight",
			Help: "Number of requests currently handled by this server.",
		}, base),

		requestErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "caddy", Subsystem: "http",
			Name: "request_errors_total",
			Help: "Number of requests resulting in middleware errors.",
		}, base),

		requestCount: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "caddy", Subsystem: "http",
			Name: "requests_total",
			Help: "Counter of HTTP(S) requests made.",
		}, base),

		requestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "caddy", Subsystem: "http",
			Name:    "request_duration_seconds",
			Help:    "Histogram of round-trip request durations.",
			Buckets: prometheus.DefBuckets,
		}, withCode),

		requestSize: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "caddy", Subsystem: "http",
			Name:    "request_size_bytes",
			Help:    "Total size of the request. Includes body.",
			Buckets: prometheus.ExponentialBuckets(256, 4, 8),
		}, withCode),

		responseSize: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "caddy", Subsystem: "http",
			Name:    "response_size_bytes",
			Help:    "Size of the returned response.",
			Buckets: prometheus.ExponentialBuckets(256, 4, 8),
		}, withCode),

		responseDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "caddy", Subsystem: "http",
			Name:    "response_duration_seconds",
			Help:    "Histogram of times to first byte in response bodies.",
			Buckets: prometheus.DefBuckets,
		}, withCode),
	}
}

// currentHTTPVecs and currentHTTPPerHost hold current state of HTTP metric vecs
var (
	currentHTTPVecs    *httpMetricVecs
	currentHTTPPerHost bool
)

// selectHTTPVecs returns current httpMetricVecs, creates new ones if perHost changed
func selectHTTPVecs(perHost bool) *httpMetricVecs {
	if currentHTTPVecs == nil || currentHTTPPerHost != perHost {
		currentHTTPVecs = newHTTPMetricVecs(perHost)
		currentHTTPPerHost = perHost
	}
	return currentHTTPVecs
}

// initHTTPMetrics registers the HTTP metrics vecs to the provided Prometheus registry.
// Panics if registration fails for any reason other than already being registered.
func initHTTPMetrics(registry *prometheus.Registry, vecs *httpMetricVecs) {
	registerVecs(registry, vecs.metricVecs())
}

// registerVecs registers each vec on the registry
func registerVecs(registry *prometheus.Registry, vecs []caddy.MetricVec) {
	for _, vec := range vecs {
		if err := registry.Register(vec); err != nil &&
			!errors.Is(err, prometheus.AlreadyRegisteredError{
				ExistingCollector: vec,
				NewCollector:      vec,
			}) {
			panic(err)
		}
	}
}

// connMetricVecs holds the package-level metrics for connection tracking.
type connMetricVecs struct {
	connectionsTotal   *prometheus.CounterVec
	currentConnections *prometheus.GaugeVec
	receivedBytes      *prometheus.CounterVec
	sentBytes          *prometheus.CounterVec
	listeners          *prometheus.GaugeVec
}

func (v *connMetricVecs) metricVecs() []caddy.MetricVec {
	return []caddy.MetricVec{
		v.connectionsTotal.MetricVec,
		v.currentConnections.MetricVec,
		v.receivedBytes.MetricVec,
		v.sentBytes.MetricVec,
		v.listeners.MetricVec,
	}
}

var connVecs = &connMetricVecs{
	connectionsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "caddy", Subsystem: "http",
		Name: "connections_total",
		Help: "Number of connections accepted by this server.",
	}, []string{"server"}),
	currentConnections: prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "caddy", Subsystem: "http",
		Name: "current_connections",
		Help: "Number of connections currently open on this server.",
	}, []string{"server"}),
	receivedBytes: prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "caddy", Subsystem: "http",
		Name: "received_bytes_total",
		Help: "Number of bytes received from clients on this server's connections.",
	}, []string{"server"}),
	sentBytes: prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "caddy", Subsystem: "http",
		Name: "sent_bytes_total",
		Help: "Number of bytes sent to clients on this server's connections.",
	}, []string{"server"}),
	listeners: prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "caddy", Subsystem: "http",
		Name: "listeners",
		Help: "Number of bound network sockets for this server.",
	}, []string{"server"}),
}

// initConnMetrics registers the connection metrics vecs to the provided Prometheus registry.
func initConnMetrics(registry *prometheus.Registry, vecs *connMetricVecs) {
	registerVecs(registry, vecs.metricVecs())
}

// metricsListener wraps a net.Listener to keep metrics updated with listener activity.
type metricsListener struct {
	net.Listener
	server string
	vecs   *connMetricVecs
}

func (l *metricsListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	labels := prometheus.Labels{"server": l.server}
	l.vecs.connectionsTotal.With(labels).Inc()
	cur := l.vecs.currentConnections.With(labels)
	cur.Inc()
	return &metricsConn{
		Conn:     conn,
		dec:      cur.Dec,
		received: l.vecs.receivedBytes.With(labels),
		sent:     l.vecs.sentBytes.With(labels),
	}, nil
}

// metricsConn wraps a net.Conn to keep metrics updated with connection activity.
type metricsConn struct {
	net.Conn
	once     sync.Once
	dec      func()
	received prometheus.Counter
	sent     prometheus.Counter
}

func (c *metricsConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	c.received.Add(float64(n))
	return n, err
}

func (c *metricsConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	c.sent.Add(float64(n))
	return n, err
}

func (c *metricsConn) Close() error {
	c.once.Do(c.dec)
	return c.Conn.Close()
}

// provisionOTLP wires a MeterProvider that periodically reads the process-wide
// Prometheus registry and pushes the result via OTLP. The exporter and reader
// are autoconfigured from the standard OTEL_* environment variables, matching
// the ergonomics of the existing `tracing` directive. It is a no-op when
// m.OTLP is false, and honors OTEL_METRICS_EXPORTER=none (autoexport
// short-circuits to a no-op reader in that case).
func (m *Metrics) provisionOTLP(ctx caddy.Context) error {
	if !m.OTLP {
		return nil
	}

	// Register a Prometheus -> OpenTelemetry bridge against the process-wide
	// Prometheus registry as the *default* source the NewMetricReader below
	// will read from.
	//
	// NB: despite the "With*" naming, autoexport.WithFallbackMetricProducer is
	// a package-level setter (it returns nothing) — it mutates autoexport's
	// internal producer registry and takes effect on the very next call to
	// NewMetricReader. It is NOT a MetricOption and must not be passed as one.
	// Users can still override the source by setting OTEL_METRICS_PRODUCERS.
	reg := ctx.GetMetricsRegistry()
	autoexport.WithFallbackMetricProducer(func(context.Context) (sdkmetric.Producer, error) {
		return otelprom.NewMetricProducer(otelprom.WithGatherer(reg)), nil
	})

	reader, err := autoexport.NewMetricReader(ctx)
	if err != nil {
		return fmt.Errorf("creating OTLP metric reader: %w", err)
	}

	version, _ := caddy.Version()
	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(
		semconv.WebEngineName(ServerHeader),
		semconv.WebEngineVersion(version),
	))
	if err != nil {
		return fmt.Errorf("building OTLP metrics resource: %w", err)
	}

	m.meterProvider = sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(reader),
	)

	return nil
}

// shutdown flushes and tears down the OTLP MeterProvider if one was provisioned.
// Both ForceFlush and Shutdown are always attempted so that a flush failure
// does not prevent the reader goroutines from being stopped; errors from both
// are returned joined.
func (m *Metrics) shutdown(ctx context.Context) error {
	if m == nil || m.meterProvider == nil {
		return nil
	}

	// ForceFlush gives the final collection a chance to reach the collector
	// before the reader goroutine is stopped by Shutdown.
	return errors.Join(
		m.meterProvider.ForceFlush(ctx),
		m.meterProvider.Shutdown(ctx),
	)
}

// scanConfigForHosts scans the HTTP app configuration to build a set of allowed hosts
// for metrics collection, similar to how auto-HTTPS scans for domain names.
func (m *Metrics) scanConfigForHosts(app *App) {
	if !m.PerHost {
		return
	}

	m.allowedHosts = make(map[string]struct{})
	m.hasHTTPSServer = false

	for _, srv := range app.Servers {
		// Check if this server has TLS enabled
		serverHasTLS := len(srv.TLSConnPolicies) > 0
		if serverHasTLS {
			m.hasHTTPSServer = true
		}

		// Collect hosts from route matchers
		for _, route := range srv.Routes {
			for _, matcherSet := range route.MatcherSets {
				for _, matcher := range matcherSet {
					if hm, ok := matcher.(*MatchHost); ok {
						for _, host := range *hm {
							// Only allow non-fuzzy hosts to prevent unbounded cardinality
							if !hm.fuzzy(host) {
								m.allowedHosts[strings.ToLower(host)] = struct{}{}
							}
						}
					}
				}
			}
		}
	}
}

// shouldAllowHostMetrics determines if metrics should be collected for the given host.
// This implements the cardinality protection by only allowing metrics for:
// 1. Explicitly configured hosts
// 2. Catch-all requests on HTTPS servers (if AllowCatchAllHosts is true or auto-enabled)
// 3. Catch-all requests on HTTP servers only if explicitly allowed
func (m *Metrics) shouldAllowHostMetrics(host string, isHTTPS bool) bool {
	if !m.PerHost {
		return true // host won't be used in labels anyway
	}

	normalizedHost := strings.ToLower(host)

	// Always allow explicitly configured hosts
	if _, exists := m.allowedHosts[normalizedHost]; exists {
		return true
	}

	// For catch-all requests (not in allowed hosts)
	allowCatchAll := m.ObserveCatchallHosts || (isHTTPS && m.hasHTTPSServer)
	return allowCatchAll
}

// serverNameFromContext extracts the current server name from the context.
// Returns "UNKNOWN" if none is available (should probably never happen).
func serverNameFromContext(ctx context.Context) string {
	srv, ok := ctx.Value(ServerCtxKey).(*Server)
	if !ok || srv == nil || srv.name == "" {
		return "UNKNOWN"
	}
	return srv.name
}

// metricsInstrumentedRoute wraps a compiled route Handler with metrics
// instrumentation. It wraps the entire compiled route chain once,
// collecting metrics only once per route match.
type metricsInstrumentedRoute struct {
	handler string
	next    Handler
	metrics *Metrics
}

func newMetricsInstrumentedRoute(_ caddy.Context, handler string, next Handler, m *Metrics) *metricsInstrumentedRoute {
	return &metricsInstrumentedRoute{handler: handler, next: next, metrics: m}
}

func (h *metricsInstrumentedRoute) ServeHTTP(w http.ResponseWriter, r *http.Request) error {
	server := serverNameFromContext(r.Context())
	labels := prometheus.Labels{"server": server, "handler": h.handler}
	method := caddymetrics.SanitizeMethod(r.Method)
	// the "code" value is set later, but initialized here to eliminate the possibility
	// of a panic
	statusLabels := prometheus.Labels{"server": server, "handler": h.handler, "method": method, "code": ""}

	// Determine if this is an HTTPS request
	isHTTPS := r.TLS != nil

	if h.metrics.PerHost {
		// Apply cardinality protection for host metrics
		if h.metrics.shouldAllowHostMetrics(r.Host, isHTTPS) {
			labels["host"] = strings.ToLower(r.Host)
			statusLabels["host"] = strings.ToLower(r.Host)
		} else {
			// Use a catch-all label for unallowed hosts to prevent cardinality explosion
			labels["host"] = "_other"
			statusLabels["host"] = "_other"
		}
	}

	vecs := h.metrics.vecs
	inFlight := vecs.requestInFlight.With(labels)
	inFlight.Inc()
	defer inFlight.Dec()

	start := time.Now()

	// This is a _bit_ of a hack - it depends on the ShouldBufferFunc always
	// being called when the headers are written.
	// Effectively the same behaviour as promhttp.InstrumentHandlerTimeToWriteHeader.
	writeHeaderRecorder := ShouldBufferFunc(func(status int, header http.Header) bool {
		statusLabels["code"] = caddymetrics.SanitizeCode(status)
		ttfb := time.Since(start).Seconds()
		vecs.responseDuration.With(statusLabels).Observe(ttfb)
		return false
	})
	wrec := NewResponseRecorder(w, nil, writeHeaderRecorder)
	err := h.next.ServeHTTP(wrec, r)
	dur := time.Since(start).Seconds()
	vecs.requestCount.With(labels).Inc()

	observeRequest := func(status int) {
		// If the code hasn't been set yet, and we didn't encounter an error, we're
		// probably falling through with an empty handler.
		if statusLabels["code"] == "" {
			// we still sanitize it, even though it's likely to be 0. A 200 is
			// returned on fallthrough so we want to reflect that.
			statusLabels["code"] = caddymetrics.SanitizeCode(status)
		}

		vecs.requestDuration.With(statusLabels).Observe(dur)
		vecs.requestSize.With(statusLabels).Observe(float64(computeApproximateRequestSize(r)))
		vecs.responseSize.With(statusLabels).Observe(float64(wrec.Size()))
	}

	if err != nil {
		var handlerErr HandlerError
		if errors.As(err, &handlerErr) {
			observeRequest(handlerErr.StatusCode)
		}

		vecs.requestErrors.With(labels).Inc()

		return err
	}

	observeRequest(wrec.Status())

	return nil
}

// taken from https://github.com/prometheus/client_golang/blob/6007b2b5cae01203111de55f753e76d8dac1f529/prometheus/promhttp/instrument_server.go#L298
func computeApproximateRequestSize(r *http.Request) int {
	s := 0
	if r.URL != nil {
		s += len(r.URL.String())
	}

	s += len(r.Method)
	s += len(r.Proto)
	for name, values := range r.Header {
		s += len(name)
		for _, value := range values {
			s += len(value)
		}
	}
	s += len(r.Host)

	// N.B. r.Form and r.MultipartForm are assumed to be included in r.URL.

	if r.ContentLength != -1 {
		s += int(r.ContentLength)
	}
	return s
}
