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

package creditbatchpriority

// Config holds the configuration for the CreditBatchPriority policy.
type Config struct {
	// Weights maps priority level to its dispatch weight.
	// Higher weights receive proportionally more throughput AND more consecutive dispatches.
	// When a band is selected, it receives weight credits (consecutive dispatch opportunities).
	// Bands not in this map get a default weight of 1.0.
	// Example: {10: 10.0, 0: 1.0} means priority 10 gets ~10x throughput and 10 consecutive dispatches.
	Weights map[int]float64 `json:"weights,omitempty"`
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() Config {
	return Config{
		Weights: make(map[int]float64),
	}
}

// Validate checks and applies defaults to the configuration.
func (c *Config) Validate() {
	if c.Weights == nil {
		c.Weights = make(map[int]float64)
	}
}

// getWeight returns the configured weight for a priority, or 1.0 if not configured.
func (c *Config) getWeight(priority int) float64 {
	if weight, ok := c.Weights[priority]; ok && weight > 0 {
		return weight
	}
	return 1.0
}
