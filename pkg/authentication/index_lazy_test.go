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

package authentication

import (
	"testing"

	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/kcp-dev/sdk/apis/core"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"

	"github.com/kcp-dev/kcp/pkg/indexers"
)

const (
	testWSTCluster = "wstcluster"
	testWSTName    = "custom-type"
	testWACName    = "custom-wac"
)

var testWSTPath = logicalcluster.NewPath("root:org")

// testBuiltState is an authenticatorState built from WorkspaceType and WAC at version "1".
func testBuiltState() authenticatorState {
	return authenticatorState{
		wstResourceVersion:  "1",
		wacResourceVersions: map[string]string{testWACName: "1"},
	}
}

// newLazyIndex returns a lazyIndex with wst in the local WorkspaceType indexer.
// wst may be nil for an empty indexer.
func newLazyIndex(
	t *testing.T,
	wst *tenancyv1alpha1.WorkspaceType,
	getWAC func(logicalcluster.Name, string) (*tenancyv1alpha1.WorkspaceAuthenticationConfiguration, error),
) *lazyIndex {
	t.Helper()

	localWSTIndexer := newTestWSTIndexer()
	if wst != nil {
		require.NoError(t, localWSTIndexer.Add(wst))
	}

	return &lazyIndex{
		localWSTIndexer: localWSTIndexer,
		cacheWSTIndexer: newTestWSTIndexer(),
		getWAC:          getWAC,
	}
}

func newTestWSTIndexer() cache.Indexer {
	return cache.NewIndexer(kcpcache.MetaClusterNamespaceKeyFunc, cache.Indexers{
		indexers.ByLogicalClusterPathAndName: indexers.IndexByLogicalClusterPathAndName,
	})
}

func newTestWST(resourceVersion string) *tenancyv1alpha1.WorkspaceType {
	return &tenancyv1alpha1.WorkspaceType{
		ObjectMeta: metav1.ObjectMeta{
			Name:            testWSTName,
			ResourceVersion: resourceVersion,
			Annotations: map[string]string{
				logicalcluster.AnnotationKey:         testWSTCluster,
				core.LogicalClusterPathAnnotationKey: testWSTPath.String(),
			},
		},
	}
}

// getTestWAC returns a getWAC func serving the test WAC at resourceVersion.
func getTestWAC(resourceVersion string) func(logicalcluster.Name, string) (*tenancyv1alpha1.WorkspaceAuthenticationConfiguration, error) {
	return func(_ logicalcluster.Name, name string) (*tenancyv1alpha1.WorkspaceAuthenticationConfiguration, error) {
		return &tenancyv1alpha1.WorkspaceAuthenticationConfiguration{
			ObjectMeta: metav1.ObjectMeta{
				Name:            name,
				ResourceVersion: resourceVersion,
			},
		}, nil
	}
}

func getTestWACNotFound(_ logicalcluster.Name, name string) (*tenancyv1alpha1.WorkspaceAuthenticationConfiguration, error) {
	return nil, apierrors.NewNotFound(tenancyv1alpha1.Resource("workspaceauthenticationconfigurations"), name)
}

func TestLazyIndexIsCurrent(t *testing.T) {
	t.Parallel()

	testcases := map[string]struct {
		wst      *tenancyv1alpha1.WorkspaceType
		getWAC   func(logicalcluster.Name, string) (*tenancyv1alpha1.WorkspaceAuthenticationConfiguration, error)
		expected bool
	}{
		"unchanged": {
			wst:      newTestWST("1"),
			getWAC:   getTestWAC("1"),
			expected: true,
		},
		"WorkspaceType updated": {
			wst:    newTestWST("2"),
			getWAC: getTestWAC("1"),
		},
		"WorkspaceType deleted": {
			getWAC: getTestWAC("1"),
		},
		"WAC updated": {
			wst:    newTestWST("1"),
			getWAC: getTestWAC("2"),
		},
		"WAC deleted": {
			wst:    newTestWST("1"),
			getWAC: getTestWACNotFound,
		},
	}

	for name, tc := range testcases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			idx := newLazyIndex(t, tc.wst, tc.getWAC)

			current := idx.isCurrent(testWSTPath, testWSTName, testBuiltState())
			require.Equal(t, tc.expected, current)
		})
	}
}
