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

package vtc

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/flowcontrol/framework"
	frameworkmocks "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/flowcontrol/framework/mocks"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/flowcontrol/types"
	typesmocks "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/flowcontrol/types/mocks"
)

var (
	flow1Key = types.FlowKey{ID: "flow1", Priority: 0}
	flow2Key = types.FlowKey{ID: "flow2", Priority: 0}
	flow3Key = types.FlowKey{ID: "flow3", Priority: 0}
)

func TestVTC_TypedName(t *testing.T) {
	t.Parallel()
	policy := newVTCWithState(DefaultConfig(), &state{}, VTCPolicyName)
	assert.Equal(t, VTCPolicyName, policy.TypedName().Name, "Name should match the policy's constant")
}

func TestVTC_SelectQueue_LowestCounterSelected(t *testing.T) {
	t.Parallel()
	config := DefaultConfig()
	policy := newVTCWithState(config, &state{}, VTCPolicyName)

	// pre-populate counters to ensure flow2 has the lowest
	policy.state.updateCounter("flow1", 1000.0)
	policy.state.updateCounter("flow2", 100.0) // lowest
	policy.state.updateCounter("flow3", 2000.0)

	// setup queues
	item1 := typesmocks.NewMockQueueItemAccessor(100, "req1", flow1Key)
	item2 := typesmocks.NewMockQueueItemAccessor(200, "req2", flow2Key)
	item3 := typesmocks.NewMockQueueItemAccessor(300, "req3", flow3Key)

	queue1 := &frameworkmocks.MockFlowQueueAccessor{LenV: 1, FlowKeyV: flow1Key, PeekHeadV: item1}
	queue2 := &frameworkmocks.MockFlowQueueAccessor{LenV: 1, FlowKeyV: flow2Key, PeekHeadV: item2}
	queue3 := &frameworkmocks.MockFlowQueueAccessor{LenV: 1, FlowKeyV: flow3Key, PeekHeadV: item3}

	mockBand := &frameworkmocks.MockPriorityBandAccessor{
		FlowKeysFunc: func() []types.FlowKey { return []types.FlowKey{flow1Key, flow2Key, flow3Key} },
		QueueFunc: func(id string) framework.FlowQueueAccessor {
			switch id {
			case "flow1":
				return queue1
			case "flow2":
				return queue2
			case "flow3":
				return queue3
			}
			return nil
		},
	}

	selected, err := policy.SelectQueue(mockBand)
	require.NoError(t, err, "SelectQueue should not error")
	require.NotNil(t, selected, "SelectQueue should select a queue")
	assert.Equal(t, "flow2", selected.FlowKey().ID, "Should select flow with lowest counter")
}

func TestVTC_SelectQueue_CounterUpdates(t *testing.T) {
	t.Parallel()
	config := DefaultConfig()
	policy := newVTCWithState(config, &state{}, VTCPolicyName)

	// all flows start with zero counters
	item1 := typesmocks.NewMockQueueItemAccessor(100, "req1", flow1Key)
	item2 := typesmocks.NewMockQueueItemAccessor(200, "req2", flow2Key)

	queue1 := &frameworkmocks.MockFlowQueueAccessor{LenV: 1, FlowKeyV: flow1Key, PeekHeadV: item1}
	queue2 := &frameworkmocks.MockFlowQueueAccessor{LenV: 1, FlowKeyV: flow2Key, PeekHeadV: item2}

	mockBand := &frameworkmocks.MockPriorityBandAccessor{
		FlowKeysFunc: func() []types.FlowKey { return []types.FlowKey{flow1Key, flow2Key} },
		QueueFunc: func(id string) framework.FlowQueueAccessor {
			if id == "flow1" {
				return queue1
			}
			return queue2
		},
	}

	// first selection should pick flow1 (lexicographically first when counters tied)
	selected, err := policy.SelectQueue(mockBand)
	require.NoError(t, err, "SelectQueue should not error")
	require.NotNil(t, selected, "SelectQueue should select a queue")
	assert.Equal(t, "flow1", selected.FlowKey().ID, "First selection should be flow1")

	// counters should NOT be updated by SelectQueue (updated by completion plugin instead)
	counter1 := policy.state.getCounter("flow1")
	assert.Equal(t, 0.0, counter1, "flow1 counter should not be updated by SelectQueue")

	// manually update flow1 counter to simulate completion
	policy.state.updateCounter("flow1", 100.0)

	// second selection should now pick flow2 (since flow1 has higher counter)
	selected, err = policy.SelectQueue(mockBand)
	require.NoError(t, err, "SelectQueue should not error")
	require.NotNil(t, selected, "SelectQueue should select a queue")
	assert.Equal(t, "flow2", selected.FlowKey().ID, "Second selection should be flow2 after flow1 counter increased")
}

