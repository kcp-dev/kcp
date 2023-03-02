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

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/sdk/apis/apis"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1/permissionclaims"
	apisv1alpha1informers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/apis/v1alpha1"
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
	apiExportInformer, globalAPIExportInformer apisv1alpha1informers.APIExportClusterInformer,
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
			obj, err := indexers.ByPathAndName[*apisv1alpha1.APIExport](apisv1alpha1.Resource("apiexports"), apiExportInformer.Informer().GetIndexer(), path, name)
			if errors.IsNotFound(err) {
				obj, err = indexers.ByPathAndName[*apisv1alpha1.APIExport](apisv1alpha1.Resource("apiexports"), globalAPIExportInformer.Informer().GetIndexer(), path, name)
			}
			return obj, err
		},
	}
}

// LabelsFor returns all the applicable labels for the cluster-group-resource relating to permission claims. This is
// the intersection of (1) all APIBindings in the cluster that have accepted claims for the group-resource with (2)
// associated APIExports that are claiming group-resource.
func (l *Labeler) LabelsFor(ctx context.Context, cluster logicalcluster.Name, groupResource schema.GroupResource, resourceName, resourceNamespace string) (map[string]string, bool, error) {
	labels := map[string]string{}
	admit := true

	bindings, err := l.listAPIBindingsAcceptingClaimedGroupResource(cluster, groupResource)
	if err != nil {
		return nil, admit, fmt.Errorf("error listing APIBindings in %q accepting claimed group resource %q: %w", cluster, groupResource, err)
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
			// There is no PermissionClaim for this object, no need to compute selection or admission criteria
			if claim.State != apisv1alpha1.ClaimAccepted || claim.Group != groupResource.Group || claim.Resource != groupResource.Resource {
				continue
			}
			if !Selects(claim, resourceName, resourceNamespace) {
				// The permission claim is relevant for this object, but the object does not match the criteria for admission.
				admit = false
				continue
			}
			// if the object is selected, allow it to be admitted.
			admit = true
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
			return labels, admit, nil // can only be a NotFound
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

	return labels, admit, nil
}

// Selects indicates whether an object's name and namespace are selected by an accepted PermissionClaim.
func Selects(acceptableClaim apisv1alpha1.AcceptablePermissionClaim, name, namespace string) bool {
	claim := acceptableClaim.PermissionClaim

	// All and ResourceSelector are mutually exclusive. Validation should catch this, but don't leak info if it doesn't somehow.
	if claim.All && len(claim.ResourceSelector) > 0 {
		return false
	}

	// ResourceSelector nil check to be compatible with objects created prior to the addition of the All field
	if claim.All || len(claim.ResourceSelector) == 0 {
		return true
	}

	for _, selector := range claim.ResourceSelector {
		namespaceSelected, clusterScoped := selectsNamespaces(selector.Namespaces, namespace)

		if selectsName(selector.Names, name) && (namespaceSelected || clusterScoped) {
			return true
		}

	}

	return false
}

func selectsName(names []string, objectName string) bool {
	if len(names) == 0 {
		return true
	}

	if len(names) == 1 && names[0] == apisv1alpha1.ResourceSelectorAll {
		return true
	}

	validNames := sets.NewString(names...)
	if validNames.Has(objectName) {
		return true
	}

	return false
}

// selectsNamespaces determines if an object's namespace matches a set of namespace values, and if it is cluster-scoped.
func selectsNamespaces(namespaces []string, objectNamespace string) (bool, bool) {
	// match cluster-scoped resources
	if len(namespaces) == 0 && objectNamespace == "" {
		return true, true
	}
	if (len(namespaces) == 1 && namespaces[0] == "") && objectNamespace == "" {
		return true, true
	}

	// Match all workspaces
	if len(namespaces) == 1 && namespaces[0] == apisv1alpha1.ResourceSelectorAll {
		return true, false
	}

	// Match listed namespaces
	validNamespaces := sets.NewString(namespaces...)
	if validNamespaces.Has(objectNamespace) {
		return true, false
	}

	return false, false
}
