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
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/util/retry"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"

	"github.com/kcp-dev/kcp/config/helpers"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

// TestAPIBindingWithMissingBoundCRDDoesNotBlockOtherBindings covers a workspace-wide
// failure mode: one APIBinding whose status references a bound CRD that no longer
// exists used to wedge *every* APIBinding in that workspace.
//
// newConflictChecker walks status.boundResources of all APIBindings in the workspace
// and looks each bound CRD up by schema UID. It returned a NotFound as a hard error,
// which aborts the reconcile of whichever binding is being synced -- before any
// condition is written. The result is a workspace where new bindings sit in phase
// Binding with an empty status forever, nothing is logged above V(4), and the
// failure persists across restarts because it is recorded in the stored status.
//
// Nothing prunes a boundResource whose group/resource the APIExport no longer serves,
// so such a reference can be permanent. This test reproduces that by appending one
// to a healthy binding's status, then binding a second, unrelated APIExport in the
// same workspace: that second binding must still bind.
func TestAPIBindingWithMissingBoundCRDDoesNotBlockOtherBindings(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	providerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	consumerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)

	cfg := server.BaseConfig(t)

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")
	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic cluster client for server")

	// APIBinding status is system-owned, so forging a boundResource needs a
	// privileged client; kcp-admin is rejected on apibindings/status.
	privilegedClusterClient, err := kcpclientset.NewForConfig(server.RootShardSystemMasterBaseConfig(t))
	require.NoError(t, err, "failed to construct privileged kcp cluster client for server")

	t.Logf("Install the cowboys and tlsroutes APIResourceSchemas into provider workspace %q", providerPath)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(kcpClusterClient.Cluster(providerPath).Discovery()))
	for _, file := range []string{"apiresourceschema_cowboys.yaml", "apiresourceschema_tlsroutes.yaml"} {
		err = helpers.CreateResourceFromFS(t.Context(), dynamicClusterClient.Cluster(providerPath), mapper, nil, file, testFiles)
		require.NoError(t, err, "failed to create %s", file)
	}

	// Two independent APIExports, so the two bindings in the consumer workspace share
	// nothing but the workspace itself.
	exports := map[string]apisv1alpha2.ResourceSchema{
		"cowboys": {
			Name:    "cowboys",
			Group:   "wildwest.dev",
			Schema:  "today.cowboys.wildwest.dev",
			Storage: apisv1alpha2.ResourceSchemaStorage{CRD: &apisv1alpha2.ResourceSchemaStorageCRD{}},
		},
		"tlsroutes": {
			Name:    "tlsroutes",
			Group:   "gateway.networking.k8s.io",
			Schema:  "latest.tlsroutes.gateway.networking.k8s.io",
			Storage: apisv1alpha2.ResourceSchemaStorage{CRD: &apisv1alpha2.ResourceSchemaStorageCRD{}},
		},
	}
	for name, resource := range exports {
		t.Logf("Create APIExport %q in %q", name, providerPath)
		_, err = kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Create(t.Context(), &apisv1alpha2.APIExport{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec:       apisv1alpha2.APIExportSpec{Resources: []apisv1alpha2.ResourceSchema{resource}},
		}, metav1.CreateOptions{})
		require.NoError(t, err, "failed to create APIExport %s", name)

		kcptestinghelpers.Eventually(t, func() (bool, string) {
			export, err := kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Get(t.Context(), name, metav1.GetOptions{})
			if err != nil {
				return false, err.Error()
			}
			return export.Status.IdentityHash != "", "identity hash not set yet"
		}, wait.ForeverTestTimeout, time.Millisecond*100)
	}

	bind := func(name string) {
		t.Logf("Bind APIExport %q in consumer workspace %q", name, consumerPath)
		kcptestinghelpers.Eventually(t, func() (bool, string) {
			_, err := kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Create(t.Context(), &apisv1alpha2.APIBinding{
				ObjectMeta: metav1.ObjectMeta{Name: name},
				Spec: apisv1alpha2.APIBindingSpec{
					Reference: apisv1alpha2.BindingReference{
						Export: &apisv1alpha2.ExportBindingReference{Path: providerPath.String(), Name: name},
					},
				},
			}, metav1.CreateOptions{})
			return err == nil, fmt.Sprintf("Error creating APIBinding: %v", err)
		}, wait.ForeverTestTimeout, time.Millisecond*100)
	}

	bind("cowboys")
	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(t.Context(), "cowboys", metav1.GetOptions{})
	}, kcptestinghelpers.Is(apisv1alpha2.InitialBindingCompleted))

	// Append a boundResource for a group/resource this APIExport does not serve,
	// pointing at a schema UID that has no bound CRD. Because the reconciler only
	// walks the APIExport's current resources, it will neither replace nor prune this
	// entry -- the same permanent dangling reference an APIExport leaves behind when
	// it rotates a schema while a bound CRD is missing.
	const staleUID = "00000000-0000-0000-0000-000000000000"
	t.Logf("Append a boundResource referencing the non-existent bound CRD %q", staleUID)
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		binding, err := privilegedClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(t.Context(), "cowboys", metav1.GetOptions{})
		if err != nil {
			return err
		}
		binding.Status.BoundResources = append(binding.Status.BoundResources, apisv1alpha2.BoundAPIResource{
			Group:    "stale.kcp.io",
			Resource: "widgets",
			Schema: apisv1alpha2.BoundAPIResourceSchema{
				Name:         "v1.widgets.stale.kcp.io",
				UID:          staleUID,
				IdentityHash: binding.Status.BoundResources[0].Schema.IdentityHash,
			},
		})
		_, err = privilegedClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().UpdateStatus(t.Context(), binding, metav1.UpdateOptions{})
		return err
	})
	require.NoError(t, err, "failed to append the stale boundResource")

	// Make sure the dangling reference is actually still there, otherwise the rest of
	// the test proves nothing.
	t.Logf("Verify the dangling reference persists")
	require.Never(t, func() bool {
		binding, err := kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(t.Context(), "cowboys", metav1.GetOptions{})
		if err != nil {
			return false
		}
		for _, br := range binding.Status.BoundResources {
			if br.Schema.UID == staleUID {
				return false
			}
		}
		return true
	}, 5*time.Second, time.Second, "the stale boundResource was pruned, so the scenario is no longer reproduced")

	// The actual assertion: a second, unrelated APIExport must still bind. Before the
	// fix this binding stayed in phase Binding with no conditions at all, because the
	// reconcile aborted in newConflictChecker on the dangling reference above.
	bind("tlsroutes")
	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(t.Context(), "tlsroutes", metav1.GetOptions{})
	}, kcptestinghelpers.Is(apisv1alpha2.InitialBindingCompleted))

	t.Logf("Verify the tlsroutes binding is fully bound and serving")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		binding, err := kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(t.Context(), "tlsroutes", metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		if binding.Status.Phase != apisv1alpha2.APIBindingPhaseBound {
			return false, fmt.Sprintf("phase is %q, want Bound", binding.Status.Phase)
		}
		for _, br := range binding.Status.BoundResources {
			if br.Group == "gateway.networking.k8s.io" && br.Resource == "tlsroutes" {
				return true, ""
			}
		}
		return false, fmt.Sprintf("tlsroutes not in boundResources: %v", binding.Status.BoundResources)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	// And the pre-existing binding must stay healthy too: the dangling entry is
	// ignored, not treated as a reason to tear anything down.
	t.Logf("Verify the cowboys binding is still bound")
	binding, err := kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(t.Context(), "cowboys", metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, apisv1alpha2.APIBindingPhaseBound, binding.Status.Phase)
	require.True(t, conditions.IsTrue(binding, apisv1alpha2.InitialBindingCompleted))
}
