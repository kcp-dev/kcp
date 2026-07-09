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

package objectcount

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/kcp-dev/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
)

func TestRegistryCountIncDec(t *testing.T) {
	t.Parallel()

	r := NewRegistry(10)
	ws := logicalcluster.Name("root:ws")

	require.Equal(t, int64(0), r.Count(ws))

	r.Inc(ws)
	r.Inc(ws)
	require.Equal(t, int64(2), r.Count(ws))

	r.Dec(ws)
	require.Equal(t, int64(1), r.Count(ws))

	// other clusters are unaffected
	require.Equal(t, int64(0), r.Count(logicalcluster.Name("root:other")))
}

func TestRegistryReplaceBaseResetsDeltas(t *testing.T) {
	t.Parallel()

	r := NewRegistry(10)
	ws := logicalcluster.Name("root:ws")
	gone := logicalcluster.Name("root:gone")

	r.Inc(ws)
	r.Inc(gone)
	require.Equal(t, int64(1), r.Count(ws))
	require.Equal(t, int64(1), r.Count(gone))

	r.ReplaceBase(map[logicalcluster.Name]int64{ws: 5})

	require.Equal(t, int64(5), r.Count(ws), "base must replace delta")
	require.Equal(t, int64(0), r.Count(gone), "disappeared clusters must reset to zero")

	r.Inc(ws)
	require.Equal(t, int64(6), r.Count(ws), "delta must apply on top of the new base")
}

func TestRegistryLimitFor(t *testing.T) {
	t.Parallel()

	ws := logicalcluster.Name("root:ws")

	tests := []struct {
		name         string
		defaultLimit int64
		cluster      logicalcluster.Name
		annotations  map[string]string
		want         int64
	}{
		{
			name:         "no annotation falls back to default",
			defaultLimit: 100,
			cluster:      ws,
			annotations:  nil,
			want:         100,
		},
		{
			name:         "annotation overrides default",
			defaultLimit: 100,
			cluster:      ws,
			annotations:  map[string]string{corev1alpha1.LogicalClusterMaxTotalObjectsAnnotationKey: "5"},
			want:         5,
		},
		{
			name:         "annotation zero opts out",
			defaultLimit: 100,
			cluster:      ws,
			annotations:  map[string]string{corev1alpha1.LogicalClusterMaxTotalObjectsAnnotationKey: "0"},
			want:         0,
		},
		{
			name:         "negative annotation opts out",
			defaultLimit: 100,
			cluster:      ws,
			annotations:  map[string]string{corev1alpha1.LogicalClusterMaxTotalObjectsAnnotationKey: "-1"},
			want:         -1,
		},
		{
			name:         "unparseable annotation falls back to default",
			defaultLimit: 100,
			cluster:      ws,
			annotations:  map[string]string{corev1alpha1.LogicalClusterMaxTotalObjectsAnnotationKey: "many"},
			want:         100,
		},
		{
			name:         "annotation enforces even without default",
			defaultLimit: 0,
			cluster:      ws,
			annotations:  map[string]string{corev1alpha1.LogicalClusterMaxTotalObjectsAnnotationKey: "42"},
			want:         42,
		},
		{
			name:         "default does not apply to root",
			defaultLimit: 100,
			cluster:      core.RootCluster,
			annotations:  nil,
			want:         0,
		},
		{
			name:         "default does not apply to system clusters",
			defaultLimit: 100,
			cluster:      logicalcluster.Name("system:admin"),
			annotations:  nil,
			want:         0,
		},
		{
			name:         "annotation still applies to root",
			defaultLimit: 100,
			cluster:      core.RootCluster,
			annotations:  map[string]string{corev1alpha1.LogicalClusterMaxTotalObjectsAnnotationKey: "7"},
			want:         7,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := NewRegistry(tt.defaultLimit)
			require.Equal(t, tt.want, r.LimitFor(tt.cluster, tt.annotations))
		})
	}
}

func TestRegistryConcurrentInc(t *testing.T) {
	t.Parallel()

	r := NewRegistry(0)
	ws := logicalcluster.Name("root:ws")

	const goroutines = 20
	const perGoroutine = 100

	var wg sync.WaitGroup
	for range goroutines {
		wg.Go(func() {
			for range perGoroutine {
				r.Inc(ws)
			}
		})
	}
	wg.Wait()

	require.Equal(t, int64(goroutines*perGoroutine), r.Count(ws))
}
