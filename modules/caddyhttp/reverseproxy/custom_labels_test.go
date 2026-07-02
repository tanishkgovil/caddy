package reverseproxy

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

// TestCaddyfileCustomMetricLabels verifies parsing of the custom metrics block
func TestCaddyfileCustomMetricLabels(t *testing.T) {
	for _, tc := range []struct {
		name    string
		input   string
		want    map[string]string
		wantErr bool
	}{
		{
			name:  "labels accumulate",
			input: "reverse_proxy localhost:8080 {\n metrics {\n label backend api\n label tier gold\n }\n}",
			want:  map[string]string{"backend": "api", "tier": "gold"},
		},
		{
			name:    "missing value",
			input:   "reverse_proxy localhost:8080 {\n metrics {\n label backend\n }\n}",
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &Handler{}
			err := h.UnmarshalCaddyfile(caddyfile.NewTestDispenser(tc.input))
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tc.wantErr)
			}
			if !tc.wantErr && !reflect.DeepEqual(h.MetricLabels, tc.want) {
				t.Errorf("MetricLabels = %v, want %v", h.MetricLabels, tc.want)
			}
		})
	}
}

// TestCustomLabelsEndToEnd verifies that custom labels are unioned and emitted,
// and that missing labels are padded to the empty string.
func TestCustomLabelsEndToEnd(t *testing.T) {
	resetMetricsState(t)

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(backend.Close)
	dial := backend.Listener.Addr().String()

	hA := minimalHandler(0, &Upstream{Host: new(Host), Dial: dial})
	hA.MetricLabels = map[string]string{"backend": "api"}
	hB := minimalHandler(0, &Upstream{Host: new(Host), Dial: dial})
	hB.MetricLabels = map[string]string{"region": "us"}

	// replicate the unioning logic from app provision
	union := append(hA.MetricLabelKeys(), hB.MetricLabelKeys()...)
	slices.Sort(union)
	selectReverseProxyVecs(union, prometheus.NewPedanticRegistry())

	serve := func(h *Handler) {
		req := prepareTestRequest(httptest.NewRequest(http.MethodGet, "http://example.com/", nil))
		rec := httptest.NewRecorder()
		err := h.ServeHTTP(rec, req, caddyhttp.HandlerFunc(func(http.ResponseWriter, *http.Request) error { return nil }))
		if err != nil {
			t.Fatalf("ServeHTTP: %v", err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("status: got %d, want 200", rec.Code)
		}
	}
	serve(hA)
	serve(hB)

	vecs := currentVecs()

	aLabels := prometheus.Labels{"upstream": dial, "backend": "api", "region": ""}
	if got := testutil.ToFloat64(vecs.upstreamRequestsTotal.With(aLabels)); got != 1 {
		t.Errorf("A series: got %v, want 1", got)
	}
	bLabels := prometheus.Labels{"upstream": dial, "backend": "", "region": "us"}
	if got := testutil.ToFloat64(vecs.upstreamRequestsTotal.With(bLabels)); got != 1 {
		t.Errorf("B series: got %v, want 1", got)
	}
}

// resetMetricsState restores the global metrics state for testing purposes.
func resetMetricsState(t *testing.T) {
	t.Helper()
	reset := func() {
		currentKeys = nil
		activeVecs.Store(newReverseProxyMetricVecs(nil))
	}
	reset()
	t.Cleanup(reset)
}
