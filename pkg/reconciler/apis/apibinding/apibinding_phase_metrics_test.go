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
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
)

func newTestController() *controller {
	return &controller{
		countedAPIBindings:          make(map[string]string),
		countedAPIBindingConditions: make(map[string]map[string]string),
	}
}

func binding(cluster, name string, phase apisv1alpha2.APIBindingPhaseType) *apisv1alpha2.APIBinding {
	return &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Annotations: map[string]string{"kcp.io/cluster": cluster},
		},
		Status: apisv1alpha2.APIBindingStatus{Phase: phase},
	}
}

func TestHandlePhaseMetricsOnAdd(t *testing.T) {
	t.Run("new binding with non-empty phase is tracked", func(t *testing.T) {
		c := newTestController()
		b := binding("root:ws", "test", apisv1alpha2.APIBindingPhaseBound)
		c.handlePhaseMetricsOnAdd(b)
		require.Len(t, c.countedAPIBindings, 1)
		for _, v := range c.countedAPIBindings {
			require.Equal(t, string(apisv1alpha2.APIBindingPhaseBound), v)
		}
	})

	t.Run("new binding with empty phase is tracked but metric not emitted", func(t *testing.T) {
		c := newTestController()
		b := binding("root:ws", "test", "")
		c.handlePhaseMetricsOnAdd(b)
		require.Len(t, c.countedAPIBindings, 1)
		for _, v := range c.countedAPIBindings {
			require.Equal(t, "", v)
		}
	})

	t.Run("duplicate add is ignored", func(t *testing.T) {
		c := newTestController()
		b := binding("root:ws", "test", apisv1alpha2.APIBindingPhaseBinding)
		c.handlePhaseMetricsOnAdd(b)
		c.handlePhaseMetricsOnAdd(b) // second add should be a no-op
		require.Len(t, c.countedAPIBindings, 1)
	})
}

func TestHandlePhaseMetricsOnUpdate(t *testing.T) {
	t.Run("phase change updates tracked state", func(t *testing.T) {
		c := newTestController()
		old := binding("root:ws", "test", apisv1alpha2.APIBindingPhaseBinding)
		new := binding("root:ws", "test", apisv1alpha2.APIBindingPhaseBound)
		c.handlePhaseMetricsOnAdd(old)
		c.handlePhaseMetricsOnUpdate(old, new)
		for _, v := range c.countedAPIBindings {
			require.Equal(t, string(apisv1alpha2.APIBindingPhaseBound), v)
		}
	})

	t.Run("no-op when phase unchanged", func(t *testing.T) {
		c := newTestController()
		b := binding("root:ws", "test", apisv1alpha2.APIBindingPhaseBound)
		c.handlePhaseMetricsOnAdd(b)
		c.handlePhaseMetricsOnUpdate(b, b)
		for _, v := range c.countedAPIBindings {
			require.Equal(t, string(apisv1alpha2.APIBindingPhaseBound), v)
		}
	})
}

func TestHandlePhaseMetricsOnDelete(t *testing.T) {
	t.Run("delete removes tracked binding", func(t *testing.T) {
		c := newTestController()
		b := binding("root:ws", "test", apisv1alpha2.APIBindingPhaseBound)
		c.handlePhaseMetricsOnAdd(b)
		require.Len(t, c.countedAPIBindings, 1)
		c.handlePhaseMetricsOnDelete(b)
		require.Empty(t, c.countedAPIBindings)
	})

	t.Run("delete of unknown binding is a no-op", func(t *testing.T) {
		c := newTestController()
		b := binding("root:ws", "unknown", apisv1alpha2.APIBindingPhaseBound)
		require.NotPanics(t, func() { c.handlePhaseMetricsOnDelete(b) })
		require.Empty(t, c.countedAPIBindings)
	})
}

func TestHandlePhaseMetricsConcurrency(t *testing.T) {
	c := newTestController()
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := "binding-" + string(rune('a'+i%26))
			b := binding("root:ws", name, apisv1alpha2.APIBindingPhaseBinding)
			c.handlePhaseMetricsOnAdd(b)
		}(i)
	}
	wg.Wait()
}
