package caddy

import (
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// MetricVec is the interface that MetricsTracker expects for metric vectors.
type MetricVec interface {
	prometheus.Collector
	DeletePartialMatch(labels prometheus.Labels) int
}

// MetricsTracker stores (vector, labels) registrations during a reload.
type MetricsTracker struct {
	mu      sync.Mutex
	entries map[MetricVec]map[string]prometheus.Labels
}

func newMetricsTracker() *MetricsTracker {
	return &MetricsTracker{entries: make(map[MetricVec]map[string]prometheus.Labels)}
}

// This only exists for testing
func NewMetricsTracker() *MetricsTracker {
	return newMetricsTracker()
}

// Records the (vector, labels) pair in the tracker's internal map. The key is a
// string representation of the labels. The value is the original labels map. If 
// the pair already exists, it does nothing.
func (t *MetricsTracker) RegisterMetric(vec MetricVec, labels prometheus.Labels) {
	if t == nil || vec == nil {
		return
	}
	key := labelsKey(labels)
	t.mu.Lock()
	defer t.mu.Unlock()
	m, ok := t.entries[vec]
	if !ok {
		m = make(map[string]prometheus.Labels)
		t.entries[vec] = m
	}
	if _, exists := m[key]; !exists {
		m[key] = cloneLabels(labels)
	}
}

// Snapshot dumps tracker's contents into a ConfigSnapshot.
func (t *MetricsTracker) Snapshot() *ConfigSnapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := &ConfigSnapshot{
		Generation: generationCounter.Add(1),
		Captured:   time.Now(),
		Entries:    make(map[MetricVec][]prometheus.Labels, len(t.entries)),
	}
	for vec, set := range t.entries {
		list := make([]prometheus.Labels, 0, len(set))
		for _, labels := range set {
			list = append(list, labels)
		}
		out.Entries[vec] = list
	}
	return out
}

// ConfigSnapshot is the immutable view of reload's registered (vector, labels) pairs.
type ConfigSnapshot struct {
	Generation uint64
	Captured   time.Time
	Entries    map[MetricVec][]prometheus.Labels
}

// CurrentSnapshot returns the most recently published ConfigSnapshot or nil.
func CurrentSnapshot() *ConfigSnapshot {
	return activeSnapshot.Load()
}

// swapSnapshot replaces the active ConfigSnapshot with the given one and
// returns the prior snapshot or nil if there was none.
func swapSnapshot(snap *ConfigSnapshot) *ConfigSnapshot {
	return activeSnapshot.Swap(snap)
}

// pruneStaleLabels deletes series from each MetricVec whose (vec, labels)
// existed in prev but is absent in curr. Returns the total number deleted.
// if prev is nil, it means this is the first load and nothing will be pruned.
// if curr is nil, it means the config was wiped and all labels get deleted.
func pruneStaleLabels(prev, curr *ConfigSnapshot) int {
	if prev == nil {
		return 0
	}

	var deleted int
	for vec, prevLabels := range prev.Entries {
		var currKeys map[string]struct{}
		if curr != nil {
			if list, ok := curr.Entries[vec]; ok {
				currKeys = make(map[string]struct{}, len(list))
				for _, l := range list {
					currKeys[labelsKey(l)] = struct{}{}
				}
			}
		}

		for _, labels := range prevLabels {
			if _, alive := currKeys[labelsKey(labels)]; alive {
				continue
			}
			deleted += vec.DeletePartialMatch(labels)
		}
	}
	return deleted
}

var (
	generationCounter atomic.Uint64
	activeSnapshot    atomic.Pointer[ConfigSnapshot]
)

// labelsKey produces a stringified version of the labels map, with keys sorted 
// and separated by null bytes.
func labelsKey(labels prometheus.Labels) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(0)
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(labels[k])
	}
	return b.String()
}

// cloneLabels creates a deep copy of the given labels map.
func cloneLabels(in prometheus.Labels) prometheus.Labels {
	out := make(prometheus.Labels, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
