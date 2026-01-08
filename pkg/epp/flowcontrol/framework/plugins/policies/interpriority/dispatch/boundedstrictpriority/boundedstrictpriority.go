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

// Package boundedstrictpriority provides a `framework.InterPriorityDispatchPolicy` that implements
// strict priority with configurable min/max share bounds per priority band.
package boundedstrictpriority

import (
	"encoding/json"
	"sort"
	"sync"

	ctrl "sigs.k8s.io/controller-runtime"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/flowcontrol/framework"
	dispatch "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/flowcontrol/framework/plugins/policies/interpriority/dispatch"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/plugins"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"
)

// BoundedStrictPriorityPolicyName is the name of the BoundedStrictPriority policy implementation.
const BoundedStrictPriorityPolicyName dispatch.RegisteredPolicyName = "BoundedStrictPriority"

var logger = ctrl.Log.WithName("bounded-strict-priority-policy")

func init() {
	dispatch.MustRegisterPolicy(BoundedStrictPriorityPolicyName, func() (framework.InterPriorityDispatchPolicy, error) {
		return newBoundedStrictPriority(DefaultConfig(), string(BoundedStrictPriorityPolicyName)), nil
	})
	plugins.Register(string(BoundedStrictPriorityPolicyName), newBoundedStrictPriorityFactory)
}

// newBoundedStrictPriorityFactory is the factory function for the BoundedStrictPriority policy.
func newBoundedStrictPriorityFactory(name string, rawConfig json.RawMessage, _ plugins.Handle) (plugins.Plugin, error) {
	config := DefaultConfig()
	if len(rawConfig) > 0 {
		if err := json.Unmarshal(rawConfig, &config); err != nil {
			return nil, err
		}
	}
	config.Validate()
	return newBoundedStrictPriority(config, name), nil
}

// boundedStrictPriority implements `framework.InterPriorityDispatchPolicy` using strict priority
// with min/max share bounds. Uses VTC-style counters updated on request completion.
type boundedStrictPriority struct {
	typedName plugins.TypedName
	config    Config

	mu       sync.Mutex
	counters map[int]float64 // cumulative tokens per priority
	total    float64         // total tokens across all priorities
}

func newBoundedStrictPriority(config Config, name string) *boundedStrictPriority {
	config.Validate()

	return &boundedStrictPriority{
		typedName: plugins.TypedName{Type: framework.InterPriorityDispatchPolicyType, Name: name},
		config:    config,
		counters:  make(map[int]float64),
	}
}

// TypedName returns the type and name of the plugin instance.
func (p *boundedStrictPriority) TypedName() plugins.TypedName {
	return p.typedName
}

// SelectBand chooses which priority band should get the next dispatch slot.
// It enforces min/max share bounds while defaulting to strict priority behavior.
func (p *boundedStrictPriority) SelectBand(bands []framework.PriorityBandAccessor) (framework.PriorityBandAccessor, error) {
	if len(bands) == 0 {
		return nil, nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	// cold start: use strict priority until we have enough data
	if p.total < p.config.ColdStartThreshold {
		return p.selectStrictPriority(bands), nil
	}

	// check if any priority is below its floor and has work (lowest priority first)
	bandsLowestFirst := sortBandsByPriority(bands, true)
	for _, band := range bandsLowestFirst {
		if !bandHasWork(band) {
			continue
		}
		pri := band.Priority()
		share := p.counters[pri] / p.total
		minShare := p.config.getMinShare(pri)

		if share < minShare {
			logger.V(logutil.DEBUG).Info("elevating starving priority",
				"priority", pri,
				"currentShare", share,
				"minShare", minShare)
			return band, nil
		}
	}

	// strict priority, but skip bands at their ceiling (highest priority first)
	bandsHighestFirst := sortBandsByPriority(bands, false)
	for _, band := range bandsHighestFirst {
		if !bandHasWork(band) {
			continue
		}
		pri := band.Priority()
		share := p.counters[pri] / p.total
		maxShare := p.config.getMaxShare(pri)

		if share >= maxShare {
			logger.V(logutil.DEBUG).Info("skipping capped priority",
				"priority", pri,
				"currentShare", share,
				"maxShare", maxShare)
			continue
		}

		return band, nil
	}

	// fallback: everyone at ceiling, just use strict priority
	return p.selectStrictPriority(bands), nil
}

// selectStrictPriority returns the highest priority band with work.
func (p *boundedStrictPriority) selectStrictPriority(bands []framework.PriorityBandAccessor) framework.PriorityBandAccessor {
	for _, band := range bands {
		if bandHasWork(band) {
			return band
		}
	}
	return nil
}

// OnDispatch is a no-op; we track on completion for accurate accounting.
func (p *boundedStrictPriority) OnDispatch(_ int) {
	// no-op: update counters on completion for accurate token accounting
}

// OnDispatchComplete updates counters with actual token cost (VTC-style).
func (p *boundedStrictPriority) OnDispatchComplete(priority int, cost uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()

	increment := float64(cost)
	if cost == 0 {
		increment = 1
	}

	p.counters[priority] += increment
	p.total += increment

	logger.V(logutil.TRACE).Info("request complete",
		"priority", priority,
		"cost", cost,
		"priorityTotal", p.counters[priority],
		"grandTotal", p.total,
		"share", p.counters[priority]/p.total)
}

// GetStats returns current dispatch statistics for observability.
func (p *boundedStrictPriority) GetStats() (counters map[int]float64, total float64) {
	p.mu.Lock()
	defer p.mu.Unlock()

	counters = make(map[int]float64, len(p.counters))
	for pri, count := range p.counters {
		counters[pri] = count
	}
	return counters, p.total
}

// bandHasWork returns true if the band has at least one queue with items.
func bandHasWork(band framework.PriorityBandAccessor) bool {
	hasWork := false
	band.IterateQueues(func(queue framework.FlowQueueAccessor) bool {
		if queue.Len() > 0 {
			hasWork = true
			return false
		}
		return true
	})
	return hasWork
}

// sortBandsByPriority returns bands sorted by priority.
// If ascending is true, lowest priority first; otherwise highest first.
func sortBandsByPriority(bands []framework.PriorityBandAccessor, ascending bool) []framework.PriorityBandAccessor {
	sorted := make([]framework.PriorityBandAccessor, len(bands))
	copy(sorted, bands)

	sort.Slice(sorted, func(i, j int) bool {
		if ascending {
			return sorted[i].Priority() < sorted[j].Priority()
		}
		return sorted[i].Priority() > sorted[j].Priority()
	})

	return sorted
}
