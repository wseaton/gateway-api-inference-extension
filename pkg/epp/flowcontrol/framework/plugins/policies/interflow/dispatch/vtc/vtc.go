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

// Package vtc provides a `framework.InterFlowDispatchPolicy` that implements Virtual Token Counter (VTC)
// fairness across flows. VTC tracks the cumulative service (tokens) received by each client and prioritizes
// clients with the lowest counter to ensure token-level fairness.
package vtc

import (
	"encoding/json"
	"math"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/flowcontrol/framework"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/flowcontrol/types"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/metrics"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/plugins"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"
)

// VTCPolicyName is the name of the VTC policy implementation.
const VTCPolicyName = "VTC"

// globalState is the shared state used by all VTC policy instances across all shards.
// This ensures that virtual counters are consistent across the entire system.
//
// TEMPORARY: This singleton pattern exists because the current plugin system doesn't support
// transient lifecycle. When fairness is integrated into the EPP plugin system, this may be
// replaced with a more flexible state management approach.
var globalState = &state{}

var logger = ctrl.Log.WithName("vtc-policy")

func init() {
	metrics.Register(allMetrics()...)
	plugins.Register(VTCPolicyName, newVTCFactory)
}

// newVTCFactory is the factory function for the VTC policy.
func newVTCFactory(name string, _ json.RawMessage, _ plugins.Handle) (plugins.Plugin, error) {
	return newVTC(DefaultConfig(), name), nil
}

// vtc implements the `framework.InterFlowDispatchPolicy` interface using the Virtual Token Counter algorithm.
// It maintains a virtual counter for each flow that tracks the cumulative service (in tokens/bytes) received.
// When selecting which flow to dispatch from, it chooses the flow with the lowest counter to ensure fairness.
type vtc struct {
	typedName plugins.TypedName
	config    Config
	state     StateProvider
}

// StateProvider is an interface for accessing and updating virtual token counters.
// This allows dependency injection for testing.
type StateProvider interface {
	getCounter(flowID string) float64
	updateCounter(flowID string, amount float64)
}

// state holds the mutable state for the VTC policy using lock-free data structures.
type state struct {
	// virtualCounters uses sync.Map for lock-free concurrent access
	// key: string (FlowKey.ID), value: *atomicFloat64
	virtualCounters sync.Map

	// lastAccessTimes tracks when each counter was last accessed
	// key: string (FlowKey.ID), value: *atomic.Int64 (unix timestamp in nanoseconds)
	lastAccessTimes sync.Map

	// cleanupStarted ensures the cleanup goroutine is started only once
	cleanupStarted sync.Once

	// stopCh signals the cleanup goroutine to terminate
	stopCh chan struct{}
}

// atomicFloat64 provides lock-free atomic operations on float64 values.
// It uses bit-casting to uint64 for atomic CAS operations.
type atomicFloat64 struct {
	bits uint64
}

// Add atomically adds delta to the float64 value using lock-free CAS loop.
func (af *atomicFloat64) Add(delta float64) {
	for {
		oldBits := atomic.LoadUint64(&af.bits)
		oldVal := math.Float64frombits(oldBits)
		newVal := oldVal + delta
		newBits := math.Float64bits(newVal)
		if atomic.CompareAndSwapUint64(&af.bits, oldBits, newBits) {
			return
		}
		// CAS failed due to concurrent update, retry
	}
}

// Load atomically loads the float64 value.
func (af *atomicFloat64) Load() float64 {
	bits := atomic.LoadUint64(&af.bits)
	return math.Float64frombits(bits)
}

func newVTC(config Config, name string) framework.InterFlowDispatchPolicy {
	// ensure cleanup routine is started for global state
	globalState.startCleanup(config)
	return newVTCWithState(config, globalState, name)
}

// newVTCWithState creates a VTC instance with the specified state provider.
// This allows dependency injection for testing.
func newVTCWithState(config Config, stateProvider StateProvider, name string) *vtc {
	return &vtc{
		typedName: plugins.TypedName{Type: framework.InterFlowDispatchPolicyType, Name: name},
		config:    config,
		state:     stateProvider,
	}
}

// TypedName returns the type and name of the plugin instance.
func (v *vtc) TypedName() plugins.TypedName {
	return v.typedName
}

// SelectQueue selects the flow queue with the lowest virtual counter from the given priority band.
// This ensures that flows which have received less cumulative service get prioritized.
//
// Algorithm:
// 1. Iterate through all queues in the priority band
// 2. For each non-empty queue, get its virtual counter
// 3. Select the queue with the lowest counter value
// 4. Update that queue's counter by adding the dispatched item's byte size
// 5. If counters are tied, use lexicographic ordering of flow IDs for determinism
func (v *vtc) SelectQueue(band framework.PriorityBandAccessor) (framework.FlowQueueAccessor, error) {
	if band == nil {
		return nil, nil
	}

	keys := band.FlowKeys()
	if len(keys) == 0 {
		return nil, nil
	}

	// sort for deterministic ordering when counters are equal
	slices.SortFunc(keys, func(a, b types.FlowKey) int { return a.Compare(b) })

	var selectedQueue framework.FlowQueueAccessor
	var lowestCounter float64
	foundAny := false

	// find the queue with the lowest virtual counter
	for _, key := range keys {
		queue := band.Queue(key.ID)
		if queue == nil || queue.Len() == 0 {
			continue
		}

		counter := v.state.getCounter(key.ID)

		// select this queue if it has the lowest counter so far
		if !foundAny || counter < lowestCounter {
			selectedQueue = queue
			lowestCounter = counter
			foundAny = true
		}
	}

	if selectedQueue != nil {
		flowID := selectedQueue.FlowKey().ID
		recordQueueSelection(flowID, lowestCounter)
		logger.V(logutil.DEBUG).Info("selected queue",
			"flowID", flowID,
			"counter", lowestCounter,
			"candidates", len(keys))
	}

	return selectedQueue, nil
}

