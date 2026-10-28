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

package apibinding

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authorizationv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"
	kcptestingserver "github.com/kcp-dev/sdk/testing/server"

	"github.com/kcp-dev/kcp/config/helpers"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest"
	wildwestv1alpha1 "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest/v1alpha1"
	"github.com/kcp-dev/kcp/test/e2e/framework"
	"github.com/kcp-dev/kcp/test/server"
)

// TestMaximalPermissionPolicyWebhookEscalation validates that an
// APIExport's maximal permission policy also bounds authorization
// granted by an authz webhook, regardless of where--authorization-order
// puts the webhook.
func TestMaximalPermissionPolicyWebhookEscalation(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	for _, tt := range []struct {
		name, order string
	}{
		{"default order", ""},
		{"webhook ahead of RBAC", "AlwaysAllowGroups,AlwaysAllowPaths,Webhook,RBAC"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			escalateThroughWebhook(t, tt.order)
		})
	}
}

func escalateThroughWebhook(t *testing.T, authOrder string) {
	webhook := framework.StartAuthzWebhook(t.Context(), t, server.NoOpinion)

	args := slices.Clone(webhook.Args)
	if authOrder != "" {
		args = append(args, "--authorization-order", authOrder)
	}

	srv := kcptesting.PrivateKcpServer(t,
		kcptesting.WithDefaultTokenAuthFile(t),
		kcptestingserver.WithCustomArguments(args...),
	)
	// Avoid the known early-shutdown race (see authorizer e2e, kcp#3488).
	time.Sleep(3 * time.Second)

	cfg := srv.BaseConfig(t)

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err)
	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err)
	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err)

	providerPath, _ := kcptesting.NewWorkspaceFixture(t, srv, core.RootCluster.Path())
	consumerPath, _ := kcptesting.NewWorkspaceFixture(t, srv, core.RootCluster.Path())

	t.Logf("Install the cowboys APIExport in provider %q", providerPath)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(kcpClusterClient.Cluster(providerPath).Discovery()))
	require.NoError(t, helpers.CreateResourceFromFS(t.Context(), dynamicClusterClient.Cluster(providerPath), mapper, nil, "apiresourceschema_cowboys.yaml", testFiles))

	_, err = kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Create(t.Context(), &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{Name: "today-cowboys"},
		Spec: apisv1alpha2.APIExportSpec{
			Resources: []apisv1alpha2.ResourceSchema{
				{
					Name:    "cowboys",
					Group:   wildwest.GroupName,
					Schema:  "today.cowboys.wildwest.dev",
					Storage: apisv1alpha2.ResourceSchemaStorage{CRD: &apisv1alpha2.ResourceSchemaStorageCRD{}},
				},
			},
			MaximalPermissionPolicy: &apisv1alpha2.MaximalPermissionPolicy{Local: &apisv1alpha2.LocalAPIExportPolicy{}},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Log("Install MaximalPermissionPolicy for APIExport that grants create and reads, but no update or delete")
	clusterRole, clusterRoleBinding := createClusterRoleAndBindings(
		"cowboys-readonly",
		"apis.kcp.io:binding:system:authenticated",
		"Group",
		wildwest.GroupName,
		"cowboys",
		"",
		[]string{"create", "get", "list", "watch"},
	)
	_, err = kubeClusterClient.Cluster(providerPath).RbacV1().ClusterRoles().Create(t.Context(), clusterRole, metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = kubeClusterClient.Cluster(providerPath).RbacV1().ClusterRoleBindings().Create(t.Context(), clusterRoleBinding, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Bind the export in consumer %q", consumerPath)
	_, err = kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Create(t.Context(), &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "cowboys"},
		Spec: apisv1alpha2.APIBindingSpec{
			Reference: apisv1alpha2.BindingReference{
				Export: &apisv1alpha2.ExportBindingReference{
					Path: providerPath.String(),
					Name: "today-cowboys",
				},
			},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		resources, err := kcpClusterClient.Cluster(consumerPath).Discovery().ServerResourcesForGroupVersion(wildwestv1alpha1.SchemeGroupVersion.String())
		return err == nil && resourceExists(resources, "cowboys")
	}, wait.ForeverTestTimeout, time.Millisecond*100, "cowboys never showed up in %q discovery", consumerPath)

	t.Logf("Grant user-2 cluster-admin in %q", consumerPath)
	framework.AdmitWorkspaceAccess(t.Context(), t, kubeClusterClient, consumerPath, []string{"user-2"}, nil, true)

	gvr := wildwestv1alpha1.SchemeGroupVersion.WithResource("cowboys")
	cowboys := func(config *rest.Config) dynamic.ResourceInterface {
		client, err := kcpdynamic.NewForConfig(config)
		require.NoError(t, err)
		return client.Cluster(consumerPath).Resource(gvr).Namespace("default")
	}

	instanceAdmin := cowboys(srv.RootShardSystemMasterBaseConfig(t))
	clusterAdmin := cowboys(framework.StaticTokenUserConfig("user-2", rest.CopyConfig(cfg)))
	webhookAdmin := cowboys(framework.StaticTokenUserConfig("user-3", rest.CopyConfig(cfg)))

	// create a cowboy for the tests to get/update
	target := "authz-target"
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := instanceAdmin.Create(t.Context(), newCowboy(target, ""), metav1.CreateOptions{})
		if err != nil {
			return false, err.Error()
		}

		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*100, "the instance admin must be able to create cowboys")

	t.Log("Webhook grants no authz")
	checkAllowEverything(t, t.Context(), "no-opinion", "instance-admin", target, instanceAdmin) // kcp instance admin has implicit cluster-admin
	checkBoundByPolicy(t, t.Context(), "no-opinion", "cluster-admin", target, clusterAdmin)     // user-2 is cluster-admin
	checkDeniedEverything(t, t.Context(), "no-opinion", "no-rbac", target, webhookAdmin)        // user-3 has no access without authz-webhook

	t.Log("Let the webhook grant above the maximal permission policy")
	webhook.Handler.SetHandler(allowWebhookGrants)

	t.Log("Webhook grants effectively cluster-admin to user-2 and user-3 but MPP still gates")
	checkAllowEverything(t, t.Context(), "granting", "instance-admin", target, instanceAdmin) // kcp instance admin can still elevate above MPP
	checkBoundByPolicy(t, t.Context(), "granting", "cluster-admin", target, clusterAdmin)     // user-2 is still bound by MPP
	checkBoundByPolicy(t, t.Context(), "granting", "no-rbac", target, webhookAdmin)           // user-3 can now access but is still bound by MPP
}

// checkAllowEverything asserts every verb is permitted.
func checkAllowEverything(t *testing.T, ctx context.Context, phase, who, target string, cowboys dynamic.ResourceInterface) {
	t.Helper()

	created := createPermitted(t, ctx, phase, who, cowboys)

	_, err := cowboys.Get(ctx, target, metav1.GetOptions{})
	assert.NoErrorf(t, err, "%s/%s: get must be permitted", phase, who) //nolint:testifylint // this is a validation, not something later calls rely on

	_, err = cowboys.List(ctx, metav1.ListOptions{})
	assert.NoErrorf(t, err, "%s/%s: list must be permitted", phase, who) //nolint:testifylint // this is a validation, not something later calls rely on

	err = labelCowboy(ctx, cowboys, target, phase)
	assert.NoErrorf(t, err, "%s/%s: update must be permitted", phase, who) //nolint:testifylint // this is a validation, not something later calls rely on

	// the actor's own object, so the shared target survives for the actors that run after it
	err = cowboys.Delete(ctx, created, metav1.DeleteOptions{})
	assert.NoErrorf(t, err, "%s/%s: delete must be permitted", phase, who)
}

// checkBoundByPolicy asserts create and the reads are permitted and update and delete are forbidden.
func checkBoundByPolicy(t *testing.T, ctx context.Context, phase, who, target string, cowboys dynamic.ResourceInterface) {
	t.Helper()

	created := createPermitted(t, ctx, phase, who, cowboys)

	_, err := cowboys.Get(ctx, target, metav1.GetOptions{})
	assert.NoErrorf(t, err, "%s/%s: get must be permitted", phase, who) //nolint:testifylint // this is a validation, not something later calls rely on

	_, err = cowboys.List(ctx, metav1.ListOptions{})
	assert.NoErrorf(t, err, "%s/%s: list must be permitted", phase, who) //nolint:testifylint // this is a validation, not something later calls rely on

	err = labelCowboy(ctx, cowboys, target, phase)
	assert.Truef(t, apierrors.IsForbidden(err), "%s/%s: update must be forbidden by the maximal permission policy, got %v", phase, who, err)

	err = cowboys.Delete(ctx, created, metav1.DeleteOptions{})
	assert.Truef(t, apierrors.IsForbidden(err), "%s/%s: delete must be forbidden by the maximal permission policy, got %v", phase, who, err)
}

// checkDeniedEverything asserts every verb is forbidden.
func checkDeniedEverything(t *testing.T, ctx context.Context, phase, who, target string, cowboys dynamic.ResourceInterface) {
	t.Helper()

	_, err := cowboys.Create(ctx, newCowboy("", phase+"-"+who+"-"), metav1.CreateOptions{})
	assert.Truef(t, apierrors.IsForbidden(err), "%s/%s: create must be forbidden by the maximal permission policy, got %v", phase, who, err)

	_, err = cowboys.Get(ctx, target, metav1.GetOptions{})
	assert.Truef(t, apierrors.IsForbidden(err), "%s/%s: get must be forbidden by the maximal permission policy, got %v", phase, who, err)

	_, err = cowboys.List(ctx, metav1.ListOptions{})
	assert.Truef(t, apierrors.IsForbidden(err), "%s/%s: list must be forbidden by the maximal permission policy, got %v", phase, who, err)

	err = labelCowboy(ctx, cowboys, target, phase)
	assert.Truef(t, apierrors.IsForbidden(err), "%s/%s: update must be forbidden by the maximal permission policy, got %v", phase, who, err)

	err = cowboys.Delete(ctx, target, metav1.DeleteOptions{})
	assert.Truef(t, apierrors.IsForbidden(err), "%s/%s: delete must be forbidden by the maximal permission policy, got %v", phase, who, err)
}

// createPermitted creates a cowboy named by the server, waiting until the chain allows it - create is the
// verb that needs workspace access, the binding and the provider's policy propagated - and returns its
// name.
func createPermitted(t *testing.T, ctx context.Context, phase, who string, cowboys dynamic.ResourceInterface) string {
	t.Helper()

	var created string
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		cowboy, err := cowboys.Create(ctx, newCowboy("", phase+"-"+who+"-"), metav1.CreateOptions{})
		if err != nil {
			return false, err.Error()
		}

		created = cowboy.GetName()

		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*100, "%s/%s: create must be permitted", phase, who)

	return created
}

// allowWebhookGrants grants user-2 and user-3 allow for everything.
func allowWebhookGrants(sar authorizationv1.SubjectAccessReview) (authorizationv1.SubjectAccessReview, error) {
	switch sar.Spec.User {
	case "user-2", "user-3":
		return server.Allow(sar)
	default:
		return server.NoOpinion(sar)
	}
}

// newCowboy names the cowboy outright or has the server generate a name from generateName.
func newCowboy(name, generateName string) *unstructured.Unstructured {
	metadata := map[string]any{}
	if name != "" {
		metadata["name"] = name
	}
	if generateName != "" {
		metadata["generateName"] = generateName
	}

	return &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": wildwestv1alpha1.SchemeGroupVersion.String(),
			"kind":       "Cowboy",
			"metadata":   metadata,
		},
	}
}

// labelCowboy adds a label naming the phase, so a permitted update is a real write and not a no-op.
func labelCowboy(ctx context.Context, cowboys dynamic.ResourceInterface, name, phase string) error {
	cowboy, err := cowboys.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}

	labels := cowboy.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels["kcp.e2e/phase"] = phase
	cowboy.SetLabels(labels)

	_, err = cowboys.Update(ctx, cowboy, metav1.UpdateOptions{})

	return err
}