func TestVTC_SelectQueue_SkipsEmptyQueues(t *testing.T) {
	t.Parallel()
	config := DefaultConfig()
	policy := newVTCWithState(config, &state{}, VTCPolicyName)

	// flow2 is empty, flows 1 and 3 are not
	item1 := typesmocks.NewMockQueueItemAccessor(100, "req1", flow1Key)
	item3 := typesmocks.NewMockQueueItemAccessor(300, "req3", flow3Key)

	queue1 := &frameworkmocks.MockFlowQueueAccessor{LenV: 1, FlowKeyV: flow1Key, PeekHeadV: item1}
	queue2Empty := &frameworkmocks.MockFlowQueueAccessor{LenV: 0, FlowKeyV: flow2Key}
	queue3 := &frameworkmocks.MockFlowQueueAccessor{LenV: 1, FlowKeyV: flow3Key, PeekHeadV: item3}

	mockBand := &frameworkmocks.MockPriorityBandAccessor{
		FlowKeysFunc: func() []types.FlowKey { return []types.FlowKey{flow1Key, flow2Key, flow3Key} },
		QueueFunc: func(id string) framework.FlowQueueAccessor {
			switch id {
			case "flow1":
				return queue1
			case "flow2":
				return queue2Empty
			case "flow3":
				return queue3
			}
			return nil
		},
	}

	selected, err := policy.SelectQueue(mockBand)
	require.NoError(t, err, "SelectQueue should not error")
	require.NotNil(t, selected, "SelectQueue should select a non-empty queue")
	assert.NotEqual(t, "flow2", selected.FlowKey().ID, "Should skip empty flow2")
}

func TestVTC_SelectQueue_NilBand(t *testing.T) {
	t.Parallel()
	policy := newVTCWithState(DefaultConfig(), &state{}, VTCPolicyName)

	selected, err := policy.SelectQueue(nil)
	assert.NoError(t, err, "SelectQueue should not error on nil band")
	assert.Nil(t, selected, "SelectQueue should return nil for nil band")
}

func TestVTC_SelectQueue_EmptyBand(t *testing.T) {
	t.Parallel()
	policy := newVTCWithState(DefaultConfig(), &state{}, VTCPolicyName)

	mockBand := &frameworkmocks.MockPriorityBandAccessor{
		FlowKeysFunc: func() []types.FlowKey { return []types.FlowKey{} },
	}

	selected, err := policy.SelectQueue(mockBand)
	assert.NoError(t, err, "SelectQueue should not error on empty band")
	assert.Nil(t, selected, "SelectQueue should return nil for empty band")
}

func TestVTC_SelectQueue_AllQueuesEmpty(t *testing.T) {
	t.Parallel()
	policy := newVTCWithState(DefaultConfig(), &state{}, VTCPolicyName)

	queue1Empty := &frameworkmocks.MockFlowQueueAccessor{LenV: 0, FlowKeyV: flow1Key}
	queue2Empty := &frameworkmocks.MockFlowQueueAccessor{LenV: 0, FlowKeyV: flow2Key}

	mockBand := &frameworkmocks.MockPriorityBandAccessor{
		FlowKeysFunc: func() []types.FlowKey { return []types.FlowKey{flow1Key, flow2Key} },
		QueueFunc: func(id string) framework.FlowQueueAccessor {
			if id == "flow1" {
				return queue1Empty
			}
			return queue2Empty
		},
	}

	selected, err := policy.SelectQueue(mockBand)
	assert.NoError(t, err, "SelectQueue should not error when all queues empty")
	assert.Nil(t, selected, "SelectQueue should return nil when all queues empty")
}

func TestVTC_FairnessOverTime(t *testing.T) {
	t.Parallel()
	config := DefaultConfig()
	policy := newVTCWithState(config, &state{}, VTCPolicyName)

	// flow1 has 100 tokens per request, flow2 has 200 tokens per request
	item1 := typesmocks.NewMockQueueItemAccessor(100, "req1", flow1Key)
	item2 := typesmocks.NewMockQueueItemAccessor(200, "req2", flow2Key)

	queue1 := &frameworkmocks.MockFlowQueueAccessor{LenV: 1, FlowKeyV: flow1Key, PeekHeadV: item1}
	queue2 := &frameworkmocks.MockFlowQueueAccessor{LenV: 1, FlowKeyV: flow2Key, PeekHeadV: item2}

	mockBand := &frameworkmocks.MockPriorityBandAccessor{
		FlowKeysFunc: func() []types.FlowKey { return []types.FlowKey{flow1Key, flow2Key} },
		QueueFunc: func(id string) framework.FlowQueueAccessor {
			if id == "flow1" {
				return queue1
			}
			return queue2
		},
	}

	// simulate 10 dispatch+completion cycles
	selections := make(map[string]int)
	for i := 0; i < 10; i++ {
		selected, err := policy.SelectQueue(mockBand)
		require.NoError(t, err, "SelectQueue should not error")
		require.NotNil(t, selected, "SelectQueue should select a queue")
		selections[selected.FlowKey().ID]++

		// simulate completion with different token counts
		if selected.FlowKey().ID == "flow1" {
			policy.state.updateCounter("flow1", 100.0) // 100 tokens
		} else {
			policy.state.updateCounter("flow2", 200.0) // 200 tokens
		}
	}

	// flow2 has larger token counts so it should be selected less often to maintain fairness
	// flow1 has smaller token counts so it should be selected more often
	assert.Greater(t, selections["flow1"], selections["flow2"],
		"flow1 (fewer tokens) should be selected more often than flow2 (more tokens) for fairness")
}

