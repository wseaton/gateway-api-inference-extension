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

	"github.com/prometheus/client_golang/prometheus"
	compbasemetrics "k8s.io/component-base/metrics"

	metricsutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/metrics"
)

const metricsSubsystem = "inference_extension"

var (
	priorityBandTokensTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: metricsSubsystem,
			Name:      "inter_priority_tokens_total",
			Help:      metricsutil.HelpMsgWithStability("Total tokens (input + output) processed per priority band.", compbasemetrics.ALPHA),
		},
		[]string{"priority"},
	)

	priorityBandRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: metricsSubsystem,
			Name:      "inter_priority_requests_total",
			Help:      metricsutil.HelpMsgWithStability("Total requests completed per priority band.", compbasemetrics.ALPHA),
		},
		[]string{"priority"},
	)

	starvationInterventionsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: metricsSubsystem,
			Name:      "inter_priority_starvation_interventions_total",
			Help:      metricsutil.HelpMsgWithStability("Total number of times a band was selected due to being below its minimum guarantee.", compbasemetrics.ALPHA),
		},
		[]string{"priority", "priority_name"},
	)

	// gauges for observing VTC state
	priorityBandCounter = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: metricsSubsystem,
			Name:      "inter_priority_counter",
			Help:      metricsutil.HelpMsgWithStability("Current VTC counter value (cumulative tokens) per priority band.", compbasemetrics.ALPHA),
		},
		[]string{"priority"},
	)

	priorityBandNormalizedCounter = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: metricsSubsystem,
			Name:      "inter_priority_normalized_counter",
			Help:      metricsutil.HelpMsgWithStability("Current normalized VTC counter (counter/minRate) per priority band. Lower values indicate the band is behind.", compbasemetrics.ALPHA),
		},
		[]string{"priority"},
	)

	priorityBandDeficit = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: metricsSubsystem,
			Name:      "inter_priority_deficit",
			Help:      metricsutil.HelpMsgWithStability("Deficit of a guaranteed band relative to the highest priority band. Positive means behind, zero means caught up.", compbasemetrics.ALPHA),
		},
		[]string{"priority"},
	)
)

func allMetrics() []prometheus.Collector {
	return []prometheus.Collector{
		priorityBandTokensTotal,
		priorityBandRequestsTotal,
		starvationInterventionsTotal,
		priorityBandCounter,
		priorityBandNormalizedCounter,
		priorityBandDeficit,
	}
}

func recordDispatch(priority int, tokens uint64) {
	priorityBandTokensTotal.WithLabelValues(strconv.Itoa(priority)).Add(float64(tokens))
	priorityBandRequestsTotal.WithLabelValues(strconv.Itoa(priority)).Inc()
}

func recordStarvationIntervention(priority int, priorityName string) {
	starvationInterventionsTotal.WithLabelValues(strconv.Itoa(priority), priorityName).Inc()
}

func recordCounterState(priority int, counter float64, normalized float64, deficit float64) {
	priStr := strconv.Itoa(priority)
	priorityBandCounter.WithLabelValues(priStr).Set(counter)
	priorityBandNormalizedCounter.WithLabelValues(priStr).Set(normalized)
	priorityBandDeficit.WithLabelValues(priStr).Set(deficit)
}
