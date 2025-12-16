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

package guaranteedminimum

// Config holds the configuration for the GuaranteedMinimum policy.
type Config struct {
	// MinGuaranteedRates maps priority level to minimum guaranteed share (0.0-1.0).
	// Bands not in this map have no minimum guarantee (strict priority only).
	// Example: {10: 0.05} means priority 10 gets at least 5% of dispatches.
	MinGuaranteedRates map[int]float64 `json:"minGuaranteedRates,omitempty"`
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() Config {
	return Config{
		MinGuaranteedRates: make(map[int]float64),
	}
}

// Validate checks and applies defaults to the configuration.
func (c *Config) Validate() {
	if c.MinGuaranteedRates == nil {
		c.MinGuaranteedRates = make(map[int]float64)
	}
}
