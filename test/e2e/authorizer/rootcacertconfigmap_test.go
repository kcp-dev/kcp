/*
Copyright 2022 The KCP Authors.

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

package authorizer

import (
	"context"
	"testing"
	"time"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/kcp-dev/kcp/test/e2e/framework"
)

const DefaultRootCACertConfigmap = "kube-root-ca.crt"

func TestRootCACertConfigmap(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	server := framework.SharedKcpServer(t)
	orgClusterName := framework.NewOrganizationFixture(t, server)
	clusterName := framework.NewWorkspaceFixture(t, server, orgClusterName.Path(), framework.WithName("cluster"))

	cfg := server.BaseConfig(t)
	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err)

	t.Log("Creating namespace")
	namespace, err := kubeClusterClient.Cluster(clusterName.Path()).CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "e2e-sa-",
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err, "failed to create namespace")

	t.Log("Waiting for default configmap to be created")
	require.Eventually(t, func() bool {
		configmap, err := kubeClusterClient.Cluster(clusterName.Path()).CoreV1().ConfigMaps(namespace.Name).Get(ctx, DefaultRootCACertConfigmap, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return false
		}
		require.NoError(t, err, "failed to get configmap")

		if v, ok := configmap.Data["ca.crt"]; ok {
			if len(v) > 0 {
				return true
			}
		}

		return false
	}, wait.ForeverTestTimeout, time.Millisecond*100, "default CACert configmap not created")
}
