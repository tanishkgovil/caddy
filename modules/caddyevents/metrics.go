package caddyevents

import (
	"errors"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/caddyserver/caddy/v2"
)

var eventsMetrics = struct {
	emitted          *prometheus.CounterVec
	aborted          *prometheus.CounterVec
	handlerErrors    *prometheus.CounterVec
	dispatchDuration *prometheus.HistogramVec
}{
	emitted: prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "caddy",
		Subsystem: "events",
		Name:      "emitted_total",
		Help:      "Number of events emitted, by name and origin module.",
	}, []string{"name", "origin"}),
	aborted: prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "caddy",
		Subsystem: "events",
		Name:      "aborted_total",
		Help:      "Number of events aborted by a handler, by name and origin module.",
	}, []string{"name", "origin"}),
	handlerErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "caddy",
		Subsystem: "events",
		Name:      "handler_errors_total",
		Help:      "Number of non-abort errors returned by event handlers, by name and origin module.",
	}, []string{"name", "origin"}),
	dispatchDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "caddy",
		Subsystem: "events",
		Name:      "dispatch_duration_seconds",
		Help:      "Time for all of an event's handlers to run, by name and origin module.",
	}, []string{"name", "origin"}),
}

// eventsMetricVecs returns the metric vectors for the events app.
func eventsMetricVecs() []caddy.MetricVec {
	return []caddy.MetricVec{
		eventsMetrics.emitted,
		eventsMetrics.aborted,
		eventsMetrics.handlerErrors,
		eventsMetrics.dispatchDuration,
	}
}

// registerMetric registers c, ignoring the error if it is already registered.
func registerMetric(registry *prometheus.Registry, c prometheus.Collector) {
	if err := registry.Register(c); err != nil &&
		!errors.Is(err, prometheus.AlreadyRegisteredError{ExistingCollector: c, NewCollector: c}) {
		panic(err)
	}
}

func initEventsMetrics(registry *prometheus.Registry) {
	for _, c := range eventsMetricVecs() {
		registerMetric(registry, c)
	}
}
