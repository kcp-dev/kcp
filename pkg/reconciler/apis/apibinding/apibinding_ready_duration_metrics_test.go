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

func TestHandleReadyDurationOnUpdate(t *testing.T) {
	t.Parallel()
	t.Run("transitioning to Bound records duration without panic", func(t *testing.T) {
		t.Parallel()
		c := newTestController()
		created := time.Now().Add(-5 * time.Second)
		old := bindingWithPhaseAndCreation("root:ws", "test", apisv1alpha2.APIBindingPhaseBinding, created)
		new := bindingWithPhaseAndCreation("root:ws", "test", apisv1alpha2.APIBindingPhaseBound, created)
		c.handlePhaseMetricsOnAdd(old)
		require.NotPanics(t, func() { c.handlePhaseMetricsOnUpdate(old, new) })
	})

	t.Run("transitioning to Binding does not record duration", func(t *testing.T) {
		t.Parallel()
		c := newTestController()
		created := time.Now().Add(-2 * time.Second)
		old := bindingWithPhaseAndCreation("root:ws", "test", "", created)
		new := bindingWithPhaseAndCreation("root:ws", "test", apisv1alpha2.APIBindingPhaseBinding, created)
		c.handlePhaseMetricsOnAdd(old)
		require.NotPanics(t, func() { c.handlePhaseMetricsOnUpdate(old, new) })
	})

	t.Run("same phase does not record duration", func(t *testing.T) {
		t.Parallel()
		c := newTestController()
		created := time.Now().Add(-1 * time.Second)
		b := bindingWithPhaseAndCreation("root:ws", "test", apisv1alpha2.APIBindingPhaseBound, created)
		c.handlePhaseMetricsOnAdd(b)
		require.NotPanics(t, func() { c.handlePhaseMetricsOnUpdate(b, b) })
	})
}
