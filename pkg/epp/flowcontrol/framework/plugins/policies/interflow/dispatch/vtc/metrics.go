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
	"github.com/prometheus/client_golang/prometheus"
	compbasemetrics "k8s.io/component-base/metrics"

	metricsutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/metrics"
)

const metricsSubsystem = "inference_extension"

var (
	virtualCounter = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Subsystem: metricsSubsystem,
			Name:      "vtc_virtual_counter",
			Help:      metricsutil.HelpMsgWithStability("Current virtual token counter value per flow.", compbasemetrics.ALPHA),
		},
		[]string{"flow_id"},
	)

	queueSelections = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: metricsSubsystem,
			Name:      "vtc_queue_selections_total",
			Help:      metricsutil.HelpMsgWithStability("Number of times each flow was selected for dispatch by VTC policy.", compbasemetrics.ALPHA),
		},
		[]string{"flow_id"},
	)

	counterUpdates = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Subsystem: metricsSubsystem,
			Name:      "vtc_counter_update_tokens",
			Help:      metricsutil.HelpMsgWithStability("Distribution of token service amounts charged per request by VTC policy.", compbasemetrics.ALPHA),
			Buckets:   []float64{10, 50, 100, 200, 500, 1000, 2000, 5000, 10000},
		},
		[]string{"flow_id"},
	)

	flowsCleanedUp = prometheus.NewCounter(
		prometheus.CounterOpts{
			Subsystem: metricsSubsystem,
			Name:      "vtc_flows_cleaned_total",
			Help:      metricsutil.HelpMsgWithStability("Total number of stale flows cleaned up by VTC policy.", compbasemetrics.ALPHA),
		},
	)
)

func allMetrics() []prometheus.Collector {
	return []prometheus.Collector{
		virtualCounter,
		queueSelections,
		counterUpdates,
		flowsCleanedUp,
	}
}

func recordQueueSelection(flowID string, counterValue float64) {
	queueSelections.WithLabelValues(flowID).Inc()
	virtualCounter.WithLabelValues(flowID).Set(counterValue)
}

func recordCounterUpdate(flowID string, tokens float64) {
	counterUpdates.WithLabelValues(flowID).Observe(tokens)
	virtualCounter.WithLabelValues(flowID).Add(tokens)
}

func recordFlowCleanedUp() {
	flowsCleanedUp.Inc()
}
