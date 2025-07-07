/*
Copyright 2025 The KCP Authors.

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
	"testing"

	"github.com/stretchr/testify/require"

	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/context"
)

func TestDigestUrl(t *testing.T) {
	rootPathPrefix := "/services/replication/"
	testCases := []struct {
		urlPath             string
		expectedAccept      bool
		expectedCluster     genericapirequest.Cluster
		expectedKey         context.APIDomainKey
		expectedLogicalPath string
	}{
		{
			urlPath:             "/services/replication/my-cluster/my-cachedresource/clusters/my-cluster/apis",
			expectedAccept:      true,
			expectedKey:         "my-cluster/my-cachedresource",
			expectedCluster:     genericapirequest.Cluster{Name: "my-cluster", Wildcard: false},
			expectedLogicalPath: "/services/replication/my-cluster/my-cachedresource/clusters/my-cluster",
		},
		{
			urlPath:             "/services/replication/my-cluster/my-cachedresource/clusters/my-cluster",
			expectedAccept:      true,
			expectedKey:         "my-cluster/my-cachedresource",
			expectedCluster:     genericapirequest.Cluster{Name: "my-cluster", Wildcard: false},
			expectedLogicalPath: "/services/replication/my-cluster/my-cachedresource/clusters/my-cluster",
		},
		{
			urlPath:             "/services/replication/my-cluster/my-cachedresource/clusters/other-cluster",
			expectedAccept:      true,
			expectedKey:         "my-cluster/my-cachedresource",
			expectedCluster:     genericapirequest.Cluster{Name: "other-cluster", Wildcard: false},
			expectedLogicalPath: "/services/replication/my-cluster/my-cachedresource/clusters/other-cluster",
		},
		{
			urlPath:             "/services/replication/my-cluster/my-cachedresource/clusters",
			expectedAccept:      false,
			expectedKey:         "",
			expectedCluster:     genericapirequest.Cluster{},
			expectedLogicalPath: "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.urlPath, func(t *testing.T) {
			clusterName, key, logicalPath, accepted := digestURL(tc.urlPath, rootPathPrefix)
			require.Equal(t, tc.expectedAccept, accepted, "Accepted should match expected value")
			require.Equal(t, tc.expectedKey, key, "Key should match expected value")
			require.Equal(t, tc.expectedCluster, clusterName, "cluster name should match expected value")
			require.Equal(t, tc.expectedLogicalPath, logicalPath, "LogicalPath should match expected value")
		})
	}
}
