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
)

const (
	metricsSubsystem = "flow_control_inter_priority"
)

var (
	priorityBandDispatchesTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: metricsSubsystem,
			Name:      "dispatches_total",
			Help:      "Total number of dispatches per priority band.",
		},
		[]string{"priority"},
	)

	starvationInterventionsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: metricsSubsystem,
			Name:      "starvation_interventions_total",
			Help:      "Total number of times a band was selected due to being below its minimum guarantee.",
		},
		[]string{"priority", "priority_name"},
	)
)

func allMetrics() []prometheus.Collector {
	return []prometheus.Collector{
		priorityBandDispatchesTotal,
		starvationInterventionsTotal,
	}
}

func recordDispatch(priority int) {
	priorityBandDispatchesTotal.WithLabelValues(strconv.Itoa(priority)).Inc()
}

func recordStarvationIntervention(priority int, priorityName string) {
	starvationInterventionsTotal.WithLabelValues(strconv.Itoa(priority), priorityName).Inc()
}
