package caddy

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// freshVec creates new CounterVec with given name and labels
func freshVec(name string, labels ...string) *prometheus.CounterVec {
	return prometheus.NewCounterVec(prometheus.CounterOpts{Name: name}, labels)
}

// sortedLabelKeys returns the sorted list of label keys for a given MetricVec in a snapshot.
func sortedLabelKeys(snap *ConfigSnapshot, vec MetricVec) []string {
	out := make([]string, 0)
	for _, l := range snap.Entries[vec] {
		out = append(out, labelsKey(l))
	}
	sort.Strings(out)
	return out
}

func TestRegisterMetricBasic(t *testing.T) {
	// Registering different label sets for the same vec should produce multiple entries in 
	// the snapshot.
	tracker := NewMetricsTracker()
	vec := freshVec("test_basic", "k")

	tracker.RegisterMetric(vec, prometheus.Labels{"k": "v1"})
	tracker.RegisterMetric(vec, prometheus.Labels{"k": "v2"})

	got := sortedLabelKeys(tracker.Snapshot(), vec)
	want := []string{"k=v1", "k=v2"}
	if !equalStr(got, want) {
		t.Errorf("want %v, got %v", want, got)
	}
}

func TestRegisterMetricDuplicateHandling(t *testing.T) {
	tracker := NewMetricsTracker()
	vec := freshVec("test_dedup", "k")

	// Identical labels passed three times should produce one entry.
	tracker.RegisterMetric(vec, prometheus.Labels{"k": "v"})
	tracker.RegisterMetric(vec, prometheus.Labels{"k": "v"})
	tracker.RegisterMetric(vec, prometheus.Labels{"k": "v"})

	snap := tracker.Snapshot()
	if got := len(snap.Entries[vec]); got != 1 {
		t.Errorf("dedup failed: want 1 entry, got %d", got)
	}
}

func TestRegisterMetricDuplicateDifferentOrder(t *testing.T) {
	// Two labels with the same key/value pairs but written in different orders
	tracker := NewMetricsTracker()
	vec := freshVec("test_dedup_order", "a", "b")

	tracker.RegisterMetric(vec, prometheus.Labels{"a": "1", "b": "2"})
	tracker.RegisterMetric(vec, prometheus.Labels{"b": "2", "a": "1"})

	if got := len(tracker.Snapshot().Entries[vec]); got != 1 {
		t.Errorf("want 1 deduped entry, got %d", got)
	}
}

func TestRegisterMetricNilSafe(t *testing.T) {
	// Both nil tracker and nil vec must be no-ops
	var nilTracker *MetricsTracker
	nilTracker.RegisterMetric(freshVec("nil_t", "k"), prometheus.Labels{"k": "v"})

	tracker := NewMetricsTracker()
	tracker.RegisterMetric(nil, prometheus.Labels{"k": "v"})
	if got := len(tracker.Snapshot().Entries); got != 0 {
		t.Errorf("nil vec should not produce entries, got %d", got)
	}
}

func TestSnapshotDetachedFromTracker(t *testing.T) {
	// Changing tracker after taking a snapshot should not affect the
	// snapshot's contents.
	tracker := NewMetricsTracker()
	vec := freshVec("test_detach", "k")
	tracker.RegisterMetric(vec, prometheus.Labels{"k": "before"})

	snap := tracker.Snapshot()
	before := len(snap.Entries[vec])

	tracker.RegisterMetric(vec, prometheus.Labels{"k": "after"})

	if got := len(snap.Entries[vec]); got != before {
		t.Errorf("snapshot was mutated: had %d entries, now %d", before, got)
	}
}

func TestRegisterMetricLabelsNoChange(t *testing.T) {
	// Caller changing the labels map after registering should not affect
	// the tracker's stored labels.
	tracker := NewMetricsTracker()
	vec := freshVec("test_copy", "k")
	labels := prometheus.Labels{"k": "original"}
	tracker.RegisterMetric(vec, labels)
	labels["k"] = "mutated"

	snap := tracker.Snapshot()
	if got := snap.Entries[vec][0]["k"]; got != "original" {
		t.Errorf("tracker captured mutated value: got %q", got)
	}
}

