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

package quota

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	"github.com/kcp-dev/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	topologyv1alpha1 "github.com/kcp-dev/sdk/apis/topology/v1alpha1"
	kcptesting "github.com/kcp-dev/sdk/testing"

	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestOpenAPIv3(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	cfg := server.BaseConfig(t)

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating kube cluster client")

	wsPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))

	require.EventuallyWithT(t, func(c *assert.CollectT) {
		t.Logf("Checking /openapi/v3 paths for %q", wsPath)
		openAPIV3 := kubeClusterClient.Cluster(wsPath).Discovery().OpenAPIV3()
		paths, err := openAPIV3.Paths()
		require.NoError(c, err, "error retrieving %q openapi v3 paths", wsPath)

		got := sets.NewString()
		for path := range paths {
			got.Insert(path)
		}

		expected := sets.NewString(
			"api/v1",
			"apis/admissionregistration.k8s.io/v1",
			"apis/authentication.k8s.io/v1",
			// any many more

			"apis/"+tenancyv1alpha1.SchemeGroupVersion.String(),
			"apis/"+apisv1alpha1.SchemeGroupVersion.String(),
			"apis/"+corev1alpha1.SchemeGroupVersion.String(),
			"apis/"+topologyv1alpha1.SchemeGroupVersion.String(),
		)
		require.Empty(c, expected.Difference(got))
	}, wait.ForeverTestTimeout, 100*time.Millisecond)
}
