/*
Copyright 2022 The kcp Authors.

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
	"github.com/stretchr/testify/assert"
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

	kcpdiscovery "github.com/kcp-dev/client-go/discovery"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/kcp-dev/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	"github.com/kcp-dev/sdk/apis/tenancy/initialization"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	kcptesting "github.com/kcp-dev/sdk/testing"
	kcptestinghelpers "github.com/kcp-dev/sdk/testing/helpers"

	"github.com/kcp-dev/kcp/pkg/authorization"
	"github.com/kcp-dev/kcp/pkg/virtual/initializingworkspaces"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestInitializingWorkspacesVirtualWorkspaceDiscovery(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "control-plane")

	source := kcptesting.SharedKcpServer(t)
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

	source := kcptesting.SharedKcpServer(t)
	wsPath, _ := kcptesting.NewWorkspaceFixture(t, source, core.RootCluster.Path())
	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	sourceConfig := source.BaseConfig(t)

	sourceKcpClusterClient, err := kcpclientset.NewForConfig(sourceConfig)
	require.NoError(t, err)

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(sourceConfig)
	require.NoError(t, err)

	framework.AdmitWorkspaceAccess(ctx, t, kubeClusterClient, wsPath, []string{"user-1"}, nil, false)

	// Create a Workspace that will not be Initializing and should not be shown in the virtual workspace
	kcptesting.NewWorkspaceFixture(t, source, wsPath)

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
		kcptestinghelpers.EventuallyReady(t, func() (conditions.Getter, error) {
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
			// only add urls belonging to initializing workspaces
			if strings.Contains(vwURL.URL, initializingworkspaces.VirtualWorkspaceName) {
				vwURLs = append(vwURLs, vwURL.URL)
			}
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
		kcptestinghelpers.Eventually(t, func() (bool, string) {
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
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			_, err := sourceKcpClusterClient.CoreV1alpha1().Cluster(wsPath.Join(wsName)).LogicalClusters().Get(ctx, corev1alpha1.LogicalClusterName, metav1.GetOptions{})
			require.NoError(c, err, "got %#v error getting logicalcluster %q, expected success", err, wsPath.Join(wsName))
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
			// This loop needs longer timeout because SAR deny cache is refreshed every 30 seconds,
			// so it might take up to 30 seconds for the above RBAC changes to be reflected in authorization decisions.
			// Because of that, we can't use exactly 30 seconds as timeout, instead we use 1 minute to be on the safe side.
			require.Eventually(t, func() bool {
				clusters, err := user1VwKcpClusterClients[initializer].CoreV1alpha1().LogicalClusters().List(ctx, metav1.ListOptions{}) // no list options, all filtering is implicit
				if kcptestinghelpers.TolerateOrFail(t, err, errors.IsForbidden) {
					return false
				}
				actual.Items = append(actual.Items, clusters.Items...)
				return true
			}, 2*wait.ForeverTestTimeout, 100*time.Millisecond)
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
		initializerWsShard := kcptesting.WorkspaceShardOrDie(t, sourceKcpClusterClient, &initializerWs)

		ws, err := sourceKcpClusterClient.TenancyV1alpha1().Cluster(wsPath).Workspaces().Create(ctx, func() *tenancyv1alpha1.Workspace {
			w := workspaceForType(workspacetypes["gamma"], testLabelSelector)
			kcptesting.WithShard(initializerWsShard.Name)(w)
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
				require.Equal(t, watch.Added, evt.Type)
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
				require.Equal(t, watch.Deleted, evt.Type)
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
			Type: &tenancyv1alpha1.WorkspaceTypeReference{
				Name: tenancyv1alpha1.WorkspaceTypeName(workspaceType.Name),
				Path: logicalcluster.From(workspaceType).String(),
			},
		},
	}
}

// TestInitializingWorkspacesVirtualWorkspaceInitializerPermissions exercises the
// declarative-RBAC mode of the initializing VW content proxy: when the WorkspaceType
// declares initializerPermissions, the proxy evaluates each request in-process and
// forwards with the controller's own identity plus a synthetic group, instead of
// impersonating the workspace owner. The test verifies that:
//
//   - in-scope requests (configmaps GET) succeed,
//   - out-of-scope requests (secrets GET) are rejected with 403 by the proxy,
//   - direct shard requests asserting the synthetic group themselves are not honored
//     (the front-proxy strips the group prefix before authentication).
//
// This mirrors TestTerminatingWorkspacesVirtualWorkspaceTerminatorPermissions in
// the terminatingworkspaces e2e suite; both VWs share the same proxy machinery and
// any divergence in behaviour between them should fail one of these two tests.
func TestInitializingWorkspacesVirtualWorkspaceInitializerPermissions(t *testing.T) {
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

	const username = "user-1"
	framework.AdmitWorkspaceAccess(ctx, t, kubeClusterClient, wsPath, []string{username}, nil, false)

	// Suffix so this test can run in parallel with itself / the existing suite.
	const characters = "abcdefghijklmnopqrstuvwxyz"
	suffix := func() string {
		b := make([]byte, 10)
		for i := range b {
			b[i] = characters[rand.Intn(len(characters))]
		}
		return string(b)
	}

	t.Log("Create a workspacetype with initializerPermissions scoped to configmaps only")
	wst := &tenancyv1alpha1.WorkspaceType{
		ObjectMeta: metav1.ObjectMeta{
			Name: "scoped" + suffix(),
		},
		Spec: tenancyv1alpha1.WorkspaceTypeSpec{
			Initializer: true,
			InitializerPermissions: []rbacv1.PolicyRule{{
				APIGroups: []string{""},
				Resources: []string{"configmaps"},
				Verbs:     []string{"get", "list", "create", "update", "delete"},
			}},
		},
	}
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		_, err := sourceKcpClusterClient.TenancyV1alpha1().Cluster(wsPath).WorkspaceTypes().Create(ctx, wst, metav1.CreateOptions{})
		require.NoError(c, err)
	}, wait.ForeverTestTimeout, 100*time.Millisecond)
	source.Artifact(t, func() (runtime.Object, error) {
		return sourceKcpClusterClient.TenancyV1alpha1().Cluster(wsPath).WorkspaceTypes().Get(ctx, wst.Name, metav1.GetOptions{})
	})

	t.Log("Wait for WorkspaceType and its virtual workspace URLs to be ready")
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		wst, err = sourceKcpClusterClient.TenancyV1alpha1().Cluster(wsPath).WorkspaceTypes().Get(ctx, wst.Name, metav1.GetOptions{})
		require.NoError(c, err)
		require.NotEmpty(c, wst.Status.VirtualWorkspaces)
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	t.Log("Create a workspace of that type; it will get stuck in Initializing because the initializer is not removed")
	wsTemplate := workspaceForType(wst, map[string]string{"internal.kcp.io/e2e-test": t.Name()})
	var ws *tenancyv1alpha1.Workspace
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		// Don't reassign wsTemplate on error: Create returns (nil, err) on failure,
		// which would clobber the template for the next retry and produce
		// "name or generateName is required" on subsequent attempts.
		created, createErr := sourceKcpClusterClient.TenancyV1alpha1().Cluster(wsPath).Workspaces().Create(ctx, wsTemplate, metav1.CreateOptions{})
		require.NoError(c, createErr)
		ws = created
	}, wait.ForeverTestTimeout, 100*time.Millisecond)
	source.Artifact(t, func() (runtime.Object, error) {
		return sourceKcpClusterClient.TenancyV1alpha1().Cluster(wsPath).Workspaces().Get(ctx, ws.Name, metav1.GetOptions{})
	})
	initializer := initialization.InitializerForType(wst)
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		ws, err = sourceKcpClusterClient.TenancyV1alpha1().Cluster(wsPath).Workspaces().Get(ctx, ws.Name, metav1.GetOptions{})
		require.NoError(c, err)
		require.Contains(c, ws.Annotations, "internal.tenancy.kcp.io/shard")
		require.Equal(c, corev1alpha1.LogicalClusterPhaseInitializing, ws.Status.Phase)
		require.Contains(c, ws.Status.Initializers, initializer)
	}, wait.ForeverTestTimeout, 100*time.Millisecond)
	wsClusterName := logicalcluster.Name(ws.Spec.Cluster)

	const nsName = "scoped-perms-test"
	const cmName = "scoped-target"
	t.Log("Seed a namespace + configmap (in-scope) and a secret (out-of-scope) inside the initializing workspace as admin")
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		_, err := kubeClusterClient.Cluster(wsClusterName.Path()).CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: nsName},
		}, metav1.CreateOptions{})
		if !errors.IsAlreadyExists(err) {
			require.NoError(c, err)
		}
	}, wait.ForeverTestTimeout, 100*time.Millisecond)
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		_, err := kubeClusterClient.Cluster(wsClusterName.Path()).CoreV1().ConfigMaps(nsName).Create(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: cmName},
			Data:       map[string]string{"key": "value"},
		}, metav1.CreateOptions{})
		if !errors.IsAlreadyExists(err) {
			require.NoError(c, err)
		}
	}, wait.ForeverTestTimeout, 100*time.Millisecond)
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		_, err := kubeClusterClient.Cluster(wsClusterName.Path()).CoreV1().Secrets(nsName).Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "out-of-scope"},
			Data:       map[string][]byte{"k": []byte("v")},
		}, metav1.CreateOptions{})
		if !errors.IsAlreadyExists(err) {
			require.NoError(c, err)
		}
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	t.Log("Grant user-1 the initialize verb on the workspacetype")
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		_, err := kubeClusterClient.Cluster(wsPath).RbacV1().ClusterRoles().Create(ctx, &rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{Name: string(initializer) + "-initializer"},
			Rules: []rbacv1.PolicyRule{{
				Verbs:         []string{"initialize"},
				Resources:     []string{"workspacetypes"},
				ResourceNames: []string{wst.Name},
				APIGroups:     []string{"tenancy.kcp.io"},
			}},
		}, metav1.CreateOptions{})
		if !errors.IsAlreadyExists(err) {
			require.NoError(c, err)
		}
		_, err = kubeClusterClient.Cluster(wsPath).RbacV1().ClusterRoleBindings().Create(ctx, &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: string(initializer) + "-initializer"},
			RoleRef: rbacv1.RoleRef{
				Kind:     "ClusterRole",
				APIGroup: "rbac.authorization.k8s.io",
				Name:     string(initializer) + "-initializer",
			},
			Subjects: []rbacv1.Subject{{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     "User",
				Name:     username,
			}},
		}, metav1.CreateOptions{})
		if !errors.IsAlreadyExists(err) {
			require.NoError(c, err)
		}
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	t.Log("Resolve the initializing VW URL on the workspace's shard")
	vwURLs := []string{}
	for _, vwURL := range wst.Status.VirtualWorkspaces {
		if strings.Contains(vwURL.URL, initializingworkspaces.VirtualWorkspaceName) {
			vwURLs = append(vwURLs, vwURL.URL)
		}
	}
	require.NotEmpty(t, vwURLs, "expected at least one initializing VW URL on the workspacetype")
	targetVwURL, found, err := framework.VirtualWorkspaceURL(ctx, sourceKcpClusterClient, ws, vwURLs)
	require.NoError(t, err)
	require.True(t, found)

	vwConfig := rest.AddUserAgent(rest.CopyConfig(sourceConfig), t.Name()+"-virtual")
	vwConfig.Host = targetVwURL
	vwUser1 := framework.StaticTokenUserConfig(username, vwConfig)
	user1Kube, err := kcpkubernetesclientset.NewForConfig(vwUser1)
	require.NoError(t, err)

	t.Log("In-scope: GET configmap through the VW succeeds (declared in initializerPermissions)")
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		cm, err := user1Kube.Cluster(wsClusterName.Path()).CoreV1().ConfigMaps(nsName).Get(ctx, cmName, metav1.GetOptions{})
		require.NoError(c, err)
		require.Equal(c, "value", cm.Data["key"])
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	t.Log("Out-of-scope: GET secret through the VW is rejected with 403 by the proxy's RBAC evaluator")
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		_, err := user1Kube.Cluster(wsClusterName.Path()).CoreV1().Secrets(nsName).Get(ctx, "out-of-scope", metav1.GetOptions{})
		require.Error(c, err)
		require.True(c, errors.IsForbidden(err), "expected forbidden, got: %v", err)
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	t.Log("Self-asserted synthetic groups against the front-proxy must be stripped (no privilege escalation)")
	// Build a config that talks to the front-proxy directly (not the VW), authenticating as
	// user-2 (no initialize verb) but asserting the synthetic group via Impersonate. The
	// front-proxy --authentication-drop-groups list strips system:kcp:initializer:*, so the
	// request must reach the shard *without* the synthetic group and be denied normally.
	directConfig := rest.AddUserAgent(rest.CopyConfig(sourceConfig), t.Name()+"-direct")
	directConfig = framework.StaticTokenUserConfig("user-2", directConfig)
	directConfig.Impersonate = rest.ImpersonationConfig{
		UserName: "user-2",
		// Forge the fully-qualified synthetic group: system:kcp:initializer:<wst-path>.
		Groups: []string{authorization.InitializerGroup(initializer)},
	}
	directKube, err := kcpkubernetesclientset.NewForConfig(directConfig)
	require.NoError(t, err)
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		_, err := directKube.Cluster(wsClusterName.Path()).CoreV1().ConfigMaps(nsName).List(ctx, metav1.ListOptions{})
		require.Error(c, err, "self-asserted synthetic group should not grant access")
		// After the front-proxy strips the synthetic group, user-2 has no access to the
		// workspace at all. The shard surfaces that as 403 (workspace_content_authorizer
		// denies) or 401 (impersonation rejected); NotFound is also accepted because in
		// some code paths a user with no workspace-content access sees the cluster URL
		// as non-existent rather than forbidden.
		require.True(c, errors.IsForbidden(err) || errors.IsUnauthorized(err) || errors.IsNotFound(err),
			"expected forbidden/unauthorized/notfound, got: %v", err)
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	t.Log("Remove our initializer through the VW so the workspace can finish initializing")
	user1Kcp, err := kcpclientset.NewForConfig(vwUser1)
	require.NoError(t, err)
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		lc, err := user1Kcp.Cluster(wsClusterName.Path()).CoreV1alpha1().LogicalClusters().Get(ctx, corev1alpha1.LogicalClusterName, metav1.GetOptions{})
		require.NoError(c, err)
		mod := lc.DeepCopy()
		mod.Status.Initializers = initialization.EnsureInitializerAbsent(initializer, mod.Status.Initializers)
		oldData, err := json.Marshal(corev1alpha1.LogicalCluster{Status: lc.Status})
		require.NoError(c, err)
		newData, err := json.Marshal(corev1alpha1.LogicalCluster{Status: mod.Status})
		require.NoError(c, err)
		patch, err := jsonpatch.CreateMergePatch(oldData, newData)
		require.NoError(c, err)
		_, err = user1Kcp.Cluster(wsClusterName.Path()).CoreV1alpha1().LogicalClusters().Patch(ctx, corev1alpha1.LogicalClusterName, types.MergePatchType, patch, metav1.PatchOptions{}, "status")
		require.NoError(c, err)
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	require.EventuallyWithT(t, func(c *assert.CollectT) {
		ws, err = sourceKcpClusterClient.TenancyV1alpha1().Cluster(wsPath).Workspaces().Get(ctx, ws.Name, metav1.GetOptions{})
		require.NoError(c, err)
		require.Equal(c, corev1alpha1.LogicalClusterPhaseReady, ws.Status.Phase)
	}, wait.ForeverTestTimeout, 100*time.Millisecond)
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
		t.Logf("Workspace %s (accessible via /clusters/%s) on %s shard is stuck in initializing", workspace.Name, workspace.Spec.Cluster, kcptesting.WorkspaceShardOrDie(t, kcpClient, &workspace).Name)
	}
	return true
}
