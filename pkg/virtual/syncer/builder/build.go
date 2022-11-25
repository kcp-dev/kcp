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

package builder

import (
	"strings"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	kcpinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/forwardingregistry"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/rootapiserver"
	"github.com/kcp-dev/kcp/pkg/virtual/syncer/controllers/apireconciler"
	"github.com/kcp-dev/kcp/pkg/virtual/syncer/transformations"
	"github.com/kcp-dev/kcp/pkg/virtual/syncer/upsyncer"
)

const (
	// SyncerVirtualWorkspaceName holds the name of the virtual workspace for the syncer, used to sync resources from upstream to downstream.
	SyncerVirtualWorkspaceName string = "syncer"
	// UpsyncerVirtualWorkspaceName holds the name of the virtual workspace for the upsyncer, used to sync resources from downstream to upstream.
	UpsyncerVirtualWorkspaceName string = "upsyncer"
)

// BuildVirtualWorkspace builds two virtual workspaces, SyncerVirtualWorkspace and UpsyncerVirtualWorkspace by instantiating a DynamicVirtualWorkspace which,
// combined with a ForwardingREST REST storage implementation, serves a SyncTargetAPI list maintained by the APIReconciler controller.
func BuildVirtualWorkspace(
	rootPathPrefix string,
	kubeClusterClient kcpkubernetesclientset.ClusterInterface,
	dynamicClusterClient kcpdynamic.ClusterInterface,
	kcpClusterClient kcpclientset.ClusterInterface,
	wildcardKcpInformers kcpinformers.SharedInformerFactory,
) []rootapiserver.NamedVirtualWorkspace {
	if !strings.HasSuffix(rootPathPrefix, "/") {
		rootPathPrefix += "/"
	}

	// Setup the APIReconciler indexes to share between both virtualworkspaces.
	if err := wildcardKcpInformers.Workload().V1alpha1().SyncTargets().Informer().AddIndexers(cache.Indexers{
		apireconciler.IndexSyncTargetsByExport: apireconciler.IndexSyncTargetsByExports,
	}); err != nil {
		return nil
	}

	if err := wildcardKcpInformers.Apis().V1alpha1().APIExports().Informer().AddIndexers(cache.Indexers{
		apireconciler.IndexAPIExportsByAPIResourceSchema: apireconciler.IndexAPIExportsByAPIResourceSchemas,
	}); err != nil {
		return nil
	}

	provider := templateProvider{
		kubeClusterClient:    kubeClusterClient,
		dynamicClusterClient: dynamicClusterClient,
		kcpClusterClient:     kcpClusterClient,
		wildcardKcpInformers: wildcardKcpInformers,
		rootPathPrefix:       rootPathPrefix,
	}

	return []rootapiserver.NamedVirtualWorkspace{
		{
			Name: SyncerVirtualWorkspaceName,
			VirtualWorkspace: provider.newTemplate(templateParameters{
				virtualWorkspaceName:  SyncerVirtualWorkspaceName,
				filteredResourceState: workloadv1alpha1.ResourceStateSync,
				restProviderBuilder:   NewSyncerRestProvider,
				allowedAPIFilter:      nil,
				transformer: &transformations.SyncerResourceTransformer{
					TransformationProvider:   &transformations.SpecDiffTransformation{},
					SummarizingRulesProvider: &transformations.DefaultSummarizingRules{},
				},
				storageWrapperBuilder: forwardingregistry.WithStaticLabelSelector,
			}).buildVirtualWorkspace(),
		},
		{
			Name: UpsyncerVirtualWorkspaceName,
			VirtualWorkspace: provider.newTemplate(templateParameters{
				virtualWorkspaceName:  UpsyncerVirtualWorkspaceName,
				filteredResourceState: workloadv1alpha1.ResourceStateUpsync,
				restProviderBuilder:   NewUpSyncerRestProvider,
				allowedAPIFilter: func(apiGroupResource schema.GroupResource) bool {
					// Only allow persistentvolumes and Pods to be Upsynced.
					return apiGroupResource.Group == "" && apiGroupResource.Resource == "persistentvolumes" || apiGroupResource.Group == "" && apiGroupResource.Resource == "pods"
				},
				transformer:           &upsyncer.UpsyncerResourceTransformer{},
				storageWrapperBuilder: upsyncer.WithStaticLabelSelectorAndInWriteCallsCheck,
			}).buildVirtualWorkspace(),
		},
	}
}
