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
// priority band receives at least its configured minimum share of dispatches over a sliding window.
package guaranteedminimum

import (
	"encoding/json"
	"sync"
	"time"

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

// guaranteedMinimum implements `framework.InterPriorityDispatchPolicy` with minimum guarantees.
// It tracks dispatch counts in a circular buffer to calculate rates over a sliding window,
// ensuring lower-priority bands receive at least their configured minimum share.
type guaranteedMinimum struct {
	typedName plugins.TypedName
	config    Config
	clock     func() time.Time

	mu            sync.Mutex
	buckets       []bucket          // circular buffer of time buckets
	currentBucket int               // index of the current bucket
	bucketStart   time.Time         // start time of current bucket
	totalCounts   map[int]uint64    // running total per priority across all buckets
	grandTotal    uint64            // running total across all priorities
}

// bucket holds dispatch counts for a single time slice.
type bucket struct {
	counts map[int]uint64 // priority -> dispatch count in this bucket
	total  uint64         // total dispatches in this bucket
}

func newGuaranteedMinimum(config Config, name string) *guaranteedMinimum {
	return newGuaranteedMinimumWithClock(config, name, time.Now)
}

func newGuaranteedMinimumWithClock(config Config, name string, clock func() time.Time) *guaranteedMinimum {
	config.Validate()

	buckets := make([]bucket, config.BucketCount)
	for i := range buckets {
		buckets[i] = bucket{counts: make(map[int]uint64)}
	}

	return &guaranteedMinimum{
		typedName:     plugins.TypedName{Type: framework.InterPriorityDispatchPolicyType, Name: name},
		config:        config,
		clock:         clock,
		buckets:       buckets,
		currentBucket: 0,
		bucketStart:   clock(),
		totalCounts:   make(map[int]uint64),
		grandTotal:    0,
	}
}

// TypedName returns the type and name of the plugin instance.
func (p *guaranteedMinimum) TypedName() plugins.TypedName {
	return p.typedName
}

// SelectBand chooses which priority band should get the next dispatch slot.
// It first checks if any band with a minimum guarantee is below its target rate.
// If so, the most starved band is selected. Otherwise, strict priority applies.
func (p *guaranteedMinimum) SelectBand(bands []framework.PriorityBandAccessor) (framework.PriorityBandAccessor, error) {
	if len(bands) == 0 {
		return nil, nil
	}

	p.mu.Lock()
	p.advanceBuckets()

	// phase 1: find the most starved band (largest deficit below minimum)
	var starvedBand framework.PriorityBandAccessor
	var largestDeficit float64

	for _, band := range bands {
		if !bandHasWork(band) {
			continue
		}

		pri := band.Priority()
		minRate, hasGuarantee := p.config.MinGuaranteedRates[pri]
		if !hasGuarantee || minRate <= 0 {
			continue
		}

		actualRate := p.rateForPriority(pri)
		deficit := minRate - actualRate

		if deficit > 0 && deficit > largestDeficit {
			starvedBand = band
			largestDeficit = deficit
		}
	}
	p.mu.Unlock()

	if starvedBand != nil {
		recordStarvationIntervention(starvedBand.Priority(), starvedBand.PriorityName())
		logger.V(logutil.DEBUG).Info("selecting starved band",
			"priority", starvedBand.Priority(),
			"priorityName", starvedBand.PriorityName(),
			"deficit", largestDeficit)
		return starvedBand, nil
	}

	// phase 2: no starvation, use strict priority (highest with work wins)
	for _, band := range bands {
		if bandHasWork(band) {
			logger.V(logutil.TRACE).Info("selecting band via strict priority",
				"priority", band.Priority(),
				"priorityName", band.PriorityName())
			return band, nil
		}
	}

	return nil, nil
}

// OnDispatchComplete records a dispatch for rate tracking.
func (p *guaranteedMinimum) OnDispatchComplete(priority int, _ uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.advanceBuckets()

	// increment current bucket
	p.buckets[p.currentBucket].counts[priority]++
	p.buckets[p.currentBucket].total++

	// update running totals
	p.totalCounts[priority]++
	p.grandTotal++

	recordDispatch(priority)

	// log rate info at trace level for debugging priority dispatch
	if logger.V(logutil.TRACE).Enabled() {
		rate := p.rateForPriority(priority)
		logger.V(logutil.TRACE).Info("dispatch recorded",
			"priority", priority,
			"dispatchCount", p.totalCounts[priority],
			"grandTotal", p.grandTotal,
			"currentRate", rate)
	}
}

// advanceBuckets rotates the circular buffer if enough time has passed.
// caller must hold p.mu.
func (p *guaranteedMinimum) advanceBuckets() {
	now := p.clock()
	bucketDuration := p.config.BucketDuration()

	for now.Sub(p.bucketStart) >= bucketDuration {
		// move to next bucket
		p.currentBucket = (p.currentBucket + 1) % len(p.buckets)

		// subtract the old bucket's counts from running totals before clearing
		oldBucket := &p.buckets[p.currentBucket]
		for pri, count := range oldBucket.counts {
			p.totalCounts[pri] -= count
		}
		p.grandTotal -= oldBucket.total

		// clear the bucket for reuse
		oldBucket.counts = make(map[int]uint64)
		oldBucket.total = 0

		p.bucketStart = p.bucketStart.Add(bucketDuration)
	}
}

// rateForPriority calculates the dispatch rate for a priority over the sliding window.
// caller must hold p.mu.
func (p *guaranteedMinimum) rateForPriority(priority int) float64 {
	if p.grandTotal == 0 {
		return 0
	}
	return float64(p.totalCounts[priority]) / float64(p.grandTotal)
}

// GetStats returns current dispatch statistics for observability.
// This is goroutine-safe.
func (p *guaranteedMinimum) GetStats() (rates map[int]float64, total uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.advanceBuckets()

	rates = make(map[int]float64, len(p.totalCounts))
	for pri := range p.totalCounts {
		rates[pri] = p.rateForPriority(pri)
	}
	return rates, p.grandTotal
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