func TestVTC_ConfigurableWeights(t *testing.T) {
	t.Parallel()

	// test with custom weights
	config := Config{
		InputTokenWeight:  2.0,
		OutputTokenWeight: 4.0,
	}
	policy := newVTCWithState(config, &state{}, VTCPolicyName)

	// simulate a completion with 50 input tokens and 25 output tokens
	policy.OnRequestComplete("flow1", 0, 50, 25)

	// verify weighted calculation:
	// inputService = 50 * 2.0 = 100.0
	// outputService = 25 * 4.0 = 100.0
	// totalService = 200.0
	counter := policy.state.getCounter("flow1")
	assert.Equal(t, 200.0, counter, "Counter should reflect weighted token calculation")
}

func TestVTC_NegativeTokenValidation(t *testing.T) {
	t.Parallel()
	config := DefaultConfig()
	policy := newVTCWithState(config, &state{}, VTCPolicyName)

	// attempt to update with negative input tokens
	policy.OnRequestComplete("flow1", 0, -100, 50)
	counter1 := policy.state.getCounter("flow1")
	assert.Equal(t, 0.0, counter1, "Counter should remain at zero when input tokens are negative")

	// attempt to update with negative output tokens
	policy.OnRequestComplete("flow2", 0, 50, -100)
	counter2 := policy.state.getCounter("flow2")
	assert.Equal(t, 0.0, counter2, "Counter should remain at zero when output tokens are negative")

	// attempt to update with both negative
	policy.OnRequestComplete("flow3", 0, -50, -100)
	counter3 := policy.state.getCounter("flow3")
	assert.Equal(t, 0.0, counter3, "Counter should remain at zero when both token counts are negative")

	// verify that valid update works after invalid attempts
	policy.OnRequestComplete("flow1", 0, 100, 50)
	counter1Updated := policy.state.getCounter("flow1")
	assert.Equal(t, 150.0, counter1Updated, "Counter should update correctly with valid token counts")
}

func TestVTC_ZeroTokensAreValid(t *testing.T) {
	t.Parallel()
	policy := newVTCWithState(DefaultConfig(), &state{}, VTCPolicyName)

	// zero tokens should be valid (no-op but not rejected)
	policy.OnRequestComplete("flow1", 0, 0, 0)
	counter := policy.state.getCounter("flow1")
	assert.Equal(t, 0.0, counter, "Zero tokens should be valid")

	// partial zero should work
	policy.OnRequestComplete("flow1", 0, 100, 0)
	assert.Equal(t, 100.0, policy.state.getCounter("flow1"))

	policy.OnRequestComplete("flow1", 0, 0, 50)
	assert.Equal(t, 150.0, policy.state.getCounter("flow1"))
}

func TestVTC_SelectQueue_LexicographicTieBreaker(t *testing.T) {
	t.Parallel()
	policy := newVTCWithState(DefaultConfig(), &state{}, VTCPolicyName)

	// all flows have identical counters (0.0) - should pick lexicographically first
	flowA := types.FlowKey{ID: "aaa", Priority: 0}
	flowB := types.FlowKey{ID: "bbb", Priority: 0}
	flowZ := types.FlowKey{ID: "zzz", Priority: 0}

	itemA := typesmocks.NewMockQueueItemAccessor(100, "req-a", flowA)
	itemB := typesmocks.NewMockQueueItemAccessor(100, "req-b", flowB)
	itemZ := typesmocks.NewMockQueueItemAccessor(100, "req-z", flowZ)

	queueA := &frameworkmocks.MockFlowQueueAccessor{LenV: 1, FlowKeyV: flowA, PeekHeadV: itemA}
	queueB := &frameworkmocks.MockFlowQueueAccessor{LenV: 1, FlowKeyV: flowB, PeekHeadV: itemB}
	queueZ := &frameworkmocks.MockFlowQueueAccessor{LenV: 1, FlowKeyV: flowZ, PeekHeadV: itemZ}

	mockBand := &frameworkmocks.MockPriorityBandAccessor{
		FlowKeysFunc: func() []types.FlowKey { return []types.FlowKey{flowZ, flowB, flowA} }, // shuffled order
		QueueFunc: func(id string) framework.FlowQueueAccessor {
			switch id {
			case "aaa":
				return queueA
			case "bbb":
				return queueB
			case "zzz":
				return queueZ
			}
			return nil
		},
	}

	selected, err := policy.SelectQueue(mockBand)
	require.NoError(t, err)
	require.NotNil(t, selected)
	assert.Equal(t, "aaa", selected.FlowKey().ID, "Should pick lexicographically first when counters are tied")
}

