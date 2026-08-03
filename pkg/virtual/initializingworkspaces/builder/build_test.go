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
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authenticationv1 "k8s.io/api/authentication/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/authentication/user"
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
	"github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic/context"

	"github.com/kcp-dev/kcp/pkg/authorization"
	"github.com/kcp-dev/kcp/pkg/indexers"
)

func TestDigestUrl(t *testing.T) {
	t.Parallel()
	rootPathPrefix := "/services/initializingworkspaces/"
	testCases := []struct {
		urlPath             string
		expectedAccept      bool
		expectedCluster     request.Cluster
		expectedKey         context.APIDomainKey
		expectedLogicalPath string
	}{
		{
			urlPath:             "/services/initializingworkspaces/test-initializer/clusters/test-cluster/apis/workload.kcp.io/v1alpha1/synctargets",
			expectedAccept:      true,
			expectedCluster:     request.Cluster{Name: "test-cluster", Wildcard: false, PartialMetadataRequest: false},
			expectedKey:         "test-initializer",
			expectedLogicalPath: "/services/initializingworkspaces/test-initializer/clusters/test-cluster",
		},
		{
			urlPath:             "/services/initializingworkspaces/test-initializer/clusters/*/apis/workload.kcp.io/v1alpha1/synctargets",
			expectedAccept:      true,
			expectedCluster:     request.Cluster{Name: "", Wildcard: true, PartialMetadataRequest: false},
			expectedKey:         "test-initializer",
			expectedLogicalPath: "/services/initializingworkspaces/test-initializer/clusters/*",
		},
		{
			urlPath:             "/services/initializingworkspaces/test-initializer/clusters/",
			expectedAccept:      true,
			expectedCluster:     request.Cluster{Name: "", Wildcard: false, PartialMetadataRequest: false},
			expectedKey:         "test-initializer",
			expectedLogicalPath: "/services/initializingworkspaces/test-initializer/clusters",
		},
		{
			urlPath:             "/services/initializingworkspaces/test-initializer/clusters/*",
			expectedAccept:      true,
			expectedCluster:     request.Cluster{Name: "", Wildcard: true, PartialMetadataRequest: false},
			expectedKey:         "test-initializer",
			expectedLogicalPath: "/services/initializingworkspaces/test-initializer/clusters/*",
		},
		{
			urlPath:             "/services/initializingworkspaces/test-initializer/",
			expectedAccept:      false,
			expectedCluster:     request.Cluster{Name: "", Wildcard: false, PartialMetadataRequest: false},
			expectedKey:         "",
			expectedLogicalPath: "",
		},
		{
			urlPath:             "/services/initializingworkspaces/",
			expectedAccept:      false,
			expectedCluster:     request.Cluster{Name: "", Wildcard: false, PartialMetadataRequest: false},
			expectedKey:         "",
			expectedLogicalPath: "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.urlPath, func(t *testing.T) {
			t.Parallel()
			cluster, key, logicalPath, accepted := digestUrl(tc.urlPath, rootPathPrefix)
			require.Equal(t, tc.expectedAccept, accepted, "Accepted should match expected value")
			require.Equal(t, tc.expectedCluster, cluster, "Cluster should match expected value")
			require.Equal(t, tc.expectedKey, key, "Key should match expected value")
			require.Equal(t, tc.expectedLogicalPath, logicalPath, "LogicalPath should match expected value")
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

func TestTransportForInitializer(t *testing.T) {
	t.Parallel()

	const initializer = corev1alpha1.LogicalClusterInitializer("root:org:custom")
	syntheticGroup := authorization.InitializerGroup(initializer)

	wstAllowingCreate := &tenancyv1alpha1.WorkspaceType{
		Spec: tenancyv1alpha1.WorkspaceTypeSpec{
			InitializerPermissions: []rbacv1.PolicyRule{{
				APIGroups: []string{"apis.kcp.io"},
				Resources: []string{"apibindings"},
				Verbs:     []string{"create"},
			}},
		},
	}
	wstAllowingGetOnly := &tenancyv1alpha1.WorkspaceType{
		Spec: tenancyv1alpha1.WorkspaceTypeSpec{
			InitializerPermissions: []rbacv1.PolicyRule{{
				APIGroups: []string{"apis.kcp.io"},
				Resources: []string{"apibindings"},
				Verbs:     []string{"get"},
			}},
		},
	}
	wstNoPermissions := &tenancyv1alpha1.WorkspaceType{}

	caller := &user.DefaultInfo{
		Name:   "alice",
		UID:    "uid-alice",
		Groups: []string{"g1"},
		Extra:  map[string][]string{"scope": {"cluster:root"}},
	}
	owner := &corev1alpha1.OwnerUserInfo{
		Username: "bob",
		UID:      "uid-bob",
		Groups:   []string{"o1"},
		Extra:    map[string]authenticationv1.ExtraValue{"scope": {"cluster:root"}},
	}

	// A create request against apibindings, optionally carrying an authenticated user.
	newRequest := func(u user.Info) *http.Request {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost,
			"/clusters/some-cluster/apis/apis.kcp.io/v1alpha2/apibindings", http.NoBody)
		if u != nil {
			req = req.WithContext(request.WithUser(req.Context(), u))
		}
		return req
	}

	tests := []struct {
		name       string
		request    *http.Request
		wst        *tenancyv1alpha1.WorkspaceType
		createdBy  *corev1alpha1.OwnerUserInfo
		wantErr    bool
		wantStatus int
		// wantUser == "" means no impersonation, i.e. the VW's own system identity.
		wantUser   string
		wantGroups []string
		wantExtra  map[string][]string
	}{
		{
			name:     "system initializer uses the VW system identity",
			request:  newRequest(nil),
			wst:      nil,
			wantUser: "",
		},
		{
			name:       "caller allowed, impersonates caller plus synthetic group",
			request:    newRequest(caller),
			wst:        wstAllowingCreate,
			wantUser:   "alice",
			wantGroups: []string{syntheticGroup, "g1"},
			wantExtra:  map[string][]string{"scope": {"cluster:root"}},
		},
		{
			name:       "owner impersonation fallback",
			request:    newRequest(nil),
			wst:        wstNoPermissions,
			createdBy:  owner,
			wantUser:   "bob",
			wantGroups: []string{"o1"},
			wantExtra:  map[string][]string{"scope": {"cluster:root"}},
		},
		{
			name:       "caller denied by initializerPermissions",
			request:    newRequest(caller),
			wst:        wstAllowingGetOnly,
			wantErr:    true,
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "no user in context",
			request:    newRequest(nil),
			wst:        wstAllowingCreate,
			wantErr:    true,
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:       "no createdBy recorded",
			request:    newRequest(nil),
			wst:        wstNoPermissions,
			wantErr:    true,
			wantStatus: http.StatusInternalServerError,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			captured := &capturingRoundTripper{}
			rt, status, err := transportForInitializer(tc.request, &rest.Config{Transport: captured}, initializer, tc.wst, tc.createdBy)
			if tc.wantErr {
				require.Error(t, err)
				require.Equal(t, tc.wantStatus, status)
				require.Nil(t, rt)
				return
			}
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, status)
			require.NotNil(t, rt)

			outReq, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://kcp.test/", http.NoBody)
			require.NoError(t, err)
			resp, err := rt.RoundTrip(outReq)
			require.NoError(t, err)
			resp.Body.Close()

			got := captured.header
			if tc.wantUser == "" {
				assert.Empty(t, got.Get("Impersonate-User"))
				return
			}
			assert.Equal(t, tc.wantUser, got.Get("Impersonate-User"))
			assert.ElementsMatch(t, tc.wantGroups, got.Values("Impersonate-Group"))
			for k, vals := range tc.wantExtra {
				assert.Equal(t, vals, got.Values("Impersonate-Extra-"+k))
			}
		})
	}
}

type capturingRoundTripper struct {
	header http.Header
}

func (c *capturingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	c.header = req.Header.Clone()
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: http.NoBody}, nil
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
