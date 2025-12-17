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

// Package weightedroundrobin provides a `framework.InterPriorityDispatchPolicy` that implements
// Weighted Round-Robin (WRR) scheduling across priority bands. When a band is selected, it receives
// N consecutive dispatch opportunities (where N = weight) before rotating to the next band with work.
package weightedroundrobin

import (
	"encoding/json"
	"sync"

	ctrl "sigs.k8s.io/controller-runtime"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/flowcontrol/framework"
	dispatch "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/flowcontrol/framework/plugins/policies/interpriority/dispatch"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/plugins"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"
)

// WeightedRoundRobinPolicyName is the name of the WeightedRoundRobin policy implementation.
const WeightedRoundRobinPolicyName dispatch.RegisteredPolicyName = "WeightedRoundRobin"

var logger = ctrl.Log.WithName("weighted-round-robin-policy")

func init() {
	dispatch.MustRegisterPolicy(WeightedRoundRobinPolicyName, func() (framework.InterPriorityDispatchPolicy, error) {
		return newWeightedRoundRobin(DefaultConfig(), string(WeightedRoundRobinPolicyName)), nil
	})
	plugins.Register(string(WeightedRoundRobinPolicyName), newWeightedRoundRobinFactory)
}

// newWeightedRoundRobinFactory is the factory function for the WeightedRoundRobin policy.
func newWeightedRoundRobinFactory(name string, rawConfig json.RawMessage, _ plugins.Handle) (plugins.Plugin, error) {
	config := DefaultConfig()
	if len(rawConfig) > 0 {
		if err := json.Unmarshal(rawConfig, &config); err != nil {
			return nil, err
		}
	}
	config.Validate()
	return newWeightedRoundRobin(config, name), nil
}

// weightedRoundRobin implements `framework.InterPriorityDispatchPolicy` using Weighted Round-Robin.
// When a band is selected, it receives consecutive dispatch opportunities equal to its weight,
// then rotates to the next band with work. This provides deterministic weighted fairness.
type weightedRoundRobin struct {
	typedName plugins.TypedName
	config    Config

	mu             sync.Mutex
	currentBand    int            // priority of band currently holding credits
	credits        int            // remaining credits for currentBand
	hasActive      bool           // whether currentBand is valid
	lastBandIndex  int            // index in bands slice of last selected band (for round-robin)
	dispatchCounts map[int]uint64 // dispatch count per priority (for metrics only)
}

func newWeightedRoundRobin(config Config, name string) *weightedRoundRobin {
	config.Validate()

	return &weightedRoundRobin{
		typedName:      plugins.TypedName{Type: framework.InterPriorityDispatchPolicyType, Name: name},
		config:         config,
		dispatchCounts: make(map[int]uint64),
		lastBandIndex:  -1, // start at -1 so first selection picks index 0
	}
}

// TypedName returns the type and name of the plugin instance.
func (p *weightedRoundRobin) TypedName() plugins.TypedName {
	return p.typedName
}

// SelectBand chooses which priority band should get the next dispatch slot.
// If the current band still has credits and work, it continues to be selected.
// Otherwise, it rotates to the next band with work in round-robin order and
// grants that band credits equal to its weight.
func (p *weightedRoundRobin) SelectBand(bands []framework.PriorityBandAccessor) (framework.PriorityBandAccessor, error) {
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
		// band exhausted or no work, clear credits and rotate
		logger.V(logutil.DEBUG).Info("active band exhausted, rotating",
			"priority", p.currentBand,
			"remainingCredits", p.credits)
		p.credits = 0
		p.hasActive = false
	}

	// round-robin selection: find next band with work starting after lastBandIndex
	selectedBand := p.selectNextBandRoundRobin(bands)
	if selectedBand != nil {
		pri := selectedBand.Priority()
		weight := p.config.getWeight(pri)
		p.currentBand = pri
		p.credits = int(weight)
		p.hasActive = true

		logger.V(logutil.DEBUG).Info("selected new band via round-robin",
			"priority", pri,
			"priorityName", selectedBand.PriorityName(),
			"grantedCredits", p.credits,
			"weight", weight)
	}

	return selectedBand, nil
}

// OnDispatch is called immediately after a request is dispatched from a band.
// It decrements the credit counter for batching behavior.
func (p *weightedRoundRobin) OnDispatch(priority int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// decrement credits if this is the active band
	if p.hasActive && p.currentBand == priority && p.credits > 0 {
		p.credits--
		logger.V(logutil.TRACE).Info("credit consumed on dispatch",
			"priority", priority,
			"remainingCredits", p.credits)
	}

	// track dispatch count for metrics
	p.dispatchCounts[priority]++
}

// OnDispatchComplete is called after a request completes (response received).
// It records metrics for observability.
func (p *weightedRoundRobin) OnDispatchComplete(priority int, cost uint64) {
	// record metrics
	recordDispatch(priority, cost)

	logger.V(logutil.TRACE).Info("request complete",
		"priority", priority,
		"cost", cost)
}

// GetStats returns current dispatch statistics for observability.
func (p *weightedRoundRobin) GetStats() (dispatchCounts map[int]uint64, totalDispatches uint64, currentBand int, credits int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	dispatchCounts = make(map[int]uint64, len(p.dispatchCounts))
	for pri, count := range p.dispatchCounts {
		dispatchCounts[pri] = count
		totalDispatches += count
	}
	if p.hasActive {
		currentBand = p.currentBand
		credits = p.credits
	}
	return dispatchCounts, totalDispatches, currentBand, credits
}

// findBand locates a band by priority in the provided slice.
func (p *weightedRoundRobin) findBand(bands []framework.PriorityBandAccessor, priority int) framework.PriorityBandAccessor {
	for _, band := range bands {
		if band.Priority() == priority {
			return band
		}
	}
	return nil
}

// selectNextBandRoundRobin finds the next band with work in round-robin order.
// It starts searching from the position after the last selected band.
func (p *weightedRoundRobin) selectNextBandRoundRobin(bands []framework.PriorityBandAccessor) framework.PriorityBandAccessor {
	n := len(bands)
	if n == 0 {
		return nil
	}

	// start from the next position after lastBandIndex
	startIdx := (p.lastBandIndex + 1) % n

	// search through all bands starting from startIdx
	for i := 0; i < n; i++ {
		idx := (startIdx + i) % n
		band := bands[idx]
		if bandHasWork(band) {
			p.lastBandIndex = idx
			return band
		}
	}

	return nil
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
