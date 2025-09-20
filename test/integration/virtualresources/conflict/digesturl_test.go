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

package conflict

import (
	"testing"

	"github.com/stretchr/testify/require"

	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/context"
)

func TestDigestUrl(t *testing.T) {
	rootPathPrefix := "/services/test-conflict-vw/"
	testCases := []struct {
		urlPath             string
		expectedAccept      bool
		expectedCluster     genericapirequest.Cluster
		expectedKey         context.APIDomainKey
		expectedLogicalPath string
	}{
		{
			urlPath:             "/services/test-conflict-vw/root/export-conflict/test-1/clusters/*/api",
			expectedAccept:      true,
			expectedKey:         "test-1/root/export-conflict",
			expectedCluster:     genericapirequest.Cluster{Name: "", Wildcard: true},
			expectedLogicalPath: "/services/test-conflict-vw/root/export-conflict/test-1/clusters/*",
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
