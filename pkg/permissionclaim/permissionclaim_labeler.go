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

package permissionclaim

import (
	"context"
	"fmt"

	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/logicalcluster/v3"

	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/sdk/apis/apis"
	apisv1alpha2 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha2/permissionclaims"
	apisv1alpha2informers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/apis/v1alpha2"
)

// NonPersistedResourcesClaimable is a list of resources that are not persisted
// to etcd, and therefore should not be labeled with permission claims. The value
// means whether they are claimable or not.
var NonPersistedResourcesClaimable = map[schema.GroupResource]bool{
	authorizationv1.SchemeGroupVersion.WithResource("localsubjectaccessreviews").GroupResource(): true,
	authorizationv1.SchemeGroupVersion.WithResource("selfsubjectaccessreviews").GroupResource():  false,
	authorizationv1.SchemeGroupVersion.WithResource("selfsubjectrulesreviews").GroupResource():   false,
	authorizationv1.SchemeGroupVersion.WithResource("subjectaccessreviews").GroupResource():      true,
	authenticationv1.SchemeGroupVersion.WithResource("selfsubjectreviews").GroupResource():       false,
	authenticationv1.SchemeGroupVersion.WithResource("tokenreviews").GroupResource():             false,
}

// Labeler calculates labels to apply to all instances of a cluster-group-resource based on permission claims.
type Labeler struct {
	listAPIBindingsAcceptingClaimedGroupResource func(clusterName logicalcluster.Name, groupResource schema.GroupResource) ([]*apisv1alpha2.APIBinding, error)
	getAPIBinding                                func(clusterName logicalcluster.Name, name string) (*apisv1alpha2.APIBinding, error)
	getAPIExport                                 func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error)
}

// NewLabeler returns a new Labeler.
func NewLabeler(
	apiBindingInformer apisv1alpha2informers.APIBindingClusterInformer,
	apiExportInformer, globalAPIExportInformer apisv1alpha2informers.APIExportClusterInformer,
) *Labeler {
	return &Labeler{
		listAPIBindingsAcceptingClaimedGroupResource: func(clusterName logicalcluster.Name, groupResource schema.GroupResource) ([]*apisv1alpha2.APIBinding, error) {
			indexKey := indexers.ClusterAndGroupResourceValue(clusterName, groupResource)
			return indexers.ByIndex[*apisv1alpha2.APIBinding](apiBindingInformer.Informer().GetIndexer(), indexers.APIBindingByClusterAndAcceptedClaimedGroupResources, indexKey)
		},

		getAPIBinding: func(clusterName logicalcluster.Name, name string) (*apisv1alpha2.APIBinding, error) {
			return apiBindingInformer.Lister().Cluster(clusterName).Get(name)
		},
		getAPIExport: func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error) {
			return indexers.ByPathAndNameWithFallback[*apisv1alpha2.APIExport](apisv1alpha2.Resource("apiexports"), apiExportInformer.Informer().GetIndexer(), globalAPIExportInformer.Informer().GetIndexer(), path, name)
		},
	}
}

// normalizeEventGroupResource normalizes event resources to use the core/v1 version consistently
// This prevents race conditions between core/v1 events and events.k8s.io/v1 events.
func normalizeEventGroupResource(gr schema.GroupResource) schema.GroupResource {
	if gr.Resource == "events" && (gr.Group == "" || gr.Group == "events.k8s.io") {
		// Always normalize to core/v1 events for consistent labeling
		return schema.GroupResource{Group: "", Resource: "events"}
	}
	return gr
}

// LabelsFor returns all the applicable labels for the cluster-group-resource relating to permission claims. This is
// the intersection of (1) all APIBindings in the cluster that have accepted claims for the group-resource with (2)
// associated APIExports that are claiming group-resource.
func (l *Labeler) LabelsFor(ctx context.Context, cluster logicalcluster.Name, groupResource schema.GroupResource, resourceName string, resourceLabels map[string]string) (map[string]string, error) {
	labels := map[string]string{}
	if _, nonPersisted := NonPersistedResourcesClaimable[groupResource]; nonPersisted {
		return labels, nil
	}

	// Normalize event resources to prevent race conditions between API versions
	normalizedGR := normalizeEventGroupResource(groupResource)

	bindings, err := l.listAPIBindingsAcceptingClaimedGroupResource(cluster, normalizedGR)
	if err != nil {
		return nil, fmt.Errorf("error listing APIBindings in %q accepting claimed group resource %q: %w", cluster, groupResource, err)
	}

	logger := klog.FromContext(ctx)

	for _, binding := range bindings {
		logger := logging.WithObject(logger, binding)

		path := logicalcluster.NewPath(binding.Spec.Reference.Export.Path)
		if path.Empty() {
			path = logicalcluster.From(binding).Path()
		}
		export, err := l.getAPIExport(path, binding.Spec.Reference.Export.Name)
		if err != nil {
			continue
		}

		for _, claim := range binding.Spec.PermissionClaims {
			if claim.State != apisv1alpha2.ClaimAccepted || claim.Group != normalizedGR.Group || claim.Resource != normalizedGR.Resource || !claim.Selector.MatchAll {
				continue
			}

			k, v, err := permissionclaims.ToLabelKeyAndValue(logicalcluster.From(export), export.Name, claim.PermissionClaim)
			if err != nil {
				// extremely unlikely to get an error here - it means the json marshaling failed
				logger.Error(err, "error calculating permission claim label key and value",
					"claim", claim.String())
				continue
			}
			labels[k] = v
		}
	}

	// for APIBindings we have to set the constant label value on itself to make the APIBinding
	// pointing to an APIExport visible to the owner of the export, independently of the permission claim
	// acceptance of the binding.
	if groupResource.Group == apis.GroupName && groupResource.Resource == "apibindings" {
		binding, err := l.getAPIBinding(cluster, resourceName)
		if err != nil {
			logger.Error(err, "error getting APIBinding", "bindingName", resourceName)
			return labels, nil // can only be a NotFound
		}

		path := logicalcluster.NewPath(binding.Spec.Reference.Export.Path)
		if path.Empty() {
			path = logicalcluster.From(binding).Path()
		}
		export, err := l.getAPIExport(path, binding.Spec.Reference.Export.Name)
		if err == nil {
			k, v := permissionclaims.ToReflexiveAPIBindingLabelKeyAndValue(logicalcluster.From(export), binding.Spec.Reference.Export.Name)
			if _, found := labels[k]; !found {
				labels[k] = v
			}
		}
	}

	return labels, nil
}

// InstallIndexers adds the additional indexers that this controller requires to the informers.
func InstallIndexers(apiExportInformer apisv1alpha2informers.APIExportClusterInformer) {
	indexers.AddIfNotPresentOrDie(apiExportInformer.Informer().GetIndexer(), cache.Indexers{
		indexers.ByLogicalClusterPathAndName: indexers.IndexByLogicalClusterPathAndName,
	})
}
