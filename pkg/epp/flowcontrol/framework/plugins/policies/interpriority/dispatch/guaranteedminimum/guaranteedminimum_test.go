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
	"time"

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

// mockClock provides a controllable clock for testing.
type mockClock struct {
	mu   sync.Mutex
	time time.Time
}

func newMockClock(t time.Time) *mockClock {
	return &mockClock{time: t}
}

func (c *mockClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.time
}

func (c *mockClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.time = c.time.Add(d)
}

func TestGuaranteedMinimum_SelectBand_StrictPriorityWhenNoStarvation(t *testing.T) {
	clock := newMockClock(time.Now())
	config := Config{
		WindowSize:  10 * time.Second,
		BucketCount: 10,
		MinGuaranteedRates: map[int]float64{
			50: 0.1, // batch gets 10% minimum
		},
	}

	policy := newGuaranteedMinimumWithClock(config, "test", clock.Now)

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
	clock := newMockClock(time.Now())
	config := Config{
		WindowSize:  10 * time.Second,
		BucketCount: 10,
		MinGuaranteedRates: map[int]float64{
			50: 0.1, // batch gets 10% minimum
		},
	}

	policy := newGuaranteedMinimumWithClock(config, "test", clock.Now)

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
	clock := newMockClock(time.Now())
	config := Config{
		WindowSize:  10 * time.Second,
		BucketCount: 10,
		MinGuaranteedRates: map[int]float64{
			50: 0.1, // batch gets 10% minimum
		},
	}

	policy := newGuaranteedMinimumWithClock(config, "test", clock.Now)

	// batch is at 0% but has no work
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

func TestGuaranteedMinimum_CircularBuffer_WindowRotation(t *testing.T) {
	clock := newMockClock(time.Now())
	config := Config{
		WindowSize:  10 * time.Second,
		BucketCount: 10, // 1 second per bucket
		MinGuaranteedRates: map[int]float64{
			50: 0.1,
		},
	}

	policy := newGuaranteedMinimumWithClock(config, "test", clock.Now)

	// fill the window with dispatches
	for i := 0; i < 100; i++ {
		policy.OnDispatchComplete(100, 0)
	}

	rates, total := policy.GetStats()
	if total != 100 {
		t.Errorf("expected total 100, got %d", total)
	}
	if rates[100] != 1.0 {
		t.Errorf("expected rate 1.0, got %f", rates[100])
	}

	// advance time past the window
	clock.Advance(11 * time.Second)

	// add a new dispatch after window expired
	policy.OnDispatchComplete(50, 0)

	rates, total = policy.GetStats()
	if total != 1 {
		t.Errorf("expected total 1 after window rotation, got %d", total)
	}
	if rates[50] != 1.0 {
		t.Errorf("expected batch rate 1.0 after rotation, got %f", rates[50])
	}
}

func TestGuaranteedMinimum_CircularBuffer_SlidingWindow(t *testing.T) {
	clock := newMockClock(time.Now())
	config := Config{
		WindowSize:  10 * time.Second,
		BucketCount: 10, // 1 second per bucket
		MinGuaranteedRates: map[int]float64{
			50: 0.1,
		},
	}

	policy := newGuaranteedMinimumWithClock(config, "test", clock.Now)

	// add dispatches in first bucket
	for i := 0; i < 10; i++ {
		policy.OnDispatchComplete(100, 0)
	}

	// advance 1 second, add more
	clock.Advance(1 * time.Second)
	for i := 0; i < 10; i++ {
		policy.OnDispatchComplete(50, 0)
	}

	rates, total := policy.GetStats()
	if total != 20 {
		t.Errorf("expected total 20, got %d", total)
	}

	// advance 9 more seconds (first bucket should age out on next operation)
	clock.Advance(9 * time.Second)

	// trigger bucket rotation
	policy.OnDispatchComplete(50, 0)

	rates, total = policy.GetStats()
	// first bucket (10 online) should be gone, second bucket (10 batch) + 1 new = 11
	if total != 11 {
		t.Errorf("expected total 11 after sliding, got %d", total)
	}
	if rates[100] != 0 {
		t.Errorf("expected online rate 0 after aging out, got %f", rates[100])
	}
}

func TestGuaranteedMinimum_ConcurrentAccess(t *testing.T) {
	clock := newMockClock(time.Now())
	config := Config{
		WindowSize:  10 * time.Second,
		BucketCount: 10,
		MinGuaranteedRates: map[int]float64{
			50: 0.1,
		},
	}

	policy := newGuaranteedMinimumWithClock(config, "test", clock.Now)

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
	if total != uint64(iterations) {
		t.Errorf("expected %d total dispatches, got %d", iterations, total)
	}
}

func TestGuaranteedMinimum_SelectsMostStarvedBand(t *testing.T) {
	clock := newMockClock(time.Now())
	config := Config{
		WindowSize:  10 * time.Second,
		BucketCount: 10,
		MinGuaranteedRates: map[int]float64{
			50: 0.20, // band 50 wants 20%, getting 5% (deficit 15%)
			25: 0.10, // band 25 wants 10%, getting 2% (deficit 8%)
		},
	}

	policy := newGuaranteedMinimumWithClock(config, "test", clock.Now)

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

	// band 50 has larger deficit (15% vs 8%), so it should be selected
	got, err := policy.SelectBand(bands)
	if err != nil {
		t.Fatalf("SelectBand returned error: %v", err)
	}
	if got == nil || got.Priority() != 50 {
		t.Errorf("expected priority 50 (most starved), got %v", got)
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
