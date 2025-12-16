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

// Package guaranteedminimum provides a `framework.InterPriorityDispatchPolicy` that ensures each
// priority band receives at least its configured minimum share of dispatches using VTC-style
// cumulative counters.
package guaranteedminimum

import (
	"encoding/json"
	"math"
	"sync"

	ctrl "sigs.k8s.io/controller-runtime"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/flowcontrol/framework"
	dispatch "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/flowcontrol/framework/plugins/policies/interpriority/dispatch"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/metrics"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/plugins"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"
)

// GuaranteedMinimumPolicyName is the name of the GuaranteedMinimum policy implementation.
const GuaranteedMinimumPolicyName dispatch.RegisteredPolicyName = "GuaranteedMinimum"

var logger = ctrl.Log.WithName("guaranteed-minimum-policy")

func init() {
	metrics.Register(allMetrics()...)
	dispatch.MustRegisterPolicy(GuaranteedMinimumPolicyName, func() (framework.InterPriorityDispatchPolicy, error) {
		return newGuaranteedMinimum(DefaultConfig(), string(GuaranteedMinimumPolicyName)), nil
	})
	plugins.Register(string(GuaranteedMinimumPolicyName), newGuaranteedMinimumFactory)
}

// newGuaranteedMinimumFactory is the factory function for the GuaranteedMinimum policy.
func newGuaranteedMinimumFactory(name string, rawConfig json.RawMessage, _ plugins.Handle) (plugins.Plugin, error) {
	config := DefaultConfig()
	if len(rawConfig) > 0 {
		if err := json.Unmarshal(rawConfig, &config); err != nil {
			return nil, err
		}
	}
	config.Validate()
	return newGuaranteedMinimum(config, name), nil
}

// guaranteedMinimum implements `framework.InterPriorityDispatchPolicy` with minimum guarantees
// using VTC-style cumulative counters. A band with a minimum guarantee is "behind" if its
// normalized counter (tokens/minRate) is lower than the strict priority band's.
type guaranteedMinimum struct {
	typedName plugins.TypedName
	config    Config

	mu       sync.Mutex
	counters map[int]float64 // cumulative tokens per priority
}

func newGuaranteedMinimum(config Config, name string) *guaranteedMinimum {
	config.Validate()

	return &guaranteedMinimum{
		typedName: plugins.TypedName{Type: framework.InterPriorityDispatchPolicyType, Name: name},
		config:    config,
		counters:  make(map[int]float64),
	}
}

// TypedName returns the type and name of the plugin instance.
func (p *guaranteedMinimum) TypedName() plugins.TypedName {
	return p.typedName
}

// SelectBand chooses which priority band should get the next dispatch slot.
// It uses VTC-style normalized counters to detect starvation: a guaranteed band
// is "behind" if counters[band]/minRate < counters[strictPriority]/rate.
// If a guaranteed band is behind, it gets the dispatch; otherwise strict priority applies.
func (p *guaranteedMinimum) SelectBand(bands []framework.PriorityBandAccessor) (framework.PriorityBandAccessor, error) {
	if len(bands) == 0 {
		return nil, nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	// find highest priority band with work (strict priority winner)
	var strictPriorityBand framework.PriorityBandAccessor
	for _, band := range bands {
		if bandHasWork(band) {
			strictPriorityBand = band
			break
		}
	}

	if strictPriorityBand == nil {
		return nil, nil
	}

	// calculate strict priority band's normalized counter
	strictPri := strictPriorityBand.Priority()
	strictMinRate, strictHasGuarantee := p.config.MinGuaranteedRates[strictPri]
	var strictNormalized float64
	if strictHasGuarantee && strictMinRate > 0 {
		strictNormalized = p.counters[strictPri] / strictMinRate
	} else {
		// no guarantee means implicit rate of 1.0
		strictNormalized = p.counters[strictPri]
	}

	// find the most behind guaranteed band that is behind the strict priority band
	var mostBehindBand framework.PriorityBandAccessor
	lowestNormalized := math.MaxFloat64

	for _, band := range bands {
		if !bandHasWork(band) {
			continue
		}

		pri := band.Priority()
		minRate, hasGuarantee := p.config.MinGuaranteedRates[pri]
		if !hasGuarantee || minRate <= 0 {
			continue
		}

		normalized := p.counters[pri] / minRate
		if normalized < strictNormalized && normalized < lowestNormalized {
			lowestNormalized = normalized
			mostBehindBand = band
		}
	}

	if mostBehindBand != nil {
		recordStarvationIntervention(mostBehindBand.Priority(), mostBehindBand.PriorityName())
		logger.V(logutil.DEBUG).Info("selecting behind band",
			"priority", mostBehindBand.Priority(),
			"priorityName", mostBehindBand.PriorityName(),
			"normalized", lowestNormalized,
			"strictNormalized", strictNormalized)
		return mostBehindBand, nil
	}

	logger.V(logutil.TRACE).Info("selecting band via strict priority",
		"priority", strictPriorityBand.Priority(),
		"priorityName", strictPriorityBand.PriorityName())
	return strictPriorityBand, nil
}

// OnDispatchComplete records a dispatch by incrementing the cumulative counter.
func (p *guaranteedMinimum) OnDispatchComplete(priority int, cost uint64) {
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
func (p *guaranteedMinimum) updateCounterMetricsLocked(priority int) {
	counter := p.counters[priority]

	// calculate normalized counter
	var normalized float64
	minRate, hasGuarantee := p.config.MinGuaranteedRates[priority]
	if hasGuarantee && minRate > 0 {
		normalized = counter / minRate
	} else {
		normalized = counter // implicit rate of 1.0
	}

	// calculate deficit relative to highest counter
	// find max normalized across all priorities
	var maxNormalized float64
	for pri, cnt := range p.counters {
		rate, hasRate := p.config.MinGuaranteedRates[pri]
		var norm float64
		if hasRate && rate > 0 {
			norm = cnt / rate
		} else {
			norm = cnt
		}
		if norm > maxNormalized {
			maxNormalized = norm
		}
	}

	// deficit is how far behind this band is (positive = behind, 0 = caught up)
	deficit := maxNormalized - normalized
	if deficit < 0 {
		deficit = 0
	}

	recordCounterState(priority, counter, normalized, deficit)
}

// GetStats returns current dispatch statistics for observability.
func (p *guaranteedMinimum) GetStats() (counters map[int]float64, total float64) {
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
