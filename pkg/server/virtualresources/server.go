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

package virtualresources

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	apiextensionshelpers "k8s.io/apiextensions-apiserver/pkg/apihelpers"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apiserver/pkg/endpoints/handlers/responsewriters"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"

	cacheclient "github.com/kcp-dev/kcp/pkg/cache/client"
	"github.com/kcp-dev/kcp/pkg/cache/client/shard"
	"github.com/kcp-dev/kcp/pkg/endpointslice"
	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/reconciler/dynamicrestmapper"
	kcpfilters "github.com/kcp-dev/kcp/pkg/server/filters"
)

var (
	errorScheme = runtime.NewScheme()
	errorCodecs = serializer.NewCodecFactory(errorScheme)
)

func init() {
	errorScheme.AddUnversionedTypes(metav1.Unversioned,
		&metav1.Status{},
	)
}

type Server struct {
	GenericAPIServer *genericapiserver.GenericAPIServer
	Extra            *ExtraConfig
	drm              *dynamicrestmapper.DynamicRESTMapper
	delegate         http.Handler

	getCRD                       func(cluster logicalcluster.Name, name string) (*apiextensionsv1.CustomResourceDefinition, error)
	getUnstructuredEndpointSlice func(ctx context.Context, cluster logicalcluster.Name, shard shard.Name, gvr schema.GroupVersionResource, name string) (*unstructured.Unstructured, error)
	getAPIExportByPath           func(clusterPath logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error)
}

func NewServer(c CompletedConfig, delegationTarget genericapiserver.DelegationTarget, drm *dynamicrestmapper.DynamicRESTMapper) (*Server, error) {
	s := &Server{
		drm:      drm,
		Extra:    c.Extra,
		delegate: delegationTarget.UnprotectedHandler(),

		getUnstructuredEndpointSlice: func(ctx context.Context, cluster logicalcluster.Name, shardName shard.Name, gvr schema.GroupVersionResource, name string) (*unstructured.Unstructured, error) {
			// We assume the endpoint slice is cluster-scoped.
			return c.Extra.DynamicClusterClient.
				Cluster(cluster.Path()).
				Resource(gvr).
				Get(cacheclient.WithShardInContext(ctx, shardName), name, metav1.GetOptions{})
		},
		getCRD: func(clusterName logicalcluster.Name, name string) (*apiextensionsv1.CustomResourceDefinition, error) {
			return c.Extra.CRDLister.Lister().Cluster(clusterName).Get(name)
		},
		getAPIExportByPath: func(clusterPath logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error) {
			return indexers.ByPathAndNameWithFallback[*apisv1alpha2.APIExport](
				apisv1alpha2.Resource("apiexports"),
				c.Extra.LocalAPIExportInformer.Informer().GetIndexer(),
				c.Extra.GlobalAPIExportInformer.Informer().GetIndexer(),
				clusterPath,
				name,
			)
		},
	}

	var err error
	s.GenericAPIServer, err = c.Generic.New("virtual-resources-root-apiserver", delegationTarget)
	if err != nil {
		return nil, err
	}

	// We don't do discovery at all because it needs to be aggregated with other CRD-based resources.
	s.GenericAPIServer.DiscoveryGroupManager = nil
	s.GenericAPIServer.Handler.NonGoRestfulMux.HandlePrefix("/apis/", s.newApisHandler())

	return s, nil
}

func splitPath(path string) []string {
	path = strings.Trim(path, "/")
	if path == "" {
		return []string{}
	}
	return strings.Split(path, "/")
}

func (s *Server) newApisHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if len(splitPath(r.URL.Path)) > 3 {
			s.handleResource(w, r)
			return
		}

		s.delegate.ServeHTTP(w, r)
	}
}

