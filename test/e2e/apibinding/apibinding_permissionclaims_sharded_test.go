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

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"

	"github.com/kcp-dev/kcp/config/helpers"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/apifixtures"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

// TestAPIBindingPermissionClaimsAppliedAcrossShards reproduces
//
// Setup:
//
//   - A producer APIExport "wild.wild.west" exposes the "sheriffs" resource.
//
//   - A consumer APIExport "today-cowboys" exposes its own "cowboys" resource
//     and declares a PermissionClaim against sheriffs (using the producer's
//     identity hash).
//
//   - A consumer workspace, pinned to a non-root shard, binds *only* the
//     consumer APIExport. No workspace on the consumer's shard ever binds
//     the producer APIExport.
//
//     The permissionclaimlabel controller resolves informers from the local
//     DiscoveringDynamicSharedInformerFactory which only knows about local
//     CRDs. Because nothing on the consumer's shard ever bound the producer
//     APIExport, no bound CRD for sheriffs was ever materialised on that
//     shard. The controller fails with
//     "unable to find informer for wild.wild.west.sheriffs"
//     and PermissionClaimsApplied stays False with reason InternalError.
func TestAPIBindingPermissionClaimsAppliedAcrossShards(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)
	cfg := server.BaseConfig(t)

	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client")

	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic cluster client")

	t.Logf("Listing shards")
	shards, err := kcpClusterClient.Cluster(core.RootCluster.Path()).CoreV1alpha1().Shards().List(t.Context(), metav1.ListOptions{})
	require.NoError(t, err, "failed to list shards")
	if len(shards.Items) < 2 {
		t.Skipf("Need at least 2 shards to run this test, got %d", len(shards.Items))
	}

	// Pin the producer to root and the consumer to a different schedulable shard.
	// This guarantees that the consumer's shard never sees an APIBinding to the
	// producer APIExport, so the bound CRD for "sheriffs" is never created there.
	var consumerShard string
	for _, s := range shards.Items {
		if _, unsched := s.Annotations["experimental.core.kcp.io/unschedulable"]; unsched {
			continue
		}
		if s.Name == corev1alpha1.RootShard {
			continue
		}
		consumerShard = s.Name
		break
	}
	require.NotEmpty(t, consumerShard, "could not find a schedulable non-root shard")
	t.Logf("Producer pinned to shard %q, consumer pinned to shard %q", corev1alpha1.RootShard, consumerShard)

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	providerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("provider"), kcptesting.WithRootShard())
	consumerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("consumer"), kcptesting.WithShard(consumerShard))

	// Producer APIExport: provides sheriffs.wild.wild.west.
	t.Logf("Create producer APIExport in %q", providerPath)
	apifixtures.CreateSheriffsSchemaAndExport(t.Context(), t, providerPath, kcpClusterClient, "wild.wild.west", "producer")

	t.Logf("Wait for producer APIExport to publish a valid identity hash")
	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Get(t.Context(), "wild.wild.west", metav1.GetOptions{})
	}, kcptestinghelpers.Is(apisv1alpha2.APIExportIdentityValid))
	producerExport, err := kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Get(t.Context(), "wild.wild.west", metav1.GetOptions{})
	require.NoError(t, err)
	producerIdentityHash := producerExport.Status.IdentityHash
	require.NotEmpty(t, producerIdentityHash)

	// Consumer APIExport: own schema (cowboys) + PermissionClaim against sheriffs.
	t.Logf("Install cowboys APIResourceSchema into %q", providerPath)
	providerClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(providerClient.Cluster(providerPath).Discovery()))
	require.NoError(t, helpers.CreateResourceFromFS(t.Context(), dynamicClusterClient.Cluster(providerPath), mapper, nil, "apiresourceschema_cowboys.yaml", testFiles))

	verbs := []string{"*"}
	t.Logf("Create consumer APIExport today-cowboys with permission claim against sheriffs in %q", providerPath)
	cowboysExport := &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{Name: "today-cowboys"},
		Spec: apisv1alpha2.APIExportSpec{
			Resources: []apisv1alpha2.ResourceSchema{
				{
					Name:   "cowboys",
					Group:  "wildwest.dev",
					Schema: "today.cowboys.wildwest.dev",
					Storage: apisv1alpha2.ResourceSchemaStorage{
						CRD: &apisv1alpha2.ResourceSchemaStorageCRD{},
					},
				},
			},
			PermissionClaims: []apisv1alpha2.PermissionClaim{
				{
					GroupResource: apisv1alpha2.GroupResource{Group: "wild.wild.west", Resource: "sheriffs"},
					IdentityHash:  producerIdentityHash,
					Verbs:         verbs,
				},
			},
		},
	}
	_, err = kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Create(t.Context(), cowboysExport, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Bind consumer workspace %q (shard %q) to today-cowboys; do NOT bind producer", consumerPath, consumerShard)
	binding := &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "cowboys"},
		Spec: apisv1alpha2.APIBindingSpec{
			Reference: apisv1alpha2.BindingReference{
				Export: &apisv1alpha2.ExportBindingReference{
					Path: providerPath.String(),
					Name: "today-cowboys",
				},
			},
			PermissionClaims: []apisv1alpha2.AcceptablePermissionClaim{
				{
					ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
						PermissionClaim: apisv1alpha2.PermissionClaim{
							GroupResource: apisv1alpha2.GroupResource{Group: "wild.wild.west", Resource: "sheriffs"},
							IdentityHash:  producerIdentityHash,
							Verbs:         verbs,
						},
						Selector: apisv1alpha2.PermissionClaimSelector{MatchAll: true},
					},
					State: apisv1alpha2.ClaimAccepted,
				},
			},
		},
	}
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Create(t.Context(), binding, metav1.CreateOptions{})
		return err == nil, fmt.Sprintf("error creating APIBinding: %v", err)
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	t.Logf("Wait for InitialBindingCompleted: the cowboys schema should bind regardless of the bug")
	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(t.Context(), binding.Name, metav1.GetOptions{})
	}, kcptestinghelpers.Is(apisv1alpha2.InitialBindingCompleted))

	t.Logf("Expect PermissionClaimsApplied=True on consumer APIBinding (shard %q)", consumerShard)
	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(t.Context(), binding.Name, metav1.GetOptions{})
	}, kcptestinghelpers.Is(apisv1alpha2.PermissionClaimsApplied),
		"PermissionClaimsApplied should be True even when the producing APIExport is not bound on the consumer's shard (issue #4087)")
}
