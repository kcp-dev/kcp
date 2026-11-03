/*
Copyright 2024 The kcp Authors.

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
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	kcpinformers "github.com/kcp-dev/sdk/client/informers/externalversions"
	"github.com/kcp-dev/virtual-workspace-framework/framework"
	dynamiccontext "github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic/context"

	"github.com/kcp-dev/kcp/pkg/indexers"
)

func TestPathPattern(t *testing.T) {
	t.Parallel()

	require.Equal(t,
		"/services/initializingworkspaces/{initializer}/clusters/{cluster}",
		pathPattern("/services/initializingworkspaces/"))
}

func TestResolveRootPath(t *testing.T) {
	t.Parallel()

	const (
		initializerName = "test-initializer"
		clusterName     = "abc"
	)

	// registration order, the first accepting resolver wins
	resolvers := []struct {
		name    string
		resolve framework.RootPathResolverFunc
	}{
		{"wildcardLogicalClusters", resolveWildcardLogicalClusters},
		{"logicalClusters", resolveLogicalClusters},
		{"workspaceContent", resolveWorkspaceContent},
	}

	tests := map[string]struct {
		cluster      request.Cluster
		trail        string
		wantResolver string
		prefixToTrim string
	}{
		"wildcard logicalclusters": {
			cluster:      request.Cluster{Wildcard: true},
			trail:        "apis/core.kcp.io/v1alpha1/logicalclusters",
			wantResolver: "wildcardLogicalClusters",
			prefixToTrim: "/services/initializingworkspaces/test-initializer/clusters/*",
		},
		"wildcard other resource": {
			cluster:      request.Cluster{Wildcard: true},
			trail:        "api/v1/configmaps",
			wantResolver: "wildcardLogicalClusters",
			prefixToTrim: "/services/initializingworkspaces/test-initializer/clusters/*",
		},
		"cluster logicalclusters": {
			cluster:      request.Cluster{Name: clusterName},
			trail:        "apis/core.kcp.io/v1alpha1/logicalclusters/cluster",
			wantResolver: "logicalClusters",
			prefixToTrim: "/services/initializingworkspaces/test-initializer/clusters/abc",
		},
		"cluster other resource keeps cluster segment": {
			cluster:      request.Cluster{Name: clusterName},
			trail:        "api/v1/configmaps",
			wantResolver: "workspaceContent",
			prefixToTrim: "/services/initializingworkspaces/test-initializer",
		},
		"cluster workspace root keeps cluster segment": {
			cluster:      request.Cluster{Name: clusterName},
			trail:        "",
			wantResolver: "workspaceContent",
			prefixToTrim: "/services/initializingworkspaces/test-initializer",
		},
		"no cluster": {
			trail: "api/v1/configmaps",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			segment := tc.cluster.Name.String()
			if tc.cluster.Wildcard {
				segment = "*"
			}
			ctx := t.Context()
			if segment != "" {
				ctx = request.WithCluster(ctx, tc.cluster)
			}
			values := map[string]string{initializerWildcard: initializerName, framework.ClusterWildcard: segment}
			match := framework.PathMatch{
				Path:  "/services/initializingworkspaces/test-initializer/clusters/" + segment + "/" + tc.trail, // trailing slash when trail is empty
				Value: func(name string) string { return values[name] },
				Trail: tc.trail,
			}

			var gotResolver, gotPrefix string
			var gotCtx context.Context
			for _, r := range resolvers {
				accepted, prefixToTrim, ctx := r.resolve(ctx, match)
				if accepted {
					gotResolver, gotPrefix, gotCtx = r.name, prefixToTrim, ctx
					break
				}
			}

			require.Equal(t, tc.wantResolver, gotResolver)
			if tc.wantResolver == "" {
				return
			}
			require.Equal(t, tc.prefixToTrim, gotPrefix)
			require.Equal(t, dynamiccontext.APIDomainKey(initializerName), dynamiccontext.APIDomainKeyFrom(gotCtx))
		})
	}
}

func TestIsLogicalClusterRequest(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		path     string
		expected bool
	}{
		{"/apis/core.kcp.io/v1alpha1/logicalclusters", true},
		{"/apis/othergroup/v1alpha1/logicalclusters", false},
		{"/apis/core.kcp.io/v1alpha1/otherresource", false},
	}

	for _, tc := range testCases {
		t.Run(tc.path, func(t *testing.T) {
			t.Parallel()
			result := isLogicalClusterRequest(tc.path)
			require.Equal(t, tc.expected, result, "Result should match expected value")
		})
	}
}

func TestResolveInitializerWorkspaceType(t *testing.T) {
	t.Parallel()
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
		initializer    corev1alpha1.LogicalClusterInitializer
		local          []*tenancyv1alpha1.WorkspaceType
		cached         []*tenancyv1alpha1.WorkspaceType
		wantWST        *tenancyv1alpha1.WorkspaceType
		wantNotFound   bool
		wantParseError bool
	}{
		{
			name:        "system initializer falls through (no WST, no error)",
			initializer: tenancyv1alpha1.WorkspaceAPIBindingsInitializer, // "system:apibindings"
			wantWST:     nil,
		},
		{
			name:           "malformed initializer (no colon) — error, no Mode 2 fallback",
			initializer:    corev1alpha1.LogicalClusterInitializer("garbage"),
			wantParseError: true,
		},
		{
			name:        "WST resolved from local indexer",
			initializer: corev1alpha1.LogicalClusterInitializer("root:universal"),
			local:       []*tenancyv1alpha1.WorkspaceType{universalLocal},
			wantWST:     universalLocal,
		},
		{
			name:        "WST resolved from cache fallback when local misses",
			initializer: corev1alpha1.LogicalClusterInitializer("root:org:custom"),
			cached:      []*tenancyv1alpha1.WorkspaceType{customCached},
			wantWST:     customCached,
		},
		{
			name:         "WST encoded but missing from both indexers — fail closed",
			initializer:  corev1alpha1.LogicalClusterInitializer("root:gone"),
			wantNotFound: true,
		},
		{
			name:        "system initializer ignored even if no WST exists anywhere",
			initializer: corev1alpha1.LogicalClusterInitializer("system:custom-system-initializer"),
			wantWST:     nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			localIdx := newWSTIndexer(t, tc.local...)
			cachedIdx := newWSTIndexer(t, tc.cached...)

			got, err := resolveInitializerWorkspaceType(tc.initializer, localIdx, cachedIdx)
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

func TestBuildVirtualWorkspace(t *testing.T) {
	t.Parallel()
	rootPathPrefix := "/services/initializingworkspaces/"
	cfg := &rest.Config{
		Host: "https://example.com",
	}
	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "ClusterClientSet should not return an error")
	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "ClusterClientSet should not return an error")
	wildcardKcpInformers := kcpinformers.NewSharedInformerFactory(nil, 0)
	cachedKcpInformers := kcpinformers.NewSharedInformerFactory(nil, 0)

	virtualWorkspaces, err := BuildVirtualWorkspace(cfg, rootPathPrefix, dynamicClusterClient, kubeClusterClient, nil, wildcardKcpInformers, cachedKcpInformers)
	require.NoError(t, err, "BuildVirtualWorkspace should not return an error")

	assert.Len(t, virtualWorkspaces, 3, "There should be three virtual workspaces")
	expectedNames := map[string]struct{}{
		wildcardLogicalClustersName: {},
		logicalClustersName:         {},
		workspaceContentName:        {},
	}

	for _, vw := range virtualWorkspaces {
		t.Run(vw.Name, func(t *testing.T) {
			t.Parallel()
			assert.NotNil(t, vw.VirtualWorkspace, "VirtualWorkspace should not be nil")
			assert.Implements(t, (*framework.RootPathResolver)(nil), vw.VirtualWorkspace, "VirtualWorkspace should implement RootPathResolver")
			assert.Implements(t, (*authorizer.Authorizer)(nil), vw.VirtualWorkspace, "VirtualWorkspace should implement Authorizer")
			assert.Implements(t, (*framework.ReadyChecker)(nil), vw.VirtualWorkspace, "VirtualWorkspace should implement ReadyChecker")
			assert.NotNil(t, vw.VirtualWorkspace.Register, "Register should not be nil")

			_, exists := expectedNames[vw.Name]
			assert.True(t, exists, "VirtualWorkspace name should be one of the expected names")
		})
	}
}
