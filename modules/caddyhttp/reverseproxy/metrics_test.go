package reverseproxy

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"

	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

// histogramSampleCount is a helper that retreives the sample count for a given
// label set from a histogram vector.
func histogramSampleCount(t *testing.T, vec *prometheus.HistogramVec, labels prometheus.Labels) uint64 {
	t.Helper()
	obs, err := vec.GetMetricWith(labels)
	if err != nil {
		t.Fatalf("GetMetricWith: %v", err)
	}
	var m dto.Metric
	if err := obs.(prometheus.Metric).Write(&m); err != nil {
		t.Fatalf("Write: %v", err)
	}
	return m.GetHistogram().GetSampleCount()
}

// TestUpstreamRoundTripMetrics verifies the per-upstream metrics recorded during a single
// successful request.
func TestUpstreamRoundTripMetrics(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	t.Cleanup(backend.Close)

	dial := backend.Listener.Addr().String()
	h := minimalHandler(0, &Upstream{Host: new(Host), Dial: dial})

	req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	req = prepareTestRequest(req)
	rec := httptest.NewRecorder()

	if err := h.ServeHTTP(rec, req, caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
		return nil
	})); err != nil {
		t.Fatalf("ServeHTTP: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", rec.Code, http.StatusOK)
	}

	labels := prometheus.Labels{"upstream": dial}
	successLabels := prometheus.Labels{"upstream": dial, "status": "success"}

	if got := histogramSampleCount(t, reverseProxyMetrics.upstreamConnectDuration, successLabels); got != 1 {
		t.Errorf("connect duration samples: got %d, want 1", got)
	}
	if got := histogramSampleCount(t, reverseProxyMetrics.upstreamResponseDuration, labels); got != 1 {
		t.Errorf("response duration samples: got %d, want 1", got)
	}
	if got := histogramSampleCount(t, reverseProxyMetrics.upstreamDuration, successLabels); got != 1 {
		t.Errorf("total duration samples: got %d, want 1", got)
	}
	if got := testutil.ToFloat64(reverseProxyMetrics.upstreamConnAttempts.With(labels)); got != 1 {
		t.Errorf("connection attempts: got %v, want 1", got)
	}
	if got := testutil.ToFloat64(reverseProxyMetrics.upstreamRequestsTotal.With(labels)); got != 1 {
		t.Errorf("requests total: got %v, want 1", got)
	}
	if got := testutil.ToFloat64(reverseProxyMetrics.upstreamRequestsInFlight.With(labels)); got != 0 {
		t.Errorf("requests in flight after completion: got %v, want 0", got)
	}
	if got := testutil.ToFloat64(reverseProxyMetrics.upstreamResponsesTotal.With(prometheus.Labels{"upstream": dial, "code": "200"})); got != 1 {
		t.Errorf("responses total: got %v, want 1", got)
	}
	if got := testutil.ToFloat64(reverseProxyMetrics.upstreamLastSession.With(labels)); got <= 0 {
		t.Errorf("last session timestamp: got %v, want > 0", got)
	}
}

// TestUpstreamServerAbortMetric verifies that a mid-body upstream disconnect updates
// the upstreamServerAborts metric.
func TestUpstreamServerAbortMetric(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, bufrw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("Hijack: %v", err)
			return
		}
		// simulate a server abort by promising 100 bytes but only sending partial
		bufrw.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 100\r\n\r\npartial")
		bufrw.Flush()
		conn.Close()
	}))
	t.Cleanup(backend.Close)

	dial := backend.Listener.Addr().String()
	h := minimalHandler(0, &Upstream{Host: new(Host), Dial: dial})
	labels := prometheus.Labels{"upstream": dial}
	req := prepareTestRequest(httptest.NewRequest(http.MethodGet, "http://example.com/", nil))
	rec := httptest.NewRecorder()
	func() {
		defer func() { _ = recover() }()
		_ = h.ServeHTTP(rec, req, caddyhttp.HandlerFunc(func(http.ResponseWriter, *http.Request) error { return nil }))
	}()

	if got := testutil.ToFloat64(reverseProxyMetrics.upstreamServerAborts.With(labels)); got != 1 {
		t.Errorf("server_aborts_total: got %v, want 1", got)
	}
}

// TestUpstreamConnectionMetrics verifies that the upstream connection
// metrics update correctly when requests are proxied to an upstream.
func TestUpstreamConnectionMetrics(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	t.Cleanup(backend.Close)

	dial := backend.Listener.Addr().String()
	h := minimalHandler(0, &Upstream{Host: new(Host), Dial: dial})
	labels := prometheus.Labels{"upstream": dial}

	doRequest := func() {
		req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
		req = prepareTestRequest(req)
		rec := httptest.NewRecorder()
		if err := h.ServeHTTP(rec, req, caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
			return nil
		})); err != nil {
			t.Fatalf("ServeHTTP: %v", err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("status: got %d, want %d", rec.Code, http.StatusOK)
		}
	}

	// Create new connection
	doRequest()
	if got := testutil.ToFloat64(reverseProxyMetrics.upstreamConnAttempts.With(labels)); got != 1 {
		t.Errorf("connection attempts after first request: got %v, want 1", got)
	}
	if got := testutil.ToFloat64(reverseProxyMetrics.upstreamConnErrors.With(labels)); got != 0 {
		t.Errorf("connection errors: got %v, want 0", got)
	}
	if got := testutil.ToFloat64(reverseProxyMetrics.upstreamConnReuses.With(labels)); got != 0 {
		t.Errorf("connection reuses after first request: got %v, want 0", got)
	}

	// Reuse existing connection
	doRequest()
	if got := testutil.ToFloat64(reverseProxyMetrics.upstreamConnAttempts.With(labels)); got != 1 {
		t.Errorf("connection attempts after second request: got %v, want 1", got)
	}
	if got := testutil.ToFloat64(reverseProxyMetrics.upstreamConnReuses.With(labels)); got != 1 {
		t.Errorf("connection reuses after second request: got %v, want 1", got)
	}
}