func TestVTC_SelectQueue_NilQueueFromBand(t *testing.T) {
	t.Parallel()
	policy := newVTCWithState(DefaultConfig(), &state{}, VTCPolicyName)

	item2 := typesmocks.NewMockQueueItemAccessor(100, "req2", flow2Key)
	queue2 := &frameworkmocks.MockFlowQueueAccessor{LenV: 1, FlowKeyV: flow2Key, PeekHeadV: item2}

	mockBand := &frameworkmocks.MockPriorityBandAccessor{
		FlowKeysFunc: func() []types.FlowKey { return []types.FlowKey{flow1Key, flow2Key} },
		QueueFunc: func(id string) framework.FlowQueueAccessor {
			if id == "flow1" {
				return nil // band returns nil for this flow
			}
			return queue2
		},
	}

	selected, err := policy.SelectQueue(mockBand)
	require.NoError(t, err)
	require.NotNil(t, selected)
	assert.Equal(t, "flow2", selected.FlowKey().ID, "Should skip nil queue and select valid one")
}

func TestVTC_ConfigurableWeights_EdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		inputWeight    float64
		outputWeight   float64
		inputTokens    int
		outputTokens   int
		expectedResult float64
	}{
		{
			name:           "zero input weight",
			inputWeight:    0.0,
			outputWeight:   1.0,
			inputTokens:    100,
			outputTokens:   50,
			expectedResult: 50.0,
		},
		{
			name:           "zero output weight",
			inputWeight:    1.0,
			outputWeight:   0.0,
			inputTokens:    100,
			outputTokens:   50,
			expectedResult: 100.0,
		},
		{
			name:           "both weights zero",
			inputWeight:    0.0,
			outputWeight:   0.0,
			inputTokens:    100,
			outputTokens:   50,
			expectedResult: 0.0,
		},
		{
			name:           "fractional weights",
			inputWeight:    0.5,
			outputWeight:   0.25,
			inputTokens:    100,
			outputTokens:   100,
			expectedResult: 75.0, // 100*0.5 + 100*0.25
		},
		{
			name:           "high weight ratio favoring output",
			inputWeight:    1.0,
			outputWeight:   10.0,
			inputTokens:    100,
			outputTokens:   10,
			expectedResult: 200.0, // 100*1 + 10*10
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			config := Config{
				InputTokenWeight:  tt.inputWeight,
				OutputTokenWeight: tt.outputWeight,
			}
			policy := newVTCWithState(config, &state{}, VTCPolicyName)

			policy.OnRequestComplete("flow1", 0, tt.inputTokens, tt.outputTokens)
			counter := policy.state.getCounter("flow1")
			assert.Equal(t, tt.expectedResult, counter)
		})
	}
}