func TestPublishAndCurrentSnapshot(t *testing.T) {
	// Publishing a snapshot should make it available via CurrentSnapshot, and
	// the generation should advance.
	prevGen := generationCounter.Load()

	tracker1 := NewMetricsTracker()
	vec1 := freshVec("test_pub_a", "k")
	tracker1.RegisterMetric(vec1, prometheus.Labels{"k": "first"})
	snap1 := tracker1.Snapshot()
	swapSnapshot(snap1)

	if got := CurrentSnapshot(); got != snap1 {
		t.Fatal("CurrentSnapshot did not return the just-published snapshot")
	}
	if snap1.Generation <= prevGen {
		t.Errorf("generation did not advance: prev %d, got %d", prevGen, snap1.Generation)
	}

	tracker2 := NewMetricsTracker()
	vec2 := freshVec("test_pub_b", "k")
	tracker2.RegisterMetric(vec2, prometheus.Labels{"k": "second"})
	snap2 := tracker2.Snapshot()
	swapSnapshot(snap2)

	if snap2.Generation <= snap1.Generation {
		t.Errorf("generation did not advance across reloads")
	}
	if CurrentSnapshot() != snap2 {
		t.Error("CurrentSnapshot did not advance to snap2")
	}
	if _, ok := snap1.Entries[vec1]; !ok {
		t.Error("snap1 contents lost after second publish")
	}
}

func TestMultipleModulesRegister(t *testing.T) {
	// Multiple modules registering metrics should all have their labels tracked.
	tracker := NewMetricsTracker()

	httpReqs := freshVec("multi_http_req", "server", "handler")
	rpHealth := freshVec("multi_rp_health", "upstream")
	tlsIssuer := freshVec("multi_tls_issuer", "issuer")

	// Simulating different modules' Provision() calls.
	tracker.RegisterMetric(httpReqs, prometheus.Labels{"server": "srv0", "handler": "file_server"})
	tracker.RegisterMetric(httpReqs, prometheus.Labels{"server": "srv0", "handler": "reverse_proxy"})
	tracker.RegisterMetric(rpHealth, prometheus.Labels{"upstream": "backend1:80"})
	tracker.RegisterMetric(rpHealth, prometheus.Labels{"upstream": "backend2:80"})
	tracker.RegisterMetric(tlsIssuer, prometheus.Labels{"issuer": "acme"})

	snap := tracker.Snapshot()
	if got := len(snap.Entries); got != 3 {
		t.Fatalf("want 3 vecs tracked, got %d", got)
	}
	if got := len(snap.Entries[httpReqs]); got != 2 {
		t.Errorf("httpReqs: want 2 entries, got %d", got)
	}
	if got := len(snap.Entries[rpHealth]); got != 2 {
		t.Errorf("rpHealth: want 2 entries, got %d", got)
	}
	if got := len(snap.Entries[tlsIssuer]); got != 1 {
		t.Errorf("tlsIssuer: want 1 entry, got %d", got)
	}
}

