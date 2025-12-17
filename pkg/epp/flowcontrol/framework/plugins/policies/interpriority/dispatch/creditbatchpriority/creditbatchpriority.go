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

// Package creditbatchpriority provides a `framework.InterPriorityDispatchPolicy` that implements
// credit-based batching across priority bands. When a band is selected, it receives N consecutive
// dispatch opportunities (credits = weight) before re-evaluation, reducing the dilution effect
// of high-frequency selection loops.
package creditbatchpriority

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

// CreditBatchPriorityPolicyName is the name of the CreditBatchPriority policy implementation.
const CreditBatchPriorityPolicyName dispatch.RegisteredPolicyName = "CreditBatchPriority"

var logger = ctrl.Log.WithName("credit-batch-priority-policy")

func init() {
	dispatch.MustRegisterPolicy(CreditBatchPriorityPolicyName, func() (framework.InterPriorityDispatchPolicy, error) {
		return newCreditBatchPriority(DefaultConfig(), string(CreditBatchPriorityPolicyName)), nil
	})
	plugins.Register(string(CreditBatchPriorityPolicyName), newCreditBatchPriorityFactory)
}

// newCreditBatchPriorityFactory is the factory function for the CreditBatchPriority policy.
func newCreditBatchPriorityFactory(name string, rawConfig json.RawMessage, _ plugins.Handle) (plugins.Plugin, error) {
	config := DefaultConfig()
	if len(rawConfig) > 0 {
		if err := json.Unmarshal(rawConfig, &config); err != nil {
			return nil, err
		}
	}
	config.Validate()
	return newCreditBatchPriority(config, name), nil
}

// creditBatchPriority implements `framework.InterPriorityDispatchPolicy` using credit-based batching.
// When a band is selected via VTC-style weighted fair queuing, it receives consecutive dispatch
// opportunities equal to its weight before the next band selection.
type creditBatchPriority struct {
	typedName plugins.TypedName
	config    Config

	mu          sync.Mutex
	counters    map[int]float64 // cumulative tokens per priority (VTC-style)
	currentBand int             // priority of band currently holding credits
	credits     int             // remaining credits for currentBand
	hasActive   bool            // whether currentBand is valid
}

func newCreditBatchPriority(config Config, name string) *creditBatchPriority {
	config.Validate()

	return &creditBatchPriority{
		typedName: plugins.TypedName{Type: framework.InterPriorityDispatchPolicyType, Name: name},
		config:    config,
		counters:  make(map[int]float64),
	}
}

// TypedName returns the type and name of the plugin instance.
func (p *creditBatchPriority) TypedName() plugins.TypedName {
	return p.typedName
}

// SelectBand chooses which priority band should get the next dispatch slot.
// If the current band still has credits and work, it continues to be selected.
// Otherwise, VTC-style selection picks the band with the lowest normalized counter,
// and that band receives credits equal to its weight.
func (p *creditBatchPriority) SelectBand(bands []framework.PriorityBandAccessor) (framework.PriorityBandAccessor, error) {
	if len(bands) == 0 {
		return nil, nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	// check if current band still has credits and work
	if p.hasActive && p.credits > 0 {
		if band := p.findBand(bands, p.currentBand); band != nil && bandHasWork(band) {
			logger.V(logutil.TRACE).Info("continuing with active band",
				"priority", p.currentBand,
				"remainingCredits", p.credits)
			return band, nil
		}
		// band exhausted or no work, clear credits
		logger.V(logutil.DEBUG).Info("active band exhausted, clearing credits",
			"priority", p.currentBand,
			"remainingCredits", p.credits)
		p.credits = 0
		p.hasActive = false
	}

	// VTC-style selection for new band
	selectedBand := p.selectByLowestNormalized(bands)
	if selectedBand != nil {
		pri := selectedBand.Priority()
		weight := p.config.getWeight(pri)
		p.currentBand = pri
		p.credits = int(weight)
		p.hasActive = true

		logger.V(logutil.DEBUG).Info("selected new band via VTC",
			"priority", pri,
			"priorityName", selectedBand.PriorityName(),
			"grantedCredits", p.credits,
			"weight", weight)
	}

	return selectedBand, nil
}

// OnDispatch is called immediately after a request is dispatched from a band.
// It decrements the credit counter for batching behavior.
func (p *creditBatchPriority) OnDispatch(priority int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// decrement credits if this is the active band
	if p.hasActive && p.currentBand == priority && p.credits > 0 {
		p.credits--
		logger.V(logutil.TRACE).Info("credit consumed on dispatch",
			"priority", priority,
			"remainingCredits", p.credits)
	}
}

// OnDispatchComplete is called after a request completes (response received).
// It updates the VTC cumulative counter with actual token cost.
func (p *creditBatchPriority) OnDispatchComplete(priority int, cost uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// update VTC counter with actual cost
	increment := float64(cost)
	if cost == 0 {
		increment = 1
	}
	p.counters[priority] += increment

	// record metrics
	recordDispatch(priority, cost)
	p.updateCounterMetricsLocked(priority)

	logger.V(logutil.TRACE).Info("request complete",
		"priority", priority,
		"cost", cost,
		"counter", p.counters[priority])
}

// GetStats returns current dispatch statistics for observability.
func (p *creditBatchPriority) GetStats() (counters map[int]float64, total float64, currentBand int, credits int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	counters = make(map[int]float64, len(p.counters))
	for pri, count := range p.counters {
		counters[pri] = count
		total += count
	}
	if p.hasActive {
		currentBand = p.currentBand
		credits = p.credits
	}
	return counters, total, currentBand, credits
}

// updateCounterMetricsLocked updates the gauge metrics for a priority band.
// must be called with p.mu held.
func (p *creditBatchPriority) updateCounterMetricsLocked(priority int) {
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

// findBand locates a band by priority in the provided slice.
func (p *creditBatchPriority) findBand(bands []framework.PriorityBandAccessor, priority int) framework.PriorityBandAccessor {
	for _, band := range bands {
		if band.Priority() == priority {
			return band
		}
	}
	return nil
}

// selectByLowestNormalized implements VTC-style weighted fair queuing.
// It selects the band with the lowest normalized counter (counter / weight).
func (p *creditBatchPriority) selectByLowestNormalized(bands []framework.PriorityBandAccessor) framework.PriorityBandAccessor {
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

	return selectedBand
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
