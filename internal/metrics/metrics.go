// Copyright (c) 2026 The XGo Authors (xgo.dev). All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	requestDurationBuckets = []float64{0.1, 0.5, 1, 2, 5, 10, 30, 60, 120, 300, 600, 1800, 3600}
	lockWaitBuckets        = []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5, 10, 30, 60, 300}

	RequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "llard",
		Name:      "requests_total",
		Help:      "Total number of artifact requests by outcome.",
	}, []string{"outcome"})
	RequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "llard",
		Name:      "request_duration_seconds",
		Help:      "Artifact request duration in seconds by outcome.",
		Buckets:   requestDurationBuckets,
	}, []string{"outcome"})
	RequestsInFlight = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "llard",
		Name:      "requests_in_flight",
		Help:      "Current number of artifact requests being served.",
	})
	BuildsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "llard",
		Name:      "builds_total",
		Help:      "Total number of actual builds after request coalescing by outcome.",
	}, []string{"outcome"})
	BuildDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "llard",
		Name:      "build_duration_seconds",
		Help:      "Actual build duration in seconds by outcome.",
		Buckets:   requestDurationBuckets,
	}, []string{"outcome"})
	BuildsInProgress = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "llard",
		Name:      "builds_in_progress",
		Help:      "Current number of actual builds by target.",
	}, []string{"target"})
	BuildSharedRequests = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "llard",
		Name:      "build_shared_requests_total",
		Help:      "Total number of requests coalesced into an existing build.",
	})
	BuildCacheLookups = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "llard",
		Name:      "build_cache_lookups_total",
		Help:      "Total number of build cache lookups by target and result.",
	}, []string{"target", "result"})
	BuildLockWaiters = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "llard",
		Name:      "build_lock_waiters",
		Help:      "Current number of builds waiting to acquire a module lock by target.",
	}, []string{"target"})
	BuildLockWaitDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Namespace: "llard",
		Name:      "build_lock_wait_duration_seconds",
		Help:      "Build lock acquisition duration in seconds.",
		Buckets:   lockWaitBuckets,
	})
	BuildFailures = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "llard",
		Name:      "build_failures_total",
		Help:      "Total number of build pipeline failures by stage.",
	}, []string{"stage"})
)
