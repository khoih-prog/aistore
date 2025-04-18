// Package stats provides various structs for collecting stats
/*
 * Copyright (c) 2018-2025, NVIDIA CORPORATION. All rights reserved.
 */
package stats

import (
	"math"
	"time"
)

// HTTPReq is used for keeping track of http requests stats including number of ops, latency, throughput, etc.
// Assume single threaded access, it doesn't provide any locking on updates.
type HTTPReq struct {
	start   time.Time     // time current stats started
	cnt     int64         // total # of requests
	bytes   int64         // total bytes by all requests
	errs    int64         // number of failed requests
	latency time.Duration // Accumulated request latency

	// self maintained fields
	minLatency time.Duration
	maxLatency time.Duration
}

// NewHTTPReq returns a new stats object with given time as the starting point
func NewHTTPReq(t time.Time) HTTPReq {
	return HTTPReq{
		start:      t,
		minLatency: time.Duration(math.MaxInt64),
	}
}

// Add adds a request's result to the stats
func (s *HTTPReq) Add(size int64, delta time.Duration) {
	s.cnt++
	s.bytes += size
	s.latency += delta
	s.minLatency = min(s.minLatency, delta)
	s.maxLatency = max(s.maxLatency, delta)
}

// AddErr increases the number of failed count by 1
func (s *HTTPReq) AddErr() {
	s.errs++
}

// Total returns the total number of requests.
func (s *HTTPReq) Total() int64 {
	return s.cnt
}

// TotalBytes returns the total number of bytes by all requests.
func (s *HTTPReq) TotalBytes() int64 {
	return s.bytes
}

// MinLatency returns the minimal latency in nano second.
func (s *HTTPReq) MinLatency() int64 {
	if s.cnt == 0 {
		return 0
	}
	return int64(s.minLatency)
}

// MaxLatency returns the maximum latency in nano second.
func (s *HTTPReq) MaxLatency() int64 {
	if s.cnt == 0 {
		return 0
	}
	return int64(s.maxLatency)
}

// AvgLatency returns the avg latency in nano second.
func (s *HTTPReq) AvgLatency() int64 {
	if s.cnt == 0 {
		return 0
	}
	return int64(s.latency) / s.cnt
}

// Throughput returns throughput of requests (bytes/per second).
func (s *HTTPReq) Throughput(start, end time.Time) int64 {
	if start == end { //nolint:staticcheck // "Two times can be equal even if they are in different locations."
		return 0
	}
	return int64(float64(s.bytes) / end.Sub(start).Seconds())
}

// Start returns the start time of the stats.
func (s *HTTPReq) Start() time.Time {
	return s.start
}

// TotalErrs returns the total number of failed requests.
func (s *HTTPReq) TotalErrs() int64 {
	return s.errs
}

// Aggregate adds another stats to self
func (s *HTTPReq) Aggregate(other HTTPReq) {
	s.cnt += other.cnt
	s.bytes += other.bytes
	s.errs += other.errs
	s.latency += other.latency

	s.minLatency = min(s.minLatency, other.minLatency)
	s.maxLatency = max(s.maxLatency, other.maxLatency)
}
