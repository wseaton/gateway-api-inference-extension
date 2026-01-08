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

// Package strictpriority provides a `framework.InterPriorityDispatchPolicy` that implements strict
// priority ordering across priority bands. The highest priority band with work always wins.
package strictpriority

import (
	"encoding/json"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/flowcontrol/framework"
	dispatch "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/flowcontrol/framework/plugins/policies/interpriority/dispatch"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/plugins"
)

// StrictPriorityPolicyName is the name of the StrictPriority policy implementation.
const StrictPriorityPolicyName dispatch.RegisteredPolicyName = "StrictPriority"

func init() {
	dispatch.MustRegisterPolicy(StrictPriorityPolicyName, newStrictPriority)
	plugins.Register(string(StrictPriorityPolicyName), newStrictPriorityFactory)
}

// newStrictPriorityFactory is the factory function for the StrictPriority policy.
func newStrictPriorityFactory(name string, _ json.RawMessage, _ plugins.Handle) (plugins.Plugin, error) {
	return &strictPriority{
		typedName: plugins.TypedName{Type: framework.InterPriorityDispatchPolicyType, Name: name},
	}, nil
}

func newStrictPriority() (framework.InterPriorityDispatchPolicy, error) {
	return &strictPriority{
		typedName: plugins.TypedName{Type: framework.InterPriorityDispatchPolicyType, Name: string(StrictPriorityPolicyName)},
	}, nil
}

// strictPriority implements `framework.InterPriorityDispatchPolicy` using strict priority ordering.
// The highest priority band with work always wins, which can cause starvation of lower priority bands.
type strictPriority struct {
	typedName plugins.TypedName
}

// TypedName returns the type and name of the plugin instance.
func (p *strictPriority) TypedName() plugins.TypedName {
	return p.typedName
}

// SelectBand selects the highest priority band that has work available.
// Bands are provided in descending priority order, so the first band with work is selected.
func (p *strictPriority) SelectBand(bands []framework.PriorityBandAccessor) (framework.PriorityBandAccessor, error) {
	for _, band := range bands {
		if bandHasWork(band) {
			return band, nil
		}
	}
	return nil, nil
}

// OnDispatch is a no-op for strict priority since no state tracking is needed.
func (p *strictPriority) OnDispatch(_ int) {
	// no-op for strict priority
}

// OnDispatchComplete is a no-op for strict priority since no state tracking is needed.
func (p *strictPriority) OnDispatchComplete(_ int, _ uint64) {
	// no-op for strict priority
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
