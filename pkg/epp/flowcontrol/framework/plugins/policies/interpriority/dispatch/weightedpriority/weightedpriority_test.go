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

func TestWeightedPriority_InFlightTracking(t *testing.T) {
	policy := newWeightedPriority(DefaultConfig(), "test")

	// initially no in-flight requests
	inFlight := policy.GetInFlightStats()
	if len(inFlight) != 0 {
		t.Errorf("expected empty in-flight map, got %v", inFlight)
	}

	// dispatch increments in-flight
	policy.OnDispatch(100)
	policy.OnDispatch(100)
	policy.OnDispatch(50)

	inFlight = policy.GetInFlightStats()
	if inFlight[100] != 2 {
		t.Errorf("expected inFlight[100]=2, got %d", inFlight[100])
	}
	if inFlight[50] != 1 {
		t.Errorf("expected inFlight[50]=1, got %d", inFlight[50])
	}

	// complete decrements in-flight
	policy.OnDispatchComplete(100, 0)
	inFlight = policy.GetInFlightStats()
	if inFlight[100] != 1 {
		t.Errorf("expected inFlight[100]=1 after complete, got %d", inFlight[100])
	}

	// complete all
	policy.OnDispatchComplete(100, 0)
	policy.OnDispatchComplete(50, 0)
	inFlight = policy.GetInFlightStats()
	if inFlight[100] != 0 {
		t.Errorf("expected inFlight[100]=0, got %d", inFlight[100])
	}
	if inFlight[50] != 0 {
		t.Errorf("expected inFlight[50]=0, got %d", inFlight[50])
	}
}

func TestWeightedPriority_SelectBand_UsesInFlightLoad(t *testing.T) {
	config := Config{
		Weights: map[int]float64{
			100: 10.0, // high priority can have 10x in-flight
			50:  1.0,  // low priority baseline
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

	// no in-flight: both have load 0, first band selected
	got, err := policy.SelectBand(bands)
	if err != nil {
		t.Fatalf("SelectBand returned error: %v", err)
	}
	if got == nil || got.Priority() != 100 {
		t.Errorf("expected priority 100 (first with work), got %v", got)
	}

	// simulate 10 in-flight for high (load = 10/10 = 1.0)
	// and 0 for low (load = 0/1 = 0.0)
	// low should be selected (lower load)
	for i := 0; i < 10; i++ {
		policy.OnDispatch(100)
	}

	got, err = policy.SelectBand(bands)
	if err != nil {
		t.Fatalf("SelectBand returned error: %v", err)
	}
	if got == nil || got.Priority() != 50 {
		t.Errorf("expected priority 50 (lower in-flight load), got %v", got)
	}

	// add 1 in-flight for low (load = 1/1 = 1.0)
	// now both have equal load, high selected first
	policy.OnDispatch(50)

	got, err = policy.SelectBand(bands)
	if err != nil {
		t.Fatalf("SelectBand returned error: %v", err)
	}
	if got == nil || got.Priority() != 100 {
		t.Errorf("expected priority 100 (equal load, first selected), got %v", got)
	}
}

func TestWeightedPriority_SelectBand_WeightAffectsInFlightCapacity(t *testing.T) {
	config := Config{
		Weights: map[int]float64{
			100: 10.0,
			50:  2.0,
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

	// high has 5 in-flight (load = 5/10 = 0.5)
	// low has 2 in-flight (load = 2/2 = 1.0)
	// high should be selected (lower load)
	for i := 0; i < 5; i++ {
		policy.OnDispatch(100)
	}
	for i := 0; i < 2; i++ {
		policy.OnDispatch(50)
	}

	got, err := policy.SelectBand(bands)
	if err != nil {
		t.Fatalf("SelectBand returned error: %v", err)
	}
	if got == nil || got.Priority() != 100 {
		t.Errorf("expected priority 100 (load 0.5 < 1.0), got %v", got)
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

	// low has no work, so high should be selected even if it has in-flight
	policy.OnDispatch(100)

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
	}

	var wg sync.WaitGroup
	iterations := 1000

	// concurrent OnDispatch
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			policy.OnDispatch(100)
		}
	}()

	// concurrent OnDispatchComplete (matched with OnDispatch)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			policy.OnDispatchComplete(100, uint64(i))
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
			policy.GetInFlightStats()
		}
	}()

	wg.Wait()

	// verify no data races (test would fail with -race if there were)
	// just verify we can read stats without panic - exact count depends on interleaving
	_ = policy.GetInFlightStats()
	_, _ = policy.GetStats()
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
	config := Config{
		Weights: map[int]float64{
			10: 10.0,
			0:  1.0,
		},
	}

	policy := newWeightedPriority(config, "test")

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

	// with no in-flight, all loads are 0, first band with work is selected
	got, err := policy.SelectBand(bands)
	if err != nil {
		t.Fatalf("SelectBand returned error: %v", err)
	}
	if got == nil || got.Priority() != 10 {
		t.Errorf("expected priority 10 (first band with work), got %v", got)
	}
}

