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
	"net/url"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/util/retry"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"
	adminv1alpha1 "github.com/kcp-dev/sdk/apis/admin/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned"
	kcpclusterclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"

	"github.com/kcp-dev/kcp/config/helpers"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/apifixtures"
	wildwestv1alpha1 "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest/v1alpha1"
	wildwestclientset "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

const (
	// claimerGroup is the API group the claiming APIExport itself exports. It is
	// what a PermissionClaimPolicy keys its grants on.
	claimerGroup = "wild.wild.west"
	// claimedGroup is the API group the claiming APIExport claims without an
	// identityHash. Two independent providers export it below.
	claimedGroup = "wildwest.dev"
	// uncoveredGroup is a group no PermissionClaimPolicy mentions; claiming it
	// without an identityHash must be rejected.
	uncoveredGroup = "gateway.networking.k8s.io"

	claimingExportName = "sheriffs-claimer"
	cowboysExportName  = "today-cowboys"
	cowboysSchemaName  = "today.cowboys.wildwest.dev"
	policyName         = "identity-agnostic-e2e"
)

// TestAPIExportIdentityAgnosticPermissionClaims exercises a permission claim
// without an identityHash, gated by a PermissionClaimPolicy.
//
// One claiming APIExport (exporting wild.wild.west sheriffs) claims
// wildwest.dev cowboys without naming an identity. Two consumer workspaces
// accept that claim while binding *different* cowboys providers. Through the
// claiming export's virtual workspace a wildcard list must return the cowboys
// of both consumers, i.e. the single identity-less claim spans two distinct
// identities.
func TestAPIExportIdentityAgnosticPermissionClaims(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	// A PermissionClaimPolicy is installation-wide: it reserves every API group
	// it names, so only its providers may export them. wildwest.dev and
	// wild.wild.west are used by many other tests, so this must not run against
	// a shared server.
	server := kcptesting.PrivateKcpServer(t)

	ctx := t.Context()

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	providerAPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("provider-a"))
	providerBPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("provider-b"))
	claimerPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("claimer"))
	consumerAPath, consumerA := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("consumer-a"))
	consumerBPath, consumerB := kcptesting.NewWorkspaceFixture(t, server, orgPath, kcptesting.WithName("consumer-b"))

	cfg := server.BaseConfig(t)

	kcpClusterClient, err := kcpclusterclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")
	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic cluster client for server")
	wildwestClusterClient, err := wildwestclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct wildwest cluster client for server")

	// 1. The policy, written through the Admin virtual workspace.
	t.Logf("Create a PermissionClaimPolicy through the Admin virtual workspace")
	adminClient := adminVirtualWorkspaceClient(t, cfg)
	policy := &adminv1alpha1.PermissionClaimPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: policyName},
		Spec: adminv1alpha1.PermissionClaimPolicySpec{
			// the e2e base config authenticates as kcp-admin, a member of
			// system:kcp:admin, which is also what the Admin VW requires.
			Providers: []adminv1alpha1.PermissionClaimPolicySubject{
				{Kind: adminv1alpha1.PermissionClaimPolicySubjectGroup, Name: "system:kcp:admin"},
				{Kind: adminv1alpha1.PermissionClaimPolicySubjectGroup, Name: "system:masters"},
			},
			Claims: []adminv1alpha1.PermissionClaimRule{
				{
					Claimer: claimerGroup,
					Groups:  []string{claimedGroup},
				},
			},
		},
	}
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := adminClient.AdminV1alpha1().PermissionClaimPolicies().Create(ctx, policy, metav1.CreateOptions{})
		if err != nil {
			return false, fmt.Sprintf("error creating PermissionClaimPolicy: %v", err)
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "unable to create the PermissionClaimPolicy through /services/admin")
	t.Cleanup(func() {
		_ = adminClient.AdminV1alpha1().PermissionClaimPolicies().Delete(ctx, policyName, metav1.DeleteOptions{})
	})

	t.Logf("The policy is readable back through the Admin virtual workspace")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		got, err := adminClient.AdminV1alpha1().PermissionClaimPolicies().Get(ctx, policyName, metav1.GetOptions{})
		if err != nil {
			return false, fmt.Sprintf("error getting PermissionClaimPolicy: %v", err)
		}
		if !got.Allows(claimerGroup, claimedGroup) {
			return false, fmt.Sprintf("policy does not allow %s to claim %s: %#v", claimerGroup, claimedGroup, got.Spec)
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	// 2. Two genuinely different producers of wildwest.dev cowboys.
	t.Logf("Create two independent cowboys providers")
	createCowboysProvider(t, dynamicClusterClient, kcpClusterClient, cfg, providerAPath)
	createCowboysProvider(t, dynamicClusterClient, kcpClusterClient, cfg, providerBPath)

	identityA := waitForExportIdentity(t, kcpClusterClient, providerAPath, cowboysExportName)
	identityB := waitForExportIdentity(t, kcpClusterClient, providerBPath, cowboysExportName)
	require.NotEqual(t, identityA, identityB, "the two cowboys providers must have different identities")
	t.Logf("provider-a identity %s, provider-b identity %s", identityA, identityB)

	// 3. The claiming APIExport: exports wild.wild.west sheriffs, claims
	//    wildwest.dev cowboys without an identityHash.
	t.Logf("Create the sheriffs APIResourceSchema and the claiming APIExport in %s", claimerPath)
	apifixtures.CreateSheriffsSchemaAndExport(ctx, t, claimerPath, kcpClusterClient, claimerGroup, "identity-agnostic claimer")
	sheriffsSchemaName := fmt.Sprintf("today.sheriffs.%s", claimerGroup)

	claimingExport := &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{Name: claimingExportName},
		Spec: apisv1alpha2.APIExportSpec{
			Resources: []apisv1alpha2.ResourceSchema{
				{
					Name:   "sheriffs",
					Group:  claimerGroup,
					Schema: sheriffsSchemaName,
					Storage: apisv1alpha2.ResourceSchemaStorage{
						CRD: &apisv1alpha2.ResourceSchemaStorageCRD{},
					},
				},
			},
			PermissionClaims: []apisv1alpha2.PermissionClaim{
				{
					GroupResource: apisv1alpha2.GroupResource{Group: claimedGroup, Resource: "cowboys"},
					Verbs:         []string{"*"},
					// deliberately no IdentityHash: resolved per consumer workspace.
				},
			},
		},
	}
	// admission only admits this once the policy has reached the shard's
	// cache-backed informer.
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := kcpClusterClient.Cluster(claimerPath).ApisV1alpha2().APIExports().Create(ctx, claimingExport, metav1.CreateOptions{})
		if err != nil {
			return false, fmt.Sprintf("error creating claiming APIExport: %v", err)
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "identity-less permission claim was never admitted")

	// 4. Consumers, each binding a different cowboys provider plus the claiming export.
	t.Logf("Bind consumer-a to provider-a and consumer-b to provider-b")
	apifixtures.BindToExport(ctx, t, providerAPath, cowboysExportName, consumerAPath, kcpClusterClient)
	apifixtures.BindToExport(ctx, t, providerBPath, cowboysExportName, consumerBPath, kcpClusterClient)

	t.Logf("Verify each consumer bound its own provider's identity")
	require.Equal(t, identityA, waitForBoundCowboysIdentity(t, kcpClusterClient, consumerAPath), "consumer-a must bind provider-a's identity")
	require.Equal(t, identityB, waitForBoundCowboysIdentity(t, kcpClusterClient, consumerBPath), "consumer-b must bind provider-b's identity")

	t.Logf("Bind both consumers to the claiming APIExport and accept the identity-less claim")
	for _, consumerPath := range []logicalcluster.Path{consumerAPath, consumerBPath} {
		apifixtures.BindToExport(ctx, t, claimerPath, claimingExportName, consumerPath, kcpClusterClient)
		acceptAllClaims(t, kcpClusterClient, consumerPath, claimingExportName)
	}

	t.Logf("Wait for the identity-less claim to be applied in both consumers")
	for _, consumerPath := range []logicalcluster.Path{consumerAPath, consumerBPath} {
		kcptestinghelpers.Eventually(t, func() (bool, string) {
			binding, err := kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(ctx, claimingExportName, metav1.GetOptions{})
			if err != nil {
				return false, err.Error()
			}
			for _, claim := range binding.Status.AppliedPermissionClaims {
				if claim.Group == claimedGroup && claim.Resource == "cowboys" && claim.IdentityHash == "" {
					return true, ""
				}
			}
			return false, fmt.Sprintf("identity-less cowboys claim not applied yet: %#v", binding.Status.AppliedPermissionClaims)
		}, wait.ForeverTestTimeout, 100*time.Millisecond, "identity-less claim was never applied in %s", consumerPath)
	}

	// 5. One cowboy per consumer.
	t.Logf("Create a cowboy in each consumer workspace")
	createCowboy(t, wildwestClusterClient, consumerAPath, "cowboy-a")
	createCowboy(t, wildwestClusterClient, consumerBPath, "cowboy-b")

	// 6. The core assertion: one identity-less claim, two producers, one wildcard list.
	t.Logf("Wait for the claiming APIExport's virtual workspace URL")
	vwCfg := rest.CopyConfig(cfg)
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		slice, err := kcpClusterClient.Cluster(claimerPath).ApisV1alpha1().APIExportEndpointSlices().Get(ctx, claimingExportName, metav1.GetOptions{})
		if err != nil {
			return false, fmt.Sprintf("error getting APIExportEndpointSlice: %v", err)
		}
		urls := framework.ExportVirtualWorkspaceURLs(slice)
		var found bool
		vwCfg.Host, found, err = framework.VirtualWorkspaceURL(ctx, cfg, consumerA, urls)
		if err != nil {
			return false, fmt.Sprintf("error getting virtual workspace URL: %v", err)
		}
		return found, fmt.Sprintf("waiting for virtual workspace URLs to be available: %v", urls)
	}, wait.ForeverTestTimeout, 100*time.Millisecond)
	t.Logf("Got claiming APIExport virtual workspace URL %s", vwCfg.Host)

	// both consumers live on the same shard of a private, single-shard server,
	// so one virtual workspace URL serves both of them.
	requireSameShard(t, cfg, consumerA, consumerB)

	wildwestVWClient, err := wildwestclientset.NewForConfig(vwCfg)
	require.NoError(t, err, "failed to construct wildwest client for the virtual workspace")

	t.Logf("Wildcard list cowboys through the claiming APIExport: both identities must show up")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		list, err := wildwestVWClient.WildwestV1alpha1().Cowboys().List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, fmt.Sprintf("error wildcard listing cowboys: %v", err)
		}
		names := make([]string, 0, len(list.Items))
		for _, cowboy := range list.Items {
			names = append(names, cowboy.Name)
		}
		sort.Strings(names)
		if len(names) != 2 || names[0] != "cowboy-a" || names[1] != "cowboy-b" {
			return false, fmt.Sprintf("expected [cowboy-a cowboy-b], got %v", names)
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "one identity-less claim did not span both producers")

	// 7. Per-cluster access through the same virtual workspace.
	t.Logf("Get and update consumer-a's cowboy through the virtual workspace")
	consumerAClusterPath := logicalcluster.NewPath(consumerA.Spec.Cluster)
	vwCowboyClient := wildwestVWClient.Cluster(consumerAClusterPath).WildwestV1alpha1().Cowboys("default")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		cowboy, err := vwCowboyClient.Get(ctx, "cowboy-a", metav1.GetOptions{})
		if err != nil {
			return false, fmt.Sprintf("error getting cowboy-a: %v", err)
		}
		cowboy.Spec.Intent = "through the virtual workspace"
		if _, err := vwCowboyClient.Update(ctx, cowboy, metav1.UpdateOptions{}); err != nil {
			return false, fmt.Sprintf("error updating cowboy-a: %v", err)
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "unable to get and update a claimed cowboy per cluster")

	t.Logf("The update is visible in the consumer workspace itself")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		cowboy, err := wildwestClusterClient.Cluster(consumerAPath).WildwestV1alpha1().Cowboys("default").Get(ctx, "cowboy-a", metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		if cowboy.Spec.Intent != "through the virtual workspace" {
			return false, fmt.Sprintf("intent is %q", cowboy.Spec.Intent)
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	t.Logf("consumer-b's cowboy is not reachable under consumer-a's cluster")
	_, err = wildwestVWClient.Cluster(consumerAClusterPath).WildwestV1alpha1().Cowboys("default").Get(ctx, "cowboy-b", metav1.GetOptions{})
	require.Error(t, err, "cowboy-b must not be visible in consumer-a's cluster")

	// 8. An identity-less claim on a group no policy covers stays rejected.
	t.Logf("An identity-less claim for a group no policy covers is rejected by admission")
	uncovered := &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{Name: "uncovered-claimer"},
		Spec: apisv1alpha2.APIExportSpec{
			Resources: []apisv1alpha2.ResourceSchema{
				{
					Name:   "sheriffs",
					Group:  claimerGroup,
					Schema: sheriffsSchemaName,
					Storage: apisv1alpha2.ResourceSchemaStorage{
						CRD: &apisv1alpha2.ResourceSchemaStorageCRD{},
					},
				},
			},
			PermissionClaims: []apisv1alpha2.PermissionClaim{
				{
					GroupResource: apisv1alpha2.GroupResource{Group: uncoveredGroup, Resource: "tlsroutes"},
					Verbs:         []string{"*"},
				},
			},
		},
	}
	_, err = kcpClusterClient.Cluster(claimerPath).ApisV1alpha2().APIExports().Create(ctx, uncovered, metav1.CreateOptions{})
	require.Error(t, err, "an identity-less claim for an uncovered group must be rejected")
	require.Contains(t, err.Error(), "identityHash is required", "unexpected rejection reason: %v", err)
}

