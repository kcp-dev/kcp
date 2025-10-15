/*
Copyright 2025 The KCP Authors.

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

package terminatingworkspaces

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"path"
	"strings"
	"testing"
	"time"

	jsonpatch "github.com/evanphx/json-patch"
	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/endpoints/discovery"
	"k8s.io/client-go/rest"

	kcpdiscovery "github.com/kcp-dev/client-go/discovery"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"

	"github.com/kcp-dev/kcp/cmd/virtual-workspaces/options"
	"github.com/kcp-dev/kcp/pkg/virtual/terminatingworkspaces"
	"github.com/kcp-dev/kcp/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/tenancy/termination"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/kcp/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/kcp/sdk/testing/helpers"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestFinalizingWorkspacesVirtualWorkspaceDiscovery(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	source := kcptesting.SharedKcpServer(t)
	rootShardCfg := source.RootShardSystemMasterBaseConfig(t)
	rootShardCfg.Host += path.Join(options.DefaultRootPathPrefix, terminatingworkspaces.VirtualWorkspaceName, "something")

	virtualWorkspaceDiscoveryClient, err := kcpdiscovery.NewForConfig(rootShardCfg)
	require.NoError(t, err)

	_, apiResourceLists, err := virtualWorkspaceDiscoveryClient.ServerGroupsAndResources()
	require.NoError(t, err)

	require.Empty(t, cmp.Diff([]*metav1.APIResourceList{{
		GroupVersion: "v1",
	}, {
		TypeMeta: metav1.TypeMeta{
			Kind:       "APIResourceList",
			APIVersion: "v1",
		},
		GroupVersion: "core.kcp.io/v1alpha1",
		APIResources: []metav1.APIResource{
			{
				Kind:               "LogicalCluster",
				Name:               "logicalclusters",
				SingularName:       "logicalcluster",
				Categories:         []string{"kcp"},
				Verbs:              metav1.Verbs{"get", "list", "watch"},
				StorageVersionHash: discovery.StorageVersionHash("", "core.kcp.io", "v1alpha1", "LogicalCluster"),
			},
			{
				Kind: "LogicalCluster",
				Name: "logicalclusters/status",
			},
		},
	}}, apiResourceLists))
}

func TestFinalizingWorkspacesVirtualWorkspaceAccess(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	source := kcptesting.SharedKcpServer(t)
	wsPath, _ := kcptesting.NewWorkspaceFixture(t, source, core.RootCluster.Path())
	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	sourceConfig := source.BaseConfig(t)

	sourceKcpClusterClient, err := kcpclientset.NewForConfig(sourceConfig)
	require.NoError(t, err)

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(sourceConfig)
	require.NoError(t, err)

	username := "user-1"
	framework.AdmitWorkspaceAccess(ctx, t, kubeClusterClient, wsPath, []string{username}, nil, false)

	testLabelSelector := map[string]string{
		"internal.kcp.io/e2e-test": t.Name(),
	}

	t.Log("Create a workspacetype with a finalizer")
	// WorkspaceTypes and the initializer names will have to be globally unique, so we add some suffix here
	// to ensure that parallel test runs do not impact our ability to verify this behavior. WorkspaceType names
	// are pretty locked down, using this regex: '^[A-Z0-9][a-zA-Z0-9]+$' - so we just add some simple lowercase suffix.
	const characters = "abcdefghijklmnopqrstuvwxyz"
	suffix := func() string {
		b := make([]byte, 10)
		for i := range b {
			b[i] = characters[rand.Intn(len(characters))]
		}
		return string(b)
	}

	// store workspaces and their randomized names under easy to find aliases
	workspaceTypes := map[string]*tenancyv1alpha1.WorkspaceType{
		"alpha": {},
		"beta":  {},
		"gamma": {},
	}
	for name := range workspaceTypes {
		wst := &tenancyv1alpha1.WorkspaceType{
			ObjectMeta: metav1.ObjectMeta{
				Name: name + "-" + suffix(),
			},
			Spec: tenancyv1alpha1.WorkspaceTypeSpec{
				Finalizer: true,
			},
		}
		workspaceTypes[name] = wst
	}
	// make gamma extend alpha and beta
	workspaceTypes["gamma"].Spec.Extend.With = []tenancyv1alpha1.WorkspaceTypeReference{
		{Path: wsPath.String(), Name: tenancyv1alpha1.TypeName(workspaceTypes["alpha"].Name)},
		{Path: wsPath.String(), Name: tenancyv1alpha1.TypeName(workspaceTypes["beta"].Name)},
	}

	// create workspacetypes
	for _, wst := range workspaceTypes {
		_, err := sourceKcpClusterClient.TenancyV1alpha1().Cluster(wsPath).WorkspaceTypes().Create(ctx, wst, metav1.CreateOptions{})
		require.NoError(t, err)
		source.Artifact(t, func() (runtime.Object, error) {
			return sourceKcpClusterClient.TenancyV1alpha1().Cluster(wsPath).WorkspaceTypes().Get(ctx, wst.Name, metav1.GetOptions{})
		})
	}

	t.Log("Wait for WorkspaceTypes to have their type extensions resolved")
	for _, wst := range workspaceTypes {
		name := wst.Name
		kcptestinghelpers.EventuallyReady(t, func() (conditions.Getter, error) {
			return sourceKcpClusterClient.TenancyV1alpha1().Cluster(wsPath).WorkspaceTypes().Get(ctx, name, metav1.GetOptions{})
		}, "could not wait for readiness on WorkspaceType %s|%s", wsPath.String(), name)
	}

	t.Log("Create workspaces using the new types")
	// store workspaces and their randomized names under easy to find aliases
	workspaces := map[string]*tenancyv1alpha1.Workspace{
		"alpha": {},
		"beta":  {},
		"gamma": {},
	}
	for name, wst := range workspaceTypes {
		var ws *tenancyv1alpha1.Workspace
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			ws, err = sourceKcpClusterClient.TenancyV1alpha1().Cluster(wsPath).Workspaces().Create(ctx, workspaceForType(wst, testLabelSelector), metav1.CreateOptions{})
			require.NoError(c, err)
		}, wait.ForeverTestTimeout, time.Millisecond*100)
		source.Artifact(t, func() (runtime.Object, error) {
			return sourceKcpClusterClient.TenancyV1alpha1().Cluster(wsPath).Workspaces().Get(ctx, ws.Name, metav1.GetOptions{})
		})
		workspaces[name] = ws
	}

	t.Log("Wait until workspaces are assigned to a shard")
	for name, ws := range workspaces {
		var w *tenancyv1alpha1.Workspace
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			w, err = sourceKcpClusterClient.TenancyV1alpha1().Cluster(wsPath).Workspaces().Get(ctx, ws.Name, metav1.GetOptions{})
			require.NoError(c, err)
			require.Contains(c, w.Annotations, "internal.tenancy.kcp.io/shard")
		}, wait.ForeverTestTimeout, time.Millisecond*100)
		workspaces[name] = w
	}

	t.Log("Send workspaces into deleting state")
	for _, ws := range workspaces {
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			err = sourceKcpClusterClient.TenancyV1alpha1().Cluster(wsPath).Workspaces().Delete(ctx, ws.Name, metav1.DeleteOptions{})
			require.NoError(c, err)
		}, wait.ForeverTestTimeout, time.Millisecond*100)
	}

	t.Log("Ensure workspaces still exist and have a deletion timestamp")
	for _, ws := range workspaces {
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			ws, err := sourceKcpClusterClient.TenancyV1alpha1().Cluster(wsPath).Workspaces().Get(ctx, ws.Name, metav1.GetOptions{})
			require.NoError(c, err)
			require.False(c, ws.GetDeletionTimestamp().IsZero())
		}, wait.ForeverTestTimeout, time.Millisecond*100)
	}

	t.Log("Wait for workspace types to have virtual workspace URLs published")
	for name, wst := range workspaceTypes {
		var wt *tenancyv1alpha1.WorkspaceType
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			wt, err = sourceKcpClusterClient.TenancyV1alpha1().Cluster(wsPath).WorkspaceTypes().Get(ctx, wst.Name, metav1.GetOptions{})
			require.NoError(c, err)
			require.NotEmpty(c, wt.Status.VirtualWorkspaces)
		}, wait.ForeverTestTimeout, time.Millisecond*100)
		workspaceTypes[name] = wt
	}

	t.Log("Create clients through the virtual workspace")
	adminVwKcpClusterClients := map[string]kcpclientset.ClusterInterface{}
	user1VwKcpClusterClients := map[string]kcpclientset.ClusterInterface{}
	user1VwKubeClusterClients := map[string]kcpkubernetesclientset.ClusterInterface{}

	for name, wst := range workspaceTypes {
		vwURLs := []string{}
		for _, vwURL := range wst.Status.VirtualWorkspaces {
			if strings.Contains(vwURL.URL, terminatingworkspaces.VirtualWorkspaceName) {
				vwURLs = append(vwURLs, vwURL.URL)
			}
		}
		ws := workspaces[name]
		targetVwURL, foundTargetVwURL, err := framework.VirtualWorkspaceURL(ctx, sourceKcpClusterClient, ws, vwURLs)
		require.NoError(t, err)
		require.True(t, foundTargetVwURL, "didn't find a VirtualWorkspace URL for %v finalizer and %v workspace", name, ws)

		virtualWorkspaceConfig := rest.AddUserAgent(rest.CopyConfig(sourceConfig), t.Name()+"-virtual")
		virtualWorkspaceConfig.Host = targetVwURL

		user1Config := framework.StaticTokenUserConfig("user-1", virtualWorkspaceConfig)

		virtualKcpClusterClient, err := kcpclientset.NewForConfig(user1Config)
		require.NoError(t, err)
		user1VwKcpClusterClients[name] = virtualKcpClusterClient

		virtualKubeClusterClient, err := kcpkubernetesclientset.NewForConfig(user1Config)
		require.NoError(t, err)
		user1VwKubeClusterClients[name] = virtualKubeClusterClient

		adminVirtualKcpClusterClient, err := kcpclientset.NewForConfig(virtualWorkspaceConfig)
		require.NoError(t, err)
		adminVwKcpClusterClients[name] = adminVirtualKcpClusterClient
	}

	t.Log("Ensure that LIST calls through the virtual workspace as admin succeed")
	for name := range workspaces {
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			_, err := adminVwKcpClusterClients[name].CoreV1alpha1().LogicalClusters().List(ctx, metav1.ListOptions{})
			require.NoError(c, err)
		}, wait.ForeverTestTimeout, 100*time.Millisecond)
	}

	t.Log("Ensure that LIST calls through the virtual workspace fail authorization")
	for name := range workspaces {
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			_, err := user1VwKcpClusterClients[name].CoreV1alpha1().LogicalClusters().List(ctx, metav1.ListOptions{})
			require.True(c, errors.IsForbidden(err))
		}, wait.ForeverTestTimeout, 100*time.Millisecond)
	}

	t.Log("Set up RBAC to allow future calls to succeed")
	for _, wt := range workspaceTypes {
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			role, err := kubeClusterClient.Cluster(wsPath).RbacV1().ClusterRoles().Create(ctx, &rbacv1.ClusterRole{
				ObjectMeta: metav1.ObjectMeta{
					Name: string(termination.FinalizerForType(wt)) + "-finalizer",
				},
				Rules: []rbacv1.PolicyRule{
					{
						Verbs:         []string{"finalize"},
						Resources:     []string{"workspacetypes"},
						ResourceNames: []string{wt.Name},
						APIGroups:     []string{"tenancy.kcp.io"},
					},
				},
			}, metav1.CreateOptions{})
			require.NoError(c, err)
			source.Artifact(t, func() (runtime.Object, error) {
				return kubeClusterClient.Cluster(wsPath).RbacV1().ClusterRoles().Get(ctx, role.Name, metav1.GetOptions{})
			})
			binding, err := kubeClusterClient.Cluster(wsPath).RbacV1().ClusterRoleBindings().Create(ctx, &rbacv1.ClusterRoleBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: role.Name,
				},
				RoleRef: rbacv1.RoleRef{
					Kind:     "ClusterRole",
					APIGroup: "rbac.authorization.k8s.io",
					Name:     role.Name,
				},
				Subjects: []rbacv1.Subject{{
					APIGroup: "rbac.authorization.k8s.io",
					Kind:     "User",
					Name:     "user-1",
				}},
			}, metav1.CreateOptions{})
			require.NoError(c, err)
			source.Artifact(t, func() (runtime.Object, error) {
				return kubeClusterClient.Cluster(wsPath).RbacV1().ClusterRoleBindings().Get(ctx, binding.Name, metav1.GetOptions{})
			})
		}, wait.ForeverTestTimeout, 100*time.Millisecond)
	}

	t.Log("Ensure that LIST calls through the virtual workspace eventually show the correct values")
	for name := range workspaces {
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			_, err := user1VwKcpClusterClients[name].CoreV1alpha1().LogicalClusters().List(ctx, metav1.ListOptions{})
			require.NoError(c, err)
		}, wait.ForeverTestTimeout, 100*time.Millisecond)
	}

	alphaFinalizer := string(termination.FinalizerForType(workspaceTypes["alpha"]))
	betaFinalizer := string(termination.FinalizerForType(workspaceTypes["beta"]))
	gammaFinalizer := string(termination.FinalizerForType(workspaceTypes["gamma"]))
	// expect alpha and beta to see two logicalclusters each: the one of their own
	// respective workspacetype and the one from workspacetype gamma since it
	// inherits both alpha and beta
	expLogicalClusters := map[string][][]string{
		"alpha": {
			{alphaFinalizer},
			{alphaFinalizer, betaFinalizer, gammaFinalizer},
		},
		"beta": {
			{betaFinalizer},
			{alphaFinalizer, betaFinalizer, gammaFinalizer},
		},
		"gamma": {
			{alphaFinalizer, betaFinalizer, gammaFinalizer},
		},
	}

	t.Log("Ensure that Logicalclusters have the expected finalizers set")
	for name := range workspaces {
		var clusters *corev1alpha1.LogicalClusterList
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			clusters, err = user1VwKcpClusterClients[name].CoreV1alpha1().LogicalClusters().List(ctx, metav1.ListOptions{})
			require.NoError(c, err)
		}, wait.ForeverTestTimeout, 100*time.Millisecond)

		// check that the number of logical clusters matches
		require.Equal(t, len(clusters.Items), len(expLogicalClusters[name]))

		for _, cluster := range clusters.Items {
			// check that spec finalizers are set correctly
			specF := termination.FinalizersToStrings(cluster.Spec.Finalizers)
			require.Contains(t, expLogicalClusters[name], specF) // contains compares the finalizers with all objects in the exp [][]string and does the heavy lifting for us
		}
	}

	t.Log("Start WATCH streams to confirm new logicalclusters get added when workspaces move into finalization")
	for name, wst := range workspaceTypes {
		user1Client := user1VwKcpClusterClients[name]

		watcher, err := user1Client.CoreV1alpha1().LogicalClusters().Watch(ctx, metav1.ListOptions{})
		require.NoError(t, err)

		ws := workspaceForType(wst, testLabelSelector)

		// create a new workspace and put it into deletion
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			ws, err = sourceKcpClusterClient.TenancyV1alpha1().Cluster(wsPath).Workspaces().Create(ctx, ws, metav1.CreateOptions{})
			require.NoError(c, err)
		}, wait.ForeverTestTimeout, 100*time.Millisecond)
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			ws, err = sourceKcpClusterClient.TenancyV1alpha1().Cluster(wsPath).Workspaces().Get(ctx, ws.Name, metav1.GetOptions{})
			require.NoError(c, err)
			require.Equal(c, ws.Status.Phase, corev1alpha1.LogicalClusterPhaseReady)
		}, wait.ForeverTestTimeout, 100*time.Millisecond)
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			err = sourceKcpClusterClient.TenancyV1alpha1().Cluster(wsPath).Workspaces().Delete(ctx, ws.Name, metav1.DeleteOptions{})
			require.NoError(c, err)
		}, wait.ForeverTestTimeout, 100*time.Millisecond)

		for {
			select {
			case evt := <-watcher.ResultChan():
				// there might be other actors doing who-knows-what on the workspaces, so we need to specifically
				// look for the first event *relating to the new workspace* that we get
				if logicalcluster.From(evt.Object.(metav1.Object)).String() != ws.Spec.Cluster {
					continue
				}
				// we expect modify events to come through to the watcher, since once the workspace is marked for deletion
				// it will affect the logical cluster
				require.Equal(t, evt.Type, watch.Modified)
			case <-time.Tick(wait.ForeverTestTimeout):
				t.Fatalf("never saw a watch modified event for the %s initializer", name)
			}
			break
		}
	}

	t.Log("Testing Modifications through virtual workspace")
	for name := range workspaces {
		t.Logf("For workspace %q", name)
		user1Client := user1VwKcpClusterClients[name]
		var origLcs *corev1alpha1.LogicalClusterList

		require.EventuallyWithT(t, func(c *assert.CollectT) {
			origLcs, err = user1Client.CoreV1alpha1().LogicalClusters().List(ctx, metav1.ListOptions{})
			require.NoError(c, err)
		}, wait.ForeverTestTimeout, 100*time.Millisecond)

		finalizer := termination.FinalizerForType(workspaceTypes[name])

		for _, origLc := range origLcs.Items {
			t.Log("\tModifying a non-finalizer field should be invalid")
			mod := origLc.DeepCopy()
			origLc.Annotations["wrong"] = "wrong"
			patch, err := generatePatchBytes(mod, &origLc)
			require.NoError(t, err)
			lcPath := logicalcluster.NewPath(origLc.Annotations["kcp.io/cluster"])
			if lcPath.Empty() {
				t.Errorf("could not find logicalcluster path for %v", origLc)
			}
			require.EventuallyWithT(t, func(c *assert.CollectT) {
				_, err = user1Client.Cluster(lcPath).CoreV1alpha1().LogicalClusters().Patch(ctx, corev1alpha1.LogicalClusterName, types.MergePatchType, patch, metav1.PatchOptions{})
				require.True(c, errors.IsInvalid(err))

				// Since Invalid is a generic error, which is not exclusive to an
				// initializer failing our custom updateValidation, we need to check for it
				// as well.
				// Unfortunately, it is not possible to make use of
				// field.Error.Origin to do so, as we convert our field.ErrorList into an
				// errors.StatusError, thus loosing this information. As a result, our only
				// option is to reconstruct the expected error message.
				expErrMsg := fmt.Sprintf("only removing the %q finalizer from metadata.finalizers is supported", finalizer)
				// for now using contains seems to strike the best balance between
				// identifying the error, while not making the test too brittle as
				// kubernetes statusError creation uses a lot of squashing an string
				// manipulation to create the final exact message.
				require.Contains(t, err.Error(), expErrMsg)
			}, wait.ForeverTestTimeout, 100*time.Millisecond)

			t.Log("\tModifying a finalizer, which is not ours should be denied")
			mod = origLc.DeepCopy()
			mod.Finalizers = []string{"wrong.wrong"}
			patch, err = generatePatchBytes(&origLc, mod)
			require.NoError(t, err)
			require.EventuallyWithT(t, func(c *assert.CollectT) {
				_, err = user1Client.Cluster(lcPath).CoreV1alpha1().LogicalClusters().Patch(ctx, corev1alpha1.LogicalClusterName, types.MergePatchType, patch, metav1.PatchOptions{})
				require.True(c, errors.IsInvalid(err))
				expErrMsg := fmt.Sprintf("only removing the %q finalizer from metadata.finalizers is supported", finalizer)
				require.Contains(t, err.Error(), expErrMsg)
			}, wait.ForeverTestTimeout, 100*time.Millisecond)
		}
	}

	t.Log("Removing our finalizer should work")
	for name := range workspaces {
		finalizer := termination.FinalizerForType(workspaceTypes[name])
		user1Client := user1VwKcpClusterClients[name]
		var origLcs *corev1alpha1.LogicalClusterList

		require.EventuallyWithT(t, func(c *assert.CollectT) {
			origLcs, err = user1Client.CoreV1alpha1().LogicalClusters().List(ctx, metav1.ListOptions{})
			require.NoError(c, err)
		}, wait.ForeverTestTimeout, 100*time.Millisecond)

		for _, origLc := range origLcs.Items {
			lcPath := logicalcluster.NewPath(origLc.Annotations["kcp.io/cluster"])
			mod := origLc.DeepCopy()
			mod.Finalizers = removeByValue(mod.Finalizers, termination.FinalizerSpecToMetadata(finalizer))
			patch, err := generatePatchBytes(&origLc, mod)
			require.NoError(t, err)
			require.EventuallyWithT(t, func(c *assert.CollectT) {
				_, err = user1Client.Cluster(lcPath).CoreV1alpha1().LogicalClusters().Patch(ctx, corev1alpha1.LogicalClusterName, types.MergePatchType, patch, metav1.PatchOptions{})
				require.NoError(c, err)
			}, wait.ForeverTestTimeout, 100*time.Millisecond)
		}
	}

	t.Log("Check that the workspace is deleted")
	for _, ws := range workspaces {
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			_, err := sourceKcpClusterClient.TenancyV1alpha1().Cluster(wsPath).Workspaces().Get(ctx, ws.Name, metav1.GetOptions{})
			require.True(c, errors.IsNotFound(err))
		}, wait.ForeverTestTimeout, 100*time.Millisecond)
	}
}

func workspaceForType(workspaceType *tenancyv1alpha1.WorkspaceType, testLabelSelector map[string]string) *tenancyv1alpha1.Workspace {
	return &tenancyv1alpha1.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "e2e-workspace-",
			Labels:       testLabelSelector,
		},
		Spec: tenancyv1alpha1.WorkspaceSpec{
			Type: &tenancyv1alpha1.WorkspaceTypeReference{
				Name: tenancyv1alpha1.WorkspaceTypeName(workspaceType.Name),
				Path: logicalcluster.From(workspaceType).String(),
			},
		},
	}
}

func generatePatchBytes[t any](original, modified *t) ([]byte, error) {
	// since we don't have access to controllerruntime clients MergeFrom(), use jsonpatch
	origData, err := json.Marshal(original)
	if err != nil {
		return nil, err
	}
	modData, err := json.Marshal(modified)
	if err != nil {
		return nil, err
	}

	patch, err := jsonpatch.CreateMergePatch(origData, modData)
	if err != nil {
		return nil, err
	}
	return patch, nil
}

func removeByValue[T comparable](l []T, item T) []T {
	for i, other := range l {
		if other == item {
			return append(l[:i], l[i+1:]...)
		}
	}
	return l
}
