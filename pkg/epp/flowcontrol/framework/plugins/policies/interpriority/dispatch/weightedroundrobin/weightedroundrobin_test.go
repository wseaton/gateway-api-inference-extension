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

package weightedroundrobin

import (
	"sync"
	"testing"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/flowcontrol/framework"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/flowcontrol/types"
)

// mockFlowQueue implements framework.FlowQueueAccessor for testing.
type mockFlowQueue struct {
	flowKey types.FlowKey
	length  int
}

func (m *mockFlowQueue) Name() string                               { return "mock" }
func (m *mockFlowQueue) Capabilities() []framework.QueueCapability  { return nil }
func (m *mockFlowQueue) Len() int                                   { return m.length }
func (m *mockFlowQueue) ByteSize() uint64                           { return 0 }
func (m *mockFlowQueue) PeekHead() (types.QueueItemAccessor, error) { return nil, nil }
func (m *mockFlowQueue) PeekTail() (types.QueueItemAccessor, error) { return nil, nil }
func (m *mockFlowQueue) FlowKey() types.FlowKey                     { return m.flowKey }
func (m *mockFlowQueue) Comparator() framework.ItemComparator       { return nil }

// mockPriorityBand implements framework.PriorityBandAccessor for testing.
type mockPriorityBand struct {
	priority     int
	priorityName string
	queues       []framework.FlowQueueAccessor
}

func (m *mockPriorityBand) Priority() int        { return m.priority }
func (m *mockPriorityBand) PriorityName() string { return m.priorityName }
func (m *mockPriorityBand) FlowKeys() []types.FlowKey {
	keys := make([]types.FlowKey, len(m.queues))
	for i, q := range m.queues {
		keys[i] = q.FlowKey()
	}
	return keys
}
func (m *mockPriorityBand) Queue(id string) framework.FlowQueueAccessor {
	for _, q := range m.queues {
		if q.FlowKey().ID == id {
			return q
		}
	}
	return nil
}
func (m *mockPriorityBand) IterateQueues(callback func(queue framework.FlowQueueAccessor) bool) {
	for _, q := range m.queues {
		if !callback(q) {
			return
		}
	}
}

func TestWeightedRoundRobin_GrantsCreditsOnSelection(t *testing.T) {
	config := Config{
		Weights: map[int]float64{
			100: 10.0,
			50:  1.0,
		},
	}

	policy := newWeightedRoundRobin(config, "test")

	bands := []framework.PriorityBandAccessor{
		&mockPriorityBand{
			priority:     100,
			priorityName: "high",
			queues: []framework.FlowQueueAccessor{
				&mockFlowQueue{flowKey: types.FlowKey{ID: "flow1", Priority: 100}, length: 1},
			},
		},
		&mockPriorityBand{
			priority:     50,
			priorityName: "low",
			queues: []framework.FlowQueueAccessor{
				&mockFlowQueue{flowKey: types.FlowKey{ID: "flow2", Priority: 50}, length: 1},
			},
		},
	}

	// first selection should pick first band with work (round-robin starts at index 0)
	got, err := policy.SelectBand(bands)
	if err != nil {
		t.Fatalf("SelectBand returned error: %v", err)
	}
	if got == nil || got.Priority() != 100 {
		t.Errorf("expected priority 100, got %v", got)
	}

	// check credits were granted
	_, _, currentBand, credits := policy.GetStats()
	if currentBand != 100 {
		t.Errorf("expected currentBand 100, got %d", currentBand)
	}
	if credits != 10 {
		t.Errorf("expected 10 credits, got %d", credits)
	}
}