func (s *Server) handleResource(w http.ResponseWriter, r *http.Request) {
	pathParts := splitPath(r.URL.Path)
	// Only match /apis/<group>/<version>/<resource>/...
	if len(pathParts) <= 3 || pathParts[0] != "apis" {
		s.delegate.ServeHTTP(w, r)
		return
	}

	ctx := r.Context()
	requestInfo, ok := genericapirequest.RequestInfoFrom(ctx)
	if !ok {
		responsewriters.ErrorNegotiated(
			apierrors.NewInternalError(fmt.Errorf("no RequestInfo found in the context")),
			errorCodecs, schema.GroupVersion{}, w, r,
		)
		return
	}
	if !requestInfo.IsResourceRequest {
		// Discovery requests should have been caught earlier.
		// Maybe the delegate knows what to do.
		s.delegate.ServeHTTP(w, r)
		return
	}

	clusterNameOrWildcard, wildcard, err := genericapirequest.ClusterNameOrWildcardFrom(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if wildcard {
		clusterNameOrWildcard = "*"
	}

	gr := schema.GroupResource{
		Group:    requestInfo.APIGroup,
		Resource: requestInfo.Resource,
	}
	if gr.Group == "" {
		gr.Group = "core"
	}

	// partialMetadataRequest := kcpfilters.IsPartialMetadataRequest(ctx)
	identity := kcpfilters.IdentityFromContext(ctx)

	apiBinding, err := s.getAPIBindingForRequest(clusterNameOrWildcard.String(), gr, identity)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if apiBinding == nil {
		// Not a virtual resource: the resource is not provided by an APIBinding.
		s.delegate.ServeHTTP(w, r)
		return
	}

	var crdName string
	for _, boundResource := range apiBinding.Status.BoundResources {
		if boundResource.Group == gr.Group && boundResource.Resource == gr.Resource {
			crdName = boundResource.Schema.UID

			if len(boundResource.StorageVersions) > 0 {
				// Virtual resources have zero storage versions, because they don't
				// use CRD storage. This resource is definitely not a VR.
				s.delegate.ServeHTTP(w, r)
				return
			}

			break
		}
	}
	if crdName == "" {
		// This should not happen, the indexers returned a binding for this specific GR.
		responsewriters.ErrorNegotiated(
			apierrors.NewInternalError(fmt.Errorf("resource not available")),
			errorCodecs, schema.GroupVersion{Group: requestInfo.APIGroup, Version: requestInfo.APIVersion}, w, r,
		)
		return
	}

	// We do what the apiextensions apiserver does: return 404 on not found, !NamesAccepted or !Established.
	crd, err := s.getCRD(logicalcluster.Name("system:bound-crds"), crdName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			responsewriters.ErrorNegotiated(
				apierrors.NewNotFound(schema.GroupResource{Group: requestInfo.APIGroup, Resource: requestInfo.Resource}, requestInfo.Name),
				errorCodecs, schema.GroupVersion{Group: requestInfo.APIGroup, Version: requestInfo.APIVersion}, w, r,
			)
			return
		}
		utilruntime.HandleError(err)
		responsewriters.ErrorNegotiated(
			apierrors.NewInternalError(fmt.Errorf("error resolving resource: %v", err)),
			errorCodecs, schema.GroupVersion{Group: requestInfo.APIGroup, Version: requestInfo.APIVersion}, w, r,
		)
		return
	}
	if !apiextensionshelpers.IsCRDConditionTrue(crd, apiextensionsv1.NamesAccepted) &&
		!apiextensionshelpers.IsCRDConditionTrue(crd, apiextensionsv1.Established) {
		responsewriters.ErrorNegotiated(
			apierrors.NewNotFound(schema.GroupResource{Group: requestInfo.APIGroup, Resource: requestInfo.Resource}, requestInfo.Name),
			errorCodecs, schema.GroupVersion{Group: requestInfo.APIGroup, Version: requestInfo.APIVersion}, w, r,
		)
		return
	}

	// Get the origin APIExport, and check that the resource has virtual storage. Otherwise delegate the request.

	apiExportPath := logicalcluster.NewPath(apiBinding.Spec.Reference.Export.Path)
	if apiExportPath.Empty() {
		apiExportPath = logicalcluster.NewPath(logicalcluster.From(apiBinding).String())
	}
	apiExport, err := s.getAPIExportByPath(apiExportPath, apiBinding.Spec.Reference.Export.Name)
	if err != nil {
		utilruntime.HandleError(err)
		responsewriters.ErrorNegotiated(
			apierrors.NewInternalError(fmt.Errorf("error resolving resource: %v", err)),
			errorCodecs, schema.GroupVersion{Group: requestInfo.APIGroup, Version: requestInfo.APIVersion}, w, r,
		)
		return
	}

	var virtualStorage *apisv1alpha2.ResourceSchemaStorageVirtual
	for _, resource := range apiExport.Spec.Resources {
		if resource.Storage.Virtual != nil &&
			resource.Group == gr.Group &&
			resource.Name == gr.Resource {
			virtualStorage = resource.Storage.Virtual
			break
		}
	}
	if virtualStorage == nil {
		// Not a virtual resource: the binding's export doesn't define such resource with virtual storage.
		s.delegate.ServeHTTP(w, r)
		return
	}

	// We have a virtual resource. Get the endpoint URL, create a proxy handler and serve from that endpoint.

	apiExportShard := shard.Name(apiExport.Annotations[shard.AnnotationKey])
	if apiExportShard.Empty() {
		apiExportShard = shard.New(s.Extra.ShardName)
	}
	vrEndpointURL, err := s.getVirtualResourceURL(ctx, logicalcluster.From(apiExport), apiExportShard, virtualStorage)
	if err != nil {
		utilruntime.HandleError(err)
		if deserializeErr, ok := err.(*endpointslice.DeserializeError); ok {
			if deserializeErr.Code == endpointslice.NoEndpoints {
				responsewriters.ErrorNegotiated(
					apierrors.NewInternalError(fmt.Errorf("error resolving resource %s; please contact APIExport owner to resolve", gr)),
					errorCodecs, schema.GroupVersion{Group: requestInfo.APIGroup, Version: requestInfo.APIVersion}, w, r,
				)
				return
			}
		}
		responsewriters.ErrorNegotiated(
			apierrors.NewInternalError(fmt.Errorf("error resolving resource: %v", err)),
			errorCodecs, schema.GroupVersion{Group: requestInfo.APIGroup, Version: requestInfo.APIVersion}, w, r,
		)
		return
	}

	vrHandler, err := newVirtualResourceHandler(s.Extra.VWClientConfig, vrEndpointURL, clusterNameOrWildcard.String())
	if err != nil {
		utilruntime.HandleError(err)
		responsewriters.ErrorNegotiated(
			apierrors.NewInternalError(fmt.Errorf("error serving resource: %v", err)),
			errorCodecs, schema.GroupVersion{Group: requestInfo.APIGroup, Version: requestInfo.APIVersion}, w, r,
		)
		return
	}

	vrHandler.ServeHTTP(w, r)
}

