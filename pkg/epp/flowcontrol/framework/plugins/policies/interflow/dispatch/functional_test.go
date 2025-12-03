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

package dispatch_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/flowcontrol/framework"
	frameworkmocks "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/flowcontrol/framework/mocks"
	_ "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/flowcontrol/framework/plugins/policies/interflow/dispatch/besthead"
	_ "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/flowcontrol/framework/plugins/policies/interflow/dispatch/roundrobin"
	_ "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/flowcontrol/framework/plugins/policies/interflow/dispatch/vtc"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/flowcontrol/types"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/plugins"
)

// interFlowDispatchPolicies lists the policies to test for conformance.
// these are registered via init() in their respective packages.
var interFlowDispatchPolicies = []string{
	"BestHead",
	"RoundRobin",
	"VTC",
}

// TestInterFlowDispatchPolicy_Conformance is the main conformance test suite for `framework.InterFlowDispatchPolicy`
// implementations.
// It iterates over all policy implementations registered via plugins.Register and runs a series of
// sub-tests to ensure they adhere to the `framework.InterFlowDispatchPolicy` contract.
func TestInterFlowDispatchPolicyConformance(t *testing.T) {
	t.Parallel()

	for _, policyName := range interFlowDispatchPolicies {
		t.Run(policyName, func(t *testing.T) {
			t.Parallel()

			factory, ok := plugins.Registry[policyName]
			require.True(t, ok, "Policy %s not found in plugins.Registry", policyName)

			pluginInstance, err := factory(policyName, nil, nil)
			require.NoError(t, err, "Policy factory for %s failed", policyName)
			require.NotNil(t, pluginInstance, "Factory for %s should return a non-nil policy instance", policyName)

			policy, ok := pluginInstance.(framework.InterFlowDispatchPolicy)
			require.True(t, ok, "Plugin %s is not an InterFlowDispatchPolicy", policyName)

			t.Run("Initialization", func(t *testing.T) {
				t.Parallel()
				assert.NotEmpty(t, policy.TypedName().Name, "Name() for %s should return a non-empty string", policyName)
			})

			t.Run("SelectQueue", func(t *testing.T) {
				t.Parallel()
				runSelectQueueConformanceTests(t, policy)
			})
		})
	}
}

func runSelectQueueConformanceTests(t *testing.T, policy framework.InterFlowDispatchPolicy) {
	t.Helper()

	flowIDEmpty := "flow-empty"
	mockQueueEmpty := &frameworkmocks.MockFlowQueueAccessor{
		LenV:         0,
		PeekHeadErrV: framework.ErrQueueEmpty,
		FlowKeyV:     types.FlowKey{ID: flowIDEmpty},
	}

	testCases := []struct {
		name          string
		band          framework.PriorityBandAccessor
		expectErr     bool
		expectNil     bool
		expectedQueue framework.FlowQueueAccessor
	}{
		{
			name:      "With a nil priority band accessor",
			band:      nil,
			expectErr: false,
			expectNil: true,
		},
		{
			name: "With an empty priority band accessor",
			band: &frameworkmocks.MockPriorityBandAccessor{
				FlowKeysFunc:      func() []types.FlowKey { return []types.FlowKey{} },
				IterateQueuesFunc: func(callback func(queue framework.FlowQueueAccessor) bool) { /* no-op */ },
			},
			expectErr: false,
			expectNil: true,
		},
		{
			name: "With a band that has one empty queue",
			band: &frameworkmocks.MockPriorityBandAccessor{
				FlowKeysFunc: func() []types.FlowKey { return []types.FlowKey{{ID: flowIDEmpty}} },
				QueueFunc: func(fID string) framework.FlowQueueAccessor {
					if fID == flowIDEmpty {
						return mockQueueEmpty
					}
					return nil
				},
			},
			expectErr: false,
			expectNil: true,
		},
		{
			name: "With a band that has multiple empty queues",
			band: &frameworkmocks.MockPriorityBandAccessor{
				FlowKeysFunc: func() []types.FlowKey { return []types.FlowKey{{ID: flowIDEmpty}, {ID: "flow-empty-2"}} },
				QueueFunc: func(fID string) framework.FlowQueueAccessor {
					return mockQueueEmpty
				},
			},
			expectErr: false,
			expectNil: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			selectedQueue, err := policy.SelectQueue(tc.band)

			if tc.expectErr {
				require.Error(t, err, "SelectQueue for policy %s should return an error", policy.TypedName().Name)
			} else {
				require.NoError(t, err, "SelectQueue for policy %s should not return an error", policy.TypedName().Name)
			}

			if tc.expectNil {
				assert.Nil(t, selectedQueue, "SelectQueue for policy %s should return a nil queue", policy.TypedName().Name)
			} else {
				assert.NotNil(t, selectedQueue, "SelectQueue for policy %s should not return a nil queue", policy.TypedName().Name)
			}

			if tc.expectedQueue != nil {
				assert.Equal(t, tc.expectedQueue.FlowKey(), selectedQueue.FlowKey(),
					"SelectQueue for policy %s returned an unexpected queue", policy.TypedName().Name)
			}
		})
	}
}