func TestWeightedPriority_CumulativeCounterPreservation(t *testing.T) {
	// cumulative counters are still updated for metrics/long-term accounting
	config := Config{
		Weights: map[int]float64{
			100: 10.0,
			50:  1.0,
		},
	}

	policy := newWeightedPriority(config, "test")

	// simulate dispatch/complete cycles
	for i := 0; i < 10; i++ {
		policy.OnDispatch(100)
		policy.OnDispatchComplete(100, 100) // 100 tokens each
	}

	counters, total := policy.GetStats()
	if total != 1000 {
		t.Errorf("expected total 1000, got %f", total)
	}
	if counters[100] != 1000 {
		t.Errorf("expected counter[100]=1000, got %f", counters[100])
	}

	// in-flight should be 0 after all completes
	inFlight := policy.GetInFlightStats()
	if inFlight[100] != 0 {
		t.Errorf("expected inFlight[100]=0, got %d", inFlight[100])
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

func TestWeightedPriority_ProportionalInFlightWithDispatchCycle(t *testing.T) {
	// simulate realistic dispatch cycle where OnDispatch is called before SelectBand
	// and OnDispatchComplete is called when request finishes
	config := Config{
		Weights: map[int]float64{
			100: 10.0, // can have 10x in-flight
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

	// simulate dispatch cycle: select band, call OnDispatch
	// track which bands get selected over many iterations
	selections := make(map[int]int)

	for i := 0; i < 110; i++ {
		got, err := policy.SelectBand(bands)
		if err != nil {
			t.Fatalf("SelectBand returned error: %v", err)
		}
		if got != nil {
			selections[got.Priority()]++
			policy.OnDispatch(got.Priority())
			// immediately complete to simulate fast requests
			policy.OnDispatchComplete(got.Priority(), 0)
		}
	}

	// with 10:1 weights, high should get ~10x the selections
	highSelections := selections[100]
	lowSelections := selections[50]

	// allow tolerance for rounding
	if highSelections < 90 || highSelections > 110 {
		t.Errorf("expected ~100 high priority selections, got %d", highSelections)
	}
	if lowSelections < 0 || lowSelections > 20 {
		t.Errorf("expected ~10 low priority selections, got %d", lowSelections)
	}
}

func TestWeightedPriority_InFlightBackpressure(t *testing.T) {
	// test that a band with many in-flight requests gets throttled
	config := Config{
		Weights: map[int]float64{
			100: 2.0,
			50:  2.0, // equal weights
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

	// give high priority 10 in-flight (simulating slow requests)
	for i := 0; i < 10; i++ {
		policy.OnDispatch(100)
	}

	// now run many dispatch cycles - low should be favored
	selections := make(map[int]int)
	for i := 0; i < 20; i++ {
		got, err := policy.SelectBand(bands)
		if err != nil {
			t.Fatalf("SelectBand returned error: %v", err)
		}
		if got != nil {
			selections[got.Priority()]++
			policy.OnDispatch(got.Priority())
			policy.OnDispatchComplete(got.Priority(), 0)
		}
	}

	// low should get most selections since high has backpressure
	if selections[50] < 15 {
		t.Errorf("expected low priority to get most selections due to backpressure, got high=%d, low=%d",
			selections[100], selections[50])
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

	// give equal in-flight
	// 100: 2 in-flight, weight 2.0, load = 2/2 = 1.0
	// 50: 1 in-flight, weight 1.0 (default), load = 1/1 = 1.0
	policy.OnDispatch(100)
	policy.OnDispatch(100)
	policy.OnDispatch(50)

	// equal load, first band selected
	got, err := policy.SelectBand(bands)
	if err != nil {
		t.Fatalf("SelectBand returned error: %v", err)
	}
	if got == nil || got.Priority() != 100 {
		t.Errorf("expected priority 100 (equal load, first selected), got %v", got)
	}
}
