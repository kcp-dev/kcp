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
	"context"
	"fmt"
	"maps"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.etcd.io/etcd/api/v3/mvccpb"
	clientv3 "go.etcd.io/etcd/client/v3"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kcp-dev/logicalcluster/v3"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	corev1alpha1listers "github.com/kcp-dev/sdk/client/listers/core/v1alpha1"

	kcpetcd "github.com/kcp-dev/kcp/pkg/etcd"
)

func TestScannerScanOnce(t *testing.T) {
	t.Parallel()

	kv := newFakeKV(map[string]string{
		// built-in namespaced
		"/registry/core/configmaps/root:ws/default/cm1": "v",
		"/registry/core/configmaps/root:ws/default/cm2": "v",
		// built-in cluster-scoped
		"/registry/rbac.authorization.k8s.io/clusterroles/root:ws/role1": "v",
		// CRD
		"/registry/mygroup.io/widgets/customresources/root:ws/default/w1": "v",
		// identity-based cluster-scoped (ambiguous 5 segments)
		"/registry/mygroup.io/things/abc123def/root:ws/t1": "v",
		// events must not be counted
		"/registry/core/events/root:ws/default/ev1":          "v",
		"/registry/events.k8s.io/events/root:ws/default/ev2": "v",
		// other clusters
		"/registry/core/configmaps/root:other/default/cm": "v",
		"/registry/core/secrets/system:admin/default/s":   "v",
	})

	s := newTestScanner(kv, NewRegistry(0))

	counts, err := s.scanOnce(context.Background(), sets.New("root:ws", "root:other"))
	require.NoError(t, err)

	require.Equal(t, map[logicalcluster.Name]int64{
		"root:ws":      5,
		"root:other":   1,
		"system:admin": 1,
	}, counts)
}

func TestScannerScanOncePaginates(t *testing.T) {
	t.Parallel()

	// More keys than one page (ScanPageSize = 1000) to force pagination.
	kvs := map[string]string{}
	total := int(kcpetcd.ScanPageSize) + 500
	for i := range total {
		kvs[fmt.Sprintf("/registry/core/configmaps/root:ws/default/cm-%05d", i)] = "v"
	}
	kv := newFakeKV(kvs)

	s := newTestScanner(kv, NewRegistry(0))

	counts, err := s.scanOnce(context.Background(), sets.New("root:ws"))
	require.NoError(t, err)
	require.Equal(t, map[logicalcluster.Name]int64{"root:ws": int64(total)}, counts)
}

func TestScannerTickGatesOnConfiguration(t *testing.T) {
	t.Parallel()

	kv := newFakeKV(map[string]string{
		"/registry/core/configmaps/root:ws/default/cm1": "v",
	})

	tests := []struct {
		name         string
		defaultLimit int64
		annotations  map[string]string
		wantActive   bool
		wantCount    int64
	}{
		{
			name:         "disabled without default limit and annotations",
			defaultLimit: 0,
			wantActive:   false,
			wantCount:    0,
		},
		{
			name:         "enabled via default limit",
			defaultLimit: 10,
			wantActive:   true,
			wantCount:    1,
		},
		{
			name:         "enabled via annotation only",
			defaultLimit: 0,
			annotations:  map[string]string{corev1alpha1.LogicalClusterMaxTotalObjectsAnnotationKey: "10"},
			wantActive:   true,
			wantCount:    1,
		},
		{
			name:         "annotation opt-out only does not enable",
			defaultLimit: 0,
			annotations:  map[string]string{corev1alpha1.LogicalClusterMaxTotalObjectsAnnotationKey: "0"},
			wantActive:   false,
			wantCount:    0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			registry := NewRegistry(tt.defaultLimit)
			s := newTestScanner(kv, registry)
			s.lcLister = fakeLogicalClusterClusterLister{
				newLogicalCluster("root:ws", tt.annotations),
			}

			s.tick(context.Background())

			require.Equal(t, tt.wantActive, registry.EnforcementActive())
			require.Equal(t, tt.wantCount, registry.Count(logicalcluster.Name("root:ws")))
		})
	}
}

func newTestScanner(kv clientv3.KV, registry *Registry) *Scanner {
	return NewScanner(kv, "/registry", time.Minute, registry, fakeLogicalClusterClusterLister{}, "root")
}

func newLogicalCluster(cluster logicalcluster.Name, annotations map[string]string) *corev1alpha1.LogicalCluster {
	all := map[string]string{logicalcluster.AnnotationKey: string(cluster)}
	maps.Copy(all, annotations)
	return &corev1alpha1.LogicalCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:        corev1alpha1.LogicalClusterName,
			Annotations: all,
		},
	}
}

type fakeLogicalClusterClusterLister []*corev1alpha1.LogicalCluster

func (l fakeLogicalClusterClusterLister) List(_ labels.Selector) ([]*corev1alpha1.LogicalCluster, error) {
	return l, nil
}

func (l fakeLogicalClusterClusterLister) Cluster(cluster logicalcluster.Name) corev1alpha1listers.LogicalClusterLister {
	panic("not implemented")
}

// fakeKV implements clientv3.KV for range reads with pagination, mirroring the
// harness used by the logicalclustermigration data cleanup tests.
type fakeKV struct {
	kvs map[string]string
}

func newFakeKV(kvs map[string]string) *fakeKV { return &fakeKV{kvs: kvs} }

func (f *fakeKV) Get(_ context.Context, key string, opts ...clientv3.OpOption) (*clientv3.GetResponse, error) {
	op := clientv3.OpGet(key, opts...)
	rangeEnd := string(op.RangeBytes())
	limit := op.Limit()

	keys := make([]string, 0, len(f.kvs))
	for k := range f.kvs {
		if k >= key && (rangeEnd == "" || k < rangeEnd) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)

	more := false
	if limit > 0 && int64(len(keys)) > limit {
		keys = keys[:limit]
		more = true
	}

	resp := &clientv3.GetResponse{More: more}
	for _, k := range keys {
		resp.Kvs = append(resp.Kvs, &mvccpb.KeyValue{Key: []byte(k), Value: []byte(f.kvs[k])})
	}
	return resp, nil
}

func (f *fakeKV) Put(context.Context, string, string, ...clientv3.OpOption) (*clientv3.PutResponse, error) {
	panic("not implemented")
}

func (f *fakeKV) Delete(context.Context, string, ...clientv3.OpOption) (*clientv3.DeleteResponse, error) {
	panic("not implemented")
}

func (f *fakeKV) Compact(context.Context, int64, ...clientv3.CompactOption) (*clientv3.CompactResponse, error) {
	panic("not implemented")
}

func (f *fakeKV) Do(context.Context, clientv3.Op) (clientv3.OpResponse, error) {
	panic("not implemented")
}

func (f *fakeKV) Txn(context.Context) clientv3.Txn { panic("not implemented") }
