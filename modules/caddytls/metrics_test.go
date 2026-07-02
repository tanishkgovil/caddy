package caddytls

import (
	"context"
	"crypto/x509"
	"testing"

	"github.com/caddyserver/certmagic"
	"github.com/mholt/acmez/v3/acme"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// Issuer that returns a fixed cert/error
type fakeIssuer struct {
	key  string
	cert *certmagic.IssuedCertificate
	err  error
}

func (i fakeIssuer) IssuerKey() string { return i.key }
func (i fakeIssuer) Issue(context.Context, *x509.CertificateRequest) (*certmagic.IssuedCertificate, error) {
	return i.cert, i.err
}

// Ensures the cert metrics are updated from real certmagic events
func TestObserveCertEvents(t *testing.T) {
	// helper that tries to obtain a cert
	obtain := func(iss certmagic.Issuer, name string) {
		var cfg *certmagic.Config
		cache := certmagic.NewCache(certmagic.CacheOptions{
			GetConfigForCert: func(certmagic.Certificate) (*certmagic.Config, error) { return cfg, nil },
		})
		t.Cleanup(cache.Stop)
		cfg = certmagic.New(cache, certmagic.Config{
			Storage: &certmagic.FileStorage{Path: t.TempDir()},
			Issuers: []certmagic.Issuer{iss},
			OnEvent: func(_ context.Context, name string, data map[string]any) error {
				observeCertEvent(name, data)
				return nil
			},
		})
		cfg.ObtainCertSync(context.Background(), name)
	}

	obtain(fakeIssuer{key: "test-a", err: acme.Problem{Type: acme.ProblemTypeRateLimited}}, "fail.example.com")
	obtain(fakeIssuer{key: "test-b", cert: &certmagic.IssuedCertificate{Certificate: []byte("dummy")}}, "ok.example.com")

	if got := testutil.ToFloat64(tlsMetrics.certObtaining.WithLabelValues("false")); got != 2 {
		t.Errorf("cert_obtaining: got %v, want 2", got)
	}
	if got := testutil.ToFloat64(tlsMetrics.certFailed.WithLabelValues("test-a", "rateLimited", "false")); got != 1 {
		t.Errorf("cert_failed: got %v, want 1", got)
	}
	if got := testutil.ToFloat64(tlsMetrics.certObtained.WithLabelValues("test-b", "false")); got != 1 {
		t.Errorf("cert_obtained: got %v, want 1", got)
	}
}
