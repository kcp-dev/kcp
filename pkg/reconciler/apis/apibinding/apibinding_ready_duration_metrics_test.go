/*
Copyright 2026 The kcp Authors.

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

package apibinding

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/component-base/metrics/legacyregistry"

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
)

func bindingWithPhaseAndCreation(cluster, name string, phase apisv1alpha2.APIBindingPhaseType, created time.Time) *apisv1alpha2.APIBinding {
	return &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Annotations:       map[string]string{logicalcluster.AnnotationKey: cluster},
			CreationTimestamp: metav1.Time{Time: created},
		},
		Status: apisv1alpha2.APIBindingStatus{Phase: phase},
	}
}

// readyDurationSampleCount returns the number of APIBinding ready durations
// recorded for the given shard.
func readyDurationSampleCount(t *testing.T, shardName string) uint64 {
	t.Helper()

	families, err := legacyregistry.DefaultGatherer.Gather()
	require.NoError(t, err)
	for _, family := range families {
		if family.GetName() != "kcp_apibinding_ready_duration_ms" {
			continue
		}
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetName() == "shard" && label.GetValue() == shardName {
					return metric.GetHistogram().GetSampleCount()
				}
			}
		}
	}
	return 0
}

func TestHandleReadyDurationMetricOnUpdate(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		oldPhase apisv1alpha2.APIBindingPhaseType
		newPhase apisv1alpha2.APIBindingPhaseType
		recorded bool
	}{
		"transitioning from Binding to Bound records duration": {
			oldPhase: apisv1alpha2.APIBindingPhaseBinding,
			newPhase: apisv1alpha2.APIBindingPhaseBound,
			recorded: true,
		},
		"transitioning from no phase to Bound records duration": {
			oldPhase: "",
			newPhase: apisv1alpha2.APIBindingPhaseBound,
			recorded: true,
		},
		"transitioning to Binding does not record duration": {
			oldPhase: "",
			newPhase: apisv1alpha2.APIBindingPhaseBinding,
		},
		"staying Bound does not record duration": {
			oldPhase: apisv1alpha2.APIBindingPhaseBound,
			newPhase: apisv1alpha2.APIBindingPhaseBound,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// a dedicated shard name keeps the parallel subtests from
			// observing each other's samples.
			c := &controller{shardName: t.Name()}
			created := time.Now().Add(-5 * time.Second)
			old := bindingWithPhaseAndCreation("root:ws", "test", tc.oldPhase, created)
			updated := bindingWithPhaseAndCreation("root:ws", "test", tc.newPhase, created)

			c.handleReadyDurationMetricOnUpdate(old, updated)

			var expected uint64
			if tc.recorded {
				expected = 1
			}
			require.Equal(t, expected, readyDurationSampleCount(t, c.shardName))
		})
	}
}
