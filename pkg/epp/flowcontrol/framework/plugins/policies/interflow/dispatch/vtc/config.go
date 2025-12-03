/*
Copyright 2025 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package vtc

import "time"

// Config holds the configuration for the VTC (Virtual Token Counter) policy
type Config struct {
	// InputTokenWeight is the weight applied to input tokens (wp in the VTC algorithm)
	// Default: 1.0
	InputTokenWeight float64

	// OutputTokenWeight is the weight applied to output tokens (wq in the VTC algorithm)
	// Default: 1.0
	OutputTokenWeight float64

	// CounterCleanupInterval is how often to run the cleanup of stale counters
	// Default: 5 minutes (aligned with defaultFlowGCTimeout)
	CounterCleanupInterval time.Duration

	// CounterTTL is how long a counter can remain unused before being cleaned up
	// Default: 10 minutes (2x the flow GC timeout to ensure flows are cleaned first)
	CounterTTL time.Duration
}

// DefaultConfig returns a Config with sensible defaults
func DefaultConfig() Config {
	return Config{
		InputTokenWeight:       1.0,
		OutputTokenWeight:      1.0,
		CounterCleanupInterval: 5 * time.Minute,
		CounterTTL:             10 * time.Minute,
	}
}
