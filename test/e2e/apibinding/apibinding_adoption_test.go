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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/restmapper"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"

	"github.com/kcp-dev/kcp/config/helpers"
	wildwestv1alpha1 "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest/v1alpha1"
	wildwestclientset "github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

// TestAPIBindingAdoption models replacing an APIBinding with a successor bound
// to a different APIExport that serves the same resource through the same
// APIResourceSchema and identity: instances must survive the swap untouched.
func TestAPIBindingAdoption(t *testing.T) {
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
	wildwestClusterClient, err := wildwestclientset.NewForConfig(cfg)
	require.NoError(t, err)

	t.Logf("Install cowboys APIResourceSchema into provider workspace %q", providerPath)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(kcpClusterClient.Cluster(providerPath).Discovery()))
	err = helpers.CreateResourceFromFS(t.Context(), dynamicClusterClient.Cluster(providerPath), mapper, nil, "apiresourceschema_cowboys.yaml", testFiles)
	require.NoError(t, err)

	t.Logf("Create the predecessor APIExport %q", "wildwest")
	wildwestExport := &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{Name: "wildwest"},
		Spec: apisv1alpha2.APIExportSpec{
			Resources: []apisv1alpha2.ResourceSchema{
				{
					Name:    "cowboys",
					Group:   "wildwest.dev",
					Schema:  "today.cowboys.wildwest.dev",
					Storage: apisv1alpha2.ResourceSchemaStorage{CRD: &apisv1alpha2.ResourceSchemaStorageCRD{}},
				},
			},
		},
	}
	_, err = kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Create(t.Context(), wildwestExport, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Wait for the wildwest APIExport identity")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		export, err := kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Get(t.Context(), "wildwest", metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		return export.Status.IdentityHash != "", "identity hash not set yet"
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Bind %q in consumer workspace %q", "wildwest", consumerPath)
	wildwestBinding := &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "wildwest"},
		Spec: apisv1alpha2.APIBindingSpec{
			Reference: apisv1alpha2.BindingReference{
				Export: &apisv1alpha2.ExportBindingReference{Path: providerPath.String(), Name: "wildwest"},
			},
		},
	}
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Create(t.Context(), wildwestBinding, metav1.CreateOptions{})
		return err == nil, fmt.Sprintf("Error creating APIBinding: %v", err)
	}, wait.ForeverTestTimeout, time.Millisecond*100)
	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(t.Context(), "wildwest", metav1.GetOptions{})
	}, kcptestinghelpers.Is(apisv1alpha2.InitialBindingCompleted))

	t.Logf("Create a cowboy in consumer workspace %q", consumerPath)
	cowboyClient := wildwestClusterClient.WildwestV1alpha1().Cluster(consumerPath).Cowboys("default")
	var cowboyUID types.UID
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		cowboy, err := cowboyClient.Create(t.Context(), &wildwestv1alpha1.Cowboy{
			ObjectMeta: metav1.ObjectMeta{Name: "luke", Namespace: "default"},
		}, metav1.CreateOptions{})
		if err != nil {
			return false, err.Error()
		}
		cowboyUID = cowboy.UID
		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Create the successor APIExport %q reusing the %q identity and schema", "cowboys", "wildwest")
	cowboysExport := &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{Name: "cowboys"},
		Spec: apisv1alpha2.APIExportSpec{
			Identity: &apisv1alpha2.Identity{
				SecretRef: &corev1.SecretReference{Namespace: "kcp-system", Name: "wildwest"},
			},
			Resources: []apisv1alpha2.ResourceSchema{
				{
					Name:    "cowboys",
					Group:   "wildwest.dev",
					Schema:  "today.cowboys.wildwest.dev",
					Storage: apisv1alpha2.ResourceSchemaStorage{CRD: &apisv1alpha2.ResourceSchemaStorageCRD{}},
				},
			},
		},
	}
	_, err = kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Create(t.Context(), cowboysExport, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Both exports must share one identity hash")
	var identity string
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		oldExport, err := kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Get(t.Context(), "wildwest", metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		newExport, err := kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Get(t.Context(), "cowboys", metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		identity = newExport.Status.IdentityHash
		return identity != "" && identity == oldExport.Status.IdentityHash,
			fmt.Sprintf("identities differ or unset: old=%q new=%q", oldExport.Status.IdentityHash, newExport.Status.IdentityHash)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Pre-create the successor APIBinding %q; it must sit in NamingConflicts", "cowboys")
	cowboysBinding := &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "cowboys"},
		Spec: apisv1alpha2.APIBindingSpec{
			Reference: apisv1alpha2.BindingReference{
				Export: &apisv1alpha2.ExportBindingReference{Path: providerPath.String(), Name: "cowboys"},
			},
		},
	}
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Create(t.Context(), cowboysBinding, metav1.CreateOptions{})
		return err == nil, fmt.Sprintf("Error creating APIBinding: %v", err)
	}, wait.ForeverTestTimeout, time.Millisecond*100)
	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(t.Context(), "cowboys", metav1.GetOptions{})
	}, kcptestinghelpers.IsNot(apisv1alpha2.BindingUpToDate).WithReason(apisv1alpha2.NamingConflictsReason))

	t.Logf("Delete the predecessor binding %q; instances must be adopted, not deleted", "wildwest")
	err = kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Delete(t.Context(), "wildwest", metav1.DeleteOptions{})
	require.NoError(t, err)
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(t.Context(), "wildwest", metav1.GetOptions{})
		return apierrors.IsNotFound(err), fmt.Sprintf("wildwest binding still present: %v", err)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("The successor binding must become ready")
	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(t.Context(), "cowboys", metav1.GetOptions{})
	}, kcptestinghelpers.Is(apisv1alpha2.InitialBindingCompleted))

	t.Logf("The cowboy must have survived the swap with the same UID")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		cowboy, err := cowboyClient.Get(t.Context(), "luke", metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		return cowboy.UID == cowboyUID, fmt.Sprintf("UID changed: was %q, now %q", cowboyUID, cowboy.UID)
	}, wait.ForeverTestTimeout, time.Millisecond*100)
}

