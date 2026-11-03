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
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kcp-dev/virtual-workspace-framework/framework"
	dynamiccontext "github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic/context"
)

func TestPathPattern(t *testing.T) {
	t.Parallel()

	require.Equal(t,
		"/services/replication/{sliceCluster}/{sliceName}/clusters/{cluster}",
		pathPattern("/services/replication/"))
}

func TestResolveRootPath(t *testing.T) {
	t.Parallel()

	values := map[string]string{
		sliceClusterWildcard:      "my-cluster",
		sliceNameWildcard:         "my-clustercachedresource",
		framework.ClusterWildcard: "other-cluster",
	}
	match := framework.PathMatch{
		Path:  "/services/replication/my-cluster/my-clustercachedresource/clusters/other-cluster/apis",
		Value: func(name string) string { return values[name] },
		Trail: "apis",
	}

	accepted, prefixToStrip, ctx := resolveRootPath(t.Context(), match)

	require.True(t, accepted)
	require.Equal(t, "/services/replication/my-cluster/my-clustercachedresource/clusters/other-cluster", prefixToStrip)
	require.Equal(t, dynamiccontext.APIDomainKey("my-cluster/my-clustercachedresource"), dynamiccontext.APIDomainKeyFrom(ctx))
}
