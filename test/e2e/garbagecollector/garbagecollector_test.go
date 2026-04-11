/*
Copyright 2022 The kcp Authors.

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

package garbagecollector

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
	metav1ac "k8s.io/client-go/applyconfigurations/meta/v1"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/sdk/apis/core"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"

	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestGarbageCollectorBuiltInCoreV1Types(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	cfg := server.BaseConfig(t)

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err, "error creating kube cluster client")

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))

	wsPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("gc-builtins"))

	t.Logf("Creating owner configmap")
	owner, err := kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default").Apply(t.Context(),
		corev1ac.ConfigMap("owner", "default"),
		metav1.ApplyOptions{FieldManager: "e2e-test-runner"})
	require.NoError(t, err, "Error applying owner configmap %s|default/owner", wsPath)

	t.Logf("Creating owned configmap")
	owned, err := kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default").Apply(t.Context(),
		corev1ac.ConfigMap("owned", "default").
			WithOwnerReferences(metav1ac.OwnerReference().
				WithAPIVersion("v1").
				WithKind("ConfigMap").
				WithName(owner.Name).
				WithUID(owner.UID)),
		metav1.ApplyOptions{FieldManager: "e2e-test-runner"})
	require.NoError(t, err, "Error applying owned configmap %s|default/owned", wsPath)

	t.Logf("Deleting owner configmap")
	err = kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default").Delete(t.Context(), owner.Name, metav1.DeleteOptions{})

	t.Logf("Waiting for the owned configmap to be garbage collected")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err = kubeClusterClient.Cluster(wsPath).CoreV1().ConfigMaps("default").Get(t.Context(), owned.Name, metav1.GetOptions{})
		return apierrors.IsNotFound(err), fmt.Sprintf("configmap not garbage collected: %s", owned.Name)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "error waiting for owned configmap to be garbage collected")
}
