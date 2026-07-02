package caddy

import (
	"context"
	"testing"

	"github.com/caddyserver/certmagic"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// Ensures storage operations through the metered wrapper are counted.
func TestMeteredStorage(t *testing.T) {
	rawFS := &certmagic.FileStorage{Path: t.TempDir()}
	s := newMeteredStorage(rawFS)

	// ensure Trylocker is still implemented
	if _, ok := s.(certmagic.TryLocker); !ok {
		t.Fatal("wrapper doesn't implement TryLocker")
	}

	before := testutil.ToFloat64(storageMetrics.ops.WithLabelValues("store", "success"))
	if err := s.Store(context.Background(), "test", []byte("value")); err != nil {
		t.Fatalf("store: %v", err)
	}
	if got := testutil.ToFloat64(storageMetrics.ops.WithLabelValues("store", "success")) - before; got != 1 {
		t.Errorf("store ops delta: got %v, want 1", got)
	}
}
