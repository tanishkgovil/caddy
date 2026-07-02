package caddy

// CustomMetricLabeler is implemented by modules that declare custom metric labels.
type CustomMetricLabeler interface {
	// MetricLabelKeys returns the custom label keys that this module declares.
	MetricLabelKeys() []string

	// RegisterMetrics registers the module's metrics with the given union of custom keys
	RegisterMetrics(union []string, ctx Context)
}