// adminVirtualWorkspaceClient builds a client against /services/admin, where
// installation-wide objects such as PermissionClaimPolicies are written.
// Access requires membership in system:kcp:admin, which the e2e base config's
// kcp-admin client certificate carries.
func adminVirtualWorkspaceClient(t *testing.T, cfg *rest.Config) kcpclientset.Interface {
	t.Helper()

	vwCfg := rest.CopyConfig(cfg)
	vwURL, err := url.Parse(cfg.Host)
	require.NoError(t, err, "failed to parse base config host %q", cfg.Host)
	vwURL.Path = "/services/admin"
	vwCfg.Host = vwURL.String()

	client, err := kcpclientset.NewForConfig(vwCfg)
	require.NoError(t, err, "failed to construct a client for the Admin virtual workspace")
	return client
}

// createCowboysProvider installs the cowboys schema and an APIExport for it,
// making the workspace an independent producer of wildwest.dev cowboys.
func createCowboysProvider(t *testing.T, dynamicClusterClient kcpdynamic.ClusterInterface, kcpClusterClient kcpclusterclientset.ClusterInterface, cfg *rest.Config, providerPath logicalcluster.Path) {
	t.Helper()

	t.Logf("Install the cowboys APIResourceSchema into %s", providerPath)
	discoveryClient, err := kcpclusterclientset.NewForConfig(cfg)
	require.NoError(t, err)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(discoveryClient.Cluster(providerPath).Discovery()))
	require.NoError(t, helpers.CreateResourceFromFS(t.Context(), dynamicClusterClient.Cluster(providerPath), mapper, nil, "apiresourceschema_cowboys.yaml", testFiles))

	t.Logf("Create the cowboys APIExport in %s", providerPath)
	export := &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{Name: cowboysExportName},
		Spec: apisv1alpha2.APIExportSpec{
			Resources: []apisv1alpha2.ResourceSchema{
				{
					Name:   "cowboys",
					Group:  claimedGroup,
					Schema: cowboysSchemaName,
					Storage: apisv1alpha2.ResourceSchemaStorage{
						CRD: &apisv1alpha2.ResourceSchemaStorageCRD{},
					},
				},
			},
		},
	}
	_, err = kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Create(t.Context(), export, metav1.CreateOptions{})
	require.NoError(t, err, "error creating the cowboys APIExport in %s", providerPath)
}

