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
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	adminv1alpha1 "github.com/kcp-dev/sdk/apis/admin/v1alpha1"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned"
	kcpclusterclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"

	"github.com/kcp-dev/kcp/config/helpers"
	"github.com/kcp-dev/kcp/pkg/reconciler/admin/apiidentity"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

// TestAPIExportIdentityRegistry exercises the identity registry: the first
// APIExport to use an identity owns it, another APIExport with a copy of the
// key shares the identity and is recorded as a holder, the oldest holder
// inherits the identity when the owner is deleted, the entry is gone once
// the last export is deleted, and the next export with the key registers
// the identity anew.
func TestAPIExportIdentityRegistry(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	server := kcptesting.SharedKcpServer(t)

	orgPath, _ := kcptesting.NewWorkspaceFixture(t, server, core.RootCluster.Path(), kcptesting.WithType(core.RootCluster.Path(), "organization"))
	ownerPath, ownerWS := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	holderPath, holderWS := kcptesting.NewWorkspaceFixture(t, server, orgPath)
	newcomerPath, newcomerWS := kcptesting.NewWorkspaceFixture(t, server, orgPath)

	cfg := server.BaseConfig(t)
	kcpClusterClient, err := kcpclusterclientset.NewForConfig(cfg)
	require.NoError(t, err)
	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cfg)
	require.NoError(t, err)
	dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
	require.NoError(t, err)

	// the registry is read through the Admin workspace
	vwCfg := rest.CopyConfig(cfg)
	vwURL, err := url.Parse(cfg.Host)
	require.NoError(t, err)
	vwURL.Path = "/services/admin"
	vwCfg.Host = vwURL.String()
	adminClient, err := kcpclientset.NewForConfig(vwCfg)
	require.NoError(t, err)

	for _, path := range []logicalcluster.Path{ownerPath, holderPath, newcomerPath} {
		t.Logf("Install cowboys APIResourceSchema into %q", path)
		mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(kcpClusterClient.Cluster(path).Discovery()))
		require.NoError(t, helpers.CreateResourceFromFS(t.Context(), dynamicClusterClient.Cluster(path), mapper, nil, "apiresourceschema_cowboys.yaml", testFiles))
	}

	newExport := func() *apisv1alpha2.APIExport {
		return &apisv1alpha2.APIExport{
			ObjectMeta: metav1.ObjectMeta{Name: "wildwest"},
			Spec: apisv1alpha2.APIExportSpec{
				Resources: []apisv1alpha2.ResourceSchema{{
					Name: "cowboys", Group: "wildwest.dev", Schema: "today.cowboys.wildwest.dev",
					Storage: apisv1alpha2.ResourceSchemaStorage{CRD: &apisv1alpha2.ResourceSchemaStorageCRD{}},
				}},
			},
		}
	}
	exportRef := func(ws *tenancyv1alpha1.Workspace, export *apisv1alpha2.APIExport) adminv1alpha1.ExportReference {
		return adminv1alpha1.ExportReference{Cluster: ws.Spec.Cluster, Name: export.Name, UID: string(export.UID)}
	}
	getExport := func(path logicalcluster.Path) (*apisv1alpha2.APIExport, error) {
		return kcpClusterClient.Cluster(path).ApisV1alpha2().APIExports().Get(t.Context(), "wildwest", metav1.GetOptions{})
	}
	waitForIdentity := func(path logicalcluster.Path) *apisv1alpha2.APIExport {
		t.Helper()
		var export *apisv1alpha2.APIExport
		kcptestinghelpers.Eventually(t, func() (bool, string) {
			var err error
			export, err = getExport(path)
			if err != nil {
				return false, err.Error()
			}
			cond := conditions.Get(export, apisv1alpha2.APIExportIdentityValid)
			if cond == nil || cond.Status != corev1.ConditionTrue || export.Status.IdentityHash == "" {
				return false, fmt.Sprintf("IdentityValid=%v identityHash=%q", cond, export.Status.IdentityHash)
			}
			return true, ""
		}, wait.ForeverTestTimeout, 100*time.Millisecond)
		return export
	}
	waitForEntry := func(identity string, timeout time.Duration, check func(*adminv1alpha1.APIIdentity) (bool, string)) {
		t.Helper()
		kcptestinghelpers.Eventually(t, func() (bool, string) {
			entry, err := adminClient.AdminV1alpha1().APIIdentities().Get(t.Context(), identity, metav1.GetOptions{})
			if err != nil {
				return false, err.Error()
			}
			return check(entry)
		}, timeout, 250*time.Millisecond)
	}
	// a deleted owner is taken as gone only after the grace period
	afterGrace := apiidentity.MissingGracePeriod + wait.ForeverTestTimeout
	copyKey := func(from, to logicalcluster.Path) {
		t.Helper()
		secret, err := kubeClusterClient.Cluster(from).CoreV1().Secrets("kcp-system").Get(t.Context(), "wildwest", metav1.GetOptions{})
		require.NoError(t, err)
		_, err = kubeClusterClient.Cluster(to).CoreV1().Namespaces().Create(t.Context(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kcp-system"}}, metav1.CreateOptions{})
		require.True(t, err == nil || apierrors.IsAlreadyExists(err), "creating kcp-system: %v", err)
		_, err = kubeClusterClient.Cluster(to).CoreV1().Secrets("kcp-system").Create(t.Context(), &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "wildwest"},
			Data:       map[string][]byte{apisv1alpha1.SecretKeyAPIExportIdentity: secret.Data[apisv1alpha1.SecretKeyAPIExportIdentity]},
		}, metav1.CreateOptions{})
		require.NoError(t, err)
	}
	createWithKey := func(path logicalcluster.Path) {
		t.Helper()
		export := newExport()
		export.Spec.Identity = &apisv1alpha2.Identity{SecretRef: &corev1.SecretReference{Namespace: "kcp-system", Name: "wildwest"}}
		_, err := kcpClusterClient.Cluster(path).ApisV1alpha2().APIExports().Create(t.Context(), export, metav1.CreateOptions{})
		require.NoError(t, err)
	}

	t.Logf("Create the owner APIExport in %q: it claims the identity", ownerPath)
	_, err = kcpClusterClient.Cluster(ownerPath).ApisV1alpha2().APIExports().Create(t.Context(), newExport(), metav1.CreateOptions{})
	require.NoError(t, err)
	owner := waitForIdentity(ownerPath)
	identity := owner.Status.IdentityHash
	waitForEntry(identity, wait.ForeverTestTimeout, func(entry *adminv1alpha1.APIIdentity) (bool, string) {
		return entry.Status.Owner == exportRef(ownerWS, owner) && len(entry.Status.Holders) == 0 && !entry.Status.Core,
			fmt.Sprintf("owner=%v holders=%v core=%v", entry.Status.Owner, entry.Status.Holders, entry.Status.Core)
	})

	t.Logf("Copy the owner's key into %q: the holder shares the identity and is recorded", holderPath)
	copyKey(ownerPath, holderPath)
	createWithKey(holderPath)
	holder := waitForIdentity(holderPath)
	require.Equal(t, identity, holder.Status.IdentityHash, "same key, same identity")
	waitForEntry(identity, wait.ForeverTestTimeout, func(entry *adminv1alpha1.APIIdentity) (bool, string) {
		if len(entry.Status.Holders) != 1 || !entry.Status.Holders[0].Equal(exportRef(holderWS, holder)) {
			return false, fmt.Sprintf("holders=%v", entry.Status.Holders)
		}
		return conditions.IsTrue(entry, adminv1alpha1.APIIdentityShared), "not marked Shared yet"
	})

	t.Logf("Delete the owner: the holder becomes the owner and keeps serving")
	require.NoError(t, kcpClusterClient.Cluster(ownerPath).ApisV1alpha2().APIExports().Delete(t.Context(), "wildwest", metav1.DeleteOptions{}))
	waitForEntry(identity, afterGrace, func(entry *adminv1alpha1.APIIdentity) (bool, string) {
		if !entry.Status.Owner.Equal(exportRef(holderWS, holder)) {
			return false, fmt.Sprintf("owner=%v", entry.Status.Owner)
		}
		return len(entry.Status.Holders) == 0 && conditions.Get(entry, adminv1alpha1.APIIdentityOwnerMissing) == nil,
			fmt.Sprintf("holders=%v ownerMissing=%v", entry.Status.Holders, conditions.Get(entry, adminv1alpha1.APIIdentityOwnerMissing))
	})
	holder, err = getExport(holderPath)
	require.NoError(t, err)
	require.Equal(t, identity, holder.Status.IdentityHash, "the identity does not change hands' value")
	require.True(t, conditions.IsTrue(holder, apisv1alpha2.APIExportIdentityValid))

	t.Logf("Delete the last export: the identity is gone from the registry")
	copyKey(holderPath, newcomerPath) // keep the key around for the newcomer
	require.NoError(t, kcpClusterClient.Cluster(holderPath).ApisV1alpha2().APIExports().Delete(t.Context(), "wildwest", metav1.DeleteOptions{}))
	kcptestinghelpers.Eventually(t, func() (bool, string) {
		_, err := adminClient.AdminV1alpha1().APIIdentities().Get(t.Context(), identity, metav1.GetOptions{})
		return apierrors.IsNotFound(err), fmt.Sprintf("get: %v", err)
	}, afterGrace, 250*time.Millisecond)

	t.Logf("A newcomer with the key registers the identity anew")
	createWithKey(newcomerPath)
	newcomer := waitForIdentity(newcomerPath)
	require.Equal(t, identity, newcomer.Status.IdentityHash, "same key, same identity")
	waitForEntry(identity, wait.ForeverTestTimeout, func(entry *adminv1alpha1.APIIdentity) (bool, string) {
		return entry.Status.Owner == exportRef(newcomerWS, newcomer) && len(entry.Status.Holders) == 0,
			fmt.Sprintf("owner=%v holders=%v", entry.Status.Owner, entry.Status.Holders)
	})
}
