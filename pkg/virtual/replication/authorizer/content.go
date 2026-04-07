/*
Copyright 2025 The kcp Authors.

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
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	kcpinformers "github.com/kcp-dev/sdk/client/informers/externalversions"

	"github.com/kcp-dev/kcp/pkg/authorization/delegated"
	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/informer"
	"github.com/kcp-dev/kcp/pkg/reconciler/apis/apibinding"
	"github.com/kcp-dev/kcp/pkg/virtual/replication/apidomainkey"
)

type contentAuthorizer struct {
	getCachedResourceEndpointSlice func(cluster logicalcluster.Name, name string) (*cachev1alpha1.CachedResourceEndpointSlice, error)
	getAPIExportByPath             func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error)
	getAPIBinding                  func(cluster logicalcluster.Name, name string) (*apisv1alpha2.APIBinding, error)
	getLogicalCluster              func(clusterName logicalcluster.Name) (*corev1alpha1.LogicalCluster, error)

	newDelegatedAuthorizer func(cluster logicalcluster.Name) (authorizer.Authorizer, error)
}

var readOnlyVerbs = sets.New("get", "list", "watch")

// NewContentAuthorizer creates an authorizer that checks apiexports/content permission
// on the APIExport referenced by the CachedResourceEndpointSlice in the request URL.
func NewContentAuthorizer(
	kubeClusterClient kcpkubernetesclientset.ClusterInterface,
	localKcpInformers kcpinformers.SharedInformerFactory,
	globalKcpInformers kcpinformers.SharedInformerFactory,
) authorizer.Authorizer {
	return &contentAuthorizer{
		getCachedResourceEndpointSlice: informer.NewScopedGetterWithFallback(
			localKcpInformers.Cache().V1alpha1().CachedResourceEndpointSlices().Lister(),
			globalKcpInformers.Cache().V1alpha1().CachedResourceEndpointSlices().Lister(),
		),

		getAPIExportByPath: func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error) {
			return indexers.ByPathAndNameWithFallback[*apisv1alpha2.APIExport](
				apisv1alpha2.Resource("apiexports"),
				localKcpInformers.Apis().V1alpha2().APIExports().Informer().GetIndexer(),
				globalKcpInformers.Apis().V1alpha2().APIExports().Informer().GetIndexer(),
				path,
				name,
			)
		},

		getAPIBinding: func(cluster logicalcluster.Name, name string) (*apisv1alpha2.APIBinding, error) {
			return localKcpInformers.Apis().V1alpha2().APIBindings().Lister().Cluster(cluster).Get(name)
		},

		getLogicalCluster: func(clusterName logicalcluster.Name) (*corev1alpha1.LogicalCluster, error) {
			return localKcpInformers.Core().V1alpha1().LogicalClusters().Lister().Cluster(clusterName).Get(corev1alpha1.LogicalClusterName)
		},

		newDelegatedAuthorizer: func(cluster logicalcluster.Name) (authorizer.Authorizer, error) {
			return delegated.NewDelegatedAuthorizer(cluster, kubeClusterClient, delegated.Options{})
		},
	}
}

func (a *contentAuthorizer) Authorize(ctx context.Context, attr authorizer.Attributes) (authorizer.Decision, string, error) {
	if !readOnlyVerbs.Has(attr.GetVerb()) {
		return authorizer.DecisionDeny, "write access to Replication virtual workspace is not allowed", nil
	}

	apiDomianKey, err := apidomainkey.FromContext(ctx)
	if err != nil {
		return authorizer.DecisionNoOpinion, "", fmt.Errorf("invalid API domain key")
	}

	targetCluster, err := genericapirequest.ValidClusterFrom(ctx)
	if err != nil {
		return authorizer.DecisionNoOpinion, "", fmt.Errorf("error getting valid cluster from context: %w", err)
	}

	slice, err := a.getCachedResourceEndpointSlice(apiDomianKey.Cluster, apiDomianKey.Name)
	if err != nil {
		return authorizer.DecisionNoOpinion, "", err
	}
	exportPath := logicalcluster.NewPath(slice.Spec.APIExport.Path)
	if exportPath.Empty() {
		exportPath = logicalcluster.From(slice).Path()
	}
	export, err := a.getAPIExportByPath(exportPath, slice.Spec.APIExport.Name)
	if err != nil {
		return authorizer.DecisionNoOpinion, "APIExport not found", err
	}

	if !targetCluster.Wildcard && attr.IsResourceRequest() {
		// For a concrete cluster request, verify the target cluster has a binding to this export.
		lc, err := a.getLogicalCluster(targetCluster.Name)
		if err != nil {
			return authorizer.DecisionNoOpinion, "logical cluster not found", err
		}
		boundResources, err := apibinding.GetResourceBindings(lc)
		if err != nil {
			return authorizer.DecisionNoOpinion, "failed to retrieve bound resources", err
		}

		// Find the GR from the export's resource entry that references this endpoint slice.
		var wrappedGR schema.GroupResource
		for _, res := range export.Spec.Resources {
			if res.Storage.Virtual != nil &&
				res.Storage.Virtual.Reference.Name == slice.Name {
				wrappedGR = schema.GroupResource{Group: res.Group, Resource: res.Name}
				break
			}
		}

		var binding *apisv1alpha2.APIBinding
		if boundResourceLock, hasBinding := boundResources[wrappedGR.String()]; hasBinding {
			binding, err = a.getAPIBinding(targetCluster.Name, boundResourceLock.Name)
		}
		if err != nil || binding == nil {
			return authorizer.DecisionDeny, "could not find suitable APIBinding in target logical cluster", nil //nolint:nilerr // this is on purpose, we want to deny, not return a server error
		}
	}

	// Check that the user has apiexports/content permission on the export.
	authz, err := a.newDelegatedAuthorizer(logicalcluster.From(export))
	if err != nil {
		return authorizer.DecisionNoOpinion, "",
			fmt.Errorf("error creating delegated authorizer for API export %q, workspace %q: %w", export.Name, logicalcluster.From(export), err)
	}
	SARAttributes := authorizer.AttributesRecord{
		APIGroup:        apisv1alpha1.SchemeGroupVersion.Group,
		APIVersion:      apisv1alpha1.SchemeGroupVersion.Version,
		User:            attr.GetUser(),
		Verb:            attr.GetVerb(),
		Resource:        "apiexports",
		ResourceRequest: true,
		Subresource:     "content",
		Name:            export.Name,
	}
	dec, reason, err := authz.Authorize(ctx, SARAttributes)
	if err != nil {
		return authorizer.DecisionNoOpinion, "",
			fmt.Errorf("error authorizing RBAC in API export %q, workspace %q: %w", export.Name, logicalcluster.From(export), err)
	}
	if dec != authorizer.DecisionAllow {
		return authorizer.DecisionDeny, reason, nil
	}

	return authorizer.DecisionAllow, "found CachedResource reference", nil
}
