/*
Copyright 2025 The kcp Authors.

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

package builder

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"

	"github.com/kcp-dev/kcp/pkg/indexers"
)

func TestResolveTerminatorWorkspaceType(t *testing.T) {
	wst := func(cluster, name string) *tenancyv1alpha1.WorkspaceType {
		return &tenancyv1alpha1.WorkspaceType{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
				Annotations: map[string]string{
					"kcp.io/cluster": cluster,
					"kcp.io/path":    cluster,
				},
			},
		}
	}

	universalLocal := wst("root", "universal")
	customCached := wst("root:org", "custom")

	tests := []struct {
		name           string
		terminator     corev1alpha1.LogicalClusterTerminator
		local          []*tenancyv1alpha1.WorkspaceType
		cached         []*tenancyv1alpha1.WorkspaceType
		wantWST        *tenancyv1alpha1.WorkspaceType
		wantNotFound   bool
		wantParseError bool
	}{
		{
			name:       "system terminator falls through (no WST, no error)",
			terminator: corev1alpha1.LogicalClusterTerminator("system:apibindings"),
			wantWST:    nil,
		},
		{
			name:           "malformed terminator (no colon) — error, no Mode 2 fallback",
			terminator:     corev1alpha1.LogicalClusterTerminator("garbage"),
			wantParseError: true,
		},
		{
			name:       "WST resolved from local indexer",
			terminator: corev1alpha1.LogicalClusterTerminator("root:universal"),
			local:      []*tenancyv1alpha1.WorkspaceType{universalLocal},
			wantWST:    universalLocal,
		},
		{
			name:       "WST resolved from cache fallback when local misses",
			terminator: corev1alpha1.LogicalClusterTerminator("root:org:custom"),
			cached:     []*tenancyv1alpha1.WorkspaceType{customCached},
			wantWST:    customCached,
		},
		{
			name:         "WST encoded but missing from both indexers — fail closed",
			terminator:   corev1alpha1.LogicalClusterTerminator("root:gone"),
			wantNotFound: true,
		},
		{
			name:       "system terminator ignored even if no WST exists anywhere",
			terminator: corev1alpha1.LogicalClusterTerminator("system:custom-system-terminator"),
			wantWST:    nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			localIdx := newWSTIndexer(t, tc.local...)
			cachedIdx := newWSTIndexer(t, tc.cached...)

			got, err := resolveTerminatorWorkspaceType(tc.terminator, localIdx, cachedIdx)
			switch {
			case tc.wantParseError:
				require.Error(t, err)
				require.False(t, apierrors.IsNotFound(err), "parse error should not be NotFound, got %v", err)
				require.Nil(t, got)
			case tc.wantNotFound:
				require.Error(t, err)
				require.True(t, apierrors.IsNotFound(err), "expected NotFound, got %v", err)
				require.Nil(t, got)
			default:
				require.NoError(t, err)
				require.Equal(t, tc.wantWST, got)
			}
		})
	}
}

func newWSTIndexer(t *testing.T, objs ...*tenancyv1alpha1.WorkspaceType) cache.Indexer {
	t.Helper()
	idx := cache.NewIndexer(kcpcache.MetaClusterNamespaceKeyFunc, cache.Indexers{
		indexers.ByLogicalClusterPathAndName: indexers.IndexByLogicalClusterPathAndName,
	})
	for _, o := range objs {
		require.NoError(t, idx.Add(o))
	}
	return idx
}

func TestIsDiscoveryRequest(t *testing.T) {
	tt := []struct {
		path string
		exp  bool
	}{
		{"/api", true},
		{"/api/v1", true},
		{"/api/somegroup", true},
		{"/apis/somegroup", true},
		{"/apis/somegroup/v1", true},
		{"/healthz", false},
		{"/", false},
		{"/api/v1/namespace", false},
		{"/apis/somegroup/v1/namespaces", false},
	}
	for _, tc := range tt {
		t.Run(fmt.Sprintf("%q", tc.path), func(t *testing.T) {
			if res := isDiscoveryRequest(tc.path); res != tc.exp {
				t.Errorf("Exp %t, got %t", tc.exp, res)
			}
		})
	}
}
