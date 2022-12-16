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
	"fmt"
	"math/rand"
	"sort"
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

	corev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/core/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy/initialization"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	tenancyv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
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
		GroupVersion: "tenancy.kcp.dev/v1alpha1",
		APIResources: []metav1.APIResource{
			{
				Kind:               "LogicalCluster",
				Name:               "logicalclusters",
				SingularName:       "logicalcluster",
				Categories:         []string{"kcp"},
				Verbs:              metav1.Verbs{"get", "list", "watch"},
				StorageVersionHash: discovery.StorageVersionHash("", "tenancy.kcp.dev", "v1alpha1", "LogicalCluster"),
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
	clusterName := framework.NewWorkspaceFixture(t, source, tenancyv1alpha1.RootCluster.Path())
	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	sourceConfig := source.BaseConfig(t)

	sourceKcpClusterClient, err := kcpclientset.NewForConfig(sourceConfig)
	require.NoError(t, err)

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(sourceConfig)
	require.NoError(t, err)

	framework.AdmitWorkspaceAccess(t, ctx, kubeClusterClient, clusterName.Path(), []string{"user-1"}, nil, false)

	// Create a Workspace that will not be Initializing and should not be shown in the virtual workspace
	framework.NewWorkspaceFixture(t, source, clusterName.Path())

	testLabelSelector := map[string]string{
		"internal.kcp.dev/e2e-test": t.Name(),
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
	workspacetypeExtensions := map[string]tenancyv1alpha1.WorkspaceTypesExtension{
		"alpha": {},
		"beta":  {},
		"gamma": {With: []tenancyv1alpha1.WorkspaceTypesReference{
			{Path: clusterName.String(), Name: tenancyv1alpha1.TypeName(workspacetypeNames["alpha"])},
			{Path: clusterName.String(), Name: tenancyv1alpha1.TypeName(workspacetypeNames["beta"])},
		}},
	}
	for _, name := range []string{
		"alpha",
		"beta",
		"gamma",
	} {
		cwt, err := sourceKcpClusterClient.TenancyV1alpha1().Cluster(clusterName.Path()).WorkspaceTypes().Create(ctx, &tenancyv1alpha1.WorkspaceType{
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
			return sourceKcpClusterClient.TenancyV1alpha1().Cluster(clusterName.Path()).WorkspaceTypes().Get(ctx, cwt.Name, metav1.GetOptions{})
		})
		workspacetypes[name] = cwt
	}

	t.Log("Wait for WorkspaceTypes to have their type extensions resolved")
	for _, name := range []string{
		"alpha",
		"beta",
		"gamma",
	} {
		cwtName := workspacetypes[name].Name
		framework.EventuallyReady(t, func() (conditions.Getter, error) {
			return sourceKcpClusterClient.TenancyV1alpha1().Cluster(clusterName.Path()).WorkspaceTypes().Get(ctx, cwtName, metav1.GetOptions{})
		}, "could not wait for readiness on WorkspaceType %s|%s", clusterName.String(), cwtName)
	}

	t.Log("Create workspaces using the new types, which will get stuck in initializing")
	var wsNames []string
	for _, workspaceType := range []string{
		"alpha",
		"beta",
		"gamma",
	} {
		var ws *tenancyv1beta1.Workspace
		require.Eventually(t, func() bool {
			ws, err = sourceKcpClusterClient.TenancyV1beta1().Cluster(clusterName.Path()).Workspaces().Create(ctx, workspaceForType(workspacetypes[workspaceType], testLabelSelector), metav1.CreateOptions{})
			return err == nil
		}, wait.ForeverTestTimeout, time.Millisecond*100)
		source.Artifact(t, func() (runtime.Object, error) {
			return sourceKcpClusterClient.TenancyV1beta1().Cluster(clusterName.Path()).Workspaces().Get(ctx, ws.Name, metav1.GetOptions{})
		})
		wsNames = append(wsNames, ws.Name)
	}

	t.Log("Wait for workspaces to get stuck in initializing")
	var workspaces *tenancyv1beta1.WorkspaceList
	require.Eventually(t, func() bool {
		workspaces, err = sourceKcpClusterClient.TenancyV1beta1().Cluster(clusterName.Path()).Workspaces().List(ctx, metav1.ListOptions{
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
		return workspacesStuckInInitializing(t, workspaces.Items...)
	}, wait.ForeverTestTimeout, 100*time.Millisecond)
	workspacesByType := map[string]tenancyv1beta1.Workspace{}
	for i := range workspaces.Items {
		workspacesByType[tenancyv1alpha1.ObjectName(workspaces.Items[i].Spec.Type.Name)] = workspaces.Items[i]
	}

	t.Log("Wait for cluster workspace types to have virtual workspace URLs published")
	for _, initializer := range []string{
		"alpha",
		"beta",
		"gamma",
	} {
		var cwt *tenancyv1alpha1.WorkspaceType
		require.Eventually(t, func() bool {
			cwt, err = sourceKcpClusterClient.TenancyV1alpha1().Cluster(clusterName.Path()).WorkspaceTypes().Get(ctx, workspacetypes[initializer].Name, metav1.GetOptions{})
			require.NoError(t, err)
			if len(cwt.Status.VirtualWorkspaces) == 0 {
				t.Logf("cluster workspace type %q|%q does not have virtual workspace URLs published yet", logicalcluster.From(cwt), cwt.Name)
				return false
			}
			return true
		}, wait.ForeverTestTimeout, 100*time.Millisecond)
		workspacetypes[initializer] = cwt
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
		virtualWorkspaceConfig := rest.AddUserAgent(rest.CopyConfig(sourceConfig), t.Name()+"-virtual")
		virtualWorkspaceConfig.Host = workspacetypes[initializer].Status.VirtualWorkspaces[0].URL
		virtualKcpClusterClient, err := kcpclientset.NewForConfig(framework.UserConfig("user-1", virtualWorkspaceConfig))
		require.NoError(t, err)
		virtualKubeClusterClient, err := kcpkubernetesclientset.NewForConfig(framework.UserConfig("user-1", virtualWorkspaceConfig))
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
			return err == nil, fmt.Sprintf("%v", err)
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
		cwt := workspacetypes[initializer]
		role, err := kubeClusterClient.Cluster(clusterName.Path()).RbacV1().ClusterRoles().Create(ctx, &rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: string(initialization.InitializerForType(cwt)) + "-initializer",
			},
			Rules: []rbacv1.PolicyRule{
				{
					Verbs:         []string{"initialize"},
					Resources:     []string{"workspacetypes"},
					ResourceNames: []string{cwt.Name},
					APIGroups:     []string{"tenancy.kcp.dev"},
				},
			},
		}, metav1.CreateOptions{})
		require.NoError(t, err)
		source.Artifact(t, func() (runtime.Object, error) {
			return kubeClusterClient.Cluster(clusterName.Path()).RbacV1().ClusterRoles().Get(ctx, role.Name, metav1.GetOptions{})
		})
		binding, err := kubeClusterClient.Cluster(clusterName.Path()).RbacV1().ClusterRoleBindings().Create(ctx, &rbacv1.ClusterRoleBinding{
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
			return kubeClusterClient.Cluster(clusterName.Path()).RbacV1().ClusterRoleBindings().Get(ctx, binding.Name, metav1.GetOptions{})
		})
	}

	t.Log("Ensure that LIST calls through the virtual workspace eventually show the correct values")
	for _, wsName := range wsNames {
		require.Eventually(t, func() bool {
			_, err := sourceKcpClusterClient.CoreV1alpha1().Cluster(clusterName.Path().Join(wsName)).LogicalClusters().Get(ctx, corev1alpha1.LogicalClusterName, metav1.GetOptions{})
			require.True(t, err == nil || errors.IsForbidden(err), "got %#v error getting logicalcluster %q, expected unauthorized or success", err, clusterName.Path().Join(wsName))
			return err == nil
		}, wait.ForeverTestTimeout, 100*time.Millisecond)
	}

	for initializer, expected := range map[string][]tenancyv1beta1.Workspace{
		"alpha": {workspacesByType[workspacetypeNames["alpha"]], workspacesByType[workspacetypeNames["gamma"]]},
		"beta":  {workspacesByType[workspacetypeNames["beta"]], workspacesByType[workspacetypeNames["gamma"]]},
		"gamma": {workspacesByType[workspacetypeNames["gamma"]]},
	} {
		sort.Slice(expected, func(i, j int) bool {
			return expected[i].UID < expected[j].UID
		})
		var actual *corev1alpha1.LogicalClusterList
		require.Eventually(t, func() bool {
			actual, err = user1VwKcpClusterClients[initializer].CoreV1alpha1().LogicalClusters().List(ctx, metav1.ListOptions{}) // no list options, all filtering is implicit
			if err != nil && !errors.IsForbidden(err) {
				require.NoError(t, err)
			}
			return err == nil
		}, wait.ForeverTestTimeout, 100*time.Millisecond)
		lclusters, expectedClusters := sets.NewString(), sets.NewString()
		for i := range actual.Items {
			lclusters.Insert(logicalcluster.From(&actual.Items[i]).String())
		}
		for i := range expected {
			expectedClusters.Insert(expected[i].Status.Cluster)
		}
		sort.Slice(actual.Items, func(i, j int) bool {
			return actual.Items[i].UID < actual.Items[j].UID
		})
		require.Equal(t, expectedClusters.List(), lclusters.List(), "unexpected clusters for initializer %q", initializer)
	}

	t.Log("Start WATCH streams to confirm behavior on changes")
	watchers := map[string]watch.Interface{}
	for _, initializer := range []string{
		"alpha",
		"beta",
		"gamma",
	} {
		watcher, err := user1VwKcpClusterClients[initializer].CoreV1alpha1().LogicalClusters().Watch(ctx, metav1.ListOptions{
			ResourceVersion: workspaces.ResourceVersion,
		})
		require.NoError(t, err)
		watchers[initializer] = watcher
	}

	t.Log("Adding a new workspace that the watchers should see")
	ws, err := sourceKcpClusterClient.TenancyV1beta1().Cluster(clusterName.Path()).Workspaces().Create(ctx, workspaceForType(workspacetypes["gamma"], testLabelSelector), metav1.CreateOptions{})
	require.NoError(t, err)
	source.Artifact(t, func() (runtime.Object, error) {
		return sourceKcpClusterClient.TenancyV1beta1().Cluster(clusterName.Path()).Workspaces().Get(ctx, ws.Name, metav1.GetOptions{})
	})
	require.Eventually(t, func() bool {
		workspace, err := sourceKcpClusterClient.TenancyV1beta1().Cluster(clusterName.Path()).Workspaces().Get(ctx, ws.Name, metav1.GetOptions{})
		if err != nil {
			t.Logf("error listing workspaces: %v", err)
			return false
		}
		return workspacesStuckInInitializing(t, *workspace)
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	ws, err = sourceKcpClusterClient.TenancyV1beta1().Cluster(clusterName.Path()).Workspaces().Get(ctx, ws.Name, metav1.GetOptions{})
	require.NoError(t, err)
	wsClusterName := logicalcluster.Name(ws.Status.Cluster)

	t.Logf("Waiting for watchers to see the logicalcluster in %s for workspace %s", ws.Status.Cluster, ws.Name)
	for initializer, watcher := range watchers {
		for {
			select {
			case evt := <-watcher.ResultChan():
				// there might be other actors doing who-knows-what on the workspaces, so we need to specifically
				// look for the first event *relating to the new workspace* that we get
				if logicalcluster.From(evt.Object.(metav1.Object)).String() != ws.Status.Cluster {
					continue
				}
				require.Equal(t, evt.Type, watch.Added)
			case <-time.Tick(wait.ForeverTestTimeout):
				t.Fatalf("never saw a watche event for the %s initializer", initializer)
			}
			break
		}
	}

	t.Log("Access an object inside of the workspace")
	for _, initializer := range []string{
		"alpha",
		"beta",
		"gamma",
	} {
		coreClusterClient := user1VwKubeClusterClients[initializer]

		nsName := "testing"
		_, err := coreClusterClient.Cluster(wsClusterName.Path()).CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}}, metav1.CreateOptions{})
		if err != nil && !errors.IsAlreadyExists(err) {
			require.NoError(t, err)
		}

		labelSelector := map[string]string{
			"internal.kcp.dev/test-initializer": initializer,
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
	}

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
	for _, initializer := range []string{
		"alpha",
		"beta",
		"gamma",
	} {
		clusterClient := user1VwKcpClusterClients[initializer].CoreV1alpha1().LogicalClusters()
		this, err := clusterClient.Cluster(wsClusterName.Path()).Get(ctx, corev1alpha1.LogicalClusterName, metav1.GetOptions{})
		require.NoError(t, err)

		t.Logf("Attempt to do something more than just removing our initializer %q, get denied", initializer)
		patchBytes := patchBytesFor(this, func(workspace *corev1alpha1.LogicalCluster) {
			workspace.Status.Initializers = []corev1alpha1.LogicalClusterInitializer{"wrong"}
		})
		_, err = clusterClient.Cluster(wsClusterName.Path()).Patch(ctx, corev1alpha1.LogicalClusterName, types.MergePatchType, patchBytes, metav1.PatchOptions{}, "status")
		if !errors.IsInvalid(err) {
			t.Fatalf("got %#v error from patch, expected invalid", err)
		}

		t.Logf("Remove just our initializer %q", initializer)
		patchBytes = patchBytesFor(this, func(workspace *corev1alpha1.LogicalCluster) {
			workspace.Status.Initializers = initialization.EnsureInitializerAbsent(initialization.InitializerForType(workspacetypes[initializer]), workspace.Status.Initializers)
		})
		_, err = clusterClient.Cluster(wsClusterName.Path()).Patch(ctx, corev1alpha1.LogicalClusterName, types.MergePatchType, patchBytes, metav1.PatchOptions{}, "status")
		require.NoError(t, err)
	}

	for initializer, watcher := range watchers {
		for {
			select {
			case evt := <-watcher.ResultChan():
				if evt.Type == watch.Modified {
					continue // we will see some modification events from the above patch and the resulting controller reactions
				}
				// there might be other actors doing who-knows-what on the workspaces, so we need to specifically
				// look for the first event *relating to the new workspace* that we get
				if logicalcluster.From(evt.Object.(metav1.Object)).String() != ws.Status.Cluster {
					continue
				}
				require.Equal(t, evt.Type, watch.Deleted)
			case <-time.Tick(wait.ForeverTestTimeout):
				t.Fatalf("never saw a watch event for the %s initializer", initializer)
			}
			break
		}
	}

	t.Log("Ensure accessing objects in the workspace is forbidden now that it is not initializing")
	for _, initializer := range []string{
		"alpha",
		"beta",
		"gamma",
	} {
		kubeClusterClient := user1VwKubeClusterClients[initializer].Cluster(wsClusterName.Path()).CoreV1().ConfigMaps("testing")
		_, err := kubeClusterClient.List(ctx, metav1.ListOptions{})
		if !errors.IsForbidden(err) {
			t.Fatalf("got %#v error from initial list, expected unauthorized", err)
		}
	}

	t.Log("Ensure get workspace requests are 404 now that it is not initializing")
	for _, initializer := range []string{
		"alpha",
		"beta",
		"gamma",
	} {
		wsClient := user1VwKcpClusterClients[initializer].CoreV1alpha1().LogicalClusters()
		_, err := wsClient.Cluster(wsClusterName.Path()).Get(ctx, corev1alpha1.LogicalClusterName, metav1.GetOptions{})
		if !errors.IsNotFound(err) {
			t.Fatalf("got error from get, expected not found: %v", err)
		}
	}
}

func workspaceForType(workspaceType *tenancyv1alpha1.WorkspaceType, testLabelSelector map[string]string) *tenancyv1beta1.Workspace {
	return &tenancyv1beta1.Workspace{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "e2e-workspace-",
			Labels:       testLabelSelector,
		},
		Spec: tenancyv1beta1.WorkspaceSpec{
			Type: tenancyv1beta1.WorkspaceTypeReference{
				Name: tenancyv1alpha1.WorkspaceTypesName(workspaceType.Name),
				Path: logicalcluster.From(workspaceType).String(),
			},
		},
	}
}

func workspacesStuckInInitializing(t *testing.T, workspaces ...tenancyv1beta1.Workspace) bool {
	for _, workspace := range workspaces {
		if workspace.Status.Phase != corev1alpha1.LogicalClusterPhaseInitializing {
			t.Logf("workspace %s is in %s, not %s", workspace.Name, workspace.Status.Phase, corev1alpha1.LogicalClusterPhaseInitializing)
			return false
		}
		if len(workspace.Status.Initializers) == 0 {
			t.Logf("workspace %s has no initializers", workspace.Name)
			return false
		}
	}
	return true
}
