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
	"testing"
	"time"

	jsonpatch "github.com/evanphx/json-patch"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/kcp-dev/logicalcluster"
	"github.com/stretchr/testify/require"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/endpoints/discovery"
	clientgodiscovery "k8s.io/client-go/discovery"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/kcp-dev/kcp/pkg/apis/tenancy/initialization"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	tenancyv1alpha1client "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/typed/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestInitializingWorkspacesVirtualWorkspaceDiscovery(t *testing.T) {
	t.Parallel()

	source := framework.SharedKcpServer(t)

	rawConfig, err := source.RawConfig()
	require.NoError(t, err)

	adminCluster := rawConfig.Clusters["system:admin"]
	adminContext := rawConfig.Contexts["system:admin"]
	virtualWorkspaceRawConfig := rawConfig.DeepCopy()

	virtualWorkspaceRawConfig.Clusters["virtual"] = adminCluster.DeepCopy()
	virtualWorkspaceRawConfig.Clusters["virtual"].Server = adminCluster.Server + "/services/initializingworkspaces/whatever"
	virtualWorkspaceRawConfig.Contexts["virtual"] = adminContext.DeepCopy()
	virtualWorkspaceRawConfig.Contexts["virtual"].Cluster = "virtual"

	virtualWorkspaceConfig, err := clientcmd.NewNonInteractiveClientConfig(*virtualWorkspaceRawConfig, "virtual", nil, nil).ClientConfig()
	require.NoError(t, err)

	virtualWorkspaceDiscoveryClient, err := clientgodiscovery.NewDiscoveryClientForConfig(virtualWorkspaceConfig)
	require.NoError(t, err)
	_, apiResourceLists, err := virtualWorkspaceDiscoveryClient.WithCluster(logicalcluster.Wildcard).ServerGroupsAndResources()
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
				Kind:               "ClusterWorkspace",
				Name:               "clusterworkspaces",
				SingularName:       "clusterworkspace",
				Categories:         []string{"kcp"},
				Verbs:              metav1.Verbs{"list", "watch"},
				StorageVersionHash: discovery.StorageVersionHash("", "tenancy.kcp.dev", "v1alpha1", "ClusterWorkspace"),
			},
		},
	}}, apiResourceLists))
}

