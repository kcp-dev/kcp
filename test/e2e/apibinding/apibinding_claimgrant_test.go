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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/restmapper"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"

	"github.com/kcp-dev/kcp/config/helpers"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/apifixtures"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

// TestAPIBindingClaimGrant covers claim grants (enhancements KEP 0008 §6a):
// the sheriffs export lists the cowboys export in claimableBy for sheriffs,
// so the cowboys export may claim sheriffs without naming an identity hash.
// kcp resolves the identity into the claiming export's status, the consumer
// accepts the claim without a hash either, and the cowboys provider reads
// the consumer's sheriffs through its virtual workspace. A claim with no
// identity, no export reference and no grant stays unresolved.
func TestAPIBindingClaimGrant(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	sheriffsPath, _ := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	cowboysPath, cowboysWS := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	consumerPath, consumerWS := kcptesting.NewWorkspaceFixture(t, server, orgPath)

	cfg := server.BaseConfig(t)
	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err)
	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err)

	const sheriffsGroup = "wild.wild.west"
	sheriffsGVR := schema.GroupVersionResource{Group: sheriffsGroup, Version: "v1", Resource: "sheriffs"}

	t.Logf("Create the sheriffs export in %q, granting the cowboys export in %q the claim on sheriffs", sheriffsPath, cowboysPath)
	apifixtures.CreateSheriffsSchemaAndExport(t.Context(), t, sheriffsPath, kcpClusterClient, sheriffsGroup, "claim grants")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		export, err := kcpClusterClient.Cluster(sheriffsPath).ApisV1alpha2().APIExports().Get(t.Context(), sheriffsGroup, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		export.Spec.Resources[0].ClaimableBy = []apisv1alpha2.ClaimGrant{{
			Export: &apisv1alpha2.ExportBindingReference{Path: logicalcluster.Name(cowboysWS.Spec.Cluster).String(), Name: "today-cowboys"},
			Verbs:  []string{"get", "list", "watch"},
		}}
		_, err = kcpClusterClient.Cluster(sheriffsPath).ApisV1alpha2().APIExports().Update(t.Context(), export, metav1.UpdateOptions{})
		return err == nil, fmt.Sprintf("error updating APIExport: %v", err)
	}, wait.ForeverTestTimeout, time.Millisecond*100)
	var sheriffsIdentity string
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		export, err := kcpClusterClient.Cluster(sheriffsPath).ApisV1alpha2().APIExports().Get(t.Context(), sheriffsGroup, metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		sheriffsIdentity = export.Status.IdentityHash
		return sheriffsIdentity != "", "no identity yet"
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Create the cowboys export in %q claiming sheriffs by grant (no identity hash) and widgets by nothing", cowboysPath)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(kcpClusterClient.Cluster(cowboysPath).Discovery()))
	err = helpers.CreateResourceFromFS(t.Context(), dynamicClusterClient.Cluster(cowboysPath), mapper, nil, "apiresourceschema_cowboys.yaml", testFiles)
	require.NoError(t, err)
	cowboysExport := &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{Name: "today-cowboys"},
		Spec: apisv1alpha2.APIExportSpec{
			Resources: []apisv1alpha2.ResourceSchema{{
				Name: "cowboys", Group: "wildwest.dev", Schema: "today.cowboys.wildwest.dev",
				Storage: apisv1alpha2.ResourceSchemaStorage{CRD: &apisv1alpha2.ResourceSchemaStorageCRD{}},
			}},
			PermissionClaims: []apisv1alpha2.PermissionClaim{
				{GroupResource: apisv1alpha2.GroupResource{Group: sheriffsGroup, Resource: "sheriffs"}, Verbs: []string{"*"}},
				{GroupResource: apisv1alpha2.GroupResource{Group: "nobody.example.io", Resource: "widgets"}, Verbs: []string{"get"}},
			},
		},
	}
	_, err = kcpClusterClient.Cluster(cowboysPath).ApisV1alpha2().APIExports().Create(t.Context(), cowboysExport, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("The sheriffs claim resolves by grant with capped verbs; the widgets claim stays unresolved")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		export, err := kcpClusterClient.Cluster(cowboysPath).ApisV1alpha2().APIExports().Get(t.Context(), "today-cowboys", metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		var sheriffs, widgets *apisv1alpha2.ResolvedPermissionClaim
		for i := range export.Status.PermissionClaims {
			switch export.Status.PermissionClaims[i].Resource {
			case "sheriffs":
				sheriffs = &export.Status.PermissionClaims[i]
			case "widgets":
				widgets = &export.Status.PermissionClaims[i]
			}
		}
		if sheriffs == nil || sheriffs.ResolvedBy != apisv1alpha2.ClaimResolvedByGrant || sheriffs.IdentityHash != sheriffsIdentity {
			return false, fmt.Sprintf("sheriffs claim not resolved by grant yet: %v", export.Status.PermissionClaims)
		}
		if len(sheriffs.Verbs) != 3 {
			return false, fmt.Sprintf("sheriffs claim verbs not capped by the grant: %v", sheriffs.Verbs)
		}
		if widgets != nil {
			return false, fmt.Sprintf("widgets claim must stay unresolved: %v", *widgets)
		}
		cond := conditions.Get(export, apisv1alpha2.ClaimsResolved)
		if cond == nil || cond.Reason != apisv1alpha2.ClaimUnresolvedReason {
			return false, fmt.Sprintf("ClaimsResolved should be False/%s: %v", apisv1alpha2.ClaimUnresolvedReason, cond)
		}
		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Bind both exports in %q, accepting the sheriffs claim without an identity hash", consumerPath)
	apifixtures.BindToExport(t.Context(), t, sheriffsPath, sheriffsGroup, consumerPath, kcpClusterClient)
	cowboysBinding := &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "cowboys"},
		Spec: apisv1alpha2.APIBindingSpec{
			Reference: apisv1alpha2.BindingReference{Export: &apisv1alpha2.ExportBindingReference{Path: cowboysPath.String(), Name: "today-cowboys"}},
			PermissionClaims: []apisv1alpha2.AcceptablePermissionClaim{{
				ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
					PermissionClaim: apisv1alpha2.PermissionClaim{GroupResource: apisv1alpha2.GroupResource{Group: sheriffsGroup, Resource: "sheriffs"}, Verbs: []string{"*"}},
					Selector:        apisv1alpha2.PermissionClaimSelector{MatchAll: true},
				},
				State: apisv1alpha2.ClaimAccepted,
			}},
		},
	}
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Create(t.Context(), cowboysBinding, metav1.CreateOptions{})
		return err == nil, fmt.Sprintf("error creating APIBinding: %v", err)
	}, wait.ForeverTestTimeout, time.Millisecond*100)
	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(t.Context(), "cowboys", metav1.GetOptions{})
	}, kcptestinghelpers.Is(apisv1alpha2.InitialBindingCompleted))
	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(t.Context(), "cowboys", metav1.GetOptions{})
	}, kcptestinghelpers.Is(apisv1alpha2.PermissionClaimsApplied))

	t.Logf("The binding sees the export's claims with the resolved identity")
	binding, err := kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(t.Context(), "cowboys", metav1.GetOptions{})
	require.NoError(t, err)
	var exported *apisv1alpha2.PermissionClaim
	for i := range binding.Status.ExportPermissionClaims {
		if binding.Status.ExportPermissionClaims[i].Resource == "sheriffs" {
			exported = &binding.Status.ExportPermissionClaims[i]
		}
	}
	require.NotNil(t, exported)
	require.Equal(t, sheriffsIdentity, exported.IdentityHash, "the resolved identity is reported to the consumer")

	t.Logf("Create a sheriff in %q", consumerPath)
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := dynamicClusterClient.Cluster(consumerPath).Resource(sheriffsGVR).Namespace("default").Create(t.Context(), &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": sheriffsGVR.GroupVersion().String(),
			"kind":       "Sheriff",
			"metadata":   map[string]interface{}{"name": "wyatt", "namespace": "default"},
		}}, metav1.CreateOptions{})
		return err == nil, fmt.Sprintf("error creating sheriff: %v", err)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("The cowboys provider reads the sheriff through its virtual workspace")
	var vwURL string
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		slice, err := kcpClusterClient.Cluster(cowboysPath).ApisV1alpha1().APIExportEndpointSlices().Get(t.Context(), "today-cowboys", metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		var found bool
		vwURL, found, err = framework.VirtualWorkspaceURL(t.Context(), cfg, consumerWS, framework.ExportVirtualWorkspaceURLs(slice))
		if err != nil {
			return false, err.Error()
		}
		return found, fmt.Sprintf("no virtual workspace URL for the consumer's shard yet: %v", slice.Status.APIExportEndpoints)
	}, wait.ForeverTestTimeout, time.Millisecond*100)
	rawConfig, err := server.RawConfig()
	require.NoError(t, err)
	vwClient, err := kcpdynamic.NewForConfig(apiexportVWConfig(t, rawConfig, vwURL))
	require.NoError(t, err)
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		list, err := vwClient.Resource(sheriffsGVR).List(t.Context(), metav1.ListOptions{})
		if err != nil {
			return false, err.Error()
		}
		for _, item := range list.Items {
			if item.GetName() == "wyatt" {
				return true, ""
			}
		}
		return false, fmt.Sprintf("sheriff not visible yet, got %d items", len(list.Items))
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("The grant caps verbs: deleting through the virtual workspace is denied")
	err = vwClient.Resource(sheriffsGVR).Cluster(logicalcluster.NewPath(consumerWS.Spec.Cluster)).Namespace("default").Delete(t.Context(), "wyatt", metav1.DeleteOptions{})
	require.Error(t, err, "delete is not within the granted verbs")
}
