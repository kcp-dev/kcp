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

package builder

import (
	"testing"

	"github.com/stretchr/testify/require"

	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/virtual-workspace-framework/framework"
	dynamiccontext "github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic/context"
)

func TestPathPatterns(t *testing.T) {
	t.Parallel()

	require.Equal(t,
		[]string{
			"/services/apiresourceschema/{consumerCluster}",
			"/services/apiresourceschema/{consumerCluster}/clusters/{cluster}",
		},
		pathPatterns("/services/apiresourceschema/"))
}

func TestResolveRootPath(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		consumer     string
		cluster      string
		accepted     bool
		prefixToTrim string
	}{
		"consumer only":            {consumer: "abc", accepted: true, prefixToTrim: "/services/apiresourceschema/abc"},
		"wildcard cluster":         {consumer: "abc", cluster: "*", accepted: true, prefixToTrim: "/services/apiresourceschema/abc/clusters/*"},
		"consumer as cluster":      {consumer: "abc", cluster: "abc", accepted: true, prefixToTrim: "/services/apiresourceschema/abc/clusters/abc"},
		"other cluster":            {consumer: "abc", cluster: "other"},
		"wildcard consumer":        {consumer: "*"},
		"workspace path consumer":  {consumer: "root:org"},
		"workspace path, wildcard": {consumer: "root:org", cluster: "*"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			path := "/services/apiresourceschema/" + tc.consumer
			if tc.cluster != "" {
				path += "/clusters/" + tc.cluster
			}
			values := map[string]string{consumerClusterWildcard: tc.consumer, framework.ClusterWildcard: tc.cluster}
			match := framework.NewPathMatch(path+"/apis/apis.kcp.io/v1alpha1/apiresourceschemas", "apis/apis.kcp.io/v1alpha1/apiresourceschemas", values)

			accepted, prefixToTrim, ctx := resolveRootPath(t.Context(), match)

			require.Equal(t, tc.accepted, accepted)
			if !tc.accepted {
				return
			}
			require.Equal(t, tc.prefixToTrim, prefixToTrim)
			require.Equal(t, &genericapirequest.Cluster{Name: "abc"}, genericapirequest.ClusterFrom(ctx))
			require.Equal(t, dynamiccontext.APIDomainKey("abc"), dynamiccontext.APIDomainKeyFrom(ctx))
		})
	}
}
