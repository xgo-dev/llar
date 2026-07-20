package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestCollectorsRegistered(t *testing.T) {
	RequestsTotal.WithLabelValues("test")
	RequestDuration.WithLabelValues("test")
	BuildsTotal.WithLabelValues("test")
	BuildDuration.WithLabelValues("test")
	BuildsInProgress.WithLabelValues("test")
	BuildCacheLookups.WithLabelValues("test", "hit")
	BuildLockWaiters.WithLabelValues("test")
	BuildFailures.WithLabelValues("test")

	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	registered := make(map[string]bool, len(families))
	for _, family := range families {
		registered[family.GetName()] = true
	}
	for _, name := range []string{
		"llard_requests_total",
		"llard_request_duration_seconds",
		"llard_requests_in_flight",
		"llard_builds_total",
		"llard_build_duration_seconds",
		"llard_builds_in_progress",
		"llard_build_shared_requests_total",
		"llard_build_cache_lookups_total",
		"llard_build_lock_waiters",
		"llard_build_lock_wait_duration_seconds",
		"llard_build_failures_total",
	} {
		if !registered[name] {
			t.Errorf("metric %q is not registered", name)
		}
	}
}
