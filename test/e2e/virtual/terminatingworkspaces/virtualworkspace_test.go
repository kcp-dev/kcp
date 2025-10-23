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
	"slices"
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

func TestTerminatingWorkspacesVirtualWorkspaceDiscovery(t *testing.T) {
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

func TestTerminatingWorkspacesVirtualWorkspaceAccess(t *testing.T) {
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

	t.Log("Create workspacetypes with a terminators")
	// store workspaces and their randomized names under easy to find aliases
	workspaceTypes := map[string]*tenancyv1alpha1.WorkspaceType{
		"alpha": {},
		"beta":  {},
		"gamma": {},
	}
	for name := range workspaceTypes {
		wst := &tenancyv1alpha1.WorkspaceType{
			ObjectMeta: metav1.ObjectMeta{
				// WorkspaceTypes and the terminator names will have to be globally unique, so we add some suffix here
				// to ensure that parallel test runs do not impact our ability to verify this behavior.
				Name: name + "-" + randSuffix(),
			},
			Spec: tenancyv1alpha1.WorkspaceTypeSpec{
				Terminator: true,
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

	t.Log("Wait until workspaces are assigned to a shard, in phase ready, and have terminators")
	for name, ws := range workspaces {
		var w *tenancyv1alpha1.Workspace
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			w, err = sourceKcpClusterClient.TenancyV1alpha1().Cluster(wsPath).Workspaces().Get(ctx, ws.Name, metav1.GetOptions{})
			require.NoError(c, err)
			require.Contains(c, w.Annotations, "internal.tenancy.kcp.io/shard")
			require.Equal(c, w.Status.Phase, corev1alpha1.LogicalClusterPhaseReady)
			require.NotEmpty(c, w.Status.Terminators)
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
	// We need to track all combinations of virtualWorkspaces (defined by
	// workspacetypes) and targetWorkspace (defined by the shard it's on). This is
	// required because in multi-shard setups, the gamma workspace can be on a
	// different shard than alpha and beta. As a result, when using alpha's or
	// beta's virtual workspace, we need to use gammas shard url when we want to
	// access gamma.
	// first key is workspaceType, second key is targetWorkspace
	user1VwKcpClusterClients := make(map[string]map[string]kcpclientset.ClusterInterface)
	adminVwKcpClusterClients := make(map[string]map[string]kcpclientset.ClusterInterface)

	for wstName, wst := range workspaceTypes {
		vwURLs := []string{}
		for _, vwURL := range wst.Status.VirtualWorkspaces {
			// filter out any URLs not belonging to terminating virtual workspace
			if strings.Contains(vwURL.URL, terminatingworkspaces.VirtualWorkspaceName) {
				vwURLs = append(vwURLs, vwURL.URL)
			}
		}

		for targetWsName, targetWorkspace := range workspaces {
			// only if our workspacetype is part of the terminators of the target workspace, we need to build
			// the clientset
			if slices.Contains(targetWorkspace.Status.Terminators, termination.TerminatorForType(wst)) {
				// skip any combinations, which are not able to see any workspaces.
				// Namely these are alpha&beta clients, which use the shard url that
				// gamma is not scheduled on. We cannot simplify this filtering, because
				// at the moment the workspaces get assigned to shards randomly, meaning
				// all combinations of alpha/beta/gamma are possible. Additionally by
				// doing this filtering now, we can simplify the testing below, because
				// we only have clients that yield usable results.
				if targetWorkspace.Name != workspaces[wstName].Name && targetWorkspace.Annotations["internal.tenancy.kcp.io/shard"] == workspaces[wstName].Annotations["internal.tenancy.kcp.io/shard"] {
					continue
				}

				targetVwURL, foundTargetVwURL, err := framework.VirtualWorkspaceURL(ctx, sourceKcpClusterClient, targetWorkspace, vwURLs)
				require.NoError(t, err)
				require.True(t, foundTargetVwURL)

				// build the clientsets
				virtualWorkspaceConfig := rest.AddUserAgent(rest.CopyConfig(sourceConfig), t.Name()+"-virtual")
				virtualWorkspaceConfig.Host = targetVwURL

				user1Config := framework.StaticTokenUserConfig("user-1", virtualWorkspaceConfig)

				// make sure the outer map exists, before adding to the inner map
				if _, ok := user1VwKcpClusterClients[wstName]; !ok {
					user1VwKcpClusterClients[wstName] = make(map[string]kcpclientset.ClusterInterface)
				}
				virtualKcpClusterClient, err := kcpclientset.NewForConfig(user1Config)
				require.NoError(t, err)
				user1VwKcpClusterClients[wstName][targetWsName] = virtualKcpClusterClient

				// make sure the outer map exists, before adding to the inner map
				if _, ok := adminVwKcpClusterClients[wstName]; !ok {
					adminVwKcpClusterClients[wstName] = make(map[string]kcpclientset.ClusterInterface)
				}
				adminVirtualKcpClusterClient, err := kcpclientset.NewForConfig(virtualWorkspaceConfig)
				require.NoError(t, err)
				adminVwKcpClusterClients[wstName][targetWsName] = adminVirtualKcpClusterClient
			}
		}
	}

	t.Log("Ensure that LIST calls through the virtual workspace as admin succeed")
	for name := range workspaces {
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			for _, client := range adminVwKcpClusterClients[name] {
				_, err := client.CoreV1alpha1().LogicalClusters().List(ctx, metav1.ListOptions{})
				require.NoError(c, err)
			}
		}, wait.ForeverTestTimeout, 100*time.Millisecond)
	}

	t.Log("Ensure that LIST calls through the virtual workspace fail authorization")
	for name := range workspaces {
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			for _, client := range user1VwKcpClusterClients[name] {
				_, err := client.CoreV1alpha1().LogicalClusters().List(ctx, metav1.ListOptions{})
				require.True(c, errors.IsForbidden(err))
			}
		}, wait.ForeverTestTimeout, 100*time.Millisecond)
	}

	t.Log("Set up RBAC to allow future calls to succeed")
	for _, wt := range workspaceTypes {
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			role, err := kubeClusterClient.Cluster(wsPath).RbacV1().ClusterRoles().Create(ctx, &rbacv1.ClusterRole{
				ObjectMeta: metav1.ObjectMeta{
					Name: string(termination.TerminatorForType(wt)) + "-terminator",
				},
				Rules: []rbacv1.PolicyRule{
					{
						Verbs:         []string{"terminate"},
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
			for _, client := range user1VwKcpClusterClients[name] {
				_, err := client.CoreV1alpha1().LogicalClusters().List(ctx, metav1.ListOptions{})
				require.NoError(c, err)
			}
		}, wait.ForeverTestTimeout, 100*time.Millisecond)
	}

	alphaTerminator := string(termination.TerminatorForType(workspaceTypes["alpha"]))
	betaTerminator := string(termination.TerminatorForType(workspaceTypes["beta"]))
	gammaTerminator := string(termination.TerminatorForType(workspaceTypes["gamma"]))
	// expect alpha and beta to see two logicalclusters each: the one of their own
	// respective workspacetype and the one from workspacetype gamma since it
	// inherits both alpha and beta
	expLogicalClusters := map[string][][]string{
		"alpha": {
			{alphaTerminator},
			{alphaTerminator, betaTerminator, gammaTerminator},
		},
		"beta": {
			{betaTerminator},
			{alphaTerminator, betaTerminator, gammaTerminator},
		},
		"gamma": {
			{alphaTerminator, betaTerminator, gammaTerminator},
		},
	}

	t.Log("Ensure that Logicalclusters have the expected terminators set")
	for name := range workspaces {
		var clusters []corev1alpha1.LogicalCluster
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			for _, client := range user1VwKcpClusterClients[name] {
				cls, err := client.CoreV1alpha1().LogicalClusters().List(ctx, metav1.ListOptions{})
				require.NoError(c, err)
				clusters = append(clusters, cls.Items...)
			}
		}, wait.ForeverTestTimeout, 100*time.Millisecond)

		// check that the number of logical clusters matches
		require.Equal(t, len(expLogicalClusters[name]), len(clusters))

		for _, cluster := range clusters {
			// check that spec terminators are set correctly
			st := termination.TerminatorsToStrings(cluster.Spec.Terminators)
			require.Contains(t, expLogicalClusters[name], st) // contains compares the terminators with all objects in the exp [][]string and does the heavy lifting for us
			// check that the status terminators are set correctly
			require.Contains(t, expLogicalClusters[name], st)
		}
	}

	t.Log("Testing Modifications through virtual workspace")
	for name, wst := range workspaceTypes {
		t.Logf("For workspacetype/virtual-workspace %q", name)

		for _, client := range user1VwKcpClusterClients[name] {
			terminator := termination.TerminatorForType(wst)

			origLcs := []corev1alpha1.LogicalCluster{}
			require.EventuallyWithT(t, func(c *assert.CollectT) {
				cls, err := client.CoreV1alpha1().LogicalClusters().List(ctx, metav1.ListOptions{})
				require.NoError(c, err)
				origLcs = append(origLcs, cls.Items...)
			}, wait.ForeverTestTimeout, 100*time.Millisecond)

			for _, origLc := range origLcs {
				lcPath := logicalcluster.NewPath(origLc.Annotations["kcp.io/cluster"])
				if lcPath.Empty() {
					t.Errorf("could not find logicalcluster path for %v", origLc)
				}

				t.Log("\tModifying a non-terminator field should be not supported")
				mod := origLc.DeepCopy()
				mod.Annotations["wrong"] = "wrong"
				patch, err := generatePatchBytes(mod, &origLc)
				require.NoError(t, err)
				require.EventuallyWithT(t, func(c *assert.CollectT) {
					_, err = client.Cluster(lcPath).CoreV1alpha1().LogicalClusters().Patch(ctx, corev1alpha1.LogicalClusterName, types.MergePatchType, patch, metav1.PatchOptions{})
					// we expect MethodNotSupported here as the storage layer denies any non-status field updates
					require.True(c, errors.IsMethodNotSupported(err))
				}, wait.ForeverTestTimeout, 100*time.Millisecond)

				t.Log("\tModifying a terminator, which is not ours should be denied")
				mod = origLc.DeepCopy()
				mod.Status.Terminators = []corev1alpha1.LogicalClusterTerminator{"wrong:wrong"}
				patch, err = generatePatchBytes(&origLc, mod)
				require.NoError(t, err)
				require.EventuallyWithT(t, func(c *assert.CollectT) {
					_, err = client.Cluster(lcPath).CoreV1alpha1().LogicalClusters().Patch(ctx, corev1alpha1.LogicalClusterName, types.MergePatchType, patch, metav1.PatchOptions{}, "status")
					require.True(c, errors.IsInvalid(err))
					// Since Invalid is a generic error, which is not exclusive to an
					// terminator failing our custom updateValidation, we need to check for it
					// as well.
					// Unfortunately, it is not possible to make use of
					// field.Error.Origin to do so, as we convert our field.ErrorList into an
					// errors.StatusError, thus loosing this information. As a result, our only
					// option is to reconstruct the expected error message.
					expErrMsg := fmt.Sprintf("only removing the %q terminator is supported", terminator)
					// for now using contains seems to strike the best balance between
					// identifying the error, while not making the test too brittle as
					// kubernetes statusError creation uses a lot of squashing an string
					// manipulation to create the final exact message.
					require.Contains(t, err.Error(), expErrMsg)
				}, wait.ForeverTestTimeout, 100*time.Millisecond)
			}
		}
	}

	t.Log("Removing our terminator should work")
	for name := range workspaceTypes {
		terminator := termination.TerminatorForType(workspaceTypes[name])

		for _, client := range user1VwKcpClusterClients[name] {
			origLcs := []corev1alpha1.LogicalCluster{}
			require.EventuallyWithT(t, func(c *assert.CollectT) {
				cls, err := client.CoreV1alpha1().LogicalClusters().List(ctx, metav1.ListOptions{})
				require.NoError(c, err)
				origLcs = append(origLcs, cls.Items...)
			}, wait.ForeverTestTimeout, 100*time.Millisecond)

			for _, origLc := range origLcs {
				lcPath := logicalcluster.NewPath(origLc.Annotations["kcp.io/cluster"])
				mod := origLc.DeepCopy()
				mod.Status.Terminators = removeByValue(mod.Status.Terminators, terminator)
				patch, err := generatePatchBytes(&origLc, mod)
				require.NoError(t, err)
				require.EventuallyWithT(t, func(c *assert.CollectT) {
					_, err = client.Cluster(lcPath).CoreV1alpha1().LogicalClusters().Patch(ctx, corev1alpha1.LogicalClusterName, types.MergePatchType, patch, metav1.PatchOptions{}, "status")
					require.NoError(c, err)
				}, wait.ForeverTestTimeout, 100*time.Millisecond)
			}
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

func TestTerminatingWorkspacesVirtualWorkspaceWatch(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	source := kcptesting.SharedKcpServer(t)
	wsPath, _ := kcptesting.NewWorkspaceFixture(t, source, core.RootCluster.Path())
	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	sourceConfig := source.BaseConfig(t)

	sourceKcpClusterClient, err := kcpclientset.NewForConfig(sourceConfig)
	require.NoError(t, err)

	testLabelSelector := map[string]string{
		"internal.kcp.io/e2e-test": t.Name(),
	}

	t.Log("Create workspacetypes with terminators")
	workspaceTypes := map[string]*tenancyv1alpha1.WorkspaceType{
		"parent": {},
		"child":  {},
	}
	for name := range workspaceTypes {
		wst := &tenancyv1alpha1.WorkspaceType{
			ObjectMeta: metav1.ObjectMeta{
				Name: name + "-" + randSuffix(),
			},
			Spec: tenancyv1alpha1.WorkspaceTypeSpec{
				Terminator: true,
			},
		}
		workspaceTypes[name] = wst
	}
	// make child extend the parent
	workspaceTypes["child"].Spec.Extend.With = []tenancyv1alpha1.WorkspaceTypeReference{
		{Path: wsPath.String(), Name: tenancyv1alpha1.TypeName(workspaceTypes["parent"].Name)},
	}

	// create workspacetypes
	for _, wst := range workspaceTypes {
		_, err := sourceKcpClusterClient.TenancyV1alpha1().Cluster(wsPath).WorkspaceTypes().Create(ctx, wst, metav1.CreateOptions{})
		require.NoError(t, err)
		source.Artifact(t, func() (runtime.Object, error) {
			return sourceKcpClusterClient.TenancyV1alpha1().Cluster(wsPath).WorkspaceTypes().Get(ctx, wst.Name, metav1.GetOptions{})
		})
	}

	t.Log("Wait for WorkspaceTypes to have their type extensions resolved and vw URLs published")
	for name, wst := range workspaceTypes {
		wt := &tenancyv1alpha1.WorkspaceType{}
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			wt, err = sourceKcpClusterClient.TenancyV1alpha1().Cluster(wsPath).WorkspaceTypes().Get(ctx, wst.Name, metav1.GetOptions{})
			require.NoError(c, err)
			require.NotEmpty(c, wt.Status.VirtualWorkspaces)
		}, wait.ForeverTestTimeout, 100*time.Millisecond)
		workspaceTypes[name] = wt
	}

	shards := []corev1alpha1.Shard{}
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		s, err := sourceKcpClusterClient.Cluster(core.RootCluster.Path()).CoreV1alpha1().Shards().List(ctx, metav1.ListOptions{})
		require.NoError(c, err)
		shards = s.Items
	}, wait.ForeverTestTimeout, time.Millisecond*100)

	shardForWorkspace := map[string]*struct {
		shard *corev1alpha1.Shard
	}{
		"parent": {},
		"child":  {},
	}

	t.Log("Create clients through the virtual workspace")
	// pre-decide which workspace will be scheduled to which shard, so we can start the watchers beforehand.
	// For multi-sharded setups, make sure that parent and child workspaces will be on different shards
	isMultishard := len(shards) > 1
	if isMultishard {
		shardForWorkspace["parent"].shard = &shards[0]
		shardForWorkspace["child"].shard = &shards[1]
	} else {
		shardForWorkspace["parent"].shard = &shards[0]
		shardForWorkspace["child"].shard = &shards[0]
	}

	type connection struct {
		watcher   watch.Interface
		clientset kcpclientset.ClusterInterface
		workspace *tenancyv1alpha1.Workspace
	}
	// watchConnections is a collection of all clientset combinations which we expect to return results:
	// first key is WorkspaceType, second key TargetWorkspace
	watchConnections := map[string]map[string]*connection{
		"parent": make(map[string]*connection),
		"child":  make(map[string]*connection),
	}

	t.Log("Start watchers for virtual workspace combinations")
	// create clients for all suitable connections
	watchConnections["parent"]["parent"] = &connection{}
	watchConnections["child"]["child"] = &connection{}
	watchConnections["parent"]["child"] = &connection{}
	for wstName, targetCon := range watchConnections {
		for targetWsName, con := range targetCon {
			config := rest.AddUserAgent(rest.CopyConfig(sourceConfig), t.Name()+"-virtual")
			url := terminatorUrlFromWorkspaceTypeAndShard(workspaceTypes[wstName], shardForWorkspace[targetWsName].shard)
			require.NotEmpty(t, url)
			config.Host = url
			clientset, err := kcpclientset.NewForConfig(config)
			require.NoError(t, err)
			con.clientset = clientset

			var watcher watch.Interface
			require.EventuallyWithT(t, func(c *assert.CollectT) {
				watcher, err = clientset.CoreV1alpha1().LogicalClusters().Watch(ctx, metav1.ListOptions{})
				require.NoError(c, err)
				require.NotNil(c, watcher) // if we are too fast, it is possible for .Watch to return no error but a nil watcher
			}, wait.ForeverTestTimeout, 100*time.Millisecond)
			con.watcher = watcher
		}
	}

	t.Log("Create workspaces and put them into deletion")
	for name, wst := range workspaceTypes {
		ws := workspaceForType(wst, testLabelSelector)
		kcptesting.WithShard(shardForWorkspace[name].shard.Name)(ws)
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			ws, err = sourceKcpClusterClient.TenancyV1alpha1().Cluster(wsPath).Workspaces().Create(ctx, ws, metav1.CreateOptions{})
			require.NoError(c, err)
		}, wait.ForeverTestTimeout, 100*time.Millisecond)
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			ws, err = sourceKcpClusterClient.TenancyV1alpha1().Cluster(wsPath).Workspaces().Get(ctx, ws.Name, metav1.GetOptions{})
			require.NoError(c, err)
			require.Contains(c, ws.Annotations, "internal.tenancy.kcp.io/shard")
			require.Equal(c, ws.Status.Phase, corev1alpha1.LogicalClusterPhaseReady)
			require.NotEmpty(c, ws.Status.Terminators)
		}, wait.ForeverTestTimeout, 100*time.Millisecond)
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			err = sourceKcpClusterClient.TenancyV1alpha1().Cluster(wsPath).Workspaces().Delete(ctx, ws.Name, metav1.DeleteOptions{})
			require.NoError(c, err)
		}, wait.ForeverTestTimeout, 100*time.Millisecond)
		// add all the details of the workspace into watchConnections, so we can use them later
		for _, targetCon := range watchConnections {
			for targetWs, con := range targetCon {
				if name == targetWs {
					con.workspace = ws
				}
			}
		}
	}

	t.Log("Check that watchers have received events for workspaces")
	for name, targetCon := range watchConnections {
		for _, con := range targetCon {
			for {
				select {
				case evt := <-con.watcher.ResultChan():
					obj, ok := evt.Object.(metav1.Object)
					if !ok {
						continue
					}
					// there might be other actors doing who-knows-what on the workspaces, so we need to specifically
					// look for the first event *relating to the new workspace* that we get
					cluster, ok := obj.GetAnnotations()["kcp.io/cluster"]
					if !ok {
						continue
					}
					if cluster != con.workspace.Spec.Cluster {
						continue
					}
					// we are searching for a modified event where the object is marked for deletion
					if evt.Type != watch.Modified || obj.GetDeletionTimestamp().IsZero() {
						continue
					}
				case <-time.Tick(wait.ForeverTestTimeout):
					t.Fatalf("never saw a watch modified event for vw %q and targetWs %q", name, con.workspace.Name)
				}
				break
			}
		}
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

func terminatorUrlFromWorkspaceTypeAndShard(wst *tenancyv1alpha1.WorkspaceType, shard *corev1alpha1.Shard) string {
	vwURLs := []string{}
	for _, vwURL := range wst.Status.VirtualWorkspaces {
		// filter out any URLs not belonging to terminating virtual workspace
		if strings.Contains(vwURL.URL, terminatingworkspaces.VirtualWorkspaceName) {
			vwURLs = append(vwURLs, vwURL.URL)
		}
	}
	for _, vwURL := range vwURLs {
		if strings.HasPrefix(vwURL, shard.Spec.VirtualWorkspaceURL) {
			return vwURL
		}
	}
	return ""
}

func removeByValue[T comparable](l []T, item T) []T {
	for i, other := range l {
		if other == item {
			return append(l[:i], l[i+1:]...)
		}
	}
	return l
}

func randSuffix() string {
	// WorkspaceType names are pretty locked down, using this regex: '^[A-Z0-9][a-zA-Z0-9]+$' - so we just add some simple lowercase suffix.
	const characters = "abcdefghijklmnopqrstuvwxyz"
	b := make([]byte, 10)
	for i := range b {
		b[i] = characters[rand.Intn(len(characters))]
	}
	return string(b)
}
