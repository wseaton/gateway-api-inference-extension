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

package weightedpriority

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

func TestWeightedPriority_SelectBand_SelectsLowestNormalized(t *testing.T) {
	config := Config{
		Weights: map[int]float64{
			100: 10.0, // high priority weight
			50:  1.0,  // low priority weight
		},
	}

	policy := newWeightedPriority(config, "test")

	// simulate equal dispatches - priority 100 should be selected (lower normalized)
	// after 10 dispatches each: normalized_100 = 10/10 = 1, normalized_50 = 10/1 = 10
	for i := 0; i < 10; i++ {
		policy.OnDispatchComplete(100, 0)
		policy.OnDispatchComplete(50, 0)
	}

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

	// priority 100 has lower normalized, should be selected
	got, err := policy.SelectBand(bands)
	if err != nil {
		t.Fatalf("SelectBand returned error: %v", err)
	}
	if got == nil || got.Priority() != 100 {
		t.Errorf("expected priority 100 (lower normalized), got %v", got)
	}
}

func TestWeightedPriority_SelectBand_WeightAffectsSelection(t *testing.T) {
	config := Config{
		Weights: map[int]float64{
			100: 10.0, // high gets 10x weight
			50:  1.0,  // low gets 1x weight
		},
	}

	policy := newWeightedPriority(config, "test")

	// give high priority 100 dispatches, low priority 5
	// normalized_100 = 100/10 = 10, normalized_50 = 5/1 = 5
	// low priority has lower normalized, should be selected
	for i := 0; i < 100; i++ {
		policy.OnDispatchComplete(100, 0)
	}
	for i := 0; i < 5; i++ {
		policy.OnDispatchComplete(50, 0)
	}

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

	// low priority (normalized=5) < high priority (normalized=10), so low is selected
	got, err := policy.SelectBand(bands)
	if err != nil {
		t.Fatalf("SelectBand returned error: %v", err)
	}
	if got == nil || got.Priority() != 50 {
		t.Errorf("expected priority 50 (lower normalized due to fewer dispatches), got %v", got)
	}
}

func TestWeightedPriority_SelectBand_SkipsEmptyBands(t *testing.T) {
	config := Config{
		Weights: map[int]float64{
			100: 10.0,
			50:  1.0,
		},
	}

	policy := newWeightedPriority(config, "test")

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

func TestWeightedPriority_ConcurrentAccess(t *testing.T) {
	config := Config{
		Weights: map[int]float64{
			100: 10.0,
		},
	}

	policy := newWeightedPriority(config, "test")

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
	_, total := policy.GetStats()
	if total != float64(iterations) {
		t.Errorf("expected %d total dispatches, got %f", iterations, total)
	}
}

func TestWeightedPriority_SelectsMostBehindBand(t *testing.T) {
	config := Config{
		Weights: map[int]float64{
			100: 10.0, // high
			50:  5.0,  // medium
			25:  1.0,  // low
		},
	}

	policy := newWeightedPriority(config, "test")

	// set up counters so each has different normalized values
	// high: 50 dispatches, normalized = 50/10 = 5
	// medium: 20 dispatches, normalized = 20/5 = 4
	// low: 10 dispatches, normalized = 10/1 = 10
	for i := 0; i < 50; i++ {
		policy.OnDispatchComplete(100, 0)
	}
	for i := 0; i < 20; i++ {
		policy.OnDispatchComplete(50, 0)
	}
	for i := 0; i < 10; i++ {
		policy.OnDispatchComplete(25, 0)
	}

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
			priorityName: "medium",
			queues: []framework.FlowQueueAccessor{
				&mockFlowQueue{flowKey: types.FlowKey{ID: "flow2", Priority: 50}, length: 1},
			},
		},
		&mockPriorityBand{
			priority:     25,
			priorityName: "low",
			queues: []framework.FlowQueueAccessor{
				&mockFlowQueue{flowKey: types.FlowKey{ID: "flow3", Priority: 25}, length: 1},
			},
		},
	}

	// medium (normalized=4) is most behind, should be selected
	got, err := policy.SelectBand(bands)
	if err != nil {
		t.Fatalf("SelectBand returned error: %v", err)
	}
	if got == nil || got.Priority() != 50 {
		t.Errorf("expected priority 50 (most behind), got %v", got)
	}
}

func TestWeightedPriority_TypedName(t *testing.T) {
	policy := newWeightedPriority(DefaultConfig(), "test-policy")

	typedName := policy.TypedName()
	if typedName.Type != framework.InterPriorityDispatchPolicyType {
		t.Errorf("expected type %s, got %s", framework.InterPriorityDispatchPolicyType, typedName.Type)
	}
	if typedName.Name != "test-policy" {
		t.Errorf("expected name 'test-policy', got %s", typedName.Name)
	}
}

func TestWeightedPriority_ColdStart_SelectsFirstWithWork(t *testing.T) {
	// on cold start (no dispatches yet), all normalized values are 0
	// should select first band with work (arbitrary but deterministic)
	config := Config{
		Weights: map[int]float64{
			10: 10.0,
			0:  1.0,
		},
	}

	policy := newWeightedPriority(config, "test")

	// no dispatches yet - all counters are 0
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

	// with all counters at 0, normalized values are equal (0/x = 0 for all x)
	// first band with work is selected
	got, err := policy.SelectBand(bands)
	if err != nil {
		t.Fatalf("SelectBand returned error: %v", err)
	}
	if got == nil || got.Priority() != 10 {
		t.Errorf("expected priority 10 (first band with work), got %v", got)
	}
}

