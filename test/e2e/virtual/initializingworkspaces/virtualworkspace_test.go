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
	"strings"
	"testing"
	"time"

	jsonpatch "github.com/evanphx/json-patch"
	"github.com/google/go-cmp/cmp"
	"github.com/kcp-dev/logicalcluster"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/endpoints/discovery"
	clientgodiscovery "k8s.io/client-go/discovery"
	"k8s.io/client-go/tools/clientcmd"

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
	orgClusterName := framework.NewOrganizationFixture(t, source)
	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	sourceConfig := source.DefaultConfig(t)
	rawConfig, err := source.RawConfig()
	require.NoError(t, err)

	kcpClusterClient, err := kcpclient.NewClusterForConfig(sourceConfig)
	require.NoError(t, err)

	// Create a Workspace that will not be Initializing and should not be shown in the virtual workspace
	framework.NewWorkspaceFixture(t, source, orgClusterName, "Universal")

	const characters = "abcdefghijklmnopqrstuvwxyz"
	suffix := func() string {
		b := make([]byte, 10)
		for i := range b {
			b[i] = characters[rand.Intn(len(characters))]
		}
		return string(b)
	}
	uniqueSuffix := suffix()

	t.Log("Create workspace types that add initializers")
	// ClusterWorkspaceTypes and the initializer names will have to be globally unique, so we add some suffix here
	// to ensure that parallel test runs do not impact our ability to verify this behavior. ClusterWorkspaceType names
	// are pretty locked down, using this regex: '^[A-Z][a-zA-Z0-9]+$' - so we just add some simple lowercase suffix.
	alpha := tenancyv1alpha1.ClusterWorkspaceInitializer(fmt.Sprintf("alpha%s", uniqueSuffix))
	beta := tenancyv1alpha1.ClusterWorkspaceInitializer(fmt.Sprintf("beta%s", uniqueSuffix))
	gamma := tenancyv1alpha1.ClusterWorkspaceInitializer(fmt.Sprintf("gamma%s", uniqueSuffix))
	for name, initializers := range map[tenancyv1alpha1.ClusterWorkspaceInitializer][]tenancyv1alpha1.ClusterWorkspaceInitializer{
		alpha: {alpha},
		beta:  {beta},
		gamma: {alpha, beta},
	} {
		_, err = kcpClusterClient.Cluster(orgClusterName).TenancyV1alpha1().ClusterWorkspaceTypes().Create(ctx, &tenancyv1alpha1.ClusterWorkspaceType{
			ObjectMeta: metav1.ObjectMeta{
				Name: string(name),
			},
			Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
				Initializers: initializers,
			},
		}, metav1.CreateOptions{})
		require.NoError(t, err)
	}

	t.Log("Create workspaces that using the new types, which will get stuck in initializing")
	for _, initializer := range []tenancyv1alpha1.ClusterWorkspaceInitializer{alpha, beta, gamma} {
		_, err := kcpClusterClient.Cluster(orgClusterName).TenancyV1alpha1().ClusterWorkspaces().Create(ctx, workspaceForType(string(initializer)), metav1.CreateOptions{})
		require.NoError(t, err)
	}

	t.Log("Wait for workspaces to get stuck in initializing")
	require.Eventually(t, func() bool {
		workspaces, err := kcpClusterClient.Cluster(orgClusterName).TenancyV1alpha1().ClusterWorkspaces().List(ctx, metav1.ListOptions{})
		require.NoError(t, err)
		return workspacesStuckInInitializing(t, workspaces.Items...) == 3
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	t.Log("Create clients through the virtual workspace")
	adminCluster := rawConfig.Clusters["system:admin"]
	adminContext := rawConfig.Contexts["system:admin"]
	virtualWorkspaceRawConfig := rawConfig.DeepCopy()

	clients := map[tenancyv1alpha1.ClusterWorkspaceInitializer]tenancyv1alpha1client.ClusterWorkspaceInterface{}
	for _, initializerName := range []string{string(alpha), string(beta)} {
		virtualWorkspaceRawConfig.Clusters[initializerName] = adminCluster.DeepCopy()
		virtualWorkspaceRawConfig.Clusters[initializerName].Server = adminCluster.Server + "/services/initializingworkspaces/" + initializerName
		virtualWorkspaceRawConfig.Contexts[initializerName] = adminContext.DeepCopy()
		virtualWorkspaceRawConfig.Contexts[initializerName].Cluster = initializerName
		virtualWorkspaceConfig, err := clientcmd.NewNonInteractiveClientConfig(*virtualWorkspaceRawConfig, initializerName, nil, nil).ClientConfig()
		require.NoError(t, err)
		virtualKcpClusterClient, err := kcpclient.NewClusterForConfig(virtualWorkspaceConfig)
		require.NoError(t, err)
		clients[tenancyv1alpha1.ClusterWorkspaceInitializer(initializerName)] = virtualKcpClusterClient.Cluster(logicalcluster.Wildcard).TenancyV1alpha1().ClusterWorkspaces()
	}

	t.Log("Ensure that LIST calls through the virtual workspace show the correct values")
	workspaces, err := kcpClusterClient.Cluster(orgClusterName).TenancyV1alpha1().ClusterWorkspaces().List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	workspacesByType := map[string]tenancyv1alpha1.ClusterWorkspace{}
	for i := range workspaces.Items {
		workspacesByType[strings.ToLower(workspaces.Items[i].Spec.Type)] = workspaces.Items[i]
	}

	for initializer, expected := range map[tenancyv1alpha1.ClusterWorkspaceInitializer][]tenancyv1alpha1.ClusterWorkspace{
		alpha: {workspacesByType[string(alpha)], workspacesByType[string(gamma)]},
		beta:  {workspacesByType[string(beta)], workspacesByType[string(gamma)]},
	} {
		sort.Slice(expected, func(i, j int) bool {
			return expected[i].UID < expected[j].UID
		})
		actual, err := clients[initializer].List(ctx, metav1.ListOptions{}) // no list options, all filtering is implicit
		require.NoError(t, err)
		sort.Slice(actual.Items, func(i, j int) bool {
			return actual.Items[i].UID < actual.Items[j].UID
		})
		require.Empty(t, cmp.Diff(pruneVolatileFields(expected), pruneVolatileFields(actual.Items)), "cluster workspace list for initializer %s incorrect", initializer)
	}

	t.Log("Start WATCH streams to confirm behavior on changes")
	watchers := map[string]watch.Interface{}
	for _, initializer := range []tenancyv1alpha1.ClusterWorkspaceInitializer{alpha, beta} {
		watcher, err := clients[initializer].Watch(ctx, metav1.ListOptions{
			ResourceVersion: workspaces.ResourceVersion,
		})
		require.NoError(t, err)
		watchers[string(initializer)] = watcher
	}

	t.Log("Adding a new workspace that both watchers should see")
	ws, err := kcpClusterClient.Cluster(orgClusterName).TenancyV1alpha1().ClusterWorkspaces().Create(ctx, workspaceForType(string(gamma)), metav1.CreateOptions{})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		workspace, err := kcpClusterClient.Cluster(orgClusterName).TenancyV1alpha1().ClusterWorkspaces().Get(ctx, ws.Name, metav1.GetOptions{})
		require.NoError(t, err)
		return workspacesStuckInInitializing(t, *workspace) == 1
	}, wait.ForeverTestTimeout, 100*time.Millisecond)

	ws, err = kcpClusterClient.Cluster(orgClusterName).TenancyV1alpha1().ClusterWorkspaces().Get(ctx, ws.Name, metav1.GetOptions{})
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
	newData, err := json.Marshal(tenancyv1alpha1.ClusterWorkspace{
		Status: obj.Status,
	})
	require.NoError(t, err)

	patchBytes, err := jsonpatch.CreateMergePatch(oldData, newData)
	require.NoError(t, err)
	ws, err = kcpClusterClient.Cluster(orgClusterName).TenancyV1alpha1().ClusterWorkspaces().Patch(ctx, obj.Name, types.MergePatchType, patchBytes, metav1.PatchOptions{}, "status")
	require.NoError(t, err)

	for initializer, watcher := range watchers {
		for {
			select {
			case evt := <-watcher.ResultChan():
				if evt.Object.(metav1.Object).GetUID() != ws.UID {
					// skip other objects that some actors touch
					continue
				}

				// there might be other actors doing who-knows-what on the workspaces, so we need to specifically
				// look for the first event *relating to the new workspace* that we get
				if evt.Type == watch.Modified {
					obj := evt.Object.(*tenancyv1alpha1.ClusterWorkspace)
					t.Logf("ignoring MODIFIED event for %s initializer: %s", initializer, cmp.Diff(ws, obj))
					ws = obj
					continue // we will see some modification events from the above patch and the resulting controller reactions
				}

				require.Equal(t, evt.Type, watch.Deleted)
			case <-time.Tick(wait.ForeverTestTimeout):
				t.Fatalf("never saw a watch event for the %s initializer", initializer)
			}
			break
		}
	}
}