func TestWeightedRoundRobin_ConsecutiveDispatchesWhileCredits(t *testing.T) {
	config := Config{
		Weights: map[int]float64{
			100: 5.0,
			50:  1.0,
		},
	}

	policy := newWeightedRoundRobin(config, "test")

	bands := []framework.PriorityBandAccessor{
		&mockPriorityBand{
			priority:     100,
			priorityName: "high",
			queues: []framework.FlowQueueAccessor{
				&mockFlowQueue{flowKey: types.FlowKey{ID: "flow1", Priority: 100}, length: 100},
			},
		},
		&mockPriorityBand{
			priority:     50,
			priorityName: "low",
			queues: []framework.FlowQueueAccessor{
				&mockFlowQueue{flowKey: types.FlowKey{ID: "flow2", Priority: 50}, length: 100},
			},
		},
	}

	// first 5 selections should all return high priority
	for i := 0; i < 5; i++ {
		got, err := policy.SelectBand(bands)
		if err != nil {
			t.Fatalf("iteration %d: SelectBand returned error: %v", i, err)
		}
		if got == nil || got.Priority() != 100 {
			t.Errorf("iteration %d: expected priority 100 (still has credits), got %v", i, got)
		}
		policy.OnDispatch(100)
	}

	// credits exhausted, next selection should rotate to low priority
	got, err := policy.SelectBand(bands)
	if err != nil {
		t.Fatalf("SelectBand returned error: %v", err)
	}
	if got == nil || got.Priority() != 50 {
		t.Errorf("expected priority 50 (rotation after credits exhausted), got %v", got)
	}
}

func TestWeightedRoundRobin_ClearCreditsWhenBandHasNoWork(t *testing.T) {
	config := Config{
		Weights: map[int]float64{
			100: 10.0,
			50:  1.0,
		},
	}

	policy := newWeightedRoundRobin(config, "test")

	// high priority has work initially
	highQueue := &mockFlowQueue{flowKey: types.FlowKey{ID: "flow1", Priority: 100}, length: 1}
	bands := []framework.PriorityBandAccessor{
		&mockPriorityBand{
			priority:     100,
			priorityName: "high",
			queues:       []framework.FlowQueueAccessor{highQueue},
		},
		&mockPriorityBand{
			priority:     50,
			priorityName: "low",
			queues: []framework.FlowQueueAccessor{
				&mockFlowQueue{flowKey: types.FlowKey{ID: "flow2", Priority: 50}, length: 100},
			},
		},
	}

	// select high, gets 10 credits
	got, _ := policy.SelectBand(bands)
	if got.Priority() != 100 {
		t.Fatalf("expected initial selection of priority 100")
	}
	policy.OnDispatch(100)

	// simulate high priority queue becoming empty
	highQueue.length = 0

	// next selection should clear credits and select low
	got, err := policy.SelectBand(bands)
	if err != nil {
		t.Fatalf("SelectBand returned error: %v", err)
	}
	if got == nil || got.Priority() != 50 {
		t.Errorf("expected priority 50 (high has no work), got %v", got)
	}

	// verify credits were cleared and low got new credits
	_, _, currentBand, credits := policy.GetStats()
	if currentBand != 50 {
		t.Errorf("expected currentBand 50, got %d", currentBand)
	}
	if credits != 1 {
		t.Errorf("expected 1 credit for low priority, got %d", credits)
	}
}

func TestWeightedRoundRobin_RoundRobinPattern(t *testing.T) {
	config := Config{
		Weights: map[int]float64{
			100: 2.0,
			50:  1.0,
		},
	}

	policy := newWeightedRoundRobin(config, "test")

	bands := []framework.PriorityBandAccessor{
		&mockPriorityBand{
			priority:     100,
			priorityName: "high",
			queues: []framework.FlowQueueAccessor{
				&mockFlowQueue{flowKey: types.FlowKey{ID: "flow1", Priority: 100}, length: 100},
			},
		},
		&mockPriorityBand{
			priority:     50,
			priorityName: "low",
			queues: []framework.FlowQueueAccessor{
				&mockFlowQueue{flowKey: types.FlowKey{ID: "flow2", Priority: 50}, length: 100},
			},
		},
	}

	selections := make(map[int]int)

	// run 9 selections (should be: 2 high, 1 low, 2 high, 1 low, 2 high, 1 low = pattern)
	for i := 0; i < 9; i++ {
		got, err := policy.SelectBand(bands)
		if err != nil {
			t.Fatalf("iteration %d: SelectBand returned error: %v", i, err)
		}
		if got != nil {
			selections[got.Priority()]++
			policy.OnDispatch(got.Priority())
		}
	}

	// with 2:1 weights, expect 6 high and 3 low
	if selections[100] != 6 {
		t.Errorf("expected 6 high priority selections, got %d", selections[100])
	}
	if selections[50] != 3 {
		t.Errorf("expected 3 low priority selections, got %d", selections[50])
	}
}

