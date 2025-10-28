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
	"errors"
	"fmt"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	klabels "k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/jsonpath"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/logicalcluster/v3"

	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/reconciler/dynamicrestmapper"
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
	dynRESTMapper    *dynamicrestmapper.DynamicRESTMapper
	dynClusterClient kcpdynamic.ClusterInterface

	listAPIBindingsAcceptingClaimedGroupResource func(clusterName logicalcluster.Name, groupResource schema.GroupResource) ([]*apisv1alpha2.APIBinding, error)
	getAPIBinding                                func(clusterName logicalcluster.Name, name string) (*apisv1alpha2.APIBinding, error)
	getAPIExport                                 func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error)
}

// NewLabeler returns a new Labeler.
func NewLabeler(
	apiBindingInformer apisv1alpha2informers.APIBindingClusterInformer,
	apiExportInformer, globalAPIExportInformer apisv1alpha2informers.APIExportClusterInformer,
	dynRESTMapper *dynamicrestmapper.DynamicRESTMapper,
	dynClusterClient kcpdynamic.ClusterInterface,
) *Labeler {
	return &Labeler{
		dynRESTMapper:    dynRESTMapper,
		dynClusterClient: dynClusterClient,

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
func (l *Labeler) LabelsFor(ctx context.Context, cluster logicalcluster.Name, groupResource schema.GroupResource, claimedObject *unstructured.Unstructured) (map[string]string, error) {
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
			if claim.State != apisv1alpha2.ClaimAccepted || claim.Group != normalizedGR.Group || claim.Resource != normalizedGR.Resource {
				continue
			}

			claimLogger := logger.WithValues("claim", claim.String())

			if match, err := l.claimMatchesObject(ctx, cluster, &claim, claimedObject); err != nil {
				claimLogger.Error(err, "error matching object")
				continue
			} else if !match {
				continue
			}

			k, v, err := permissionclaims.ToLabelKeyAndValue(logicalcluster.From(export), export.Name, claim.PermissionClaim)
			if err != nil {
				// extremely unlikely to get an error here - it means the json marshaling failed
				claimLogger.Error(err, "error calculating permission claim label key and value")
				continue
			}
			labels[k] = v
		}
	}

	// for APIBindings we have to set the constant label value on itself to make the APIBinding
	// pointing to an APIExport visible to the owner of the export, independently of the permission claim
	// acceptance of the binding.
	if groupResource.Group == apis.GroupName && groupResource.Resource == "apibindings" {
		binding, err := l.getAPIBinding(cluster, claimedObject.GetName())
		if err != nil {
			logger.Error(err, "error getting APIBinding", "bindingName", claimedObject.GetName())
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

func (l *Labeler) claimMatchesObject(ctx context.Context, cluster logicalcluster.Name, claim *apisv1alpha2.AcceptablePermissionClaim, obj *unstructured.Unstructured) (bool, error) {
	if claim.Selector.MatchAll {
		return true, nil
	}

	if sel := claim.Selector.LabelSelector; len(sel.MatchLabels) > 0 || len(sel.MatchExpressions) > 0 {
		selector, err := metav1.LabelSelectorAsSelector(&sel)
		if err != nil {
			return false, fmt.Errorf("error calculating permission claim label key and value: %w", err)
		}

		return selector.Matches(klabels.Set(obj.GetLabels())), nil
	}

	for _, ref := range claim.Selector.References {
		match, err := l.referenceMatchesObject(ctx, cluster, ref, obj)
		if match || err != nil {
			return match, err
		}
	}

	return false, nil
}

func (l *Labeler) referenceMatchesObject(ctx context.Context, cluster logicalcluster.Name, ref apisv1alpha2.PermissionClaimReference, obj *unstructured.Unstructured) (bool, error) {
	// invalid objects should never have been admitted in the first place, this is just to catch possible panics
	if ref.JSONPath == nil {
		return false, errors.New("invalid claim: no JSONPath specified")
	}

	nameExpr := jsonpath.New("name").AllowMissingKeys(true)
	if err := nameExpr.Parse(ref.JSONPath.NamePath); err != nil {
		return false, fmt.Errorf("invalid claim: invalid JSONPath name expression: %w", err)
	}

	var namespaceExpr *jsonpath.JSONPath
	if ref.JSONPath.NamespacePath != "" {
		namespaceExpr = jsonpath.New("namespace").AllowMissingKeys(true)
		if err := namespaceExpr.Parse(ref.JSONPath.NamespacePath); err != nil {
			return false, fmt.Errorf("invalid claim: invalid JSONPath namespace expression: %w", err)
		}
	}

	gvrs, err := l.dynRESTMapper.ForCluster(cluster).ResourcesFor(schema.GroupVersionResource{
		Group:    ref.Group,
		Resource: ref.Resource,
		// Version is left blank to let the mapper decide
	})
	if err != nil {
		return false, fmt.Errorf("failed to resolve group resource %s/%s: %w", ref.Group, ref.Resource, err)
	}

	allObjects, err := l.dynClusterClient.Cluster(cluster.Path()).Resource(gvrs[0]).List(ctx, metav1.ListOptions{})
	if err != nil {
		return false, fmt.Errorf("failed to list referring resource: %w", err)
	}

	for _, referringObject := range allObjects.Items {
		results, err := nameExpr.FindResults(referringObject.Object)
		if err != nil {
			return false, fmt.Errorf("failed to apply JSONPath: %w", err)
		}

		if l := len(results); l == 0 {
			continue
		} else if l > 1 {
			return false, fmt.Errorf("invalid JSONPath, expected one result, got %d", l)
		}

		result := results[0]
		if l := len(result); l == 0 {
			continue
		} else if l > 1 {
			return false, fmt.Errorf("invalid JSONPath, expected one sub result, got %d", l)
		}

		resultValue, ok := result[0].Interface().(string)
		if !ok {
			// anything but a string doesn't mean the JSONPath is broken, but that the
			// object might be unexpected
			continue
		}

		fmt.Printf("value: %q\n", resultValue)

		// TODO: do the same for the namespace, implement the matching from the found
		// values to what is in the APIBinding, handle cluster-scoped to namspace-scoped
		// referencing.
	}

	return false, nil
}

// InstallIndexers adds the additional indexers that this controller requires to the informers.
func InstallIndexers(apiExportInformer apisv1alpha2informers.APIExportClusterInformer) {
	indexers.AddIfNotPresentOrDie(apiExportInformer.Informer().GetIndexer(), cache.Indexers{
		indexers.ByLogicalClusterPathAndName: indexers.IndexByLogicalClusterPathAndName,
	})
}
