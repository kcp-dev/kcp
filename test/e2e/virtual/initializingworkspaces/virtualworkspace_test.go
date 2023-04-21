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

package initializingworkspaces

import (
	"context"
	"encoding/json"
	"math/rand"
	"sort"
	"strings"
	"testing"
	"time"

	jsonpatch "github.com/evanphx/json-patch"
	"github.com/google/go-cmp/cmp"
	kcpdiscovery "github.com/kcp-dev/client-go/discovery"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/endpoints/discovery"
	"k8s.io/client-go/rest"

	"github.com/kcp-dev/kcp/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/tenancy/initialization"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestInitializingWorkspacesVirtualWorkspaceDiscovery(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	source := framework.SharedKcpServer(t)
	rootShardCfg := source.RootShardSystemMasterBaseConfig(t)
	rootShardCfg.Host += "/services/initializingworkspaces/whatever"

	virtualWorkspaceDiscoveryClient, err := kcpdiscovery.NewForConfig(rootShardCfg)
	require.NoError(t, err)
	_, apiResourceLists, err := virtualWorkspaceDiscoveryClient.ServerGroupsAndResources()
	require.NoError(t, err)
	require.Empty(t, cmp.Diff([]*metav1.APIResourceList{{
		GroupVersion: "v1", // TODO: we should figure out why discovery shows this empty group
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

func TestInitializingWorkspacesVirtualWorkspaceAccess(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	source := framework.SharedKcpServer(t)
	wsPath, _ := framework.NewWorkspaceFixture(t, source, core.RootCluster.Path())
	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	sourceConfig := source.BaseConfig(t)

	sourceKcpClusterClient, err := kcpclientset.NewForConfig(sourceConfig)
	require.NoError(t, err)

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(sourceConfig)
	require.NoError(t, err)

	framework.AdmitWorkspaceAccess(ctx, t, kubeClusterClient, wsPath, []string{"user-1"}, nil, false)

	// Create a Workspace that will not be Initializing and should not be shown in the virtual workspace
	framework.NewWorkspaceFixture(t, source, wsPath)

	testLabelSelector := map[string]string{
		"internal.kcp.io/e2e-test": t.Name(),
	}

	t.Log("Create workspace types that add initializers")
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
	workspacetypeNames := map[string]string{}
	for _, name := range []string{
		"alpha",
		"beta",
		"gamma",
	} {
		workspacetypeNames[name] = name + suffix()
	}

	workspacetypes := map[string]*tenancyv1alpha1.WorkspaceType{}
	workspacetypeExtensions := map[string]tenancyv1alpha1.WorkspaceTypeExtension{
		"alpha": {},
		"beta":  {},
		"gamma": {With: []tenancyv1alpha1.WorkspaceTypeReference{
			{Path: wsPath.String(), Name: tenancyv1alpha1.TypeName(workspacetypeNames["alpha"])},
			{Path: wsPath.String(), Name: tenancyv1alpha1.TypeName(workspacetypeNames["beta"])},
		}},
	}
	for _, name := range []string{
		"alpha",
		"beta",
		"gamma",
	} {
		wt, err := sourceKcpClusterClient.TenancyV1alpha1().Cluster(wsPath).WorkspaceTypes().Create(ctx, &tenancyv1alpha1.WorkspaceType{
			ObjectMeta: metav1.ObjectMeta{
				Name: workspacetypeNames[name],
			},
			Spec: tenancyv1alpha1.WorkspaceTypeSpec{
				Initializer: true,
				Extend:      workspacetypeExtensions[name],
			},
		}, metav1.CreateOptions{})
		require.NoError(t, err)
		source.Artifact(t, func() (runtime.Object, error) {
			return sourceKcpClusterClient.TenancyV1alpha1().Cluster(wsPath).WorkspaceTypes().Get(ctx, wt.Name, metav1.GetOptions{})
		})
		workspacetypes[name] = wt
	}

	t.Log("Wait for WorkspaceTypes to have their type extensions resolved")
	for _, name := range []string{
		"alpha",
		"beta",
		"gamma",
	} {
		wtName := workspacetypes[name].Name
		framework.EventuallyReady(t, func() (conditions.Getter, error) {
			return sourceKcpClusterClient.TenancyV1alpha1().Cluster(wsPath).WorkspaceTypes().Get(ctx, wtName, metav1.GetOptions{})
		}, "could not wait for readiness on WorkspaceType %s|%s", wsPath.String(), wtName)
	}

	t.Log("Create workspaces using the new types, which will get stuck in initializing")
	wsNames := make([]string, 0, 3)

	for _, workspaceType := range []string{
		"alpha",
		"beta",
		"gamma",
	} {
		var ws *tenancyv1alpha1.Workspace
		require.Eventually(t, func() bool {
			ws, err = sourceKcpClusterClient.TenancyV1alpha1().Cluster(wsPath).Workspaces().Create(ctx, workspaceForType(workspacetypes[workspaceType], testLabelSelector), metav1.CreateOptions{})
			return err == nil
		}, wait.ForeverTestTimeout, time.Millisecond*100)
		source.Artifact(t, func() (runtime.Object, error) {
			return sourceKcpClusterClient.TenancyV1alpha1().Cluster(wsPath).Workspaces().Get(ctx, ws.Name, metav1.GetOptions{})
		})
		wsNames = append(wsNames, ws.Name)
	}

	t.Log("Wait for workspaces to get stuck in initializing")
	var workspaces *tenancyv1alpha1.WorkspaceList
	require.Eventually(t, func() bool {
		workspaces, err = sourceKcpClusterClient.TenancyV1alpha1().Cluster(wsPath).Workspaces().List(ctx, metav1.ListOptions{
			LabelSelector: labels.SelectorFromSet(testLabelSelector).String(),
		})
		if err != nil {
			t.Logf("error listing workspaces: %v", err)
			return false
		}
		if len(workspaces.Items) != 3 {
			t.Logf("got %d workspaces, expected 3", len(workspaces.Items))
			return false
		}
		return workspacesStuckInInitializing(t, sourceKcpClusterClient, workspaces.Items...)
	}, wait.ForeverTestTimeout, 100*time.Millisecond)
	workspacesByType := map[string]tenancyv1alpha1.Workspace{}
	for i := range workspaces.Items {
		workspacesByType[tenancyv1alpha1.ObjectName(workspaces.Items[i].Spec.Type.Name)] = workspaces.Items[i]
	}

	t.Log("Wait for workspace types to have virtual workspace URLs published")
	for _, initializer := range []string{
		"alpha",
		"beta",
		"gamma",
	} {
		var wt *tenancyv1alpha1.WorkspaceType
		require.Eventually(t, func() bool {
			wt, err = sourceKcpClusterClient.TenancyV1alpha1().Cluster(wsPath).WorkspaceTypes().Get(ctx, workspacetypes[initializer].Name, metav1.GetOptions{})
			require.NoError(t, err)
			if len(wt.Status.VirtualWorkspaces) == 0 {
				t.Logf("workspace type %q|%q does not have virtual workspace URLs published yet", logicalcluster.From(wt), wt.Name)
				return false
			}
			return true
		}, wait.ForeverTestTimeout, 100*time.Millisecond)
		workspacetypes[initializer] = wt
	}

	t.Log("Create clients through the virtual workspace")
	adminVwKcpClusterClients := map[string]kcpclientset.ClusterInterface{}
	user1VwKcpClusterClients := map[string]kcpclientset.ClusterInterface{}
	user1VwKubeClusterClients := map[string]kcpkubernetesclientset.ClusterInterface{}
	for _, initializer := range []string{
		"alpha",
		"beta",
		"gamma",
	} {
		wt, hasWt := workspacetypes[initializer]
		require.True(t, hasWt, "didn't find a WorkspaceType for %v initializer", initializer)

		initialWs, hasInitialWs := workspacesByType[wt.Name]
		require.True(t, hasInitialWs, "didn't find a Workspace for %v initializer with type %v", initializer, wt.Name)

		ws, err := sourceKcpClusterClient.TenancyV1alpha1().Cluster(wsPath).Workspaces().Get(ctx, initialWs.Name, metav1.GetOptions{})
		require.NoError(t, err)
		vwURLs := []string{}
		for _, vwURL := range workspacetypes[initializer].Status.VirtualWorkspaces {
			vwURLs = append(vwURLs, vwURL.URL)
		}

		targetVwURL, foundTargetVwURL, err := framework.VirtualWorkspaceURL(ctx, sourceKcpClusterClient, ws, vwURLs)
		require.NoError(t, err)
		require.True(t, foundTargetVwURL, "didn't find a VirtualWorkspace URL for %v initializer and %v workspace", initializer, ws.Name)

		virtualWorkspaceConfig := rest.AddUserAgent(rest.CopyConfig(sourceConfig), t.Name()+"-virtual")
		virtualWorkspaceConfig.Host = targetVwURL
		virtualKcpClusterClient, err := kcpclientset.NewForConfig(framework.StaticTokenUserConfig("user-1", virtualWorkspaceConfig))
		require.NoError(t, err)
		virtualKubeClusterClient, err := kcpkubernetesclientset.NewForConfig(framework.StaticTokenUserConfig("user-1", virtualWorkspaceConfig))
		require.NoError(t, err)
		user1VwKcpClusterClients[initializer] = virtualKcpClusterClient
		user1VwKubeClusterClients[initializer] = virtualKubeClusterClient

		adminVirtualKcpClusterClient, err := kcpclientset.NewForConfig(virtualWorkspaceConfig)
		require.NoError(t, err)
		adminVwKcpClusterClients[initializer] = adminVirtualKcpClusterClient
	}

	t.Log("Ensure that LIST calls through the virtual workspace as admin succeed")
	for _, initializer := range []string{
		"alpha",
		"beta",
		"gamma",
	} {
		framework.Eventually(t, func() (bool, string) {
			_, err := adminVwKcpClusterClients[initializer].CoreV1alpha1().LogicalClusters().List(ctx, metav1.ListOptions{})
			if err != nil {
				return false, err.Error()
			}
			return true, ""
		}, wait.ForeverTestTimeout, 100*time.Millisecond)
	}

	t.Log("Ensure that LIST calls through the virtual workspace fail authorization")
	for _, initializer := range []string{
		"alpha",
		"beta",
		"gamma",
	} {
		_, err := user1VwKcpClusterClients[initializer].CoreV1alpha1().LogicalClusters().List(ctx, metav1.ListOptions{})
		if !errors.IsForbidden(err) {
			t.Fatalf("got %#v error from initial list, expected unauthorized", err)
		}
	}

	t.Log("Set up RBAC to allow future calls to succeed")
	for _, initializer := range []string{
		"alpha",
		"beta",
		"gamma",
	} {
		wt := workspacetypes[initializer]
		role, err := kubeClusterClient.Cluster(wsPath).RbacV1().ClusterRoles().Create(ctx, &rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: string(initialization.InitializerForType(wt)) + "-initializer",
			},
			Rules: []rbacv1.PolicyRule{
				{
					Verbs:         []string{"initialize"},
					Resources:     []string{"workspacetypes"},
					ResourceNames: []string{wt.Name},
					APIGroups:     []string{"tenancy.kcp.io"},
				},
			},
		}, metav1.CreateOptions{})
		require.NoError(t, err)
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
		require.NoError(t, err)
		source.Artifact(t, func() (runtime.Object, error) {
			return kubeClusterClient.Cluster(wsPath).RbacV1().ClusterRoleBindings().Get(ctx, binding.Name, metav1.GetOptions{})
		})
	}

	t.Log("Ensure that LIST calls through the virtual workspace eventually show the correct values")
	for _, wsName := range wsNames {
		require.Eventually(t, func() bool {
			_, err := sourceKcpClusterClient.CoreV1alpha1().Cluster(wsPath.Join(wsName)).LogicalClusters().Get(ctx, corev1alpha1.LogicalClusterName, metav1.GetOptions{})
			require.True(t, err == nil || errors.IsForbidden(err), "got %#v error getting logicalcluster %q, expected unauthorized or success", err, wsPath.Join(wsName))
			return err == nil
		}, wait.ForeverTestTimeout, 100*time.Millisecond)
	}

	for initializers, expected := range map[string][]tenancyv1alpha1.Workspace{
		"alpha,gamma": {workspacesByType[workspacetypeNames["alpha"]], workspacesByType[workspacetypeNames["gamma"]]},
		"beta,gamma":  {workspacesByType[workspacetypeNames["beta"]], workspacesByType[workspacetypeNames["gamma"]]},
		"gamma":       {workspacesByType[workspacetypeNames["gamma"]]},
	} {
		sort.Slice(expected, func(i, j int) bool {
			return expected[i].UID < expected[j].UID
		})
		actual := &corev1alpha1.LogicalClusterList{}
		for _, initializer := range strings.Split(initializers, ",") {
			require.Eventually(t, func() bool {
				clusters, err := user1VwKcpClusterClients[initializer].CoreV1alpha1().LogicalClusters().List(ctx, metav1.ListOptions{}) // no list options, all filtering is implicit
				if err != nil {
					if !errors.IsForbidden(err) {
						require.NoError(t, err)
					}
					return false // wait until cr, crb are replicated
				}
				actual.Items = append(actual.Items, clusters.Items...)
				return true
			}, wait.ForeverTestTimeout, 100*time.Millisecond)
		}

		lclusters, expectedClusters := sets.New[string](), sets.New[string]()
		for i := range actual.Items {
			lclusters.Insert(logicalcluster.From(&actual.Items[i]).String())
		}
		for i := range expected {
			expectedClusters.Insert(expected[i].Spec.Cluster)
		}
		sort.Slice(actual.Items, func(i, j int) bool {
			return actual.Items[i].UID < actual.Items[j].UID
		})
		require.Equal(t, sets.List[string](expectedClusters), sets.List[string](lclusters), "unexpected clusters for initializers %q", initializers)
	}

	t.Log("Start WATCH streams to confirm behavior on changes")
	for _, initializer := range []string{
		"alpha",
		"beta",
		"gamma",
	} {
		watcher, err := user1VwKcpClusterClients[initializer].CoreV1alpha1().LogicalClusters().Watch(ctx, metav1.ListOptions{})
		require.NoError(t, err)

		t.Logf("Adding a new workspace that the watcher for %s initializer should see", initializer)
		wt, ok := workspacetypes[initializer]
		require.True(t, ok, "didn't find WorkspaceType for %s initializer", initializer)
		initializerWs, ok := workspacesByType[wt.Name]
		require.True(t, ok, "didn't find Workspace for %v type", wt.Name)
		initializerWsShard := framework.WorkspaceShardOrDie(t, sourceKcpClusterClient, &initializerWs)

		ws, err := sourceKcpClusterClient.TenancyV1alpha1().Cluster(wsPath).Workspaces().Create(ctx, func() *tenancyv1alpha1.Workspace {
			w := workspaceForType(workspacetypes["gamma"], testLabelSelector)
			framework.WithShard(initializerWsShard.Name)(w)
			return w
		}(), metav1.CreateOptions{})
		require.NoError(t, err)
		source.Artifact(t, func() (runtime.Object, error) {
			return sourceKcpClusterClient.TenancyV1alpha1().Cluster(wsPath).Workspaces().Get(ctx, ws.Name, metav1.GetOptions{})
		})
		require.Eventually(t, func() bool {
			workspace, err := sourceKcpClusterClient.TenancyV1alpha1().Cluster(wsPath).Workspaces().Get(ctx, ws.Name, metav1.GetOptions{})
			if err != nil {
				t.Logf("error listing workspaces: %v", err)
				return false
			}
			return workspacesStuckInInitializing(t, sourceKcpClusterClient, *workspace)
		}, wait.ForeverTestTimeout, 100*time.Millisecond)

		ws, err = sourceKcpClusterClient.TenancyV1alpha1().Cluster(wsPath).Workspaces().Get(ctx, ws.Name, metav1.GetOptions{})
		require.NoError(t, err)
		wsClusterName := logicalcluster.Name(ws.Spec.Cluster)

		t.Logf("Waiting for a watcher for %s initializer to see the logicalcluster in %s for workspace %s", initializer, ws.Spec.Cluster, ws.Name)
		for {
			select {
			case evt := <-watcher.ResultChan():
				// there might be other actors doing who-knows-what on the workspaces, so we need to specifically
				// look for the first event *relating to the new workspace* that we get
				if logicalcluster.From(evt.Object.(metav1.Object)).String() != ws.Spec.Cluster {
					continue
				}
				require.Equal(t, evt.Type, watch.Added)
			case <-time.Tick(wait.ForeverTestTimeout):
				t.Fatalf("never saw a watche event for the %s initializer", initializer)
			}
			break
		}

		t.Log("Access an object inside of the workspace")
		coreClusterClient := user1VwKubeClusterClients[initializer]

		nsName := "testing"
		_, err = coreClusterClient.Cluster(wsClusterName.Path()).CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}}, metav1.CreateOptions{})
		if err != nil && !errors.IsAlreadyExists(err) {
			require.NoError(t, err)
		}

		labelSelector := map[string]string{
			"internal.kcp.io/test-initializer": initializer,
		}
		configMaps, err := coreClusterClient.Cluster(wsClusterName.Path()).CoreV1().ConfigMaps(nsName).List(ctx, metav1.ListOptions{LabelSelector: labels.SelectorFromSet(labelSelector).String()})
		require.NoError(t, err)
		require.Empty(t, cmp.Diff(configMaps.Items, []corev1.ConfigMap{}))

		configMap, err := coreClusterClient.Cluster(wsClusterName.Path()).CoreV1().ConfigMaps(nsName).Create(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "whatever" + suffix(),
				Labels: labelSelector,
			},
			Data: map[string]string{
				"key": "value",
			},
		}, metav1.CreateOptions{})
		require.NoError(t, err)

		configMaps, err = coreClusterClient.Cluster(wsClusterName.Path()).CoreV1().ConfigMaps(nsName).List(ctx, metav1.ListOptions{LabelSelector: labels.SelectorFromSet(labelSelector).String()})
		require.NoError(t, err)
		require.Empty(t, cmp.Diff(configMaps.Items, []corev1.ConfigMap{*configMap}))

		t.Logf("Ensure that the object for initializer %q is visible from outside the virtual workspace", initializer)
		configMaps, err = coreClusterClient.Cluster(wsClusterName.Path()).CoreV1().ConfigMaps(nsName).List(ctx, metav1.ListOptions{LabelSelector: labels.SelectorFromSet(labelSelector).String()})
		require.NoError(t, err)
		require.Empty(t, cmp.Diff(configMaps.Items, []corev1.ConfigMap{*configMap}))

		err = coreClusterClient.Cluster(wsClusterName.Path()).CoreV1().ConfigMaps(nsName).Delete(ctx, configMap.Name, metav1.DeleteOptions{})
		require.NoError(t, err)

		configMaps, err = coreClusterClient.Cluster(wsClusterName.Path()).CoreV1().ConfigMaps(nsName).List(ctx, metav1.ListOptions{LabelSelector: labels.SelectorFromSet(labelSelector).String()})
		require.NoError(t, err)
		require.Empty(t, cmp.Diff(configMaps.Items, []corev1.ConfigMap{}))

		patchBytesFor := func(ws *corev1alpha1.LogicalCluster, mutator func(*corev1alpha1.LogicalCluster)) []byte {
			previous := ws.DeepCopy()
			oldData, err := json.Marshal(corev1alpha1.LogicalCluster{
				Status: previous.Status,
			})
			require.NoError(t, err)

			obj := ws.DeepCopy()
			mutator(obj)
			newData, err := json.Marshal(corev1alpha1.LogicalCluster{
				Status: obj.Status,
			})
			require.NoError(t, err)

			patchBytes, err := jsonpatch.CreateMergePatch(oldData, newData)
			require.NoError(t, err)
			return patchBytes
		}

		t.Log("Transitioning the new workspace out of initializing")
		clusterClient := user1VwKcpClusterClients[initializer].CoreV1alpha1().LogicalClusters()
		logicalCluster, err := clusterClient.Cluster(wsClusterName.Path()).Get(ctx, corev1alpha1.LogicalClusterName, metav1.GetOptions{})
		require.NoError(t, err)

		t.Logf("Attempt to do something more than just removing our initializer %q, get denied", initializer)
		patchBytes := patchBytesFor(logicalCluster, func(workspace *corev1alpha1.LogicalCluster) {
			workspace.Status.Initializers = []corev1alpha1.LogicalClusterInitializer{"wrong"}
		})
		_, err = clusterClient.Cluster(wsClusterName.Path()).Patch(ctx, corev1alpha1.LogicalClusterName, types.MergePatchType, patchBytes, metav1.PatchOptions{}, "status")
		if !errors.IsInvalid(err) {
			t.Fatalf("got %#v error from patch, expected invalid", err)
		}

		t.Logf("Remove just our initializer %q", initializer)
		patchBytes = patchBytesFor(logicalCluster, func(workspace *corev1alpha1.LogicalCluster) {
			workspace.Status.Initializers = initialization.EnsureInitializerAbsent(initialization.InitializerForType(workspacetypes[initializer]), workspace.Status.Initializers)
		})
		_, err = clusterClient.Cluster(wsClusterName.Path()).Patch(ctx, corev1alpha1.LogicalClusterName, types.MergePatchType, patchBytes, metav1.PatchOptions{}, "status")
		require.NoError(t, err)

		t.Logf("Waiting for a watcher for %s initializer to see an update to the logicalcluster in %s for workspace %s", initializer, ws.Spec.Cluster, ws.Name)
		for {
			select {
			case evt := <-watcher.ResultChan():
				if evt.Type == watch.Modified {
					continue // we will see some modification events from the above patch and the resulting controller reactions
				}
				// there might be other actors doing who-knows-what on the workspaces, so we need to specifically
				// look for the first event *relating to the new workspace* that we get
				if logicalcluster.From(evt.Object.(metav1.Object)).String() != ws.Spec.Cluster {
					continue
				}
				require.Equal(t, evt.Type, watch.Deleted)
			case <-time.Tick(wait.ForeverTestTimeout):
				t.Fatalf("never saw a watch event for the %s initializer", initializer)
			}
			break
		}

		t.Log("Ensure accessing objects in the workspace is forbidden now that it is not initializing")
		kubeClusterClient := user1VwKubeClusterClients[initializer].Cluster(wsClusterName.Path()).CoreV1().ConfigMaps("testing")
		_, err = kubeClusterClient.List(ctx, metav1.ListOptions{})
		if !errors.IsForbidden(err) {
			t.Fatalf("got %#v error from initial list, expected unauthorized", err)
		}

		t.Log("Ensure get workspace requests are 404 now that it is not initializing")
		wsClient := user1VwKcpClusterClients[initializer].CoreV1alpha1().LogicalClusters()
		_, err = wsClient.Cluster(wsClusterName.Path()).Get(ctx, corev1alpha1.LogicalClusterName, metav1.GetOptions{})
		if !errors.IsNotFound(err) {
			t.Fatalf("got error from get, expected not found: %v", err)
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
			Type: tenancyv1alpha1.WorkspaceTypeReference{
				Name: tenancyv1alpha1.WorkspaceTypeName(workspaceType.Name),
				Path: logicalcluster.From(workspaceType).String(),
			},
		},
	}
}

func workspacesStuckInInitializing(t *testing.T, kcpClient kcpclientset.ClusterInterface, workspaces ...tenancyv1alpha1.Workspace) bool {
	t.Helper()

	for _, workspace := range workspaces {
		if workspace.Status.Phase != corev1alpha1.LogicalClusterPhaseInitializing {
			t.Logf("workspace %s is in %s, not %s", workspace.Name, workspace.Status.Phase, corev1alpha1.LogicalClusterPhaseInitializing)
			return false
		}
		if len(workspace.Status.Initializers) == 0 {
			t.Logf("workspace %s has no initializers", workspace.Name)
			return false
		}
		t.Logf("Workspace %s (accessible via /clusters/%s) on %s shard is stuck in initializing", workspace.Name, workspace.Spec.Cluster, framework.WorkspaceShardOrDie(t, kcpClient, &workspace).Name)
	}
	return true
}