func pruneVolatileFields(items []tenancyv1alpha1.ClusterWorkspace) []tenancyv1alpha1.ClusterWorkspace {
	for i := range items {
		items[i].ResourceVersion = ""
		items[i].ManagedFields = nil
		items[i].Finalizers = nil
	}
	return items
}

func workspaceForType(workspaceType string) *tenancyv1alpha1.ClusterWorkspace {
	return &tenancyv1alpha1.ClusterWorkspace{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "e2e-workspace-",
		},
		Spec: tenancyv1alpha1.ClusterWorkspaceSpec{
			Type: strings.ToUpper(string(workspaceType[0])) + workspaceType[1:],
		},
	}
}

func workspacesStuckInInitializing(t *testing.T, workspaces ...tenancyv1alpha1.ClusterWorkspace) int {
	ret := 0
	for _, workspace := range workspaces {
		if workspace.Status.Phase != tenancyv1alpha1.ClusterWorkspacePhaseInitializing {
			t.Logf("workspace %s is in %s, not %s", workspace.Name, workspace.Status.Phase, tenancyv1alpha1.ClusterWorkspacePhaseInitializing)
			continue
		}
		if len(workspace.Status.Initializers) == 0 {
			t.Logf("workspace %s has no initializers", workspace.Name)
			continue
		}
		if workspaceLabelsUpToDate(t, workspace) {
			ret++
		}
	}
	return ret
}

// this is really an implementation detail of the virtual workspace, but since we have a couple of moving pieces
// we do ultimately need to wait for labels to propagate before checking anything else, or the VW will not work
func workspaceLabelsUpToDate(t *testing.T, workspace tenancyv1alpha1.ClusterWorkspace) bool {
	if workspace.ObjectMeta.Labels[tenancyv1alpha1.ClusterWorkspacePhaseLabel] != string(tenancyv1alpha1.ClusterWorkspacePhaseInitializing) {
		t.Logf("workspace %s phase label is not updated yet", workspace.Name)
		return false
	}
	for _, initializer := range workspace.Status.Initializers {
		if _, exists := workspace.ObjectMeta.Labels[string(tenancyv1alpha1.ClusterWorkspaceInitializerLabelPrefix+initializer)]; !exists {
			t.Logf("workspace %s initializer labels are not updated yet", workspace.Name)
			return false
		}
	}
	return true
}
