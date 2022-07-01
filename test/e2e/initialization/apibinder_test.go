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

package initialization

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kcp-dev/logicalcluster"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	"github.com/kcp-dev/kcp/pkg/reconciler/tenancy/initialization/apibinder"
	initializationv1alpha1 "github.com/kcp-dev/kcp/pkg/reconciler/tenancy/initialization/apis/initialization/v1alpha1"
	initializationclient "github.com/kcp-dev/kcp/pkg/reconciler/tenancy/initialization/client/clientset/versioned"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest"
	wildwestv1alpha1 "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest/v1alpha1"
	wildwestclientset "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/clientset/versioned"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestAPIBindingsInitialization(t *testing.T) {
	t.Parallel()

	server := framework.SharedKcpServer(t)
	workspace := framework.CreateNewWorkspaceFixture(t, server, tenancyv1alpha1.RootCluster)
	clusterName := logicalcluster.From(workspace).Join(workspace.Name)
	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	t.Log("Bootstrapping the APIBinder initializer")
	cfg := server.DefaultConfig(t)
	kcpClusterClient, err := kcpclient.NewClusterForConfig(cfg)
	require.NoError(t, err)
	rootCfg := framework.ShardConfig(t, kcpClusterClient, workspace.Status.Location.Current, cfg)
	err = apibinder.Bootstrap(ctx, rootCfg, clusterName)
	require.NoError(t, err)

	t.Log("Creating an APIExport for Cowboys")
	export := wildwest.Export(ctx, t, server, clusterName)

	t.Log("Configuring the APIBinder ClusterWorkspaceType to add Cowboys")
	initializationClusterClient, err := initializationclient.NewClusterForConfig(cfg)
	require.NoError(t, err)
	apiSetClient := initializationClusterClient.Cluster(clusterName).InitializationV1alpha1().APISets()

	config, err := apiSetClient.Create(ctx, &initializationv1alpha1.APISet{
		ObjectMeta: metav1.ObjectMeta{
			Name: "apibinder", // configuration must share a name with the ClusterWorkspaceType
		},
		Spec: initializationv1alpha1.APISetSpec{Bindings: []apisv1alpha1.APIBindingSpec{
			wildwest.BindingFor(export).Spec,
		}},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	server.Artifact(t, func() (runtime.Object, error) {
		return apiSetClient.Get(ctx, config.Name, metav1.GetOptions{})
	})

	t.Log("Waiting for the APISet to be valid")
	framework.Eventually(t, func() (bool, string) {
		config, err = apiSetClient.Get(ctx, config.Name, metav1.GetOptions{})
		require.NoError(t, err, "could not fetch APISet")
		done := conditions.IsTrue(config, initializationv1alpha1.APIBindingsValid)
		var reason string
		if !done {
			condition := conditions.Get(config, initializationv1alpha1.APIBindingsValid)
			if condition != nil {
				reason = fmt.Sprintf("Not done waiting for APISet %q|%q to be valid: %s: %s", clusterName.String(), config.Name, condition.Reason, condition.Message)
			} else {
				reason = fmt.Sprintf("Not done waiting for APISet %q|%q to be valid: no condition present", clusterName.String(), config.Name)
			}
		}
		return done, reason
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "new APISet never became valid")

	t.Log("Creating a new ClusterWorkspace that will use the APIBinder initializer")
	kcpClusterClient, err := kcpclient.NewClusterForConfig(cfg)
	require.NoError(t, err)
	clusterWorkspaceClient := kcpClusterClient.Cluster(clusterName).TenancyV1alpha1().ClusterWorkspaces()
	cw, err := clusterWorkspaceClient.Create(ctx, &tenancyv1alpha1.ClusterWorkspace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "incredible",
		},
		Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
			Type: tenancyv1alpha1.ClusterWorkspaceTypeReference{
				Name: "APIBinder",
				Path: clusterName.String(),
			},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	server.Artifact(t, func() (runtime.Object, error) {
		return clusterWorkspaceClient.Get(ctx, cw.Name, metav1.GetOptions{})
	})

	t.Log("Waiting for the new ClusterWorkspace to be ready")
	framework.Eventually(t, func() (bool, string) {
		cw, err = clusterWorkspaceClient.Get(ctx, cw.Name, metav1.GetOptions{})
		require.NoError(t, err, "could not fetch ClusterWorkspace")
		if cw.Status.Phase != tenancyv1alpha1.ClusterWorkspacePhaseReady {
			var reasons []string
			for _, condition := range cw.Status.Conditions {
				if condition.Status != corev1.ConditionTrue {
					reasons = append(reasons, fmt.Sprintf("%s: %s: %s", condition.Type, condition.Reason, condition.Message))
				}
			}
			reason := fmt.Sprintf("ClusterWorkspace %q|%q is not yet ready: ", logicalcluster.From(cw).String(), cw.Name)
			return false, reason + strings.Join(reasons, ", ")
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "new ClusterWorkspace never became ready")

	t.Log("Creating a new Cowboy in the workspace")
	childClusterName := logicalcluster.From(cw).Join(cw.Name)

	kubeClusterClient, err := kubernetes.NewClusterForConfig(cfg)
	require.NoError(t, err)
	namespaceClient := kubeClusterClient.Cluster(childClusterName).CoreV1().Namespaces()
	ns, err := namespaceClient.Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "wyoming",
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	server.Artifact(t, func() (runtime.Object, error) {
		return namespaceClient.Get(ctx, ns.Name, metav1.GetOptions{})
	})

	wildwestClusterClient, err := wildwestclientset.NewClusterForConfig(cfg)
	require.NoError(t, err)
	cowboyClient := wildwestClusterClient.Cluster(childClusterName).WildwestV1alpha1().Cowboys(ns.Name)
	cowboy, err := cowboyClient.Create(ctx, &wildwestv1alpha1.Cowboy{
		ObjectMeta: metav1.ObjectMeta{
			Name: "burt",
		},
		Spec: wildwestv1alpha1.CowboySpec{
			Intent: "herding",
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)
	server.Artifact(t, func() (runtime.Object, error) {
		return cowboyClient.Get(ctx, cowboy.Name, metav1.GetOptions{})
	})
}