func TestWeightedRoundRobin_ConcurrentAccess(t *testing.T) {
	config := Config{
		Weights: map[int]float64{
			100: 10.0,
		},
	}

	policy := newWeightedRoundRobin(config, "test")

	bands := []framework.PriorityBandAccessor{
		&mockPriorityBand{
			priority:     100,
			priorityName: "high",
			queues: []framework.FlowQueueAccessor{
				&mockFlowQueue{flowKey: types.FlowKey{ID: "flow1", Priority: 100}, length: 1},
			},
		},
	}

	var wg sync.WaitGroup
	iterations := 1000

	// concurrent dispatches
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			policy.OnDispatch(100)
			policy.OnDispatchComplete(100, 0)
		}
	}()

	// concurrent band selections
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			_, _ = policy.SelectBand(bands)
		}
	}()

	// concurrent stats reads
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			policy.GetStats()
		}
	}()

	wg.Wait()

	// verify no data races occurred (test would fail with -race if there were)
	dispatchCounts, total, _, _ := policy.GetStats()
	if total != uint64(iterations) {
		t.Errorf("expected %d total dispatches, got %d", iterations, total)
	}
	if dispatchCounts[100] != uint64(iterations) {
		t.Errorf("expected dispatchCounts[100]=%d, got %d", iterations, dispatchCounts[100])
	}
}

func TestWeightedRoundRobin_TypedName(t *testing.T) {
	policy := newWeightedRoundRobin(DefaultConfig(), "test-policy")

	typedName := policy.TypedName()
	if typedName.Type != framework.InterPriorityDispatchPolicyType {
		t.Errorf("expected type %s, got %s", framework.InterPriorityDispatchPolicyType, typedName.Type)
	}
	if typedName.Name != "test-policy" {
		t.Errorf("expected name 'test-policy', got %s", typedName.Name)
	}
}

func TestWeightedRoundRobin_EmptyBands(t *testing.T) {
	policy := newWeightedRoundRobin(DefaultConfig(), "test")

	got, err := policy.SelectBand(nil)
	if err != nil {
		t.Fatalf("SelectBand returned error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for nil bands, got %v", got)
	}

	got, err = policy.SelectBand([]framework.PriorityBandAccessor{})
	if err != nil {
		t.Fatalf("SelectBand returned error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for empty bands slice, got %v", got)
	}
}

func TestWeightedRoundRobin_AllBandsEmpty(t *testing.T) {
	config := Config{
		Weights: map[int]float64{
			100: 10.0,
			50:  1.0,
		},
	}

	policy := newWeightedRoundRobin(config, "test")

	bands := []framework.PriorityBandAccessor{
		&mockPriorityBand{
			priority:     100,
			priorityName: "high",
			queues:       []framework.FlowQueueAccessor{},
		},
		&mockPriorityBand{
			priority:     50,
			priorityName: "low",
			queues:       []framework.FlowQueueAccessor{},
		},
	}

	got, err := policy.SelectBand(bands)
	if err != nil {
		t.Fatalf("SelectBand returned error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil when all bands have no work, got %v", got)
	}
}

