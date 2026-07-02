package caddyhttp

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/caddyserver/caddy/v2"
)

// registerHTTPRequestMetrics registers every (server, handler[, host])
// label for the caddy_http_* metrics.
func registerHTTPRequestMetrics(tracker *caddy.MetricsTracker, app *App) {
	if tracker == nil || app.Metrics == nil || app.Metrics.vecs == nil {
		return
	}

	metricVecs := app.Metrics.vecs.metricVecs()

	register := func(srvName, handlerName string) {
		if handlerName == "" {
			return
		}
		if !app.Metrics.PerHost {
			base := prometheus.Labels{"server": srvName, "handler": handlerName}
			for _, vec := range metricVecs {
				tracker.RegisterMetric(vec, base)
			}
			return
		}
		hostList := make([]string, 0, len(app.Metrics.allowedHosts)+1)
		for h := range app.Metrics.allowedHosts {
			hostList = append(hostList, h)
		}
		hostList = append(hostList, "_other")
		for _, host := range hostList {
			labels := prometheus.Labels{"server": srvName, "handler": handlerName, "host": host}
			for _, vec := range metricVecs {
				tracker.RegisterMetric(vec, labels)
			}
		}
	}

	for srvName, srv := range app.Servers {
		for _, route := range srv.Routes {
			register(srvName, route.handlerName)
		}
		for _, route := range srv.NamedRoutes {
			register(srvName, route.handlerName)
		}
	}
}

// registerConnMetrics registers every server label for the
// caddy_http_connections_* metrics.
func registerConnMetrics(tracker *caddy.MetricsTracker, app *App) {
	if tracker == nil || app.Metrics == nil {
		return
	}
	vecs := connVecs.metricVecs()
	for srvName := range app.Servers {
		labels := prometheus.Labels{"server": srvName}
		for _, vec := range vecs {
			tracker.RegisterMetric(vec, labels)
		}
	}
}
