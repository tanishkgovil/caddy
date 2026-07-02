package caddyhttp

import (
	"sort"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/caddyserver/caddy/v2"
)

// labelsKey serializes a labels map into a stable, sortable string.
func labelsKey(labels prometheus.Labels) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := ""
	for i, k := range keys {
		if i > 0 {
			out += "\x00"
		}
		out += k + "=" + labels[k]
	}
	return out
}

// sortedKeysForVec returns the labels registered for vec as sorted strings.
func sortedKeysForVec(snap *caddy.ConfigSnapshot, vec caddy.MetricVec) []string {
	out := make([]string, 0)
	for _, l := range snap.Entries[vec] {
		out = append(out, labelsKey(l))
	}
	sort.Strings(out)
	return out
}

func TestRegisterHTTPRequestMetricsNoMetrics(t *testing.T) {
	// If Metrics is nil, the helper should do nothing.
	tracker := caddy.NewMetricsTracker()
	app := &App{
		Servers: map[string]*Server{
			"srv0": {Routes: RouteList{{handlerName: "static_response"}}},
		},
	}
	registerHTTPRequestMetrics(tracker, app)

	if got := len(tracker.Snapshot().Entries); got != 0 {
		t.Errorf("want 0 entries when Metrics is nil, got %d", got)
	}
}

func TestRegisterHTTPRequestMetricsBasic(t *testing.T) {
	// Without PerHost, labels should not include a host key.
	vecs := newHTTPMetricVecs(false)
	tracker := caddy.NewMetricsTracker()
	app := &App{
		Metrics: &Metrics{vecs: vecs},
		Servers: map[string]*Server{
			"srv0": {Routes: RouteList{
				{handlerName: "static_response"},
				{handlerName: "reverse_proxy"},
			}},
			"srv1": {Routes: RouteList{
				{handlerName: "file_server"},
			}},
		},
	}
	registerHTTPRequestMetrics(tracker, app)

	want := []string{
		"handler=file_server\x00server=srv1",
		"handler=reverse_proxy\x00server=srv0",
		"handler=static_response\x00server=srv0",
	}
	snap := tracker.Snapshot()
	if got := sortedKeysForVec(snap, vecs.requestCount.MetricVec); !equalStr(got, want) {
		t.Errorf("requestCount: want %v, got %v", want, got)
	}
	// Spot-check another vec — same label set across the family.
	if got := sortedKeysForVec(snap, vecs.requestDuration.MetricVec); !equalStr(got, want) {
		t.Errorf("requestDuration: want %v, got %v", want, got)
	}
}

func TestRegisterHTTPRequestMetricsNamedRoutes(t *testing.T) {
	// Named routes should also be registered, without a host key.
	vecs := newHTTPMetricVecs(false)
	tracker := caddy.NewMetricsTracker()
	app := &App{
		Metrics: &Metrics{vecs: vecs},
		Servers: map[string]*Server{
			"srv0": {
				Routes:      RouteList{{handlerName: "static_response"}},
				NamedRoutes: map[string]*Route{"shared": {handlerName: "reverse_proxy"}},
			},
		},
	}
	registerHTTPRequestMetrics(tracker, app)

	want := []string{
		"handler=reverse_proxy\x00server=srv0",
		"handler=static_response\x00server=srv0",
	}
	if got := sortedKeysForVec(tracker.Snapshot(), vecs.requestCount.MetricVec); !equalStr(got, want) {
		t.Errorf("want %v, got %v", want, got)
	}
}

func TestRegisterHTTPRequestMetricsPerHost(t *testing.T) {
	// With PerHost, labels should include a host key for each allowed host plus "_other".
	vecs := newHTTPMetricVecs(true)
	tracker := caddy.NewMetricsTracker()
	app := &App{
		Metrics: &Metrics{
			PerHost:      true,
			allowedHosts: map[string]struct{}{"example.com": {}, "www.example.com": {}},
			vecs:         vecs,
		},
		Servers: map[string]*Server{
			"srv0": {Routes: RouteList{{handlerName: "reverse_proxy"}}},
		},
	}
	registerHTTPRequestMetrics(tracker, app)

	want := []string{
		"handler=reverse_proxy\x00host=_other\x00server=srv0",
		"handler=reverse_proxy\x00host=example.com\x00server=srv0",
		"handler=reverse_proxy\x00host=www.example.com\x00server=srv0",
	}
	if got := sortedKeysForVec(tracker.Snapshot(), vecs.requestCount.MetricVec); !equalStr(got, want) {
		t.Errorf("PerHost: want %v, got %v", want, got)
	}
}

func equalStr(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
