// Copyright (c) 2018 Zededa, Inc.
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"time"
)

// MetricsMap maps from an ifname string to some metrics
type MetricsMap map[string]ZedcloudMetric

// ZedcloudMetric are metrics for one interface
type ZedcloudMetric struct {
	FailureCount  uint64
	SuccessCount  uint64
	LastFailure   time.Time
	LastSuccess   time.Time
	URLCounters   map[string]UrlcloudMetrics
	AuthFailCount uint64
}

// UrlcloudMetrics are metrics for a particular URL
type UrlcloudMetrics struct {
	TryMsgCount   int64
	TryByteCount  int64
	SentMsgCount  int64
	SentByteCount int64
	RecvMsgCount  int64
	RecvByteCount int64 // Based on content-length which could be off
}
