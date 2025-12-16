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

package guaranteedminimum

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

func TestGuaranteedMinimum_SelectBand_StrictPriorityWhenNoStarvation(t *testing.T) {
	config := Config{
		MinGuaranteedRates: map[int]float64{
			50: 0.1, // batch gets 10% minimum
		},
	}

	policy := newGuaranteedMinimum(config, "test")

	// simulate dispatches where batch is at 20% (above its 10% minimum)
	for i := 0; i < 80; i++ {
		policy.OnDispatchComplete(100, 0) // online
	}
	for i := 0; i < 20; i++ {
		policy.OnDispatchComplete(50, 0) // batch
	}

	bands := []framework.PriorityBandAccessor{
		&mockPriorityBand{
			priority:     100,
			priorityName: "online",
			queues: []framework.FlowQueueAccessor{
				&mockFlowQueue{flowKey: types.FlowKey{ID: "flow1", Priority: 100}, length: 1},
			},
		},
		&mockPriorityBand{
			priority:     50,
			priorityName: "batch",
			queues: []framework.FlowQueueAccessor{
				&mockFlowQueue{flowKey: types.FlowKey{ID: "flow2", Priority: 50}, length: 1},
			},
		},
	}

	// batch is above minimum, so strict priority should apply
	got, err := policy.SelectBand(bands)
	if err != nil {
		t.Fatalf("SelectBand returned error: %v", err)
	}
	if got == nil || got.Priority() != 100 {
		t.Errorf("expected priority 100 (strict priority), got %v", got)
	}
}

func TestGuaranteedMinimum_SelectBand_BoostsStarvedBand(t *testing.T) {
	config := Config{
		MinGuaranteedRates: map[int]float64{
			50: 0.1, // batch gets 10% minimum
		},
	}

	policy := newGuaranteedMinimum(config, "test")

	// simulate dispatches where batch is at 2% (below its 10% minimum)
	for i := 0; i < 98; i++ {
		policy.OnDispatchComplete(100, 0) // online
	}
	for i := 0; i < 2; i++ {
		policy.OnDispatchComplete(50, 0) // batch
	}

	bands := []framework.PriorityBandAccessor{
		&mockPriorityBand{
			priority:     100,
			priorityName: "online",
			queues: []framework.FlowQueueAccessor{
				&mockFlowQueue{flowKey: types.FlowKey{ID: "flow1", Priority: 100}, length: 1},
			},
		},
		&mockPriorityBand{
			priority:     50,
			priorityName: "batch",
			queues: []framework.FlowQueueAccessor{
				&mockFlowQueue{flowKey: types.FlowKey{ID: "flow2", Priority: 50}, length: 1},
			},
		},
	}

	// batch is below minimum, so it should be boosted
	got, err := policy.SelectBand(bands)
	if err != nil {
		t.Fatalf("SelectBand returned error: %v", err)
	}
	if got == nil || got.Priority() != 50 {
		t.Errorf("expected priority 50 (starved band boost), got %v", got)
	}
}

func TestGuaranteedMinimum_SelectBand_SkipsEmptyStarvedBand(t *testing.T) {
	config := Config{
		MinGuaranteedRates: map[int]float64{
			50: 0.1, // batch gets 10% minimum
		},
	}

	policy := newGuaranteedMinimum(config, "test")

	// batch has 0 dispatches but has no work
	for i := 0; i < 100; i++ {
		policy.OnDispatchComplete(100, 0)
	}

	bands := []framework.PriorityBandAccessor{
		&mockPriorityBand{
			priority:     100,
			priorityName: "online",
			queues: []framework.FlowQueueAccessor{
				&mockFlowQueue{flowKey: types.FlowKey{ID: "flow1", Priority: 100}, length: 1},
			},
		},
		&mockPriorityBand{
			priority:     50,
			priorityName: "batch",
			queues:       []framework.FlowQueueAccessor{}, // no queues
		},
	}

	// batch has no work, so online should be selected
	got, err := policy.SelectBand(bands)
	if err != nil {
		t.Fatalf("SelectBand returned error: %v", err)
	}
	if got == nil || got.Priority() != 100 {
		t.Errorf("expected priority 100, got %v", got)
	}
}