func TestReloadCapturesNewState(t *testing.T) {
	// This test simulates two reloads, each with different metric registrations,
	// and confirms that the second reload's snapshot reflects the new state, not the old.
	vec := freshVec("reload_capture", "server", "handler")

	// Reload 1: register two entries
	tracker := NewMetricsTracker()
	tracker.RegisterMetric(vec, prometheus.Labels{"server": "srv0", "handler": "file_server"})
	tracker.RegisterMetric(vec, prometheus.Labels{"server": "srv0", "handler": "reverse_proxy"})
	snap1 := tracker.Snapshot()

	// Reload 2: fresh tracker, different registrations
	tracker = NewMetricsTracker()
	tracker.RegisterMetric(vec, prometheus.Labels{"server": "srv0", "handler": "static_response"})
	tracker.RegisterMetric(vec, prometheus.Labels{"server": "srv1", "handler": "file_server"})
	snap2 := tracker.Snapshot()

	got1 := sortedLabelKeys(snap1, vec)
	want1 := []string{
		"handler=file_server\x00server=srv0",
		"handler=reverse_proxy\x00server=srv0",
	}
	if !equalStr(got1, want1) {
		t.Errorf("snap1: want %v, got %v", want1, got1)
	}

	got2 := sortedLabelKeys(snap2, vec)
	want2 := []string{
		"handler=file_server\x00server=srv1",
		"handler=static_response\x00server=srv0",
	}
	if !equalStr(got2, want2) {
		t.Errorf("snap2: want %v, got %v", want2, got2)
	}
}

func TestConcurrentRegistration(t *testing.T) {
	// Concurrent registrations should not cause lost entries
	tracker := NewMetricsTracker()
	vec := freshVec("concurrent", "k")

	const goroutines = 64
	const perGoroutine = 100

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				tracker.RegisterMetric(vec, prometheus.Labels{
					"k": fmt.Sprintf("g%d-i%d", id, i),
				})
			}
		}(g)
	}
	wg.Wait()

	snap := tracker.Snapshot()
	if got := len(snap.Entries[vec]); got != goroutines*perGoroutine {
		t.Errorf("want %d unique entries, got %d", goroutines*perGoroutine, got)
	}
}

func TestConcurrentRegisterAndSnapshot(t *testing.T) {
	// Concurrent registration and snapshotting should not cause lost entries.
	// Checks for race conditions
	tracker := NewMetricsTracker()
	vec := freshVec("concurrent_snap", "k")

	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			tracker.RegisterMetric(vec, prometheus.Labels{"k": fmt.Sprintf("i%d", i)})
		}
		close(done)
	}()

	for {
		select {
		case <-done:
			return
		default:
			_ = tracker.Snapshot()
		}
	}
}

func TestPluginPerspectiveViaContext(t *testing.T) {
	// Confirms that a plugin can register metrics via the Context's MetricsTracker,
	// and that those registrations are captured in the snapshot.
	ctx, cancel := NewContext(Context{Context: context.Background()})
	defer cancel()

	if ctx.MetricsTracker() == nil {
		t.Fatal("Context did not surface a MetricsTracker")
	}

	pluginVec := freshVec("plugin_perspective", "backend", "pool")

	ctx.MetricsTracker().RegisterMetric(pluginVec, prometheus.Labels{
		"backend": "alpha", "pool": "main",
	})
	ctx.MetricsTracker().RegisterMetric(pluginVec, prometheus.Labels{
		"backend": "beta", "pool": "main",
	})

	snap := ctx.MetricsTracker().Snapshot()
	if got := len(snap.Entries[pluginVec]); got != 2 {
		t.Errorf("plugin labels not tracked via Context; got %d entries", got)
	}
}