// OnRequestComplete implements the framework.RequestCompletionListener interface.
// It updates the virtual counter for the flow based on actual token usage.
// Note: priority is part of the interface but unused by VTC since it tracks fairness per-flow, not per-priority.
func (v *vtc) OnRequestComplete(flowID string, _ int, inputTokens int, outputTokens int) {
	if inputTokens < 0 || outputTokens < 0 {
		logger.V(logutil.DEFAULT).Info("ignoring invalid token counts",
			"flowID", flowID,
			"inputTokens", inputTokens,
			"outputTokens", outputTokens)
		return
	}

	inputService := float64(inputTokens) * v.config.InputTokenWeight
	outputService := float64(outputTokens) * v.config.OutputTokenWeight
	totalService := inputService + outputService

	v.state.updateCounter(flowID, totalService)
	recordCounterUpdate(flowID, totalService)

	logger.V(logutil.TRACE).Info("counter updated",
		"flowID", flowID,
		"inputTokens", inputTokens,
		"outputTokens", outputTokens,
		"totalService", totalService)
}

// GetGlobalCompletionListener returns a RequestCompletionListener that uses the shared global VTC state.
// This function should be called once during system initialization to get a listener that can be
// wired to the request control layer.
//
// TEMPORARY: This global state pattern and separate listener API exist to bridge the current
// plugin system gap. When fairness is properly integrated into the EPP plugin system with
// transient lifecycle support, this function will likely be removed or significantly refactored.
func GetGlobalCompletionListener() framework.RequestCompletionListener {
	return newVTCWithState(DefaultConfig(), globalState, VTCPolicyName)
}

// getCounter retrieves the virtual counter for a flow. Returns 0 if the flow hasn't been seen before.
// This is lock-free and safe for concurrent access.
func (s *state) getCounter(flowID string) float64 {
	// record access before loading to prevent resurrection race during cleanup
	s.recordAccess(flowID)

	val, ok := s.virtualCounters.Load(flowID)
	if !ok {
		return 0.0
	}

	return val.(*atomicFloat64).Load()
}

// updateCounter increments the virtual counter for a flow by the given amount using lock-free atomic operations.
// This is safe for concurrent access and will never block.
func (s *state) updateCounter(flowID string, amount float64) {
	// LoadOrStore is lock-free in sync.Map
	val, _ := s.virtualCounters.LoadOrStore(flowID, &atomicFloat64{})
	val.(*atomicFloat64).Add(amount)

	// update last access time
	s.recordAccess(flowID)
}

// recordAccess updates the last access time for a flow counter
func (s *state) recordAccess(flowID string) {
	now := time.Now().UnixNano()
	val, _ := s.lastAccessTimes.LoadOrStore(flowID, &atomic.Int64{})
	val.(*atomic.Int64).Store(now)
}

// startCleanup starts the background cleanup goroutine (only once)
func (s *state) startCleanup(config Config) {
	s.cleanupStarted.Do(func() {
		s.stopCh = make(chan struct{})
		go s.runCleanup(config)
	})
}

// stopCleanup signals the cleanup goroutine to terminate.
// Safe to call multiple times; only the first call has effect.
func (s *state) stopCleanup() {
	if s.stopCh != nil {
		select {
		case <-s.stopCh:
			// already closed
		default:
			close(s.stopCh)
		}
	}
}

// runCleanup periodically removes stale counters that haven't been accessed recently
func (s *state) runCleanup(config Config) {
	ticker := time.NewTicker(config.CounterCleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.cleanupStaleCounters(config.CounterTTL)
		case <-s.stopCh:
			return
		}
	}
}

// cleanupStaleCounters removes counters that haven't been accessed within the TTL
func (s *state) cleanupStaleCounters(ttl time.Duration) {
	now := time.Now().UnixNano()
	cutoff := now - ttl.Nanoseconds()
	const tombstone = -1

	s.lastAccessTimes.Range(func(key, value interface{}) bool {
		flowID := key.(string)
		timeVal := value.(*atomic.Int64)
		lastAccess := timeVal.Load()

		if lastAccess < cutoff {
			// atomically claim ownership of this entry for deletion
			if timeVal.CompareAndSwap(lastAccess, tombstone) {
				s.virtualCounters.Delete(flowID)
				s.lastAccessTimes.Delete(flowID)
				recordFlowCleanedUp()
				logger.V(logutil.VERBOSE).Info("cleaned up stale counter", "flowID", flowID)
			}
		}
		return true
	})
}
