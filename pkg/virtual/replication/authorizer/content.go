/*
Copyright 2025 The KCP Authors.

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

package authorizer

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	cachev1alpha1 "github.com/kcp-dev/sdk/apis/cache/v1alpha1"
	kcpinformers "github.com/kcp-dev/sdk/client/informers/externalversions"

	"github.com/kcp-dev/kcp/pkg/authorization/delegated"
	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/informer"
	"github.com/kcp-dev/kcp/pkg/reconciler/apis/apibinding"
	dynamiccontext "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/context"
	"github.com/kcp-dev/kcp/pkg/virtual/replication/apidomainkey"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
)

type contentAuthorizer struct {
	getAPIBinding                               func(cluster logicalcluster.Name, name string) (*apisv1alpha2.APIBinding, error)
	getAPIExportsByVirtualResourceIdentityAndGR func(vrIdentity string, gr schema.GroupResource) ([]*apisv1alpha2.APIExport, error)
	getAPIExportByPath                          func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error)
	getCachedResource                           func(cluster logicalcluster.Name, name string) (*cachev1alpha1.CachedResource, error)
	getLogicalCluster                           func(clusterName logicalcluster.Name) (*corev1alpha1.LogicalCluster, error)

	newDelegatedAuthorizer func(cluster logicalcluster.Name) (authorizer.Authorizer, error)
}

var readOnlyVerbs = sets.New("get", "list", "watch")

// NewContentAuthorizer creates an authorizer that checks apiexports/content permission
// on relevant APIExports that export the CachedResource in the request URL.
// APIExports must have identity that matches the one specified in the request URL.
func NewContentAuthorizer(
	kubeClusterClient kcpkubernetesclientset.ClusterInterface,
	localKcpInformers kcpinformers.SharedInformerFactory,
	globalKcpInformers kcpinformers.SharedInformerFactory,
) authorizer.Authorizer {
	return &contentAuthorizer{
		getAPIBinding: func(cluster logicalcluster.Name, name string) (*apisv1alpha2.APIBinding, error) {
			return localKcpInformers.Apis().V1alpha2().APIBindings().Lister().Cluster(cluster).Get(name)
		},

		getAPIExportsByVirtualResourceIdentityAndGR: func(vrIdentity string, gr schema.GroupResource) ([]*apisv1alpha2.APIExport, error) {
			return indexers.ByIndex[*apisv1alpha2.APIExport](
				globalKcpInformers.Apis().V1alpha2().APIExports().Informer().GetIndexer(),
				indexers.APIExportByVirtualResourceIdentitiesAndGRs,
				indexers.VirtualResourceIdentityAndGRKey(vrIdentity, gr),
			)
		},

		getAPIExportByPath: func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error) {
			return indexers.ByPathAndNameWithFallback[*apisv1alpha2.APIExport](
				apisv1alpha2.Resource("apiexports"),
				localKcpInformers.Apis().V1alpha2().APIExports().Informer().GetIndexer(),
				globalKcpInformers.Apis().V1alpha2().APIExports().Informer().GetIndexer(),
				path,
				name,
			)
		},

		getLogicalCluster: func(clusterName logicalcluster.Name) (*corev1alpha1.LogicalCluster, error) {
			return localKcpInformers.Core().V1alpha1().LogicalClusters().Lister().Cluster(clusterName).Get(corev1alpha1.LogicalClusterName)
		},

		getCachedResource: informer.NewScopedGetterWithFallback(
			localKcpInformers.Cache().V1alpha1().CachedResources().Lister(),
			globalKcpInformers.Cache().V1alpha1().CachedResources().Lister(),
		),

		newDelegatedAuthorizer: func(cluster logicalcluster.Name) (authorizer.Authorizer, error) {
			return delegated.NewDelegatedAuthorizer(cluster, kubeClusterClient, delegated.Options{})
		},
	}
}

func (a *contentAuthorizer) Authorize(ctx context.Context, attr authorizer.Attributes) (authorizer.Decision, string, error) {
	if !readOnlyVerbs.Has(attr.GetVerb()) {
		return authorizer.DecisionDeny, "write access to Replication virtual workspace is not allowed", nil
	}

	parsedKey, err := apidomainkey.Parse(dynamiccontext.APIDomainKeyFrom(ctx))
	if err != nil {
		return authorizer.DecisionNoOpinion, "",
			fmt.Errorf("invalid API domain key")
	}

	targetCluster, err := genericapirequest.ValidClusterFrom(ctx)
	if err != nil {
		return authorizer.DecisionNoOpinion, "", fmt.Errorf("error getting valid cluster from context: %w", err)
	}

	cachedResource, err := a.getCachedResource(parsedKey.CachedResourceCluster, parsedKey.CachedResourceName)
	if err != nil {
		return authorizer.DecisionNoOpinion, "", err
	}
	wrappedGR := schema.GroupResource{
		Group:    cachedResource.Spec.Group,
		Resource: cachedResource.Spec.Resource,
	}

	var exports []*apisv1alpha2.APIExport

	if targetCluster.Wildcard || !attr.IsResourceRequest() {
		// For non-resource or wildcard requests we need to check all relevant APIExports.
		exports, err = a.getAPIExportsByVirtualResourceIdentityAndGR(cachedResource.Status.IdentityHash, wrappedGR)
		if err != nil {
			return authorizer.DecisionNoOpinion, "", err
		}
	} else {
		// We have a request against a concrete cluster. There should be an associated binding in that cluster.
		lc, err := a.getLogicalCluster(targetCluster.Name)
		if err != nil {
			return authorizer.DecisionNoOpinion, "logical cluster not found", err
		}
		boundResources, err := apibinding.GetResourceBindings(lc)
		if err != nil {
			return authorizer.DecisionNoOpinion, "failed to retrieve bound resources", err
		}

		var binding *apisv1alpha2.APIBinding
		if boundResourceLock, hasBinding := boundResources[wrappedGR.String()]; hasBinding {
			binding, err = a.getAPIBinding(targetCluster.Name, boundResourceLock.Name)
		}
		if err != nil || binding == nil {
			return authorizer.DecisionDeny, "could not find suitable APIBinding in target logical cluster", nil //nolint:nilerr // this is on purpose, we want to deny, not return a server error
		}
		path := logicalcluster.NewPath(binding.Spec.Reference.Export.Path)
		if path.Empty() {
			path = logicalcluster.From(binding).Path()
		}
		export, err := a.getAPIExportByPath(path, binding.Spec.Reference.Export.Name)
		if err != nil {
			return authorizer.DecisionNoOpinion, "APIExport not found", err
		}
		exports = append(exports, export)
	}

	// Make sure the user has apiexports/content permissions to the exports that refer to this CachedResource resource.

	SARAttributes := authorizer.AttributesRecord{
		APIGroup:        apisv1alpha1.SchemeGroupVersion.Group,
		APIVersion:      apisv1alpha1.SchemeGroupVersion.Version,
		User:            attr.GetUser(),
		Verb:            attr.GetVerb(),
		Resource:        "apiexports",
		ResourceRequest: true,
		Subresource:     "content",
	}

	var seenCachedResourceReference bool
	for _, export := range exports {
		for _, resource := range export.Spec.Resources {
			if resource.Storage.Virtual == nil ||
				resource.Storage.Virtual.IdentityHash != cachedResource.Status.IdentityHash {
				continue
			}
			if resource.Group != cachedResource.Spec.Group ||
				resource.Name != cachedResource.Spec.Resource {
				continue
			}
			if resource.Storage.Virtual.Reference.APIGroup == nil ||
				*resource.Storage.Virtual.Reference.APIGroup != cachev1alpha1.SchemeGroupVersion.Group ||
				resource.Storage.Virtual.Reference.Kind != "CachedResourceEndpointSlice" ||
				resource.Storage.Virtual.Reference.Name != cachedResource.Name {
				continue
			}

			authz, err := a.newDelegatedAuthorizer(logicalcluster.From(export))
			if err != nil {
				return authorizer.DecisionNoOpinion, "",
					fmt.Errorf("error creating delegated authorizer for API export %q, workspace %q: %w", export.Name, logicalcluster.From(export), err)
			}
			SARAttributes.Name = export.Name
			dec, reason, err := authz.Authorize(ctx, SARAttributes)
			if err != nil {
				return authorizer.DecisionNoOpinion, "",
					fmt.Errorf("error authorizing RBAC in API export %q, workspace %q: %w", export.Name, logicalcluster.From(export), err)
			}
			if dec != authorizer.DecisionAllow {
				return authorizer.DecisionDeny, reason, nil
			}

			seenCachedResourceReference = true
		}
	}

	if !seenCachedResourceReference {
		return authorizer.DecisionDeny, "failed to find suitable reason to allow access to CachedResource", nil
	}

	return authorizer.DecisionAllow, "found CachedResource reference", nil
}
