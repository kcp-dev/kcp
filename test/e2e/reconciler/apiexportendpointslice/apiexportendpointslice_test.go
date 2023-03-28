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
	"fmt"
	"testing"
	"time"

	"github.com/davecgh/go-spew/spew"
	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/util/conditions"
	topologyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/topology/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

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

		if conditions.IsTrue(slice, apisv1alpha1.APIExportValid) && conditions.IsTrue(slice, apisv1alpha1.APIExportEndpointSliceURLsReady) {
			return true, ""
		}

		return false, spew.Sdump(slice.Status.Conditions)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected valid APIExport")

	t.Logf("Adding a Partition to the APIExportEndpointSlice")
	slice.Spec.Partition = partition.Name
	_, err = kcpClusterClient.Cluster(partitionClusterPath).ApisV1alpha1().APIExportEndpointSlices().Update(ctx, slice, metav1.UpdateOptions{})
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
	require.True(t, conditions.IsFalse(slice, apisv1alpha1.APIExportEndpointSliceURLsReady), "expecting URLs not ready condition")

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
	require.True(t, conditions.IsTrue(slice, apisv1alpha1.APIExportEndpointSliceURLsReady), "expecting the URLs ready condition")
}

func TestAPIExportEndpointSliceWithPartitionPrivate(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := framework.PrivateKcpServer(t)

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

	t.Logf("Creating the APIExport")
	exportClient := kcpClusterClient.ApisV1alpha1().APIExports()
	_, err = exportClient.Cluster(exportClusterPath).Create(ctx, export, metav1.CreateOptions{})
	require.NoError(t, err, "error creating APIExport")

	t.Logf("Creating the APIExportEndpointSlice")
	sliceClient := kcpClusterClient.ApisV1alpha1().APIExportEndpointSlices()
	// allow some time for APIExport to be synced onto the cache server
	framework.Eventually(t, func() (bool, string) {
		slice, err = sliceClient.Cluster(partitionClusterPath).Create(ctx, slice, metav1.CreateOptions{})
		if err != nil {
			return false, err.Error()
		}
		return true, ""
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected successful creation of APIExportEndpointSlice")
	sliceName := slice.Name

	framework.Eventually(t, func() (bool, string) {
		slice, err = kcpClusterClient.Cluster(partitionClusterPath).ApisV1alpha1().APIExportEndpointSlices().Get(ctx, sliceName, metav1.GetOptions{})
		require.NoError(t, err)
		if conditions.IsTrue(slice, apisv1alpha1.APIExportValid) {
			return true, ""
		}
		return false, spew.Sdump(slice.Status.Conditions)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected valid APIExport")
	require.True(t, conditions.IsTrue(slice, apisv1alpha1.APIExportEndpointSliceURLsReady), "expecting URLs ready condition")

	t.Logf("Creating the Partition")
	partitionClient := kcpClusterClient.TopologyV1alpha1().Partitions()
	_, err = partitionClient.Cluster(partitionClusterPath).Create(ctx, partition, metav1.CreateOptions{})
	require.NoError(t, err, "error creating Partition")

	t.Logf("Adding a Partition to the APIExportEndpointSlice")
	slice.Spec.Partition = partition.Name
	_, err = kcpClusterClient.Cluster(partitionClusterPath).ApisV1alpha1().APIExportEndpointSlices().Update(ctx, slice, metav1.UpdateOptions{})
	require.NoError(t, err, "error updating APIExportEndpointSlice")

	framework.Eventually(t, func() (bool, string) {
		s, err := kcpClusterClient.Cluster(partitionClusterPath).ApisV1alpha1().APIExportEndpointSlices().Get(ctx, sliceName, metav1.GetOptions{})
		require.NoError(t, err)
		if conditions.IsTrue(s, apisv1alpha1.PartitionValid) {
			return true, ""
		}
		return false, spew.Sdump(s.Status.Conditions)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expected valid Partition")
	require.True(t, conditions.IsTrue(slice, apisv1alpha1.APIExportEndpointSliceURLsReady), "expecting URLs ready condition")

	t.Logf("Checking that no endpoint has been populated")
	framework.Eventually(t, func() (bool, string) {
		s, err := kcpClusterClient.Cluster(partitionClusterPath).ApisV1alpha1().APIExportEndpointSlices().Get(ctx, sliceName, metav1.GetOptions{})
		require.NoError(t, err)
		if len(s.Status.APIExportEndpoints) == 0 {
			return true, ""
		}
		return false, fmt.Sprintf("expected 0 endpoints, but got: %#v", s.Status.APIExportEndpoints)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "not expecting any endpoint")

	// Endpoint tests require the edition of shards.
	// These tests are run on a private cluster to avoid side effects on other e2e tests.
	// They require the resources previously created: APIExport, APIExportEndpointSlice, etc.
	shard := &corev1alpha1.Shard{
		ObjectMeta: metav1.ObjectMeta{
			Name: "my-shard",
			Labels: map[string]string{
				"region": "apiexportendpointslice-test-region",
			},
		},
		Spec: corev1alpha1.ShardSpec{
			BaseURL: "https://base.kcp.test.dev",
		},
	}

	t.Logf("Creating a shard in the region")
	shardClient := kcpClusterClient.CoreV1alpha1().Shards()
	shard, err = shardClient.Cluster(core.RootCluster.Path()).Create(ctx, shard, metav1.CreateOptions{})
	require.NoError(t, err, "error creating Shard")

	framework.Eventually(t, func() (bool, string) {
		slice, err = kcpClusterClient.Cluster(partitionClusterPath).ApisV1alpha1().APIExportEndpointSlices().Get(ctx, sliceName, metav1.GetOptions{})
		require.NoError(t, err)
		if len(slice.Status.APIExportEndpoints) == 1 {
			return true, ""
		}
		return false, fmt.Sprintf("expected 1 endpoint, but got: %#v", slice.Status.APIExportEndpoints)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expecting a single endpoint")
	require.Contains(t, slice.Status.APIExportEndpoints[0].URL, export.Name)

	t.Logf("Updating the previously created shard")
	shard.Labels["region"] = "doesnotexist"
	shard, err = shardClient.Cluster(core.RootCluster.Path()).Update(ctx, shard, metav1.UpdateOptions{})
	require.NoError(t, err, "error updating Shard")

	framework.Eventually(t, func() (bool, string) {
		s, err := kcpClusterClient.Cluster(partitionClusterPath).ApisV1alpha1().APIExportEndpointSlices().Get(ctx, sliceName, metav1.GetOptions{})
		require.NoError(t, err)
		if len(s.Status.APIExportEndpoints) == 0 {
			return true, ""
		}
		return false, fmt.Sprintf("expected 0 endpoints, but got: %#v", s.Status.APIExportEndpoints)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expecting no endpoint")

	t.Logf("Setting back the correct label")
	shard.Labels["region"] = "apiexportendpointslice-test-region"
	shard, err = shardClient.Cluster(core.RootCluster.Path()).Update(ctx, shard, metav1.UpdateOptions{})
	require.NoError(t, err, "error updating Shard")

	framework.Eventually(t, func() (bool, string) {
		s, err := kcpClusterClient.Cluster(partitionClusterPath).ApisV1alpha1().APIExportEndpointSlices().Get(ctx, sliceName, metav1.GetOptions{})
		require.NoError(t, err)
		if len(s.Status.APIExportEndpoints) == 1 {
			return true, ""
		}
		return false, fmt.Sprintf("expected 1 endpoint, but got: %#v", s.Status.APIExportEndpoints)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expecting a single endpoint")

	t.Logf("Deleting the shard")
	err = shardClient.Cluster(core.RootCluster.Path()).Delete(ctx, shard.Name, metav1.DeleteOptions{})
	require.NoError(t, err, "error deleting Shard")

	framework.Eventually(t, func() (bool, string) {
		s, err := kcpClusterClient.Cluster(partitionClusterPath).ApisV1alpha1().APIExportEndpointSlices().Get(ctx, sliceName, metav1.GetOptions{})
		require.NoError(t, err)
		if len(s.Status.APIExportEndpoints) == 0 {
			return true, ""
		}
		return false, fmt.Sprintf("expected 0 endpoints, but got: %#v", s.Status.APIExportEndpoints)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expecting no endpoint")

	t.Logf("Creating a slice without partition")
	sliceWithAll := &apisv1alpha1.APIExportEndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "my-slice-without-partition",
		},
		Spec: apisv1alpha1.APIExportEndpointSliceSpec{
			APIExport: apisv1alpha1.ExportBindingReference{
				Path: exportClusterPath.String(),
				Name: slice.Spec.APIExport.Name,
			},
		},
	}
	sliceWithAll, err = sliceClient.Cluster(partitionClusterPath).Create(ctx, sliceWithAll, metav1.CreateOptions{})
	require.NoError(t, err, "error creating APIExportEndpointSlice")

	sliceWithAllName := sliceWithAll.Name

	framework.Eventually(t, func() (bool, string) {
		sliceWithAll, err := kcpClusterClient.Cluster(partitionClusterPath).ApisV1alpha1().APIExportEndpointSlices().Get(ctx, sliceWithAllName, metav1.GetOptions{})
		require.NoError(t, err)
		if len(sliceWithAll.Status.APIExportEndpoints) == 1 {
			return true, ""
		}
		return false, fmt.Sprintf("expected 1 endpoint, but got: %#v", sliceWithAll.Status.APIExportEndpoints)
	}, wait.ForeverTestTimeout, 100*time.Millisecond, "expecting a single endpoint for the root shard, got %d", len(sliceWithAll.Status.APIExportEndpoints))
}
