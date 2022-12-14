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

	"github.com/kcp-dev/logicalcluster/v3"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/apis/apis"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1/permissionclaims"
	apisv1alpha1informers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/logging"
)

// Labeler calculates labels to apply to all instances of a cluster-group-resource based on permission claims.
type Labeler struct {
	listAPIBindingsAcceptingClaimedGroupResource func(clusterName logicalcluster.Name, groupResource schema.GroupResource) ([]*apisv1alpha1.APIBinding, error)
	getAPIBinding                                func(clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIBinding, error)
	getAPIExport                                 func(path logicalcluster.Path, name string) (*apisv1alpha1.APIExport, error)
}

// NewLabeler returns a new Labeler.
func NewLabeler(
	apiBindingInformer apisv1alpha1informers.APIBindingClusterInformer,
	apiExportInformer apisv1alpha1informers.APIExportClusterInformer,
) *Labeler {

	indexers.AddIfNotPresentOrDie(apiExportInformer.Informer().GetIndexer(), cache.Indexers{
		indexers.ByLogicalClusterPathAndName: indexers.IndexByLogicalClusterPathAndName,
	})

	return &Labeler{
		listAPIBindingsAcceptingClaimedGroupResource: func(clusterName logicalcluster.Name, groupResource schema.GroupResource) ([]*apisv1alpha1.APIBinding, error) {
			indexKey := indexers.ClusterAndGroupResourceValue(clusterName, groupResource)
			return indexers.ByIndex[*apisv1alpha1.APIBinding](apiBindingInformer.Informer().GetIndexer(), indexers.APIBindingByClusterAndAcceptedClaimedGroupResources, indexKey)
		},

		getAPIBinding: func(clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIBinding, error) {
			return apiBindingInformer.Lister().Cluster(clusterName).Get(name)
		},
		getAPIExport: func(path logicalcluster.Path, name string) (*apisv1alpha1.APIExport, error) {
			objs, err := apiExportInformer.Informer().GetIndexer().ByIndex(indexers.ByLogicalClusterPathAndName, path.Join(name).String())
			if err != nil {
				return nil, err
			}
			if len(objs) == 0 {
				return nil, apierrors.NewNotFound(apisv1alpha1.Resource("apiexports"), path.Join(name).String())
			}
			if len(objs) > 1 {
				return nil, fmt.Errorf("multiple APIExports found for %s", path.Join(name).String())
			}
			return objs[0].(*apisv1alpha1.APIExport), nil
		},
	}
}

// LabelsFor returns all the applicable labels for the cluster-group-resource relating to permission claims. This is
// the intersection of (1) all APIBindings in the cluster that have accepted claims for the group-resource with (2)
// associated APIExports that are claiming group-resource.
func (l *Labeler) LabelsFor(ctx context.Context, cluster logicalcluster.Name, groupResource schema.GroupResource, resourceName string) (map[string]string, error) {
	labels := map[string]string{}

	bindings, err := l.listAPIBindingsAcceptingClaimedGroupResource(cluster, groupResource)
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
			if claim.State != apisv1alpha1.ClaimAccepted || claim.Group != groupResource.Group || claim.Resource != groupResource.Resource {
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