func waitForExportIdentity(t *testing.T, kcpClusterClient kcpclusterclientset.ClusterInterface, path logicalcluster.Path, name string) string {
	t.Helper()

	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(path).ApisV1alpha2().APIExports().Get(t.Context(), name, metav1.GetOptions{})
	}, kcptestinghelpers.Is(apisv1alpha2.APIExportIdentityValid), "APIExport %s|%s never got a valid identity", path, name)

	export, err := kcpClusterClient.Cluster(path).ApisV1alpha2().APIExports().Get(t.Context(), name, metav1.GetOptions{})
	require.NoError(t, err)
	require.NotEmpty(t, export.Status.IdentityHash)
	return export.Status.IdentityHash
}

// waitForBoundCowboysIdentity returns the identity hash the consumer's cowboys
// APIBinding actually bound, which is what an identity-agnostic claim resolves to.
func waitForBoundCowboysIdentity(t *testing.T, kcpClusterClient kcpclusterclientset.ClusterInterface, consumerPath logicalcluster.Path) string {
	t.Helper()

	var identity string
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		binding, err := kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(t.Context(), cowboysExportName, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		for _, bound := range binding.Status.BoundResources {
			if bound.Group == claimedGroup && bound.Resource == "cowboys" && bound.Schema.IdentityHash != "" {
				identity = bound.Schema.IdentityHash
				return true, ""
			}
		}
		return false, fmt.Sprintf("cowboys not bound yet in %s: %#v", consumerPath, binding.Status.BoundResources)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "cowboys were never bound in %s", consumerPath)
	return identity
}