func TestWeightedPriority_CounterPreservation_AcrossOperations(t *testing.T) {
	config := Config{
		Weights: map[int]float64{
			100: 10.0,
			50:  1.0,
		},
	}

	policy := newWeightedPriority(config, "test")

	// do some dispatches
	for i := 0; i < 100; i++ {
		policy.OnDispatchComplete(100, 0)
	}

	counters1, total1 := policy.GetStats()
	if total1 != 100 {
		t.Errorf("expected total 100, got %f", total1)
	}
	if counters1[100] != 100 {
		t.Errorf("expected counter[100]=100, got %f", counters1[100])
	}

	// do more dispatches
	for i := 0; i < 50; i++ {
		policy.OnDispatchComplete(50, 0)
	}

	counters2, total2 := policy.GetStats()
	if total2 != 150 {
		t.Errorf("expected total 150, got %f", total2)
	}
	if counters2[100] != 100 {
		t.Errorf("expected counter[100]=100, got %f", counters2[100])
	}
	if counters2[50] != 50 {
		t.Errorf("expected counter[50]=50, got %f", counters2[50])
	}
}

func TestWeightedPriority_CostBasedAccounting(t *testing.T) {
	config := Config{
		Weights: map[int]float64{
			100: 10.0,
			50:  1.0,
		},
	}

	policy := newWeightedPriority(config, "test")

	// dispatch with explicit costs
	policy.OnDispatchComplete(100, 500) // 500 tokens
	policy.OnDispatchComplete(100, 300) // 300 tokens
	policy.OnDispatchComplete(50, 100)  // 100 tokens

	counters, total := policy.GetStats()
	if total != 900 {
		t.Errorf("expected total 900, got %f", total)
	}
	if counters[100] != 800 {
		t.Errorf("expected counter[100]=800, got %f", counters[100])
	}
	if counters[50] != 100 {
		t.Errorf("expected counter[50]=100, got %f", counters[50])
	}
}

func TestWeightedPriority_DefaultWeight(t *testing.T) {
	// bands not in config should default to weight 1.0
	config := Config{
		Weights: map[int]float64{
			100: 2.0, // only configure priority 100
		},
	}
	policy := newWeightedPriority(config, "test")

	// simulate equal dispatches
	for i := 0; i < 100; i++ {
		policy.OnDispatchComplete(100, 0) // weight 2.0, normalized = 100/2 = 50
		policy.OnDispatchComplete(50, 0)  // weight 1.0 (default), normalized = 100/1 = 100
	}

	bands := []framework.PriorityBandAccessor{
		&mockPriorityBand{
			priority:     100,
			priorityName: "configured",
			queues: []framework.FlowQueueAccessor{
				&mockFlowQueue{flowKey: types.FlowKey{ID: "flow1", Priority: 100}, length: 1},
			},
		},
		&mockPriorityBand{
			priority:     50,
			priorityName: "unconfigured",
			queues: []framework.FlowQueueAccessor{
				&mockFlowQueue{flowKey: types.FlowKey{ID: "flow2", Priority: 50}, length: 1},
			},
		},
	}

	// priority 100 (normalized=50) < priority 50 (normalized=100)
	got, err := policy.SelectBand(bands)
	if err != nil {
		t.Fatalf("SelectBand returned error: %v", err)
	}
	if got == nil || got.Priority() != 100 {
		t.Errorf("expected priority 100 (lower normalized), got %v", got)
	}
}

func TestWeightedPriority_EmptyBands(t *testing.T) {
	policy := newWeightedPriority(DefaultConfig(), "test")

	got, err := policy.SelectBand(nil)
	if err != nil {
		t.Fatalf("SelectBand returned error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for empty bands, got %v", got)
	}

	got, err = policy.SelectBand([]framework.PriorityBandAccessor{})
	if err != nil {
		t.Fatalf("SelectBand returned error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for empty bands slice, got %v", got)
	}
}

func TestWeightedPriority_AllBandsEmpty(t *testing.T) {
	config := Config{
		Weights: map[int]float64{
			100: 10.0,
			50:  1.0,
		},
	}

	policy := newWeightedPriority(config, "test")

	bands := []framework.PriorityBandAccessor{
		&mockPriorityBand{
			priority:     100,
			priorityName: "high",
			queues:       []framework.FlowQueueAccessor{}, // no work
		},
		&mockPriorityBand{
			priority:     50,
			priorityName: "low",
			queues:       []framework.FlowQueueAccessor{}, // no work
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

func TestWeightedPriority_ProportionalThroughput(t *testing.T) {
	// verify that over many selections, throughput is proportional to weights
	config := Config{
		Weights: map[int]float64{
			100: 10.0, // should get ~10x the throughput
			50:  1.0,
		},
	}

	policy := newWeightedPriority(config, "test")

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

	selections := make(map[int]int)
	for i := 0; i < 110; i++ {
		got, err := policy.SelectBand(bands)
		if err != nil {
			t.Fatalf("SelectBand returned error: %v", err)
		}
		if got != nil {
			selections[got.Priority()]++
			policy.OnDispatchComplete(got.Priority(), 0)
		}
	}

	// with 10:1 weights, we expect ~100 high and ~10 low selections
	highSelections := selections[100]
	lowSelections := selections[50]

	// allow some tolerance for rounding
	if highSelections < 95 || highSelections > 105 {
		t.Errorf("expected ~100 high priority selections, got %d", highSelections)
	}
	if lowSelections < 5 || lowSelections > 15 {
		t.Errorf("expected ~10 low priority selections, got %d", lowSelections)
	}
}
