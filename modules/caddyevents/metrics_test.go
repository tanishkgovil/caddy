package caddyevents

import (
	"context"
	"errors"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/caddyserver/caddy/v2"
)

// Implements caddy.EventHandler for testing
type handlerFunc func(context.Context, caddy.Event) error

func (f handlerFunc) Handle(ctx context.Context, e caddy.Event) error { return f(ctx, e) }

// Ensures the events metrics are updated from real event dispatch.
func TestEventsMetrics(t *testing.T) {
	ctx, cancel := caddy.NewContext(caddy.Context{Context: context.Background()})
	t.Cleanup(cancel)

	app := &App{}
	if err := app.Provision(ctx); err != nil {
		t.Fatalf("provision: %v", err)
	}

	// two handlers, one errors and one aborts
	app.On("test_event", handlerFunc(func(context.Context, caddy.Event) error {
		return errors.New("boom")
	}))
	app.On("test_event", handlerFunc(func(context.Context, caddy.Event) error {
		return caddy.ErrEventAborted
	}))
	if err := app.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	app.Emit(ctx, "test_event", nil)

	if got := testutil.ToFloat64(eventsMetrics.emitted.WithLabelValues("test_event", "caddy")); got != 1 {
		t.Errorf("emitted: got %v, want 1", got)
	}
	if got := testutil.ToFloat64(eventsMetrics.handlerErrors.WithLabelValues("test_event", "caddy")); got != 1 {
		t.Errorf("handler_errors: got %v, want 1", got)
	}
	if got := testutil.ToFloat64(eventsMetrics.aborted.WithLabelValues("test_event", "caddy")); got != 1 {
		t.Errorf("aborted: got %v, want 1", got)
	}
	if got := testutil.CollectAndCount(eventsMetrics.dispatchDuration); got != 1 {
		t.Errorf("dispatch_duration series: got %v, want 1", got)
	}
}