// TestVTC_Integration_EndToEnd validates the complete flow from dispatch to completion to counter updates.
// this simulates the production scenario where:
// 1. VTC policy selects flows for dispatch
// 2. Requests complete with token usage
// 3. Completion listener updates counters
// 4. Next dispatch reflects updated counters
func TestVTC_Integration_EndToEnd(t *testing.T) {
	t.Parallel()

	// use isolated state for this test to avoid interference with other tests
	testState := &state{}
	config := DefaultConfig()
	policy := newVTCWithState(config, testState, VTCPolicyName)

	// get a completion listener that shares the same state
	// vtc implements framework.RequestCompletionListener
	listener := policy

	// setup three flows with different request sizes
	// flow1: 100 tokens per request
	// flow2: 200 tokens per request
	// flow3: 150 tokens per request
	item1 := typesmocks.NewMockQueueItemAccessor(100, "req1", flow1Key)
	item2 := typesmocks.NewMockQueueItemAccessor(200, "req2", flow2Key)
	item3 := typesmocks.NewMockQueueItemAccessor(150, "req3", flow3Key)

	queue1 := &frameworkmocks.MockFlowQueueAccessor{LenV: 1, FlowKeyV: flow1Key, PeekHeadV: item1}
	queue2 := &frameworkmocks.MockFlowQueueAccessor{LenV: 1, FlowKeyV: flow2Key, PeekHeadV: item2}
	queue3 := &frameworkmocks.MockFlowQueueAccessor{LenV: 1, FlowKeyV: flow3Key, PeekHeadV: item3}

	mockBand := &frameworkmocks.MockPriorityBandAccessor{
		FlowKeysFunc: func() []types.FlowKey { return []types.FlowKey{flow1Key, flow2Key, flow3Key} },
		QueueFunc: func(id string) framework.FlowQueueAccessor {
			switch id {
			case "flow1":
				return queue1
			case "flow2":
				return queue2
			case "flow3":
				return queue3
			}
			return nil
		},
	}

	// simulate 10 dispatch cycles
	type dispatchRecord struct {
		flowID       string
		inputTokens  int
		outputTokens int
	}
	var dispatches []dispatchRecord

	for i := 0; i < 10; i++ {
		// select next queue to dispatch
		selected, err := policy.SelectQueue(mockBand)
		require.NoError(t, err, "SelectQueue should not error")
		require.NotNil(t, selected, "SelectQueue should select a queue")

		flowID := selected.FlowKey().ID

		// simulate different token usage per flow
		var inputTokens, outputTokens int
		switch flowID {
		case "flow1":
			inputTokens, outputTokens = 50, 50 // 100 total
		case "flow2":
			inputTokens, outputTokens = 100, 100 // 200 total
		case "flow3":
			inputTokens, outputTokens = 75, 75 // 150 total
		}

		// record the dispatch
		dispatches = append(dispatches, dispatchRecord{
			flowID:       flowID,
			inputTokens:  inputTokens,
			outputTokens: outputTokens,
		})

		// simulate request completion via the listener
		listener.OnRequestComplete(flowID, 0, inputTokens, outputTokens)
	}

	// verify that VTC achieved token-level fairness
	// flows with higher token usage should have been selected less often
	dispatchCounts := make(map[string]int)
	totalTokens := make(map[string]float64)

	for _, d := range dispatches {
		dispatchCounts[d.flowID]++
		totalTokens[d.flowID] += float64(d.inputTokens + d.outputTokens)
	}

	// verify counters match expected totals
	for flowID, expectedTotal := range totalTokens {
		actualCounter := testState.getCounter(flowID)
		assert.Equal(t, expectedTotal, actualCounter,
			"Counter for %s should match total tokens consumed", flowID)
	}

	// verify fairness: flows should get roughly equal total service
	// allow some variation due to discrete dispatches
	avgTotal := (totalTokens["flow1"] + totalTokens["flow2"] + totalTokens["flow3"]) / 3.0
	for flowID, total := range totalTokens {
		deviation := (total - avgTotal) / avgTotal * 100
		assert.LessOrEqual(t, deviation, 50.0,
			"Flow %s total service should be within 50%% of average (got %.1f, avg %.1f, deviation %.1f%%)",
			flowID, total, avgTotal, deviation)
	}

	// verify that flow2 (highest per-request cost) was selected least often
	assert.LessOrEqual(t, dispatchCounts["flow2"], dispatchCounts["flow1"],
		"flow2 (200 tokens) should be selected no more than flow1 (100 tokens)")
	assert.LessOrEqual(t, dispatchCounts["flow2"], dispatchCounts["flow3"],
		"flow2 (200 tokens) should be selected no more than flow3 (150 tokens)")
}

// TestVTC_Integration_GlobalStateSharing verifies that GetGlobalCompletionListener returns
// a listener that shares state with policies created via the standard factory.
func TestVTC_Integration_GlobalStateSharing(t *testing.T) {
	// Note: This test uses globalState, so it cannot run in parallel with other tests
	// that also use globalState. However, since we're testing the global state behavior,
	// this is intentional.

	// clear global state before test (best effort, may have residual state from other tests)
	// in production, global state is never cleared
	testFlowID := "test-flow-" + t.Name()

	// create a policy instance via the standard factory (uses globalState)
	policy1 := newVTC(DefaultConfig(), VTCPolicyName).(*vtc)

	// get the global completion listener (also uses globalState)
	listener := GetGlobalCompletionListener()

	// verify initial counter is zero
	initialCounter := policy1.state.getCounter(testFlowID)
	assert.Equal(t, 0.0, initialCounter, "Initial counter should be zero")

	// simulate completion via the listener
	listener.OnRequestComplete(testFlowID, 0, 100, 50)

	// create a second policy instance
	policy2 := newVTC(DefaultConfig(), VTCPolicyName).(*vtc)

	// verify both policies see the updated counter
	counter1 := policy1.state.getCounter(testFlowID)
	counter2 := policy2.state.getCounter(testFlowID)

	expectedTotal := 150.0
	assert.Equal(t, expectedTotal, counter1, "policy1 should see updated counter")
	assert.Equal(t, expectedTotal, counter2, "policy2 should see updated counter (shared state)")
	assert.Equal(t, counter1, counter2, "Both policies should see identical counter values")

	// add more via policy1's OnRequestComplete
	policy1.OnRequestComplete(testFlowID, 0, 50, 50)

	// verify listener sees the update
	counter3 := policy2.state.getCounter(testFlowID)
	assert.Equal(t, 250.0, counter3, "All instances should see cumulative updates")
}

