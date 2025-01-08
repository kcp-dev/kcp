package builder

import (
	"testing"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/kcp/pkg/virtual/framework"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/context"
	kcpinformers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/rest"
)

func TestBuildVirtualWorkspace(t *testing.T) {
	rootPathPrefix := "/services/initializingworkspaces/"
	cfg := &rest.Config{
		Host: "https://example.com",
	}
	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "ClusterClientSet should not return an error")
	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "ClusterClientSet should not return an error")
	wildcardKcpInformers := kcpinformers.NewSharedInformerFactory(nil, 0)

	virtualWorkspaces, err := BuildVirtualWorkspace(cfg, rootPathPrefix, dynamicClusterClient, kubeClusterClient, wildcardKcpInformers)
	require.NoError(t, err, "BuildVirtualWorkspace should not return an error")

	assert.Len(t, virtualWorkspaces, 3, "There should be three virtual workspaces")
	expectedNames := map[string]bool{
		wildcardLogicalClustersName: false,
		logicalClustersName:         false,
		workspaceContentName:        false,
	}

	for _, vw := range virtualWorkspaces {
		t.Run(vw.Name, func(t *testing.T) {
			require.NotNil(t, vw.VirtualWorkspace, "VirtualWorkspace should not be nil")
			require.Implements(t, (*framework.RootPathResolver)(nil), vw.VirtualWorkspace, "VirtualWorkspace should implement RootPathResolver")
			require.Implements(t, (*authorizer.Authorizer)(nil), vw.VirtualWorkspace, "VirtualWorkspace should implement Authorizer")
			require.Implements(t, (*framework.ReadyChecker)(nil), vw.VirtualWorkspace, "VirtualWorkspace should implement ReadyChecker")
			require.NotNil(t, vw.VirtualWorkspace.Register, "Register should not be nil")
			_, exists := expectedNames[vw.Name]
			require.True(t, exists, "VirtualWorkspace name should be one of the expected names")
			expectedNames[vw.Name] = true
		})
	}
	for name, found := range expectedNames {
		require.True(t, found, "Expected VirtualWorkspace name %s was not found", name)
	}
}

func TestDigestUrl(t *testing.T) {
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
			expectedCluster:     request.Cluster(request.Cluster{Name: "test-cluster", Wildcard: false, PartialMetadataRequest: false}),
			expectedKey:         "test-initializer",
			expectedLogicalPath: "/services/initializingworkspaces/test-initializer/clusters/test-cluster",
		},
		{
			urlPath:             "/services/initializingworkspaces/test-initializer/clusters/*/apis/workload.kcp.io/v1alpha1/synctargets",
			expectedAccept:      true,
			expectedCluster:     request.Cluster(request.Cluster{Name: "", Wildcard: true, PartialMetadataRequest: false}),
			expectedKey:         "test-initializer",
			expectedLogicalPath: "/services/initializingworkspaces/test-initializer/clusters/*",
		},
		{
			urlPath:             "/services/initializingworkspaces/test-initializer/clusters/",
			expectedAccept:      true,
			expectedCluster:     request.Cluster(request.Cluster{Name: "", Wildcard: false, PartialMetadataRequest: false}),
			expectedKey:         "test-initializer",
			expectedLogicalPath: "/services/initializingworkspaces/test-initializer/clusters",
		},
		{
			urlPath:             "/services/initializingworkspaces/test-initializer/clusters/*",
			expectedAccept:      true,
			expectedCluster:     request.Cluster(request.Cluster{Name: "", Wildcard: true, PartialMetadataRequest: false}),
			expectedKey:         "test-initializer",
			expectedLogicalPath: "/services/initializingworkspaces/test-initializer/clusters/*",
		},
		{
			urlPath:             "/services/initializingworkspaces/test-initializer/",
			expectedAccept:      false,
			expectedCluster:     request.Cluster(request.Cluster{Name: "", Wildcard: false, PartialMetadataRequest: false}),
			expectedKey:         "",
			expectedLogicalPath: "",
		},
		{
			urlPath:             "/services/initializingworkspaces/",
			expectedAccept:      false,
			expectedCluster:     request.Cluster(request.Cluster{Name: "", Wildcard: false, PartialMetadataRequest: false}),
			expectedKey:         "",
			expectedLogicalPath: "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.urlPath, func(t *testing.T) {
			cluster, key, logicalPath, accepted := digestUrl(tc.urlPath, rootPathPrefix)
			require.Equal(t, tc.expectedAccept, accepted, "Accepted should match expected value")
			require.Equal(t, tc.expectedCluster, cluster, "Cluster should match expected value")
			require.Equal(t, tc.expectedKey, key, "Key should match expected value")
			require.Equal(t, tc.expectedLogicalPath, logicalPath, "LogicalPath should match expected value")
		})
	}
}

func TestIsLogicalClusterRequest(t *testing.T) {
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
			result := isLogicalClusterRequest(tc.path)
			require.Equal(t, tc.expected, result, "Result should match expected value")
		})
	}
}
