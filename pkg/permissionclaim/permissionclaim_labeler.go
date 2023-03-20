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

	"github.com/davecgh/go-spew/spew"
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
// associated APIExports that are claiming group-resource and possibly namespace/name.
func (l *Labeler) LabelsFor(ctx context.Context, cluster logicalcluster.Name, groupResource schema.GroupResource, resourceNamespace, resourceName string) (map[string]string, error) {
	labels := map[string]string{}

	logger := klog.FromContext(ctx)

	err := l.visitBindingsAndClaims(ctx, cluster, groupResource, resourceNamespace, resourceName,
		func(binding *apisv1alpha1.APIBinding, claim apisv1alpha1.AcceptablePermissionClaim, namespace, name string) {
			path := logicalcluster.NewPath(binding.Spec.Reference.Export.Path)
			if path.Empty() {
				path = logicalcluster.From(binding).Path()
			}
			export, err := l.getAPIExport(path, binding.Spec.Reference.Export.Name)
			if err != nil {
				return
			}
			if !Selects(claim, resourceNamespace, resourceName) {
				// The permission claim is relevant for this object, but the object does not match the criteria
				return
			}
			k, v, err := permissionclaims.ToLabelKeyAndValue(logicalcluster.From(export), export.Name, claim.PermissionClaim)
			if err != nil {
				// extremely unlikely to get an error here - it means the json marshaling failed
				logger.Error(err, "error calculating permission claim label key and value",
					"claim", claim.String())
				return
			}
			labels[k] = v
		})
	if err != nil {
		logger.Error(err, "error processing permissionclaims")
		return labels, nil
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

// ObjectPermitted determines if any PermissionClaims apply to a given object's GroupResource, namespace, or name.
// If no APIBinding or PermissionClaim exists for the GroupResource, the object is permitted.
func (l *Labeler) ObjectPermitted(ctx context.Context, cluster logicalcluster.Name, groupResource schema.GroupResource, resourceNamespace, resourceName string) (bool, error) {
	permitted := true

	logger := klog.FromContext(ctx)
	logger = logger.WithValues("cluster", cluster.String(), "groupResource", groupResource, "namespace", resourceNamespace, "name", resourceName)

	err := l.visitBindingsAndClaims(ctx, cluster, groupResource, resourceNamespace, resourceName,
		func(apiBinding *apisv1alpha1.APIBinding, claim apisv1alpha1.AcceptablePermissionClaim, namespace, name string) {
			logger := logging.WithObject(logger, apiBinding)
			logger.Info("visiting claims")
			for _, claim := range apiBinding.Spec.PermissionClaims {
				logger.WithValues("claim", spew.Sdump(claim)).Info("looking at claim")
				if !Selects(claim, resourceNamespace, resourceName) {
					logger.Info("not permitted")
					// The permission claim is relevant for this object, but the object does not match the criteria
					permitted = false
					return
				}
			}
		})
	if err != nil {
		return false, fmt.Errorf("error processing permissionclaims, %w", err)
	}
	logger.Info("decision", "permitted", permitted)
	return permitted, nil
}

// Selects indicates whether an object's name and namespace are selected by an accepted PermissionClaim.
func Selects(acceptableClaim apisv1alpha1.AcceptablePermissionClaim, namespace, name string) bool {
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
		if selectsNames(selector.Names, name) && selectsNamespaces(selector.Namespaces, namespace) {
			return true
		}
	}

	return false
}

func toStrings(names []apisv1alpha1.Name) []string {
	ret := make([]string, 0, len(names))
	for _, name := range names {
		ret = append(ret, string(name))
	}
	return ret
}

// selectsNames determines if an object's name matches a set of name values.
func selectsNames(names []apisv1alpha1.Name, objectName string) bool {
	// No name selector was provided.
	if len(names) == 0 {
		return true
	}

	validNames := sets.NewString(toStrings(names)...)

	// A value of "*" anywhere in the list means all names are claimed.
	if validNames.Has(apisv1alpha1.ResourceSelectorAll) {
		return true
	}

	// Match listed names.
	return validNames.Has(objectName)
}

// selectsNamespaces determines if an object's namespace matches a set of namespace values, and if it is cluster-scoped.
func selectsNamespaces(namespaces []apisv1alpha1.Name, objectNamespace string) bool {
	// match cluster-scoped resources
	if len(namespaces) == 0 && objectNamespace == "" {
		return true
	}

	validNamespaces := sets.NewString(toStrings(namespaces)...)

	// Match all namespaces for namespaced objects.
	if validNamespaces.Has(apisv1alpha1.ResourceSelectorAll) {
		return true
	}

	// Match listed namespaces.
	return validNamespaces.Has(objectNamespace)
}

func (l *Labeler) visitBindingsAndClaims(
	ctx context.Context,
	cluster logicalcluster.Name,
	groupResource schema.GroupResource,
	resourceNamespace, resourceName string,
	visit func(apiBinding *apisv1alpha1.APIBinding, claim apisv1alpha1.AcceptablePermissionClaim, namespace,
		name string),
) error {
	bindings, err := l.listAPIBindingsAcceptingClaimedGroupResource(cluster, groupResource)
	if err != nil {
		return err
	}

	for _, binding := range bindings {
		for _, claim := range binding.Spec.PermissionClaims {
			if claim.State != apisv1alpha1.ClaimAccepted || claim.Group != groupResource.Group || claim.Resource != groupResource.Resource {
				// There is no PermissionClaim for this object, no need to compute selection
				continue
			}

			visit(binding, claim, resourceNamespace, resourceName)
		}
	}
	return nil
}