// TestVTC_Integration_MultipleFlowsBalancing validates that VTC properly balances
// multiple flows over many dispatch cycles, ensuring token-level fairness.
func TestVTC_Integration_MultipleFlowsBalancing(t *testing.T) {
	t.Parallel()

	testState := &state{}
	config := DefaultConfig()
	policy := newVTCWithState(config, testState, VTCPolicyName)
	// vtc implements framework.RequestCompletionListener
	listener := policy

	// create 5 flows with varying token requirements
	flowConfigs := map[string]struct {
		key          types.FlowKey
		inputTokens  int
		outputTokens int
	}{
		"light":   {types.FlowKey{ID: "light", Priority: 0}, 10, 10},   // 20 total
		"medium":  {types.FlowKey{ID: "medium", Priority: 0}, 50, 50},  // 100 total
		"heavy":   {types.FlowKey{ID: "heavy", Priority: 0}, 150, 150}, // 300 total
		"xlarge":  {types.FlowKey{ID: "xlarge", Priority: 0}, 200, 200}, // 400 total
		"extreme": {types.FlowKey{ID: "extreme", Priority: 0}, 300, 200}, // 500 total
	}

	// setup mock queues
	queues := make(map[string]*frameworkmocks.MockFlowQueueAccessor)
	flowKeys := make([]types.FlowKey, 0, len(flowConfigs))

	for name, cfg := range flowConfigs {
		item := typesmocks.NewMockQueueItemAccessor(100, "req-"+name, cfg.key)
		queues[name] = &frameworkmocks.MockFlowQueueAccessor{
			LenV:      1,
			FlowKeyV:  cfg.key,
			PeekHeadV: item,
		}
		flowKeys = append(flowKeys, cfg.key)
	}

	mockBand := &frameworkmocks.MockPriorityBandAccessor{
		FlowKeysFunc: func() []types.FlowKey { return flowKeys },
		QueueFunc: func(id string) framework.FlowQueueAccessor {
			for name, cfg := range flowConfigs {
				if cfg.key.ID == id {
					return queues[name]
				}
			}
			return nil
		},
	}

	// run 100 dispatch cycles
	totalDispatches := 100
	dispatchCounts := make(map[string]int)
	totalTokensServed := make(map[string]float64)

	for i := 0; i < totalDispatches; i++ {
		selected, err := policy.SelectQueue(mockBand)
		require.NoError(t, err)
		require.NotNil(t, selected)

		flowID := selected.FlowKey().ID
		dispatchCounts[flowID]++

		// find config for this flow
		var cfg struct {
			key          types.FlowKey
			inputTokens  int
			outputTokens int
		}
		for _, c := range flowConfigs {
			if c.key.ID == flowID {
				cfg = c
				break
			}
		}

		// simulate completion
		listener.OnRequestComplete(flowID, 0, cfg.inputTokens, cfg.outputTokens)
		totalTokensServed[flowID] += float64(cfg.inputTokens + cfg.outputTokens)
	}

	// verify token-level fairness: all flows should receive similar total tokens
	var tokenTotals []float64
	for _, total := range totalTokensServed {
		tokenTotals = append(tokenTotals, total)
	}

	// calculate average and standard deviation
	var sum float64
	for _, total := range tokenTotals {
		sum += total
	}
	avg := sum / float64(len(tokenTotals))

	var varianceSum float64
	for _, total := range tokenTotals {
		diff := total - avg
		varianceSum += diff * diff
	}
	variance := varianceSum / float64(len(tokenTotals))

	// verify all flows are within reasonable range of average (within 20%)
	for flowID, total := range totalTokensServed {
		deviation := ((total - avg) / avg) * 100
		assert.InDelta(t, avg, total, avg*0.20,
			"Flow %s should be within 20%% of average (got %.0f, avg %.0f, deviation %.1f%%)",
			flowID, total, avg, deviation)
	}

	// verify that expensive flows (extreme, xlarge) got fewer dispatches
	// while cheap flows (light, medium) got more dispatches
	assert.Greater(t, dispatchCounts["light"], dispatchCounts["extreme"],
		"light flow should get more dispatches than extreme flow")
	assert.Greater(t, dispatchCounts["medium"], dispatchCounts["xlarge"],
		"medium flow should get more dispatches than xlarge flow")

	// log dispatch distribution for debugging
	t.Logf("Dispatch counts: light=%d, medium=%d, heavy=%d, xlarge=%d, extreme=%d",
		dispatchCounts["light"], dispatchCounts["medium"], dispatchCounts["heavy"],
		dispatchCounts["xlarge"], dispatchCounts["extreme"])
	t.Logf("Token totals: light=%.0f, medium=%.0f, heavy=%.0f, xlarge=%.0f, extreme=%.0f",
		totalTokensServed["light"], totalTokensServed["medium"], totalTokensServed["heavy"],
		totalTokensServed["xlarge"], totalTokensServed["extreme"])
	t.Logf("Average tokens per flow: %.2f, Variance: %.2f", avg, variance)
}