func TestInitializingWorkspacesVirtualWorkspaceAccess(t *testing.T) {
	t.Parallel()

	source := framework.SharedKcpServer(t)
	clusterName := framework.NewWorkspaceFixture(t, source, tenancyv1alpha1.RootCluster, "Universal")
	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	sourceConfig := source.DefaultConfig(t)

	sourceKcpClusterClient, err := kcpclient.NewClusterForConfig(sourceConfig)
	require.NoError(t, err)

	kubeClusterClient, err := kubernetes.NewClusterForConfig(sourceConfig)
	require.NoError(t, err)

	framework.AdmitWorkspaceAccess(t, ctx, kubeClusterClient, clusterName, []string{"user-1"}, nil, []string{"access"})

	// Create a Workspace that will not be Initializing and should not be shown in the virtual workspace
	framework.NewWorkspaceFixture(t, source, clusterName, "Universal")

	sourceKcpTenancyClient := sourceKcpClusterClient.Cluster(clusterName).TenancyV1alpha1()

	testLabelSelector := map[string]string{
		"internal.kcp.dev/e2e-test": t.Name(),
	}

	t.Log("Create workspace types that add initializers")
	// ClusterWorkspaceTypes and the initializer names will have to be globally unique, so we add some suffix here
	// to ensure that parallel test runs do not impact our ability to verify this behavior. ClusterWorkspaceType names
	// are pretty locked down, using this regex: '^[A-Z][a-zA-Z0-9]+$' - so we just add some simple lowercase suffix.
	const characters = "abcdefghijklmnopqrstuvwxyz"
	suffix := func() string {
		b := make([]byte, 10)
		for i := range b {
			b[i] = characters[rand.Intn(len(characters))]
		}
		return string(b)
	}
	clusterWorkspaceTypeNames := map[string]string{}
	for _, name := range []string{
		"alpha",
	} {
		clusterWorkspaceTypeNames[name] = name + suffix()
	}
	clusterWorkspaceInitializerNames := map[string]tenancyv1alpha1.ClusterWorkspaceInitializer{}
	for _, name := range []string{
		"alpha",
	} {
		clusterWorkspaceInitializerNames[name] = tenancyv1alpha1.ClusterWorkspaceInitializer(name + "-" + suffix())
	}

	clusterWorkspaceTypes := map[string]*tenancyv1alpha1.ClusterWorkspaceType{}
	for _, name := range []string{
		"alpha",
	} {
		cwt, err := sourceKcpTenancyClient.ClusterWorkspaceTypes().Create(ctx, &tenancyv1alpha1.ClusterWorkspaceType{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterWorkspaceTypeNames[name],
			},
			Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
				Initializer:                 true,
				AllowedParentWorkspaceTypes: []tenancyv1alpha1.ClusterWorkspaceTypeName{tenancyv1alpha1.AnyWorkspaceType},
			},
		}, metav1.CreateOptions{})
		require.NoError(t, err)
		source.Artifact(t, func() (runtime.Object, error) {
			return sourceKcpTenancyClient.ClusterWorkspaceTypes().Get(ctx, cwt.Name, metav1.GetOptions{})
		})
		clusterWorkspaceTypes[name] = cwt
	}

	t.Log("Create workspaces that using the new types, which will get stuck in initializing")
	for _, workspaceType := range []string{
		"alpha",
	} {
		var ws *tenancyv1alpha1.ClusterWorkspace
		require.Eventually(t, func() bool {
			ws, err = sourceKcpTenancyClient.ClusterWorkspaces().Create(ctx, workspaceForType(clusterWorkspaceTypes[workspaceType], testLabelSelector), metav1.CreateOptions{})
			return err == nil
		}, wait.ForeverTestTimeout, time.Millisecond*100)
		source.Artifact(t, func() (runtime.Object, error) {
			return sourceKcpTenancyClient.ClusterWorkspaces().Get(ctx, ws.Name, metav1.GetOptions{})
		})
	}

	t.Log("Wait for workspaces to get stuck in initializing")
	require.Eventually(t, func() bool {
		workspaces, err := sourceKcpTenancyClient.ClusterWorkspaces().List(ctx, metav1.ListOptions{
			LabelSelector: labels.SelectorFromSet(testLabelSelector).String(),
		})
		if err != nil {
			t.Logf("error listing workspaces: %v", err)
			return false
		}
		if len(workspaces.Items) != 1 {
			t.Logf("got %d workspaces, expected 3", len(workspaces.Items))
			return false
		}
		return workspacesStuckInInitializing(t, workspaces.Items...)
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	t.Log("Wait for cluster workspace types to have virtual workspace URLs published")
	for _, initializer := range []string{
		"alpha",
	} {
		var cwt *tenancyv1alpha1.ClusterWorkspaceType
		require.Eventually(t, func() bool {
			cwt, err = sourceKcpTenancyClient.ClusterWorkspaceTypes().Get(ctx, clusterWorkspaceTypes[initializer].Name, metav1.GetOptions{})
			require.NoError(t, err)
			if len(cwt.Status.VirtualWorkspaces) == 0 {
				t.Logf("cluster workspace type %q|%q does not have virtual workspace URLs published yet", logicalcluster.From(cwt), cwt.Name)
				return false
			}
			return true
		}, wait.ForeverTestTimeout, 100*time.Millisecond)
		clusterWorkspaceTypes[initializer] = cwt
	}

	t.Log("Create clients through the virtual workspace")
	clients := map[string]tenancyv1alpha1client.ClusterWorkspaceInterface{}
	for _, initializer := range []string{
		"alpha",
	} {
		virtualWorkspaceConfig := rest.CopyConfig(sourceConfig)
		virtualWorkspaceConfig.Host = clusterWorkspaceTypes[initializer].Status.VirtualWorkspaces[0].URL
		virtualKcpClusterClient, err := kcpclient.NewClusterForConfig(userConfig("user-1", virtualWorkspaceConfig))
		require.NoError(t, err)
		clients[initializer] = virtualKcpClusterClient.Cluster(logicalcluster.Wildcard).TenancyV1alpha1().ClusterWorkspaces()
	}

	t.Log("Ensure that LIST calls through the virtual workspace fail authorization")
	for _, initializer := range []string{
		"alpha",
	} {
		_, err := clients[initializer].List(ctx, metav1.ListOptions{})
		if !errors.IsForbidden(err) {
			t.Fatalf("got %#v error from initial list, expected unauthorized", err)
		}
	}

	t.Log("Set up RBAC to allow future calls to succeed")
	for _, initializer := range []string{
		"alpha",
	} {
		cwt := clusterWorkspaceTypes[initializer]
		role, err := kubeClusterClient.Cluster(clusterName).RbacV1().ClusterRoles().Create(ctx, &rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: string(initialization.InitializerForType(cwt)) + "-initializer",
			},
			Rules: []rbacv1.PolicyRule{
				{
					Verbs:         []string{"initialize"},
					Resources:     []string{"clusterworkspacetypes"},
					ResourceNames: []string{cwt.Name},
					APIGroups:     []string{"tenancy.kcp.dev"},
				},
			},
		}, metav1.CreateOptions{})
		require.NoError(t, err)
		source.Artifact(t, func() (runtime.Object, error) {
			return kubeClusterClient.Cluster(clusterName).RbacV1().ClusterRoles().Get(ctx, role.Name, metav1.GetOptions{})
		})
		binding, err := kubeClusterClient.Cluster(clusterName).RbacV1().ClusterRoleBindings().Create(ctx, &rbacv1.ClusterRoleBinding{
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
			return kubeClusterClient.Cluster(clusterName).RbacV1().ClusterRoleBindings().Get(ctx, binding.Name, metav1.GetOptions{})
		})
	}

	t.Log("Ensure that LIST calls through the virtual workspace eventually show the correct values")
	var workspaces *tenancyv1alpha1.ClusterWorkspaceList
	require.Eventually(t, func() bool {
		workspaces, err = sourceKcpTenancyClient.ClusterWorkspaces().List(ctx, metav1.ListOptions{
			LabelSelector: labels.SelectorFromSet(testLabelSelector).String(),
		})
		require.True(t, err == nil || errors.IsForbidden(err), "got %#v error from initial list, expected unauthorized or success", err)
		return err == nil
	}, wait.ForeverTestTimeout, 100*time.Millisecond)
	workspacesByType := map[string]tenancyv1alpha1.ClusterWorkspace{}
	for i := range workspaces.Items {
		workspacesByType[tenancyv1alpha1.ObjectName(workspaces.Items[i].Spec.Type.Name)] = workspaces.Items[i]
	}

	for initializer, expected := range map[string][]tenancyv1alpha1.ClusterWorkspace{
		"alpha": {workspacesByType[clusterWorkspaceTypeNames["alpha"]]},
	} {
		sort.Slice(expected, func(i, j int) bool {
			return expected[i].UID < expected[j].UID
		})
		var actual *tenancyv1alpha1.ClusterWorkspaceList
		require.Eventually(t, func() bool {
			actual, err = clients[initializer].List(ctx, metav1.ListOptions{}) // no list options, all filtering is implicit
			if err != nil && !errors.IsForbidden(err) {
				require.NoError(t, err)
			}
			return err == nil
		}, wait.ForeverTestTimeout, 100*time.Millisecond)
		sort.Slice(actual.Items, func(i, j int) bool {
			return actual.Items[i].UID < actual.Items[j].UID
		})
		require.Empty(t, cmp.Diff(expected, actual.Items, cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion", "ManagedFields", "Finalizers")), "cluster workspace list for initializer %s incorrect", initializer)
	}

	t.Log("Start WATCH streams to confirm behavior on changes")
	watchers := map[string]watch.Interface{}
	for _, initializer := range []string{
		"alpha",
	} {
		watcher, err := clients[initializer].Watch(ctx, metav1.ListOptions{
			ResourceVersion: workspaces.ResourceVersion,
		})
		require.NoError(t, err)
		watchers[initializer] = watcher
	}

	t.Log("Adding a new workspace that the watchers should see")
	ws, err := sourceKcpTenancyClient.ClusterWorkspaces().Create(ctx, workspaceForType(clusterWorkspaceTypes["alpha"], testLabelSelector), metav1.CreateOptions{})
	require.NoError(t, err)
	source.Artifact(t, func() (runtime.Object, error) {
		return sourceKcpTenancyClient.ClusterWorkspaces().Get(ctx, ws.Name, metav1.GetOptions{})
	})
	require.Eventually(t, func() bool {
		workspace, err := sourceKcpTenancyClient.ClusterWorkspaces().Get(ctx, ws.Name, metav1.GetOptions{})
		if err != nil {
			t.Logf("error listing workspaces: %v", err)
			return false
		}
		return workspacesStuckInInitializing(t, *workspace)
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	ws, err = sourceKcpTenancyClient.ClusterWorkspaces().Get(ctx, ws.Name, metav1.GetOptions{})
	require.NoError(t, err)

	for initializer, watcher := range watchers {
		for {
			select {
			case evt := <-watcher.ResultChan():
				// there might be other actors doing who-knows-what on the workspaces, so we need to specifically
				// look for the first event *relating to the new workspace* that we get
				if evt.Object.(metav1.Object).GetUID() != ws.UID {
					continue
				}
				require.Equal(t, evt.Type, watch.Added)
				require.Equal(t, evt.Object.(metav1.Object).GetUID(), ws.UID, "got incorrect object in watch stream for initializer %s", initializer)
			case <-time.Tick(wait.ForeverTestTimeout):
				t.Fatalf("never saw a watche event for the %s initializer", initializer)
			}
			break
		}
	}

	t.Log("Transitioning the new workspace out of initializing")
	previous := ws.DeepCopy()
	oldData, err := json.Marshal(tenancyv1alpha1.ClusterWorkspace{
		Status: previous.Status,
	})
	require.NoError(t, err)

	obj := ws.DeepCopy()
	obj.Status.Initializers = []tenancyv1alpha1.ClusterWorkspaceInitializer{}
	obj.Status.Phase = tenancyv1alpha1.ClusterWorkspacePhaseReady
	newData, err := json.Marshal(tenancyv1alpha1.ClusterWorkspace{
		Status: obj.Status,
	})
	require.NoError(t, err)

	patchBytes, err := jsonpatch.CreateMergePatch(oldData, newData)
	require.NoError(t, err)
	ws, err = sourceKcpTenancyClient.ClusterWorkspaces().Patch(ctx, obj.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{}, "status")
	require.NoError(t, err)

	for initializer, watcher := range watchers {
		for {
			select {
			case evt := <-watcher.ResultChan():
				if evt.Type == watch.Modified {
					ws = evt.Object.(*tenancyv1alpha1.ClusterWorkspace)
					continue // we will see some modification events from the above patch and the resulting controller reactions
				}
				// there might be other actors doing who-knows-what on the workspaces, so we need to specifically
				// look for the first event *relating to the new workspace* that we get
				if evt.Object.(metav1.Object).GetUID() != ws.UID {
					continue
				}
				require.Equal(t, evt.Type, watch.Deleted)
				require.Equal(t, evt.Object.(metav1.Object).GetUID(), ws.UID, "got incorrect object in watch stream for initializer %s", initializer)
			case <-time.Tick(wait.ForeverTestTimeout):
				t.Fatalf("never saw a watch event for the %s initializer", initializer)
			}
			break
		}
	}
}

func workspaceForType(workspaceType *tenancyv1alpha1.ClusterWorkspaceType, testLabelSelector map[string]string) *tenancyv1alpha1.ClusterWorkspace {
	return &tenancyv1alpha1.ClusterWorkspace{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "e2e-workspace-",
			Labels:       testLabelSelector,
		},
		Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
			Type: tenancyv1alpha1.ReferenceFor(workspaceType),
		},
	}
}

func workspacesStuckInInitializing(t *testing.T, workspaces ...tenancyv1alpha1.ClusterWorkspace) bool {
	for _, workspace := range workspaces {
		if workspace.Status.Phase != tenancyv1alpha1.ClusterWorkspacePhaseInitializing {
			t.Logf("workspace %s is in %s, not %s", workspace.Name, workspace.Status.Phase, tenancyv1alpha1.ClusterWorkspacePhaseInitializing)
			return false
		}
		if len(workspace.Status.Initializers) == 0 {
			t.Logf("workspace %s has no initializers", workspace.Name)
			return false
		}
		if !workspaceLabelsUpToDate(t, workspace) {
			return false
		}
	}
	return true
}

// this is really an implementation detail of the virtual workspace, but since we have a couple of moving pieces
// we do ultimately need to wait for labels to propagate before checking anything else, or the VW will not work
func workspaceLabelsUpToDate(t *testing.T, workspace tenancyv1alpha1.ClusterWorkspace) bool {
	if workspace.ObjectMeta.Labels[tenancyv1alpha1.ClusterWorkspacePhaseLabel] != string(tenancyv1alpha1.ClusterWorkspacePhaseInitializing) {
		t.Logf("workspace %s phase label is not updated yet", workspace.Name)
		return false
	}
	for _, initializer := range workspace.Status.Initializers {
		key, value := initialization.InitializerToLabel(initializer)
		if got, exists := workspace.ObjectMeta.Labels[key]; !exists || got != value {
			t.Logf("workspace %s initializer labels are not updated yet", workspace.Name)
			return false
		}
	}
	return true
}

func userConfig(username string, cfg *rest.Config) *rest.Config {
	cfgCopy := rest.CopyConfig(cfg)
	cfgCopy.BearerToken = username + "-token"
	return cfgCopy
}
