package caddytls

import (
	"crypto/x509"
	"errors"
	"strconv"
	"strings"

	"github.com/mholt/acmez/v3/acme"
	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/crypto/ocsp"

	"github.com/caddyserver/caddy/v2"
)

var tlsMetrics = struct {
	handshakes      *prometheus.CounterVec
	certObtaining   *prometheus.CounterVec
	certObtained    *prometheus.CounterVec
	certFailed      *prometheus.CounterVec
	certOCSPRevoked *prometheus.CounterVec
}{
	handshakes: prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "caddy",
		Subsystem: "tls",
		Name:      "handshakes_total",
		Help:      "Counter of completed TLS handshakes from clients, by resumption status.",
	}, []string{"resumed"}),
	certObtaining: prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "caddy",
		Subsystem: "tls",
		Name:      "cert_obtaining_total",
		Help:      "Number of certificate obtain attempts started, by whether it was a renewal.",
	}, []string{"renewal"}),
	certObtained: prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "caddy",
		Subsystem: "tls",
		Name:      "cert_obtained_total",
		Help:      "Number of certificates successfully obtained, by issuer and whether it was a renewal.",
	}, []string{"issuer", "renewal"}),
	certFailed: prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "caddy",
		Subsystem: "tls",
		Name:      "cert_failed_total",
		Help:      "Number of certificates unsuccessfully obtained, by issuer, error type, and whether it was a renewal.",
	}, []string{"issuer", "error_type", "renewal"}),
	certOCSPRevoked: prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "caddy",
		Subsystem: "tls",
		Name:      "cert_ocsp_revoked_total",
		Help:      "Number of managed certificates revoked via OCSP, by reason.",
	}, []string{"reason"}),
}

// tlsMetricVecs returns the metric vectors for the TLS app.
func tlsMetricVecs() []caddy.MetricVec {
	return []caddy.MetricVec{
		tlsMetrics.handshakes,
		tlsMetrics.certObtaining,
		tlsMetrics.certObtained,
		tlsMetrics.certFailed,
		tlsMetrics.certOCSPRevoked,
	}
}

// registerMetric registers c, ignoring the error if it is already registered.
func registerMetric(registry *prometheus.Registry, c prometheus.Collector) {
	if err := registry.Register(c); err != nil &&
		!errors.Is(err, prometheus.AlreadyRegisteredError{ExistingCollector: c, NewCollector: c}) {
		panic(err)
	}
}

func initTLSMetrics(registry *prometheus.Registry) {
	for _, c := range tlsMetricVecs() {
		registerMetric(registry, c)
	}
	registerMetric(registry, certExpiry)
}

// certExpiryCollector reports expiry, start time, and cache size of certs
type certExpiryCollector struct {
	notAfter  *prometheus.Desc
	notBefore *prometheus.Desc
	cacheSize *prometheus.Desc
}

var certExpiry = certExpiryCollector{
	notAfter: prometheus.NewDesc(
		"caddy_tls_cert_not_after_timestamp_seconds",
		"Expiry (NotAfter) of each cached certificate as a Unix timestamp, by subject name.",
		[]string{"identifier"}, nil,
	),
	notBefore: prometheus.NewDesc(
		"caddy_tls_cert_not_before_timestamp_seconds",
		"Validity start (NotBefore) of each cached certificate as a Unix timestamp, by subject name.",
		[]string{"identifier"}, nil,
	),
	cacheSize: prometheus.NewDesc(
		"caddy_tls_cert_cache_size",
		"Number of certificates in the cache, by subject name.",
		[]string{"identifier"}, nil,
	),
}

func (c certExpiryCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.notAfter
	ch <- c.notBefore
	ch <- c.cacheSize
}

func (c certExpiryCollector) Collect(ch chan<- prometheus.Metric) {
	certCacheMu.RLock()
	cache := certCache
	certCacheMu.RUnlock()
	if cache == nil {
		return
	}
	// Store latest cert and the number of certs per name.
	type nameStat struct {
		latest *x509.Certificate
		count  int
	}
	stats := make(map[string]nameStat)
	for _, cert := range cache.AllCerts() {
		leaf := cert.Leaf
		if leaf == nil {
			continue
		}
		for _, name := range cert.Names {
			s := stats[name]
			s.count++
			if s.latest == nil || leaf.NotAfter.After(s.latest.NotAfter) {
				s.latest = leaf
			}
			stats[name] = s
		}
	}
	for name, s := range stats {
		ch <- prometheus.MustNewConstMetric(c.notAfter, prometheus.GaugeValue, float64(s.latest.NotAfter.Unix()), name)
		ch <- prometheus.MustNewConstMetric(c.notBefore, prometheus.GaugeValue, float64(s.latest.NotBefore.Unix()), name)
		ch <- prometheus.MustNewConstMetric(c.cacheSize, prometheus.GaugeValue, float64(s.count), name)
	}
}

// observeCertEvent increments the appropriate metric for a certificate event.
func observeCertEvent(eventName string, data map[string]any) {
	renewal := strconv.FormatBool(data["renewal"] == true)
	switch eventName {
	case "cert_obtaining":
		tlsMetrics.certObtaining.With(prometheus.Labels{"renewal": renewal}).Inc()
	case "cert_obtained":
		issuer, _ := data["issuer"].(string)
		tlsMetrics.certObtained.With(prometheus.Labels{"issuer": issuer, "renewal": renewal}).Inc()
	case "cert_failed":
		err, _ := data["error"].(error)
		errType := certErrorType(err)
		issuers, _ := data["issuers"].([]string)
		for _, issuer := range issuers {
			tlsMetrics.certFailed.With(prometheus.Labels{"issuer": issuer, "error_type": errType, "renewal": renewal}).Inc()
		}
	case "cert_ocsp_revoked":
		reason, _ := data["reason"].(int)
		tlsMetrics.certOCSPRevoked.With(prometheus.Labels{"reason": ocspRevocationReason(reason)}).Inc()
	}
}

// ocspRevocationReason maps revocation reason code to label string.
func ocspRevocationReason(reason int) string {
	switch reason {
	case ocsp.Unspecified:
		return "unspecified"
	case ocsp.KeyCompromise:
		return "key_compromise"
	case ocsp.CACompromise:
		return "ca_compromise"
	case ocsp.AffiliationChanged:
		return "affiliation_changed"
	case ocsp.Superseded:
		return "superseded"
	case ocsp.CessationOfOperation:
		return "cessation_of_operation"
	case ocsp.CertificateHold:
		return "certificate_hold"
	case ocsp.RemoveFromCRL:
		return "remove_from_crl"
	case ocsp.PrivilegeWithdrawn:
		return "privilege_withdrawn"
	case ocsp.AACompromise:
		return "aa_compromise"
	default:
		return "unspecified"
	}
}

// certErrorType classifies the error type for metrics labels.
func certErrorType(err error) string {
	var problem acme.Problem
	if errors.As(err, &problem) && strings.HasPrefix(problem.Type, acme.ProblemTypeNamespace) {
		return strings.TrimPrefix(problem.Type, acme.ProblemTypeNamespace)
	}
	return "other"
}