// TestVTC_CleanupMechanism verifies that the cleanup goroutine properly removes stale counters
// while preserving active ones.
func TestVTC_CleanupMechanism(t *testing.T) {
	t.Parallel()

	// use aggressive cleanup settings for faster test
	config := Config{
		InputTokenWeight:       1.0,
		OutputTokenWeight:      1.0,
		CounterCleanupInterval: 100 * time.Millisecond,
		CounterTTL:             200 * time.Millisecond,
	}

	testState := &state{}
	policy := newVTCWithState(config, testState, VTCPolicyName)

	// start cleanup goroutine
	testState.startCleanup(config)

	// create three counters
	policy.state.updateCounter("stale-flow", 100.0)
	policy.state.updateCounter("active-flow", 200.0)
	policy.state.updateCounter("intermittent-flow", 300.0)

	// verify all counters exist
	assert.Equal(t, 100.0, policy.state.getCounter("stale-flow"))
	assert.Equal(t, 200.0, policy.state.getCounter("active-flow"))
	assert.Equal(t, 300.0, policy.state.getCounter("intermittent-flow"))

	// keep active-flow alive by accessing it periodically
	stopKeepAlive := make(chan struct{})
	defer close(stopKeepAlive)
	go func() {
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				policy.state.getCounter("active-flow")
			case <-stopKeepAlive:
				return
			}
		}
	}()

	// wait for stale counter to age beyond TTL (200ms)
	// plus cleanup interval (100ms) plus buffer
	time.Sleep(400 * time.Millisecond)

	// verify the entry was actually deleted from the map (check before calling getCounter)
	_, existsInCounters := testState.virtualCounters.Load("stale-flow")
	_, existsInTimes := testState.lastAccessTimes.Load("stale-flow")
	assert.False(t, existsInCounters, "stale-flow should be deleted from virtualCounters")
	assert.False(t, existsInTimes, "stale-flow should be deleted from lastAccessTimes")

	// verify stale-flow was cleaned up (calling getCounter will recreate lastAccessTimes entry)
	staleCounter := policy.state.getCounter("stale-flow")
	assert.Equal(t, 0.0, staleCounter, "stale-flow should be cleaned up and return 0")

	// verify active-flow still exists with correct value
	activeCounter := policy.state.getCounter("active-flow")
	assert.Equal(t, 200.0, activeCounter, "active-flow should remain with original value")

	// verify intermittent-flow was also cleaned up (not accessed)
	intermittentCounter := policy.state.getCounter("intermittent-flow")
	assert.Equal(t, 0.0, intermittentCounter, "intermittent-flow should be cleaned up")
}

// TestVTC_StopCleanup verifies that stopCleanup properly terminates the cleanup goroutine.
func TestVTC_StopCleanup(t *testing.T) {
	t.Parallel()

	config := Config{
		InputTokenWeight:       1.0,
		OutputTokenWeight:      1.0,
		CounterCleanupInterval: 50 * time.Millisecond,
		CounterTTL:             100 * time.Millisecond,
	}

	testState := &state{}
	testState.startCleanup(config)

	// add a counter
	testState.updateCounter("flow1", 100.0)

	// stop cleanup
	testState.stopCleanup()

	// calling stopCleanup multiple times should be safe
	testState.stopCleanup()
	testState.stopCleanup()

	// wait longer than TTL - counter should NOT be cleaned up since cleanup is stopped
	time.Sleep(200 * time.Millisecond)

	// counter should still exist
	counter := testState.getCounter("flow1")
	assert.Equal(t, 100.0, counter, "Counter should persist after cleanup is stopped")
}

// TestVTC_AtomicFloat64_HighContention stresses the atomicFloat64 CAS loop under contention.
func TestVTC_AtomicFloat64_HighContention(t *testing.T) {
	t.Parallel()

	testState := &state{}

	var wg sync.WaitGroup
	numGoroutines := 50
	updatesPerGoroutine := 100

	// all goroutines update the same flow concurrently
	wg.Add(numGoroutines)
	for range numGoroutines {
		go func() {
			defer wg.Done()
			for range updatesPerGoroutine {
				testState.updateCounter("contended-flow", 1.0)
			}
		}()
	}
	wg.Wait()

	expectedTotal := float64(numGoroutines * updatesPerGoroutine)
	actualCounter := testState.getCounter("contended-flow")
	assert.Equal(t, expectedTotal, actualCounter, "All updates should be accounted for under high contention")
}

