/*
Copyright 2023 The KCP Authors.

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

package apiexportendpointslice

import (
	"context"
	"embed"
	"fmt"
	"testing"
	"time"

	"github.com/davecgh/go-spew/spew"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/util/retry"

	"github.com/kcp-dev/kcp/config/helpers"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/core"
	"github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/util/conditions"
	topologyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/topology/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

//go:embed *.yaml
var testFiles embed.FS

func TestAPIExportEndpointSliceWithPartition(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := framework.SharedKcpServer(t)

	// Create Organization and Workspaces
	orgPath, _ := framework.NewOrganizationFixture(t, server)
	exportClusterPath, _ := framework.NewWorkspaceFixture(t, server, orgPath)
	partitionClusterPath, _ := framework.NewWorkspaceFixture(t, server, orgPath)

	cfg := server.BaseConfig(t)

	var err error
	kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp cluster client for server")

	export := &apisv1alpha1.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: "my-export",
		},
	}

	slice := &apisv1alpha1.APIExportEndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "my-slice",
		},
		Spec: apisv1alpha1.APIExportEndpointSliceSpec{
			APIExport: apisv1alpha1.ExportBindingReference{
				Path: exportClusterPath.String(),
				Name: export.Name,
			},
		},
	}

	partition := &topologyv1alpha1.Partition{
		ObjectMeta: metav1.ObjectMeta{
			Name: "my-partition",
		},
		Spec: topologyv1alpha1.PartitionSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"region": "apiexportendpointslice-test-region",
				},
			},
		},
	}

	t.Logf("Creating an APIExportEndpointSlice with reference to a nonexistent APIExport")
	sliceClient := kcpClusterClient.ApisV1alpha1().APIExportEndpointSlices()

	_, err = sliceClient.Cluster(partitionClusterPath).Create(ctx, slice, metav1.CreateOptions{})
	require.True(t, apierrors.IsForbidden(err), "no error creating APIExportEndpointSlice (admission should have declined it)")
	sliceList, err := sliceClient.Cluster(partitionClusterPath).List(ctx, metav1.ListOptions{})
	require.NoError(t, err, "error listing APIExportEndpointSlice")
	require.True(t, len(sliceList.Items) == 0, "not expecting any APIExportEndpointSlice")

	t.Logf("Creating the missing APIExport")
	exportClient := kcpClusterClient.ApisV1alpha1().APIExports()
	_, err = exportClient.Cluster(exportClusterPath).Create(ctx, export, metav1.CreateOptions{})
	require.NoError(t, err, "error creating APIExport")

	var sliceName string
	t.Logf("Retrying to create the APIExportEndpointSlice after the APIExport has been created")
	framework.Eventually(t, func() (bool, string) {
		created, err := sliceClient.Cluster(partitionClusterPath).Create(ctx, slice, metav1.CreateOptions{})
		if err != nil {
			return false, err.Error()
		}
		sliceName = created.Name
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected APIExportEndpointSlice creation to succeed")

	framework.Eventually(t, func() (bool, string) {
		slice, err = kcpClusterClient.Cluster(partitionClusterPath).ApisV1alpha1().APIExportEndpointSlices().Get(ctx, sliceName, metav1.GetOptions{})
		require.NoError(t, err)

		if conditions.IsTrue(slice, apisv1alpha1.APIExportValid) {
			return true, ""
		}

		return false, spew.Sdump(slice.Status.Conditions)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected valid APIExport")

	t.Logf("Adding a Partition to the APIExportEndpointSlice")
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		slice, err = kcpClusterClient.Cluster(partitionClusterPath).ApisV1alpha1().APIExportEndpointSlices().Get(ctx, sliceName, metav1.GetOptions{})
		require.NoError(t, err)
		slice.Spec.Partition = partition.Name
		_, err = kcpClusterClient.Cluster(partitionClusterPath).ApisV1alpha1().APIExportEndpointSlices().Update(ctx, slice, metav1.UpdateOptions{})
		return err
	})
	require.NoError(t, err, "error updating APIExportEndpointSlice")

	framework.Eventually(t, func() (bool, string) {
		slice, err = kcpClusterClient.Cluster(partitionClusterPath).ApisV1alpha1().APIExportEndpointSlices().Get(ctx, sliceName, metav1.GetOptions{})
		require.NoError(t, err)
		if conditions.IsFalse(slice, apisv1alpha1.PartitionValid) && conditions.GetReason(slice, apisv1alpha1.PartitionValid) == apisv1alpha1.PartitionInvalidReferenceReason {
			return true, ""
		}

		return false, spew.Sdump(slice.Status.Conditions)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected missing Partition")
	require.True(t, len(slice.Status.APIExportEndpoints) == 0, "not expecting any endpoint")
	t.Logf("Creating the missing Partition")
	partitionClient := kcpClusterClient.TopologyV1alpha1().Partitions()
	_, err = partitionClient.Cluster(partitionClusterPath).Create(ctx, partition, metav1.CreateOptions{})
	require.NoError(t, err, "error creating Partition")

	framework.Eventually(t, func() (bool, string) {
		slice, err = kcpClusterClient.Cluster(partitionClusterPath).ApisV1alpha1().APIExportEndpointSlices().Get(ctx, sliceName, metav1.GetOptions{})
		require.NoError(t, err)
		if conditions.IsTrue(slice, apisv1alpha1.PartitionValid) {
			return true, ""
		}

		return false, spew.Sdump(slice.Status.Conditions)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected valid Partition")

	t.Logf("Checking that no endpoint has been populated")
	require.True(t, len(slice.Status.APIExportEndpoints) == 0, "not expecting any endpoint")
}

func TestAPIBindingEndpointSlicesSharded(t *testing.T) {
	t.Parallel()

	framework.Suite(t, "control-plane")

	server := framework.SharedKcpServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	cfg := server.BaseConfig(t)

	t.Logf("Check if we can access shards")
	var shards *v1alpha1.ShardList
	{
		kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
		require.NoError(t, err, "failed to construct kcp cluster client for server")

		shards, err = kcpClusterClient.Cluster(core.RootCluster.Path()).CoreV1alpha1().Shards().List(ctx, metav1.ListOptions{})
		require.NoError(t, err, "failed to list shards")

		if len(shards.Items) < 2 {
			t.Skipf("Need at least 2 shards to run this test, got %d", len(shards.Items))
			return
		}
	}

	t.Logf("Setup provider workspace")
	var orgPath, providerPath logicalcluster.Path
	{
		orgPath, _ = framework.NewOrganizationFixture(t, server)
		providerPath, _ = framework.NewWorkspaceFixture(t, server, orgPath, framework.WithName("service-provider"))

		serviceProviderClient, err := kcpclientset.NewForConfig(cfg)
		require.NoError(t, err, "failed to construct kcp cluster client for server")
		dynamicClusterClient, err := kcpdynamic.NewForConfig(cfg)
		require.NoError(t, err, "failed to construct dynamic cluster client for server")

		mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(serviceProviderClient.Cluster(providerPath).Discovery()))
		err = helpers.CreateResourceFromFS(ctx, dynamicClusterClient.Cluster(providerPath), mapper, nil, "apiresourceschema_cowboys.yaml", testFiles)
		require.NoError(t, err)

		t.Logf("Create an APIExport today-cowboys in %q", providerPath)
		cowboysAPIExport := &apisv1alpha1.APIExport{
			ObjectMeta: metav1.ObjectMeta{
				Name: "today-cowboys",
			},
			Spec: apisv1alpha1.APIExportSpec{
				LatestResourceSchemas: []string{"today.cowboys.wildwest.dev"},
			},
		}
		_, err = serviceProviderClient.Cluster(providerPath).ApisV1alpha1().APIExports().Create(ctx, cowboysAPIExport, metav1.CreateOptions{})
		require.NoError(t, err)
	}

	t.Logf("Create a consumer workspaces - one per shard")
	var bindShardname string
	{
		for _, shard := range shards.Items {
			if bindShardname == "" { // bind to the first shard only
				bindShardname = shard.Name
			}
			if bindShardname != shard.Name {
				continue
			}
			consumerPath, _ := framework.NewWorkspaceFixture(t, server, orgPath, framework.WithName("consumer-bound-against-%s", shard.Name), framework.WithShard(shard.Name))

			t.Logf("Create an APIBinding in %q that points to the today-cowboys export from %q", consumerPath, providerPath)
			apiBinding := &apisv1alpha1.APIBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cowboys",
				},
				Spec: apisv1alpha1.APIBindingSpec{
					Reference: apisv1alpha1.BindingReference{
						Export: &apisv1alpha1.ExportBindingReference{
							Path: providerPath.String(),
							Name: "today-cowboys",
						},
					},
				},
			}

			kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
			require.NoError(t, err, "failed to construct kcp cluster client for server")

			framework.Eventually(t, func() (bool, string) {
				_, err = kcpClusterClient.Cluster(consumerPath).ApisV1alpha1().APIBindings().Create(ctx, apiBinding, metav1.CreateOptions{})
				return err == nil, fmt.Sprintf("Error creating APIBinding: %v", err)
			}, wait.ForeverTestTimeout, time.Millisecond*100)
		}
	}

	// TODO(mjudeikis): This will be deprecated when we deperecate APIExport urls.
	t.Logf("Check that APIExport has 2 virtual workspaces")
	{
		kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
		require.NoError(t, err, "failed to construct kcp cluster client for server")

		apiExport, err := kcpClusterClient.Cluster(providerPath).ApisV1alpha1().APIExports().Get(ctx, "today-cowboys", metav1.GetOptions{})
		require.NoError(t, err)

		//nolint:staticcheck // SA1019 VirtualWorkspaces is deprecated but not removed yet
		require.Len(t, apiExport.Status.VirtualWorkspaces, 2)
	}

	t.Logf("Create a topology PartitionSet for the providers")
	var partition *topologyv1alpha1.Partition
	{
		kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
		require.NoError(t, err, "failed to construct kcp cluster client for server")

		_, err = kcpClusterClient.Cluster(providerPath).TopologyV1alpha1().PartitionSets().Create(ctx, &topologyv1alpha1.PartitionSet{
			ObjectMeta: metav1.ObjectMeta{
				Name: "cowboys",
			},
			Spec: topologyv1alpha1.PartitionSetSpec{
				ShardSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"shared": "true",
					},
				},
			},
		}, metav1.CreateOptions{})
		require.NoError(t, err)

		// Partition should be created
		framework.Eventually(t, func() (bool, string) {
			partitions, err := kcpClusterClient.Cluster(providerPath).TopologyV1alpha1().Partitions().List(ctx, metav1.ListOptions{})
			if err == nil && len(partitions.Items) == 1 {
				partition = &partitions.Items[0]
				return true, ""
			}
			return false, fmt.Sprintf("Error listing partitions: %v", err)
		}, wait.ForeverTestTimeout, time.Millisecond*500)
	}

	t.Logf("Create APIExportEndpointSlice for consumers")
	{
		kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
		require.NoError(t, err, "failed to construct kcp cluster client for server")

		_, err = kcpClusterClient.Cluster(providerPath).ApisV1alpha1().APIExportEndpointSlices().Create(ctx, &apisv1alpha1.APIExportEndpointSlice{
			ObjectMeta: metav1.ObjectMeta{
				Name: "shared-cowboys",
			},
			Spec: apisv1alpha1.APIExportEndpointSliceSpec{
				Partition: partition.Name,
				APIExport: apisv1alpha1.ExportBindingReference{
					Path: providerPath.String(),
					Name: "today-cowboys",
				},
			},
		}, metav1.CreateOptions{})
		require.NoError(t, err)

		// we should have 1 APIExportEndpointSlice with 1 APIExportEndpoint as we bound only once.
		framework.Eventually(t, func() (bool, string) {
			slice, err := kcpClusterClient.Cluster(providerPath).ApisV1alpha1().APIExportEndpointSlices().Get(ctx, "shared-cowboys", metav1.GetOptions{})
			if len(slice.Status.APIExportEndpoints) == 1 {
				return true, ""
			}
			return false, fmt.Sprintf("APIExportEndpointSlice has %d endpoints: %v", len(slice.Status.APIExportEndpoints), err)
		}, wait.ForeverTestTimeout*50, time.Millisecond*500)
	}

	t.Logf("Create consumer on second shard and observe APIExportEndpointSlice to have second url added")
	var consumerPath logicalcluster.Path
	{
		for _, shard := range shards.Items {
			if bindShardname == shard.Name {
				continue
			}
			consumerPath, _ = framework.NewWorkspaceFixture(t, server, orgPath, framework.WithName("consumer-bound-against-%s", shard.Name), framework.WithShard(shard.Name))

			t.Logf("Create an APIBinding in %q that points to the today-cowboys export from %q", consumerPath, providerPath)
			apiBinding := &apisv1alpha1.APIBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cowboys",
				},
				Spec: apisv1alpha1.APIBindingSpec{
					Reference: apisv1alpha1.BindingReference{
						Export: &apisv1alpha1.ExportBindingReference{
							Path: providerPath.String(),
							Name: "today-cowboys",
						},
					},
				},
			}

			kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
			require.NoError(t, err, "failed to construct kcp cluster client for server")

			framework.Eventually(t, func() (bool, string) {
				_, err = kcpClusterClient.Cluster(consumerPath).ApisV1alpha1().APIBindings().Create(ctx, apiBinding, metav1.CreateOptions{})
				return err == nil, fmt.Sprintf("Error creating APIBinding: %v", err)
			}, wait.ForeverTestTimeout, time.Millisecond*500)
		}
	}

	t.Logf("Check that APIExportEndpointSlices has 2 virtual workspaces")
	{
		framework.Eventually(t, func() (bool, string) {
			kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
			require.NoError(t, err, "failed to construct kcp cluster client for server")

			slice, err := kcpClusterClient.Cluster(providerPath).ApisV1alpha1().APIExportEndpointSlices().Get(ctx, "shared-cowboys", metav1.GetOptions{})
			if len(slice.Status.APIExportEndpoints) == 2 {
				return true, ""
			}
			return false, fmt.Sprintf("APIExportEndpointSlice has %d endpoints: %v", len(slice.Status.APIExportEndpoints), err)
		}, wait.ForeverTestTimeout*50, time.Millisecond*500)
	}

	t.Logf("Delete consumer on second shard and observe APIExportEndpointSlice to have second url removed")
	{
		kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
		require.NoError(t, err, "failed to construct kcp cluster client for server")

		framework.Eventually(t, func() (bool, string) {
			err := kcpClusterClient.Cluster(consumerPath).ApisV1alpha1().APIBindings().Delete(ctx, "cowboys", metav1.DeleteOptions{})
			return err == nil, fmt.Sprintf("Error deleting APIBinding: %v", err)
		}, wait.ForeverTestTimeout, time.Millisecond*500)
	}

	t.Logf("Check that APIExportEndpointSlices has 1 virtual workspaces")
	{
		framework.Eventually(t, func() (bool, string) {
			kcpClusterClient, err := kcpclientset.NewForConfig(cfg)
			require.NoError(t, err, "failed to construct kcp cluster client for server")

			slice, err := kcpClusterClient.Cluster(providerPath).ApisV1alpha1().APIExportEndpointSlices().Get(ctx, "shared-cowboys", metav1.GetOptions{})
			if len(slice.Status.APIExportEndpoints) == 1 {
				return true, ""
			}
			return false, fmt.Sprintf("APIExportEndpointSlice has %d endpoints: %v", len(slice.Status.APIExportEndpoints), err)
		}, wait.ForeverTestTimeout*50, time.Millisecond*500)
	}
}
