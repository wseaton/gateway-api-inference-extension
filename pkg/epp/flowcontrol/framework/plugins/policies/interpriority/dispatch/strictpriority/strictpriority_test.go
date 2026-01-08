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

package strictpriority

import (
	"testing"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/flowcontrol/framework"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/flowcontrol/types"
)

// mockFlowQueue implements framework.FlowQueueAccessor for testing.
type mockFlowQueue struct {
	flowKey types.FlowKey
	length  int
}

func (m *mockFlowQueue) Name() string                                        { return "mock" }
func (m *mockFlowQueue) Capabilities() []framework.QueueCapability           { return nil }
func (m *mockFlowQueue) Len() int                                            { return m.length }
func (m *mockFlowQueue) ByteSize() uint64                                    { return 0 }
func (m *mockFlowQueue) PeekHead() (types.QueueItemAccessor, error)          { return nil, nil }
func (m *mockFlowQueue) PeekTail() (types.QueueItemAccessor, error)          { return nil, nil }
func (m *mockFlowQueue) FlowKey() types.FlowKey                              { return m.flowKey }
func (m *mockFlowQueue) Comparator() framework.ItemComparator                { return nil }

// mockPriorityBand implements framework.PriorityBandAccessor for testing.
type mockPriorityBand struct {
	priority     int
	priorityName string
	queues       []framework.FlowQueueAccessor
}

func (m *mockPriorityBand) Priority() int          { return m.priority }
func (m *mockPriorityBand) PriorityName() string   { return m.priorityName }
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

func TestStrictPriority_SelectBand(t *testing.T) {
	tests := []struct {
		name           string
		bands          []framework.PriorityBandAccessor
		wantPriority   int
		wantNil        bool
	}{
		{
			name:    "empty bands returns nil",
			bands:   []framework.PriorityBandAccessor{},
			wantNil: true,
		},
		{
			name: "all empty bands returns nil",
			bands: []framework.PriorityBandAccessor{
				&mockPriorityBand{priority: 100, priorityName: "high", queues: []framework.FlowQueueAccessor{}},
				&mockPriorityBand{priority: 50, priorityName: "low", queues: []framework.FlowQueueAccessor{}},
			},
			wantNil: true,
		},
		{
			name: "selects highest priority band with work",
			bands: []framework.PriorityBandAccessor{
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
			},
			wantPriority: 100,
		},
		{
			name: "skips empty high priority band",
			bands: []framework.PriorityBandAccessor{
				&mockPriorityBand{
					priority:     100,
					priorityName: "high",
					queues:       []framework.FlowQueueAccessor{},
				},
				&mockPriorityBand{
					priority:     50,
					priorityName: "low",
					queues: []framework.FlowQueueAccessor{
						&mockFlowQueue{flowKey: types.FlowKey{ID: "flow2", Priority: 50}, length: 1},
					},
				},
			},
			wantPriority: 50,
		},
		{
			name: "skips band with empty queues",
			bands: []framework.PriorityBandAccessor{
				&mockPriorityBand{
					priority:     100,
					priorityName: "high",
					queues: []framework.FlowQueueAccessor{
						&mockFlowQueue{flowKey: types.FlowKey{ID: "flow1", Priority: 100}, length: 0},
					},
				},
				&mockPriorityBand{
					priority:     50,
					priorityName: "low",
					queues: []framework.FlowQueueAccessor{
						&mockFlowQueue{flowKey: types.FlowKey{ID: "flow2", Priority: 50}, length: 1},
					},
				},
			},
			wantPriority: 50,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			policy, err := newStrictPriority()
			if err != nil {
				t.Fatalf("failed to create policy: %v", err)
			}

			got, err := policy.SelectBand(tt.bands)
			if err != nil {
				t.Fatalf("SelectBand returned error: %v", err)
			}

			if tt.wantNil {
				if got != nil {
					t.Errorf("expected nil, got priority %d", got.Priority())
				}
				return
			}

			if got == nil {
				t.Fatalf("expected priority %d, got nil", tt.wantPriority)
			}

			if got.Priority() != tt.wantPriority {
				t.Errorf("expected priority %d, got %d", tt.wantPriority, got.Priority())
			}
		})
	}
}

func TestStrictPriority_TypedName(t *testing.T) {
	policy, err := newStrictPriority()
	if err != nil {
		t.Fatalf("failed to create policy: %v", err)
	}

	typedName := policy.TypedName()
	if typedName.Type != framework.InterPriorityDispatchPolicyType {
		t.Errorf("expected type %s, got %s", framework.InterPriorityDispatchPolicyType, typedName.Type)
	}
	if typedName.Name != string(StrictPriorityPolicyName) {
		t.Errorf("expected name %s, got %s", StrictPriorityPolicyName, typedName.Name)
	}
}

func TestStrictPriority_OnDispatchComplete(t *testing.T) {
	policy, err := newStrictPriority()
	if err != nil {
		t.Fatalf("failed to create policy: %v", err)
	}

	// should not panic
	policy.OnDispatchComplete(100, 1024)
}
