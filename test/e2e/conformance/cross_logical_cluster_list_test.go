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

package conformance

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/kubernetes/pkg/genericcontrolplane/clientutils"

	configcrds "github.com/kcp-dev/kcp/config/crds"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy"
	tenancyapi "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	clientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	sheriffs "github.com/kcp-dev/kcp/test/e2e/conformance/othersheriffs"
	othersheriffs "github.com/kcp-dev/kcp/test/e2e/conformance/sheriffs"
	"github.com/kcp-dev/kcp/test/e2e/fixtures/wildwest/apis/wildwest"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestCrossLogicalClusterList(t *testing.T) {
	t.Parallel()

	server := framework.SharedKcpServer(t)

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	cfg := server.DefaultConfig(t)

	// Until we get rid of the multiClusterClientConfigRoundTripper and replace it with scoping,
	// make sure we don't break cross-logical cluster client listing.
	clientutils.EnableMultiCluster(cfg, nil, true)

	logicalClusters := []string{
		framework.NewOrganizationFixture(t, server),
		framework.NewOrganizationFixture(t, server),
	}
	expectedWorkspaces := sets.NewString()
	for i, logicalCluster := range logicalClusters {
		t.Logf("Bootstrapping ClusterWorkspace CRDs in logical cluster %s", logicalCluster)
		apiExtensionsClients, err := apiextensionsclient.NewClusterForConfig(cfg)
		require.NoError(t, err, "failed to construct apiextensions client for server")
		crdClient := apiExtensionsClients.Cluster(logicalCluster).ApiextensionsV1().CustomResourceDefinitions()
		workspaceCRDs := []metav1.GroupResource{
			{Group: tenancy.GroupName, Resource: "clusterworkspaces"},
		}
		err = configcrds.Create(ctx, crdClient, workspaceCRDs...)
		require.NoError(t, err, "failed to bootstrap CRDs")
		kcpClients, err := clientset.NewClusterForConfig(cfg)
		require.NoError(t, err, "failed to construct kcp client for server")

		wsName := fmt.Sprintf("ws-%d", i)

		t.Logf("Creating ClusterWorkspace CRs in logical cluster %s", logicalCluster)
		kcpClient := kcpClients.Cluster(logicalCluster)
		sourceWorkspace := &tenancyapi.ClusterWorkspace{
			ObjectMeta: metav1.ObjectMeta{
				Name: wsName,
			},
		}
		_, err = kcpClient.TenancyV1alpha1().ClusterWorkspaces().Create(ctx, sourceWorkspace, metav1.CreateOptions{})
		require.NoError(t, err, "error creating source workspace")

		expectedWorkspaces.Insert(logicalCluster + "/" + wsName)

		server.Artifact(t, func() (runtime.Object, error) {
			return kcpClient.TenancyV1alpha1().ClusterWorkspaces().Get(ctx, sourceWorkspace.Name, metav1.GetOptions{})
		})
	}

	t.Logf("Listing ClusterWorkspace CRs across logical clusters")
	kcpClients, err := clientset.NewClusterForConfig(cfg)
	require.NoError(t, err, "failed to construct kcp client for server")
	kcpClient := kcpClients.Cluster("*")
	workspaces, err := kcpClient.TenancyV1alpha1().ClusterWorkspaces().List(ctx, metav1.ListOptions{})
	require.NoError(t, err, "error listing workspaces")

	t.Logf("Expecting at least those ClusterWorkspaces we created above")
	got := sets.NewString()
	for _, ws := range workspaces.Items {
		got.Insert(ws.ClusterName + "/" + ws.Name)
	}
	require.True(t, got.IsSuperset(expectedWorkspaces), "unexpected workspaces detected")
}

func TestCrossLogicalClusterListPartialObjectMetadata(t *testing.T) {
	// ensure PartialObjectMetadata wildcard list works even with different CRD schemas
	t.Parallel()

	server := framework.SharedKcpServer(t)

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	org := framework.NewOrganizationFixture(t, server)
	w1 := framework.NewWorkspaceFixture(t, server, org, "Universal")
	w2 := framework.NewWorkspaceFixture(t, server, org, "Universal")
	w3 := framework.NewWorkspaceFixture(t, server, org, "Universal")

	cfg := server.DefaultConfig(t)

	crdClusterClient, err := apiextensionsclient.NewClusterForConfig(cfg)
	require.NoError(t, err, "failed to construct apiextensions client for server")
	dynamicClusterClient, err := dynamic.NewClusterForConfig(cfg)
	require.NoError(t, err, "failed to construct dynamic client for server")

	sheriffsGVR := schema.GroupVersionResource{Group: wildwest.GroupName, Resource: "sheriffs", Version: "v1alpha1"}
	sheriffGR := metav1.GroupResource{Group: wildwest.GroupName, Resource: "sheriffs"}

	t.Logf("Install a normal sheriffs CRD into workspace %q", w1)
	sheriffs.Create(t, crdClusterClient.Cluster(w1).ApiextensionsV1().CustomResourceDefinitions(), sheriffGR)

	t.Logf("Install another normal sheriffs CRD into workspace %q", w2)
	sheriffs.Create(t, crdClusterClient.Cluster(w1).ApiextensionsV1().CustomResourceDefinitions(), sheriffGR)

	t.Logf("Trying to wildcard list")
	_, err = dynamicClusterClient.Cluster("*").Resource(sheriffsGVR).List(ctx, metav1.ListOptions{})
	require.NoError(t, err, "expected wildcard list to work because schemas are the same")

	t.Logf("Install a different sheriffs CRD into workspace %q", w3)
	othersheriffs.Create(t, crdClusterClient.Cluster(w3).ApiextensionsV1().CustomResourceDefinitions(), sheriffGR)

	t.Logf("Trying to wildcard list and expecting it to fail now")
	require.Eventually(t, func() bool {
		_, err = dynamicClusterClient.Cluster("*").Resource(sheriffsGVR).List(ctx, metav1.ListOptions{})
		return err != nil
	}, wait.ForeverTestTimeout, time.Millisecond*100, "expected wildcard list to fail because schemas are different")

	t.Logf("Trying to wildcard list with PartialObjectMetadata content-type and it should work")
	metadataConfig := *cfg
	metadataConfig.Wrap(func(rt http.RoundTripper) http.RoundTripper {
		// we have to use this way because the dynamic client overrides the content-type :-/
		return &acceptOverrideTransport{
			RoundTripper: rt,
			accept:       "application/json;as=PartialObjectMetadataList;g=meta.k8s.io;v=v1",
		}
	})
	metadataClusterClient, err := dynamic.NewClusterForConfig(&metadataConfig)
	require.NoError(t, err, "failed to construct dynamic client for server")
	_, err = metadataClusterClient.Cluster("*").Resource(sheriffsGVR).List(ctx, metav1.ListOptions{})
	require.NoError(t, err, "expected wildcard list to work with metadata client even though schemas are different")
}

type acceptOverrideTransport struct {
	http.RoundTripper
	accept string
}

func (t *acceptOverrideTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("Accept", t.accept)
	return t.RoundTripper.RoundTrip(req)
}