func TestGuaranteedMinimum_ConcurrentAccess(t *testing.T) {
	config := Config{
		MinGuaranteedRates: map[int]float64{
			50: 0.1,
		},
	}

	policy := newGuaranteedMinimum(config, "test")

	bands := []framework.PriorityBandAccessor{
		&mockPriorityBand{
			priority:     100,
			priorityName: "online",
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

func TestGuaranteedMinimum_SelectsMostStarvedBand(t *testing.T) {
	config := Config{
		MinGuaranteedRates: map[int]float64{
			50: 0.20, // band 50 wants 20%, getting 5% (deficit 15%)
			25: 0.10, // band 25 wants 10%, getting 2% (deficit 8%)
		},
	}

	policy := newGuaranteedMinimum(config, "test")

	// 93 online, 5 for priority 50, 2 for priority 25
	for i := 0; i < 93; i++ {
		policy.OnDispatchComplete(100, 0)
	}
	for i := 0; i < 5; i++ {
		policy.OnDispatchComplete(50, 0)
	}
	for i := 0; i < 2; i++ {
		policy.OnDispatchComplete(25, 0)
	}

	bands := []framework.PriorityBandAccessor{
		&mockPriorityBand{
			priority:     100,
			priorityName: "online",
			queues: []framework.FlowQueueAccessor{
				&mockFlowQueue{flowKey: types.FlowKey{ID: "flow1", Priority: 100}, length: 1},
			},
		},
		&mockPriorityBand{
			priority:     50,
			priorityName: "batch",
			queues: []framework.FlowQueueAccessor{
				&mockFlowQueue{flowKey: types.FlowKey{ID: "flow2", Priority: 50}, length: 1},
			},
		},
		&mockPriorityBand{
			priority:     25,
			priorityName: "background",
			queues: []framework.FlowQueueAccessor{
				&mockFlowQueue{flowKey: types.FlowKey{ID: "flow3", Priority: 25}, length: 1},
			},
		},
	}

	// band 25 is most behind: normalized = 2/0.10 = 20
	// band 50 normalized = 5/0.20 = 25
	// online normalized = 93/1.0 = 93
	// both are behind online, but band 25 is more behind
	got, err := policy.SelectBand(bands)
	if err != nil {
		t.Fatalf("SelectBand returned error: %v", err)
	}
	if got == nil || got.Priority() != 25 {
		t.Errorf("expected priority 25 (most behind), got %v", got)
	}
}

func TestGuaranteedMinimum_TypedName(t *testing.T) {
	policy := newGuaranteedMinimum(DefaultConfig(), "test-policy")

	typedName := policy.TypedName()
	if typedName.Type != framework.InterPriorityDispatchPolicyType {
		t.Errorf("expected type %s, got %s", framework.InterPriorityDispatchPolicyType, typedName.Type)
	}
	if typedName.Name != "test-policy" {
		t.Errorf("expected name 'test-policy', got %s", typedName.Name)
	}
}

func TestGuaranteedMinimum_ColdStart_UsesStrictPriority(t *testing.T) {
	// on cold start (no dispatches yet), strict priority should apply
	config := Config{
		MinGuaranteedRates: map[int]float64{
			0: 0.05, // low priority gets 5% minimum
		},
	}

	policy := newGuaranteedMinimum(config, "test")

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
	// so no band is "behind" another, strict priority wins
	got, err := policy.SelectBand(bands)
	if err != nil {
		t.Fatalf("SelectBand returned error: %v", err)
	}
	if got == nil || got.Priority() != 10 {
		t.Errorf("expected priority 10 (strict priority on cold start), got %v", got)
	}
}

func TestGuaranteedMinimum_CounterPreservation_AcrossOperations(t *testing.T) {
	// counters are preserved across operations (unlike sliding window)
	config := Config{
		MinGuaranteedRates: map[int]float64{
			50: 0.1,
		},
	}

	policy := newGuaranteedMinimum(config, "test")

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

func TestGuaranteedMinimum_CostBasedAccounting(t *testing.T) {
	// verify that cost parameter is used for counter increments
	config := Config{
		MinGuaranteedRates: map[int]float64{
			50: 0.1,
		},
	}

	policy := newGuaranteedMinimum(config, "test")

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

func TestGuaranteedMinimum_NormalizedComparison(t *testing.T) {
	// verify the VTC-style normalized comparison works correctly
	config := Config{
		MinGuaranteedRates: map[int]float64{
			50: 0.1, // 10% guarantee
		},
	}

	policy := newGuaranteedMinimum(config, "test")

	// set up counters where batch is exactly at its fair share
	// online has 90, batch has 10 -> batch is at exactly 10%
	// normalized_online = 90/1.0 = 90
	// normalized_batch = 10/0.1 = 100
	// batch's normalized (100) > online's normalized (90), so batch is NOT behind
	for i := 0; i < 90; i++ {
		policy.OnDispatchComplete(100, 0)
	}
	for i := 0; i < 10; i++ {
		policy.OnDispatchComplete(50, 0)
	}

	bands := []framework.PriorityBandAccessor{
		&mockPriorityBand{
			priority:     100,
			priorityName: "online",
			queues: []framework.FlowQueueAccessor{
				&mockFlowQueue{flowKey: types.FlowKey{ID: "flow1", Priority: 100}, length: 1},
			},
		},
		&mockPriorityBand{
			priority:     50,
			priorityName: "batch",
			queues: []framework.FlowQueueAccessor{
				&mockFlowQueue{flowKey: types.FlowKey{ID: "flow2", Priority: 50}, length: 1},
			},
		},
	}

	// batch is at fair share, strict priority should apply
	got, err := policy.SelectBand(bands)
	if err != nil {
		t.Fatalf("SelectBand returned error: %v", err)
	}
	if got == nil || got.Priority() != 100 {
		t.Errorf("expected priority 100 (batch at fair share), got %v", got)
	}
}

func TestGuaranteedMinimum_HighPriorityWithGuarantee(t *testing.T) {
	// test when the high priority band also has a guarantee
	config := Config{
		MinGuaranteedRates: map[int]float64{
			100: 0.5, // online gets 50% minimum
			50:  0.1, // batch gets 10% minimum
		},
	}

	policy := newGuaranteedMinimum(config, "test")

	// online has 40 (below 50%), batch has 5 (below 10%)
	// normalized_online = 40/0.5 = 80
	// normalized_batch = 5/0.1 = 50
	// batch is more behind, but we compare against online's normalized
	for i := 0; i < 40; i++ {
		policy.OnDispatchComplete(100, 0)
	}
	for i := 0; i < 5; i++ {
		policy.OnDispatchComplete(50, 0)
	}

	bands := []framework.PriorityBandAccessor{
		&mockPriorityBand{
			priority:     100,
			priorityName: "online",
			queues: []framework.FlowQueueAccessor{
				&mockFlowQueue{flowKey: types.FlowKey{ID: "flow1", Priority: 100}, length: 1},
			},
		},
		&mockPriorityBand{
			priority:     50,
			priorityName: "batch",
			queues: []framework.FlowQueueAccessor{
				&mockFlowQueue{flowKey: types.FlowKey{ID: "flow2", Priority: 50}, length: 1},
			},
		},
	}

	// batch (normalized=50) is behind online (normalized=80), so batch should be boosted
	got, err := policy.SelectBand(bands)
	if err != nil {
		t.Fatalf("SelectBand returned error: %v", err)
	}
	if got == nil || got.Priority() != 50 {
		t.Errorf("expected priority 50 (behind high priority), got %v", got)
	}
}

func TestGuaranteedMinimum_EmptyBands(t *testing.T) {
	policy := newGuaranteedMinimum(DefaultConfig(), "test")

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

func TestGuaranteedMinimum_AllBandsEmpty(t *testing.T) {
	config := Config{
		MinGuaranteedRates: map[int]float64{
			50: 0.1,
		},
	}

	policy := newGuaranteedMinimum(config, "test")

	bands := []framework.PriorityBandAccessor{
		&mockPriorityBand{
			priority:     100,
			priorityName: "online",
			queues:       []framework.FlowQueueAccessor{}, // no work
		},
		&mockPriorityBand{
			priority:     50,
			priorityName: "batch",
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
