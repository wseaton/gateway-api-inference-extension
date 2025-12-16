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
	"strconv"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/metrics"
)

// recordDispatch records tokens and request count for a priority band.
func recordDispatch(priority int, tokens uint64) {
	metrics.RecordInterPriorityDispatch(strconv.Itoa(priority), tokens)
}

// recordStarvationIntervention records when a band is boosted due to starvation.
func recordStarvationIntervention(priority int, priorityName string) {
	metrics.RecordInterPriorityStarvationIntervention(strconv.Itoa(priority), priorityName)
}

// recordCounterState records the current VTC state for a priority band.
func recordCounterState(priority int, counter float64, normalized float64, deficit float64) {
	metrics.RecordInterPriorityCounterState(strconv.Itoa(priority), counter, normalized, deficit)
}