// TestAPIBindingWaitForSuccessor verifies deletionPolicy=WaitForSuccessor:
// binding deletion holds the finalizer (with reason WaitingForSuccessor) while
// instances exist and no successor is present; instances stay served
// throughout; the handover completes as soon as a successor binding appears.
func TestAPIBindingWaitForSuccessor(t *testing.T) {
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
	wildwestClusterClient, err := wildwestclientset.NewForConfig(cfg)
	require.NoError(t, err)

	t.Logf("Install cowboys APIResourceSchema into provider workspace %q", providerPath)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(kcpClusterClient.Cluster(providerPath).Discovery()))
	err = helpers.CreateResourceFromFS(t.Context(), dynamicClusterClient.Cluster(providerPath), mapper, nil, "apiresourceschema_cowboys.yaml", testFiles)
	require.NoError(t, err)

	t.Logf("Create the APIExport")
	export := &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{Name: "wildwest"},
		Spec: apisv1alpha2.APIExportSpec{
			Resources: []apisv1alpha2.ResourceSchema{
				{
					Name:    "cowboys",
					Group:   "wildwest.dev",
					Schema:  "today.cowboys.wildwest.dev",
					Storage: apisv1alpha2.ResourceSchemaStorage{CRD: &apisv1alpha2.ResourceSchemaStorageCRD{}},
				},
			},
		},
	}
	_, err = kcpClusterClient.Cluster(providerPath).ApisV1alpha2().APIExports().Create(t.Context(), export, metav1.CreateOptions{})
	require.NoError(t, err)

	t.Logf("Bind it with deletionPolicy=WaitForSuccessor in consumer workspace %q", consumerPath)
	binding := &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "wildwest"},
		Spec: apisv1alpha2.APIBindingSpec{
			Reference: apisv1alpha2.BindingReference{
				Export: &apisv1alpha2.ExportBindingReference{Path: providerPath.String(), Name: "wildwest"},
			},
			DeletionPolicy: apisv1alpha2.APIBindingDeletionPolicyWaitForSuccessor,
		},
	}
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Create(t.Context(), binding, metav1.CreateOptions{})
		return err == nil, fmt.Sprintf("Error creating APIBinding: %v", err)
	}, wait.ForeverTestTimeout, time.Millisecond*100)
	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(t.Context(), "wildwest", metav1.GetOptions{})
	}, kcptestinghelpers.Is(apisv1alpha2.InitialBindingCompleted))

	t.Logf("Create a cowboy in consumer workspace %q", consumerPath)
	cowboyClient := wildwestClusterClient.WildwestV1alpha1().Cluster(consumerPath).Cowboys("default")
	var cowboyUID types.UID
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		cowboy, err := cowboyClient.Create(t.Context(), &wildwestv1alpha1.Cowboy{
			ObjectMeta: metav1.ObjectMeta{Name: "luke", Namespace: "default"},
		}, metav1.CreateOptions{})
		if err != nil {
			return false, err.Error()
		}
		cowboyUID = cowboy.UID
		return true, ""
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("Delete the binding; it must stay in Terminating, waiting for a successor")
	err = kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Delete(t.Context(), "wildwest", metav1.DeleteOptions{})
	require.NoError(t, err)

	t.Logf("The binding must report WaitingForSuccessor and keep its finalizer")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		binding, err := kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(t.Context(), "wildwest", metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		cond := conditions.Get(binding, apisv1alpha2.BindingResourceDeleteSuccess)
		if cond == nil {
			return false, "condition BindingResourceDeleteSuccess not set yet"
		}
		return cond.Reason == "WaitingForSuccessor", fmt.Sprintf("condition reason is %q", cond.Reason)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	t.Logf("The cowboy must still be retrievable while the binding waits")
	cowboy, err := cowboyClient.Get(t.Context(), "luke", metav1.GetOptions{})
	require.NoError(t, err, "cowboy must remain served while the binding waits for a successor")
	require.Equal(t, cowboyUID, cowboy.UID)

	t.Logf("Create a successor binding; the wait must resolve into a handover")
	rebind := &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "wildwest-again"},
		Spec: apisv1alpha2.APIBindingSpec{
			Reference: apisv1alpha2.BindingReference{
				Export: &apisv1alpha2.ExportBindingReference{Path: providerPath.String(), Name: "wildwest"},
			},
		},
	}
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Create(t.Context(), rebind, metav1.CreateOptions{})
		return err == nil, fmt.Sprintf("Error creating APIBinding: %v", err)
	}, wait.ForeverTestTimeout, time.Millisecond*100)
	kcptestinghelpers.EventuallyCondition(t, func() (conditions.Getter, error) {
		return kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(t.Context(), "wildwest-again", metav1.GetOptions{})
	}, kcptestinghelpers.Is(apisv1alpha2.InitialBindingCompleted))

	t.Logf("The old binding must finish deletion now that the successor adopted the resources")
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := kcpClusterClient.Cluster(consumerPath).ApisV1alpha2().APIBindings().Get(t.Context(), "wildwest", metav1.GetOptions{})
		return apierrors.IsNotFound(err), fmt.Sprintf("old binding still present: %v", err)
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	kcptestinghelpers.Eventually(t, func() (bool, string) {
		cowboy, err := cowboyClient.Get(t.Context(), "luke", metav1.GetOptions{})
		if err != nil {
			return false, err.Error()
		}
		return cowboy.UID == cowboyUID, fmt.Sprintf("UID changed: was %q, now %q", cowboyUID, cowboy.UID)
	}, wait.ForeverTestTimeout, time.Millisecond*100)
}