// TestVTC_Concurrency_WithUpdates extends the basic concurrency test to include counter updates,
// verifying that SelectQueue and OnRequestComplete work correctly together under contention.
func TestVTC_Concurrency_WithUpdates(t *testing.T) {
	t.Parallel()

	config := DefaultConfig()
	policy := newVTCWithState(config, &state{}, VTCPolicyName)

	item1 := typesmocks.NewMockQueueItemAccessor(100, "req1", flow1Key)
	item2 := typesmocks.NewMockQueueItemAccessor(150, "req2", flow2Key)
	item3 := typesmocks.NewMockQueueItemAccessor(200, "req3", flow3Key)

	queue1 := &frameworkmocks.MockFlowQueueAccessor{LenV: 1, FlowKeyV: flow1Key, PeekHeadV: item1}
	queue2 := &frameworkmocks.MockFlowQueueAccessor{LenV: 1, FlowKeyV: flow2Key, PeekHeadV: item2}
	queue3 := &frameworkmocks.MockFlowQueueAccessor{LenV: 1, FlowKeyV: flow3Key, PeekHeadV: item3}

	mockBand := &frameworkmocks.MockPriorityBandAccessor{
		FlowKeysFunc: func() []types.FlowKey { return []types.FlowKey{flow1Key, flow2Key, flow3Key} },
		QueueFunc: func(id string) framework.FlowQueueAccessor {
			switch id {
			case "flow1":
				return queue1
			case "flow2":
				return queue2
			case "flow3":
				return queue3
			}
			return nil
		},
	}

	var wg sync.WaitGroup
	var totalUpdates atomic.Int64

	// goroutines doing select + update cycles
	wg.Add(10)
	for range 10 {
		go func() {
			defer wg.Done()
			for range 50 {
				selected, err := policy.SelectQueue(mockBand)
				if err == nil && selected != nil {
					policy.OnRequestComplete(selected.FlowKey().ID, 0, 50, 50)
					totalUpdates.Add(1)
				}
			}
		}()
	}
	wg.Wait()

	// verify counters are non-negative and sum to expected total
	var counterSum float64
	for _, flowID := range []string{"flow1", "flow2", "flow3"} {
		counter := policy.state.getCounter(flowID)
		assert.GreaterOrEqual(t, counter, 0.0, "Counter should never be negative")
		counterSum += counter
	}

	expectedSum := float64(totalUpdates.Load()) * 100.0 // 50 input + 50 output per update
	assert.Equal(t, expectedSum, counterSum, "Total counter sum should match total updates")
}

// TestVTC_CleanupConcurrentAccess verifies that cleanup doesn't interfere with concurrent
// counter updates and reads.
func TestVTC_CleanupConcurrentAccess(t *testing.T) {
	t.Parallel()

	config := Config{
		InputTokenWeight:       1.0,
		OutputTokenWeight:      1.0,
		CounterCleanupInterval: 50 * time.Millisecond,
		CounterTTL:             100 * time.Millisecond,
	}

	testState := &state{}
	policy := newVTCWithState(config, testState, VTCPolicyName)
	testState.startCleanup(config)

	// run concurrent operations while cleanup is running
	var wg sync.WaitGroup
	stopTest := make(chan struct{})

	// goroutine 1: continuously update active counters
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				policy.state.updateCounter("active-1", 1.0)
				policy.state.updateCounter("active-2", 1.0)
			case <-stopTest:
				return
			}
		}
	}()

	// goroutine 2: continuously read active counters
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				policy.state.getCounter("active-1")
				policy.state.getCounter("active-2")
			case <-stopTest:
				return
			}
		}
	}()

	// goroutine 3: create temporary counters that will be cleaned up
	wg.Add(1)
	go func() {
		defer wg.Done()
		counter := 0
		ticker := time.NewTicker(30 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				flowID := fmt.Sprintf("temp-%d", counter)
				policy.state.updateCounter(flowID, 100.0)
				counter++
			case <-stopTest:
				return
			}
		}
	}()

	// let it run for a while with cleanup active
	time.Sleep(500 * time.Millisecond)
	close(stopTest)
	wg.Wait()

	// verify active counters still exist and have non-zero values
	counter1 := policy.state.getCounter("active-1")
	counter2 := policy.state.getCounter("active-2")
	assert.Greater(t, counter1, 0.0, "active-1 should have accumulated value")
	assert.Greater(t, counter2, 0.0, "active-2 should have accumulated value")

	// count how many entries exist in the maps
	var countInCounters, countInTimes int
	testState.virtualCounters.Range(func(key, value interface{}) bool {
		countInCounters++
		return true
	})
	testState.lastAccessTimes.Range(func(key, value interface{}) bool {
		countInTimes++
		return true
	})

	// verify cleanup happened (should be much less than all temp counters created)
	// with 30ms interval over 500ms, we'd create ~16 temp counters
	// cleanup should reduce this significantly
	assert.Less(t, countInCounters, 20, "cleanup should have removed stale entries")
	assert.Equal(t, countInCounters, countInTimes, "both maps should have same entry count")
}
