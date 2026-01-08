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

package boundedstrictpriority

// Config holds the configuration for the BoundedStrictPriority policy.
type Config struct {
	// MinShare maps priority level to its minimum guaranteed share of dispatches.
	// Values are fractions (0.0 to 1.0). E.g., 0.2 means "at least 20%".
	// When a priority is below its floor and has work, it gets elevated.
	MinShare map[int]float64 `json:"minShare,omitempty"`

	// MaxShare maps priority level to its maximum allowed share of dispatches.
	// Values are fractions (0.0 to 1.0). E.g., 0.8 means "at most 80%".
	// When a priority hits its ceiling, lower priorities get a turn.
	MaxShare map[int]float64 `json:"maxShare,omitempty"`

	// ColdStartThreshold is the minimum total tokens before bounds are enforced.
	// Below this, pure strict priority is used to avoid noisy percentages.
	ColdStartThreshold float64 `json:"coldStartThreshold,omitempty"`
}

// DefaultConfig returns a Config with sensible defaults.
// Band 10 (high priority) gets max 80%, band 0 (low priority) gets min 20%.
func DefaultConfig() Config {
	return Config{
		MinShare: map[int]float64{
			0: 0.2, // low priority guaranteed at least 20%
		},
		MaxShare: map[int]float64{
			10: 0.8, // high priority capped at 80%
		},
		ColdStartThreshold: 1000,
	}
}

// Validate checks and applies defaults to the configuration.
func (c *Config) Validate() {
	if c.MinShare == nil {
		c.MinShare = make(map[int]float64)
	}
	if c.MaxShare == nil {
		c.MaxShare = make(map[int]float64)
	}
	if c.ColdStartThreshold <= 0 {
		c.ColdStartThreshold = 1000
	}
}

// getMinShare returns the configured min share for a priority, or 0 if not configured.
func (c *Config) getMinShare(priority int) float64 {
	if share, ok := c.MinShare[priority]; ok && share > 0 && share <= 1 {
		return share
	}
	return 0
}

// getMaxShare returns the configured max share for a priority, or 1.0 if not configured.
func (c *Config) getMaxShare(priority int) float64 {
	if share, ok := c.MaxShare[priority]; ok && share > 0 && share <= 1 {
		return share
	}
	return 1.0
}