// acceptAllClaims accepts every claim the export offers, verbatim, so an
// identity-less claim stays identity-less on the binding too.
func acceptAllClaims(t *testing.T, kcpClusterClient kcpclusterclientset.ClusterInterface, consumerPath logicalcluster.Path, bindingName string) {
	t.Helper()

	kcptestinghelpers.Eventually(t, func() (bool, string) {
		binding, err := kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(t.Context(), bindingName, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		if len(binding.Status.ExportPermissionClaims) == 0 {
			return false, "no export permission claims observed yet"
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "APIBinding %s|%s never observed the export's permission claims", consumerPath, bindingName)

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		binding, err := kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(t.Context(), bindingName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		binding.Spec.PermissionClaims = nil
		for _, claim := range binding.Status.ExportPermissionClaims {
			binding.Spec.PermissionClaims = append(binding.Spec.PermissionClaims, apisv1alpha2.AcceptablePermissionClaim{
				ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
					PermissionClaim: claim,
					Selector:        apisv1alpha2.PermissionClaimSelector{MatchAll: true},
				},
				State: apisv1alpha2.ClaimAccepted,
			})
		}
		_, err = kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Update(t.Context(), binding, metav1.UpdateOptions{})
		return err
	})
	require.NoError(t, err, "error accepting permission claims on %s|%s", consumerPath, bindingName)
}

func createCowboy(t *testing.T, wildwestClusterClient wildwestclientset.ClusterInterface, consumerPath logicalcluster.Path, name string) {
	t.Helper()

	cowboy := &wildwestv1alpha1.Cowboy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
		},
		Spec: wildwestv1alpha1.CowboySpec{Intent: "yeehaw"},
	}
	// the bound CRD is served asynchronously.
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := wildwestClusterClient.Cluster(consumerPath).WildwestV1alpha1().Cowboys("default").Create(t.Context(), cowboy, metav1.CreateOptions{})
		if err != nil {
			return false, fmt.Sprintf("error creating cowboy %s|%s: %v", consumerPath, name, err)
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "unable to create cowboy %s|%s", consumerPath, name)
}

// requireSameShard fails the test if the two workspaces are not on the same
// shard, because a single APIExport virtual workspace URL only serves one shard
// and the wildcard assertion below would then be vacuous.
func requireSameShard(t *testing.T, cfg *rest.Config, a, b *tenancyv1alpha1.Workspace) {
	t.Helper()

	shardA, err := kcptesting.WorkspaceShard(t.Context(), cfg, a)
	require.NoError(t, err)
	shardB, err := kcptesting.WorkspaceShard(t.Context(), cfg, b)
	require.NoError(t, err)
	require.Equal(t, shardA.Name, shardB.Name, "both consumers must be scheduled on the same shard for the wildcard assertion to be meaningful")
}
