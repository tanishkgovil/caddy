package encode

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

// output bytes are same as input bytes for testing purposes
type nopEncoder struct{ w io.Writer }

func (e *nopEncoder) Write(p []byte) (int, error) { return e.w.Write(p) }
func (e *nopEncoder) Close() error                { return nil }
func (e *nopEncoder) Reset(w io.Writer)           { e.w = w }
func (e *nopEncoder) Flush() error                { return nil }

func testEncode(t *testing.T) *Encode {
	t.Helper()
	ctx, cancel := caddy.NewContext(caddy.Context{Context: context.Background()})
	t.Cleanup(cancel)
	enc := &Encode{
		MinLength:   1,
		writerPools: map[string]*sync.Pool{"test": {New: func() any { return new(nopEncoder) }}},
	}
	if err := enc.Provision(ctx); err != nil {
		t.Fatalf("provision: %v", err)
	}
	return enc
}

func TestCompressionMetrics(t *testing.T) {
	enc := testEncode(t)
	body := strings.Repeat("a", 100)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "test")

	rwBefore := testutil.ToFloat64(encodeMetrics.responses.WithLabelValues("", "test"))
	inBefore := testutil.ToFloat64(encodeMetrics.uncompressedBytes.WithLabelValues("", "test"))
	outBefore := testutil.ToFloat64(encodeMetrics.compressedBytes.WithLabelValues("", "test"))

	err := enc.ServeHTTP(rec, req, caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(body))
		return nil
	}))
	if err != nil {
		t.Fatalf("ServeHTTP: %v", err)
	}

	if got := testutil.ToFloat64(encodeMetrics.responses.WithLabelValues("", "test")) - rwBefore; got != 1 {
		t.Errorf("responses delta: got %v, want 1", got)
	}
	if got := testutil.ToFloat64(encodeMetrics.uncompressedBytes.WithLabelValues("", "test")) - inBefore; got != 100 {
		t.Errorf("uncompressed bytes delta: got %v, want 100", got)
	}
	if got := testutil.ToFloat64(encodeMetrics.compressedBytes.WithLabelValues("", "test")) - outBefore; got != 100 {
		t.Errorf("compressed bytes delta (nop encoder): got %v, want 100", got)
	}
}
