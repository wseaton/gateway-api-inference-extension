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

// Package weightedpriority provides a `framework.InterPriorityDispatchPolicy` that implements
// weighted fair queuing across priority bands using VTC-style cumulative counters.
package weightedpriority

import (
	"encoding/json"
	"math"
	"sync"

	ctrl "sigs.k8s.io/controller-runtime"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/flowcontrol/framework"
	dispatch "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/flowcontrol/framework/plugins/policies/interpriority/dispatch"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/plugins"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"
)

// WeightedPriorityPolicyName is the name of the WeightedPriority policy implementation.
const WeightedPriorityPolicyName dispatch.RegisteredPolicyName = "WeightedPriority"

var logger = ctrl.Log.WithName("weighted-priority-policy")

func init() {
	dispatch.MustRegisterPolicy(WeightedPriorityPolicyName, func() (framework.InterPriorityDispatchPolicy, error) {
		return newWeightedPriority(DefaultConfig(), string(WeightedPriorityPolicyName)), nil
	})
	plugins.Register(string(WeightedPriorityPolicyName), newWeightedPriorityFactory)
}

// newWeightedPriorityFactory is the factory function for the WeightedPriority policy.
func newWeightedPriorityFactory(name string, rawConfig json.RawMessage, _ plugins.Handle) (plugins.Plugin, error) {
	config := DefaultConfig()
	if len(rawConfig) > 0 {
		if err := json.Unmarshal(rawConfig, &config); err != nil {
			return nil, err
		}
	}
	config.Validate()
	return newWeightedPriority(config, name), nil
}

// weightedPriority implements `framework.InterPriorityDispatchPolicy` using weighted fair queuing.
// Bands receive throughput proportional to their configured weight using VTC-style counters.
type weightedPriority struct {
	typedName plugins.TypedName
	config    Config

	mu       sync.Mutex
	counters map[int]float64 // cumulative tokens per priority
}

func newWeightedPriority(config Config, name string) *weightedPriority {
	config.Validate()

	return &weightedPriority{
		typedName: plugins.TypedName{Type: framework.InterPriorityDispatchPolicyType, Name: name},
		config:    config,
		counters:  make(map[int]float64),
	}
}

// TypedName returns the type and name of the plugin instance.
func (p *weightedPriority) TypedName() plugins.TypedName {
	return p.typedName
}

// SelectBand chooses which priority band should get the next dispatch slot.
// It uses weighted fair queuing: the band with the lowest normalized counter
// (cumulative_tokens / weight) is selected. This ensures bands receive throughput
// proportional to their configured weights.
func (p *weightedPriority) SelectBand(bands []framework.PriorityBandAccessor) (framework.PriorityBandAccessor, error) {
	if len(bands) == 0 {
		return nil, nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	var selectedBand framework.PriorityBandAccessor
	lowestNormalized := math.MaxFloat64

	for _, band := range bands {
		if !bandHasWork(band) {
			continue
		}

		pri := band.Priority()
		weight := p.config.getWeight(pri)
		normalized := p.counters[pri] / weight

		if normalized < lowestNormalized {
			lowestNormalized = normalized
			selectedBand = band
		}
	}

	if selectedBand != nil {
		logger.V(logutil.TRACE).Info("selecting band via weighted priority",
			"priority", selectedBand.Priority(),
			"priorityName", selectedBand.PriorityName(),
			"normalized", lowestNormalized)
	}

	return selectedBand, nil
}

// OnDispatch is a no-op for weighted priority since it doesn't use credit batching.
func (p *weightedPriority) OnDispatch(_ int) {
	// no-op for weighted priority
}

// OnDispatchComplete records a dispatch by incrementing the cumulative counter.
func (p *weightedPriority) OnDispatchComplete(priority int, cost uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// use cost if provided, otherwise count as 1
	increment := float64(cost)
	if cost == 0 {
		increment = 1
	}
	p.counters[priority] += increment

	// record metrics
	recordDispatch(priority, cost)
	p.updateCounterMetricsLocked(priority)

	logger.V(logutil.DEBUG).Info("inter-priority dispatch recorded",
		"priority", priority,
		"cost", cost,
		"increment", increment,
		"counter", p.counters[priority])
}

// updateCounterMetricsLocked updates the gauge metrics for a priority band.
// must be called with p.mu held.
func (p *weightedPriority) updateCounterMetricsLocked(priority int) {
	counter := p.counters[priority]
	weight := p.config.getWeight(priority)
	normalized := counter / weight

	// find the minimum normalized value (the "most behind" band)
	minNormalized := math.MaxFloat64
	for pri, cnt := range p.counters {
		w := p.config.getWeight(pri)
		norm := cnt / w
		if norm < minNormalized {
			minNormalized = norm
		}
	}

	// deficit is how far ahead this band is relative to the most behind band
	deficit := normalized - minNormalized
	if deficit < 0 {
		deficit = 0
	}

	recordCounterState(priority, counter, normalized, deficit)
}

// GetStats returns current dispatch statistics for observability.
func (p *weightedPriority) GetStats() (counters map[int]float64, total float64) {
	p.mu.Lock()
	defer p.mu.Unlock()

	counters = make(map[int]float64, len(p.counters))
	for pri, count := range p.counters {
		counters[pri] = count
		total += count
	}
	return counters, total
}

// bandHasWork returns true if the band has at least one queue with items.
func bandHasWork(band framework.PriorityBandAccessor) bool {
	hasWork := false
	band.IterateQueues(func(queue framework.FlowQueueAccessor) bool {
		if queue.Len() > 0 {
			hasWork = true
			return false // stop iteration
		}
		return true // continue iteration
	})
	return hasWork
}