func TestTwoContextsHaveSeparateTrackers(t *testing.T) {
	// Confirms that two separate Contexts have separate MetricsTrackers, and that
	// registrations on one do not affect the other.

	c1, cancel1 := NewContext(Context{Context: context.Background()})
	defer cancel1()
	c2, cancel2 := NewContext(Context{Context: context.Background()})
	defer cancel2()

	if c1.MetricsTracker() == c2.MetricsTracker() {
		t.Fatal("expected separate trackers per Context")
	}

	vec := freshVec("ctx_isolation", "k")
	c1.MetricsTracker().RegisterMetric(vec, prometheus.Labels{"k": "v"})

	if got := len(c2.MetricsTracker().Snapshot().Entries); got != 0 {
		t.Errorf("registration leaked across Contexts; c2 has %d entries", got)
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

// snapshotOf creates a ConfigSnapshot with the given entries.
// Avoids going through the MetricsTracker for testing purposes.
func snapshotOf(entries map[MetricVec][]prometheus.Labels) *ConfigSnapshot {
	return &ConfigSnapshot{Entries: entries}
}

// seriesCount returns the number of series registered in the Collector
func seriesCount(t *testing.T, vec prometheus.Collector) int {
	t.Helper()
	return testutil.CollectAndCount(vec)
}

func TestPruneStaleLabelsBasic(t *testing.T) {
	// Test basic case where some labels are in prev and not curr,
	// and those labels are deleted from the vec.
	vec := freshVec("prune_basic", "k")

	vec.With(prometheus.Labels{"k": "keep"}).Inc()
	vec.With(prometheus.Labels{"k": "drop1"}).Inc()
	vec.With(prometheus.Labels{"k": "drop2"}).Inc()
	if got := seriesCount(t, vec); got != 3 {
		t.Fatalf("setup: want 3 series, got %d", got)
	}

	prev := snapshotOf(map[MetricVec][]prometheus.Labels{
		vec: {
			{"k": "keep"},
			{"k": "drop1"},
			{"k": "drop2"},
		},
	})
	curr := snapshotOf(map[MetricVec][]prometheus.Labels{
		vec: {
			{"k": "keep"},
			{"k": "newly_added"},
		},
	})

	deleted := pruneStaleLabels(prev, curr)
	if deleted != 2 {
		t.Errorf("want 2 series deleted, got %d", deleted)
	}
	if got := seriesCount(t, vec); got != 1 {
		t.Errorf("after prune: want 1 series remaining, got %d", got)
	}
	// Confirm that the kept series still contains its incremented value.
	if got := testutil.ToFloat64(vec.With(prometheus.Labels{"k": "keep"})); got != 1 {
		t.Errorf("kept series lost its value: got %v", got)
	}
}

func TestPruneStaleLabelsVecDisappears(t *testing.T) {
	// Test case where entire vec is missing from curr, so all
	// its labels are deleted.
	vec := freshVec("prune_disappear", "k")
	vec.With(prometheus.Labels{"k": "a"}).Inc()
	vec.With(prometheus.Labels{"k": "b"}).Inc()
	vec.With(prometheus.Labels{"k": "c"}).Inc()

	prev := snapshotOf(map[MetricVec][]prometheus.Labels{
		vec: {
			{"k": "a"},
			{"k": "b"},
			{"k": "c"},
		},
	})
	curr := snapshotOf(map[MetricVec][]prometheus.Labels{}) // V absent

	deleted := pruneStaleLabels(prev, curr)
	if deleted != 3 {
		t.Errorf("want 3 series swept, got %d", deleted)
	}
	if got := seriesCount(t, vec); got != 0 {
		t.Errorf("want vec emptied, got %d series", got)
	}
}

func TestPruneStaleLabelsNilPrev(t *testing.T) {
	// Test first reload case. Nothing should be deleted
	vec := freshVec("prune_nil_prev", "k")
	curr := snapshotOf(map[MetricVec][]prometheus.Labels{
		vec: {{"k": "v"}},
	})
	if got := pruneStaleLabels(nil, curr); got != 0 {
		t.Errorf("want 0 deletions for nil prev, got %d", got)
	}
}

func TestPruneStaleLabelsNilCurrSweepsAll(t *testing.T) {
	// Test case where config is wiped. Every label should be deleted.
	v1 := freshVec("prune_nil_curr_a", "k")
	v2 := freshVec("prune_nil_curr_b", "k")
	v1.With(prometheus.Labels{"k": "x"}).Inc()
	v1.With(prometheus.Labels{"k": "y"}).Inc()
	v2.With(prometheus.Labels{"k": "z"}).Inc()

	prev := snapshotOf(map[MetricVec][]prometheus.Labels{
		v1: {{"k": "x"}, {"k": "y"}},
		v2: {{"k": "z"}},
	})

	if got := pruneStaleLabels(prev, nil); got != 3 {
		t.Errorf("want 3 deletions when curr is nil, got %d", got)
	}
	if c1, c2 := seriesCount(t, v1), seriesCount(t, v2); c1 != 0 || c2 != 0 {
		t.Errorf("want both vecs emptied, got v1=%d v2=%d", c1, c2)
	}
}

func TestPruneStaleLabelsCountReturn(t *testing.T) {
	// Test that return value of pruneStaleLabels is correct across multiple vecs
	v1 := freshVec("prune_count_a", "k")
	v2 := freshVec("prune_count_b", "k")
	v1.With(prometheus.Labels{"k": "live"}).Inc()
	v1.With(prometheus.Labels{"k": "stale_1"}).Inc()
	v1.With(prometheus.Labels{"k": "stale_2"}).Inc()
	v2.With(prometheus.Labels{"k": "stale_3"}).Inc()

	prev := snapshotOf(map[MetricVec][]prometheus.Labels{
		v1: {{"k": "live"}, {"k": "stale_1"}, {"k": "stale_2"}},
		v2: {{"k": "stale_3"}},
	})
	curr := snapshotOf(map[MetricVec][]prometheus.Labels{
		v1: {{"k": "live"}},
		// v2 missing — its lone label is stale
	})

	if got := pruneStaleLabels(prev, curr); got != 3 {
		t.Errorf("want 3 deletions across vecs, got %d", got)
	}
}

func TestPruneStaleLabelsIdenticalReload(t *testing.T) {
	// Test case where prev and curr are identical, so no labels should be deleted.
	vec := freshVec("prune_identical", "k")
	vec.With(prometheus.Labels{"k": "a"}).Inc()
	vec.With(prometheus.Labels{"k": "b"}).Inc()

	prev := snapshotOf(map[MetricVec][]prometheus.Labels{
		vec: {{"k": "a"}, {"k": "b"}},
	})
	curr := snapshotOf(map[MetricVec][]prometheus.Labels{
		vec: {{"k": "a"}, {"k": "b"}},
	})

	if got := pruneStaleLabels(prev, curr); got != 0 {
		t.Errorf("want 0 deletions for identical reload, got %d", got)
	}
	if got := seriesCount(t, vec); got != 2 {
		t.Errorf("want both series retained, got %d", got)
	}
}

func TestPruneStaleLabelsKeyOrderIndependent(t *testing.T) {
	// Test case where labels are written in different orders in prev and curr,
	// but they should be matched as the same series.
	vec := freshVec("prune_order", "a", "b")
	vec.With(prometheus.Labels{"a": "1", "b": "2"}).Inc()

	prev := snapshotOf(map[MetricVec][]prometheus.Labels{
		vec: {{"a": "1", "b": "2"}},
	})
	
	curr := snapshotOf(map[MetricVec][]prometheus.Labels{
		vec: {{"b": "2", "a": "1"}},
	})

	if got := pruneStaleLabels(prev, curr); got != 0 {
		t.Errorf("want 0 deletions for label-order difference, got %d", got)
	}
	if got := seriesCount(t, vec); got != 1 {
		t.Errorf("want series retained, got %d", got)
	}
}

func TestSwapSnapshotReturnsPrev(t *testing.T) {
	// Confirm that swapSnapshot returns the previous snapshot,
	// and that CurrentSnapshot reflects the latest swap.
	first := &ConfigSnapshot{Generation: generationCounter.Add(1)}
	swapSnapshot(first)

	second := &ConfigSnapshot{Generation: generationCounter.Add(1)}
	got := swapSnapshot(second)
	if got != first {
		t.Errorf("second swap: want prev to be first snapshot, got %p (first=%p)", got, first)
	}

	third := &ConfigSnapshot{Generation: generationCounter.Add(1)}
	got = swapSnapshot(third)
	if got != second {
		t.Errorf("third swap: want prev to be second snapshot, got %p (second=%p)", got, second)
	}

	if active := CurrentSnapshot(); active != third {
		t.Errorf("CurrentSnapshot after swap: want third, got %p", active)
	}
}