func (s *Server) getVirtualResourceURL(ctx context.Context, apiExportCluster logicalcluster.Name, apiExportShard shard.Name, virtual *apisv1alpha2.ResourceSchemaStorageVirtual) (string, error) {
	sliceMapping, err := s.drm.ForCluster(apiExportCluster).RESTMapping(schema.GroupKind{
		Group: ptr.Deref(virtual.Reference.APIGroup, ""),
		Kind:  virtual.Reference.Kind,
	})
	if err != nil {
		return "", err
	}

	slice, err := s.getUnstructuredEndpointSlice(ctx, apiExportCluster, apiExportShard, schema.GroupVersionResource{
		Group:    sliceMapping.Resource.Group,
		Version:  sliceMapping.Resource.Version,
		Resource: sliceMapping.Resource.Resource,
	}, virtual.Reference.Name)
	if err != nil {
		return "", err
	}

	urls, err := endpointslice.ListURLsFromUnstructured(*slice)
	if err != nil {
		return "", err
	}

	return endpointslice.FindOneURL(s.Extra.ShardVirtualWorkspaceURLGetter(), urls)
}

func (s *Server) getAPIBindingForRequest(
	clusterNameOrWildcard string,
	gr schema.GroupResource,
	identity string,
) (*apisv1alpha2.APIBinding, error) {
	var (
		apiBindings []*apisv1alpha2.APIBinding
		err         error
	)
	if clusterNameOrWildcard == "*" {
		apiBindings, err = indexers.ByIndex[*apisv1alpha2.APIBinding](
			s.Extra.APIBindingInformer.Informer().GetIndexer(),
			indexers.APIBindingByIdentityAndGroupResource,
			indexers.IdentityGroupResourceKeyFunc(identity, gr.Group, gr.Resource),
		)
	} else {
		apiBindings, err = indexers.ByIndex[*apisv1alpha2.APIBinding](
			s.Extra.APIBindingInformer.Informer().GetIndexer(),
			indexers.APIBindingByBoundResources,
			indexers.APIBindingBoundResourceValue(logicalcluster.Name(clusterNameOrWildcard), gr.Group, gr.Resource),
		)
	}
	if err != nil {
		return nil, err
	}

	if len(apiBindings) > 0 {
		// Matching cluster/identity and bound GR should mean we have the correct APIBinding.
		// This is similar to what we're doing in apiBindingAwareCRDLister when selecting
		// a binding by identity wildcard.
		return apiBindings[0], nil
	}

	// This GR does not seem to be provided by an APIBinding.
	return nil, nil
}

func newVirtualResourceHandler(cfg *rest.Config, vwURL, clusterNameOrWildcard string) (http.Handler, error) {
	scopedURL, err := url.Parse(virtualResourceURLWithCluster(vwURL, clusterNameOrWildcard))
	if err != nil {
		return nil, err
	}

	tr, err := rest.TransportFor(cfg)
	if err != nil {
		return nil, err
	}

	proxy := httputil.NewSingleHostReverseProxy(scopedURL)
	proxy.Transport = tr

	return proxy, nil
}

func virtualResourceURLWithCluster(vwURL, clusterNameOrWildcard string) string {
	// Formats the URL like so:
	//     <Virtual resource VW endpoint>/clusters/<Target cluster>
	// E.g.:
	//     /services/replication/1oget0q1249b2vcy/sheriffs/clusters/385doly4poks8a45/apis/wildwest.dev/v1alpha1/sheriffs
	return fmt.Sprintf("%s/clusters/%s", vwURL, clusterNameOrWildcard)
}