func TestWeightedRoundRobin_ColdStart(t *testing.T) {
	config := Config{
		Weights: map[int]float64{
			10: 10.0,
			0:  1.0,
		},
	}

	policy := newWeightedRoundRobin(config, "test")

	bands := []framework.PriorityBandAccessor{
		&mockPriorityBand{
			priority:     10,
			priorityName: "high",
			queues: []framework.FlowQueueAccessor{
				&mockFlowQueue{flowKey: types.FlowKey{ID: "flow1", Priority: 10}, length: 1},
			},
		},
		&mockPriorityBand{
			priority:     0,
			priorityName: "low",
			queues: []framework.FlowQueueAccessor{
				&mockFlowQueue{flowKey: types.FlowKey{ID: "flow2", Priority: 0}, length: 1},
			},
		},
	}

	// on cold start, first band with work wins (round-robin starts at 0)
	got, err := policy.SelectBand(bands)
	if err != nil {
		t.Fatalf("SelectBand returned error: %v", err)
	}
	if got == nil || got.Priority() != 10 {
		t.Errorf("expected priority 10 (first band), got %v", got)
	}
}

func TestWeightedRoundRobin_DefaultWeight(t *testing.T) {
	// bands not in config should default to weight 1.0
	config := Config{
		Weights: map[int]float64{
			100: 5.0, // only configure priority 100
		},
	}
	policy := newWeightedRoundRobin(config, "test")

	bands := []framework.PriorityBandAccessor{
		&mockPriorityBand{
			priority:     50, // not configured, defaults to weight 1
			priorityName: "unconfigured",
			queues: []framework.FlowQueueAccessor{
				&mockFlowQueue{flowKey: types.FlowKey{ID: "flow1", Priority: 50}, length: 1},
			},
		},
	}

	got, err := policy.SelectBand(bands)
	if err != nil {
		t.Fatalf("SelectBand returned error: %v", err)
	}
	if got == nil {
		t.Fatal("expected a band to be selected")
	}

	// should get 1 credit (default weight)
	_, _, _, credits := policy.GetStats()
	if credits != 1 {
		t.Errorf("expected 1 credit (default weight), got %d", credits)
	}
}

func TestWeightedRoundRobin_BatchingPattern(t *testing.T) {
	// verify the exact batching pattern with 3:1 weights
	config := Config{
		Weights: map[int]float64{
			100: 3.0,
			50:  1.0,
		},
	}

	policy := newWeightedRoundRobin(config, "test")

	bands := []framework.PriorityBandAccessor{
		&mockPriorityBand{
			priority:     100,
			priorityName: "high",
			queues: []framework.FlowQueueAccessor{
				&mockFlowQueue{flowKey: types.FlowKey{ID: "flow1", Priority: 100}, length: 100},
			},
		},
		&mockPriorityBand{
			priority:     50,
			priorityName: "low",
			queues: []framework.FlowQueueAccessor{
				&mockFlowQueue{flowKey: types.FlowKey{ID: "flow2", Priority: 50}, length: 100},
			},
		},
	}

	// expected pattern: H H H L H H H L H H H L ...
	expectedPattern := []int{100, 100, 100, 50, 100, 100, 100, 50, 100, 100, 100, 50}

	for i, expected := range expectedPattern {
		got, err := policy.SelectBand(bands)
		if err != nil {
			t.Fatalf("iteration %d: SelectBand returned error: %v", i, err)
		}
		if got == nil || got.Priority() != expected {
			t.Errorf("iteration %d: expected priority %d, got %v", i, expected, got)
		}
		policy.OnDispatch(got.Priority())
	}
}

func TestWeightedRoundRobin_SkipsEmptyBands(t *testing.T) {
	config := Config{
		Weights: map[int]float64{
			100: 10.0,
			50:  1.0,
		},
	}

	policy := newWeightedRoundRobin(config, "test")

	bands := []framework.PriorityBandAccessor{
		&mockPriorityBand{
			priority:     100,
			priorityName: "high",
			queues: []framework.FlowQueueAccessor{
				&mockFlowQueue{flowKey: types.FlowKey{ID: "flow1", Priority: 100}, length: 1},
			},
		},
		&mockPriorityBand{
			priority:     50,
			priorityName: "low",
			queues:       []framework.FlowQueueAccessor{}, // no queues
		},
	}

	// low has no work, so high should be selected
	got, err := policy.SelectBand(bands)
	if err != nil {
		t.Fatalf("SelectBand returned error: %v", err)
	}
	if got == nil || got.Priority() != 100 {
		t.Errorf("expected priority 100, got %v", got)
	}
}
