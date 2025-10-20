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

package aggregatingcrdversiondiscovery

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	autoscaling "k8s.io/api/autoscaling/v1"
	apiextensionshelpers "k8s.io/apiextensions-apiserver/pkg/apihelpers"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apiserver/pkg/endpoints/discovery"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/apiserver/pkg/warning"
	"k8s.io/client-go/rest"

	kcpdiscovery "github.com/kcp-dev/client-go/discovery"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"

	cacheclient "github.com/kcp-dev/kcp/pkg/cache/client"
	"github.com/kcp-dev/kcp/pkg/cache/client/shard"
	"github.com/kcp-dev/kcp/pkg/indexers"
)

var (
	scheme = runtime.NewScheme()
	codecs = serializer.NewCodecFactory(scheme)

	unversionedVersion = schema.GroupVersion{Group: "", Version: "v1"}
	unversionedTypes   = []runtime.Object{
		&metav1.APIResourceList{},
	}
)

func init() {
	// we need to add the options to empty v1
	metav1.AddToGroupVersion(scheme, schema.GroupVersion{Group: "", Version: "v1"})

	scheme.AddUnversionedTypes(unversionedVersion, unversionedTypes...)
}

type Server struct {
	GenericAPIServer *genericapiserver.GenericAPIServer
	Extra            *ExtraConfig
	delegate         http.Handler

	// Resources in /apis/<Group>/<Version> may use storage other than CRD.
	// Virtual resources are backed by virtual workspaces that may implement
	// any verbs, and during discovery, we need to advertise correct set of
	// verbs for each group-version tuple. storageAwareResourceVerbsProvider
	// knows what storage is defined for which resource, and based on that
	// information it can provide the correct set of verbs to advertise.
	verbsProviderFactory *storageAwareResourceVerbsProviderFactory

	getCRD             func(cluster logicalcluster.Name, name string) (*apiextensionsv1.CustomResourceDefinition, error)
	getAPIExportByPath func(clusterPath logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error)
}

func NewServer(c CompletedConfig, delegationTarget genericapiserver.DelegationTarget) (*Server, error) {
	getAPIExportByPath := func(clusterPath logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error) {
		return indexers.ByPathAndNameWithFallback[*apisv1alpha2.APIExport](
			apisv1alpha2.Resource("apiexports"),
			c.Extra.LocalAPIExportInformer.Informer().GetIndexer(),
			c.Extra.GlobalAPIExportInformer.Informer().GetIndexer(),
			clusterPath,
			name,
		)
	}

	s := &Server{
		Extra:    c.Extra,
		delegate: delegationTarget.UnprotectedHandler(),

		getCRD: func(clusterName logicalcluster.Name, name string) (*apiextensionsv1.CustomResourceDefinition, error) {
			return c.Extra.CRDLister.Lister().Cluster(clusterName).Get(name)
		},
		getAPIExportByPath: getAPIExportByPath,

		verbsProviderFactory: &storageAwareResourceVerbsProviderFactory{
			getAPIExportByPath: getAPIExportByPath,
			getAPIExportsByVirtualResourceIdentity: func(vrIdentity string) ([]*apisv1alpha2.APIExport, error) {
				return indexers.ByIndexWithFallback[*apisv1alpha2.APIExport](
					c.Extra.LocalAPIExportInformer.Informer().GetIndexer(),
					c.Extra.GlobalAPIExportInformer.Informer().GetIndexer(),
					indexers.APIExportByVirtualResourceIdentities,
					vrIdentity,
				)
			},
			virtualStorageClientOptions: &virtualStorageClientOptions{
				VWClientConfig:                     c.Extra.VWClientConfig,
				ThisShardName:                      shard.Name(c.Extra.ShardName),
				ThisShardVirtualWorkspaceURLGetter: c.Extra.ShardVirtualWorkspaceURLGetter,
				GetUnstructuredEndpointSlice: func(ctx context.Context, cluster logicalcluster.Name, shard shard.Name, gvr schema.GroupVersionResource, name string) (*unstructured.Unstructured, error) {
					// We assume the endpoint slice is cluster-scoped.
					return c.Extra.DynamicClusterClient.
						Cluster(cluster.Path()).
						Resource(gvr).
						Get(cacheclient.WithShardInContext(ctx, shard), name, metav1.GetOptions{})
				},
				APIResourceDiscoveryForResource: func(ctx context.Context, cfg *rest.Config, groupVersion schema.GroupVersion) (*metav1.APIResourceList, error) {
					c, err := kcpdiscovery.NewForConfig(cfg)
					if err != nil {
						return nil, err
					}
					return c.ServerResourcesForGroupVersion(groupVersion.Identifier())
				},
				RESTMappingFor: func(cluster logicalcluster.Name, gk schema.GroupKind) (*meta.RESTMapping, error) {
					return c.Extra.DynamicRESTMapper.ForCluster(cluster).RESTMapping(gk)
				},
			},
		},
	}

	var err error
	s.GenericAPIServer, err = c.Generic.New("aggregating-crd-version-discovery-apiserver", delegationTarget)
	if err != nil {
		return nil, err
	}

	// We perform only APIResource discovery.
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
		if len(splitPath(r.URL.Path)) == 3 {
			s.handleAPIResourceList(w, r)
			return
		}

		s.delegate.ServeHTTP(w, r)
	}
}

// apiResourcesForGroupVersion is taken from apiextensions-apiserver source,
// but we use the verbsProvider to get the list of supported verbs when
// constructing the APIResource objects.
func apiResourcesForGroupVersion(ctx context.Context, requestedGroup, requestedVersion string, crds []*apiextensionsv1.CustomResourceDefinition, verbsProviderFactory *storageAwareResourceVerbsProviderFactory) ([]metav1.APIResource, []error) {
	apiResourcesForDiscovery := []metav1.APIResource{}
	var errs []error

	for _, crd := range crds {
		if requestedGroup != crd.Spec.Group {
			continue
		}

		if !apiextensionshelpers.IsCRDConditionTrue(crd, apiextensionsv1.Established) {
			continue
		}

		var (
			storageVersionHash string
			subresources       *apiextensionsv1.CustomResourceSubresources
			foundVersion       = false
		)

		for _, v := range crd.Spec.Versions {
			if !v.Served {
				continue
			}

			// HACK: support the case when we add core resources through CRDs (kcp scenario)
			groupVersion := crd.Spec.Group + "/" + v.Name
			if crd.Spec.Group == "" {
				groupVersion = v.Name
			}

			gv := metav1.GroupVersion{Group: groupVersion, Version: v.Name}

			if v.Name == requestedVersion {
				foundVersion = true
				subresources = v.Subresources
			}
			if v.Storage {
				storageVersionHash = discovery.StorageVersionHash(logicalcluster.From(crd), gv.Group, gv.Version, crd.Spec.Names.Kind)
			}
		}

		if !foundVersion {
			// This CRD doesn't have the requested version
			continue
		}

		verbsProvider, err := verbsProviderFactory.newResourceVerbsProvider(ctx, crd, requestedVersion)
		if err != nil {
			utilruntime.HandleError(err)
			errs = append(errs, fmt.Errorf("%s.%s", crd.Status.AcceptedNames.Plural, crd.Spec.Group))
			continue
		}

		apiResourcesForDiscovery = append(apiResourcesForDiscovery, metav1.APIResource{
			Name:               crd.Status.AcceptedNames.Plural,
			SingularName:       crd.Status.AcceptedNames.Singular,
			Namespaced:         crd.Spec.Scope == apiextensionsv1.NamespaceScoped,
			Kind:               crd.Status.AcceptedNames.Kind,
			Verbs:              verbsProvider.resource(),
			ShortNames:         crd.Status.AcceptedNames.ShortNames,
			Categories:         crd.Status.AcceptedNames.Categories,
			StorageVersionHash: storageVersionHash,
		})

		if subresources != nil && subresources.Status != nil {
			apiResourcesForDiscovery = append(apiResourcesForDiscovery, metav1.APIResource{
				Name:       crd.Status.AcceptedNames.Plural + "/status",
				Namespaced: crd.Spec.Scope == apiextensionsv1.NamespaceScoped,
				Kind:       crd.Status.AcceptedNames.Kind,
				Verbs:      verbsProvider.statusSubresource(),
			})
		}

		if subresources != nil && subresources.Scale != nil {
			apiResourcesForDiscovery = append(apiResourcesForDiscovery, metav1.APIResource{
				Group:      autoscaling.GroupName,
				Version:    "v1",
				Kind:       "Scale",
				Name:       crd.Status.AcceptedNames.Plural + "/scale",
				Namespaced: crd.Spec.Scope == apiextensionsv1.NamespaceScoped,
				Verbs:      verbsProvider.scaleSubresource(),
			})
		}
	}

	return apiResourcesForDiscovery, errs
}

func (s *Server) handleAPIResourceList(w http.ResponseWriter, r *http.Request) {
	pathParts := splitPath(r.URL.Path)
	// Only match /apis/<group>/<version>.
	if len(pathParts) != 3 || pathParts[0] != "apis" {
		s.delegate.ServeHTTP(w, r)
		return
	}

	// We do only version discovery aggregation for CRDs. Reserved groups (apiextensions.kcp.io) don't belong here.
	if strings.HasSuffix(pathParts[1], ".k8s.io") || strings.HasSuffix(pathParts[1], ".kubernetes.io") {
		s.delegate.ServeHTTP(w, r)
		return
	}

	clusterName, wildcard, err := genericapirequest.ClusterNameOrWildcardFrom(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if wildcard {
		// this is the only case where wildcard works for a list because this is our special CRD lister that handles it.
		clusterName = "*"
	}

	requestedGroup := pathParts[1]
	requestedVersion := pathParts[2]

	crds, err := s.Extra.APIBindingAwareCRDLister.Cluster(clusterName).List(r.Context(), labels.Everything())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	apiResources, errs := apiResourcesForGroupVersion(r.Context(), requestedGroup, requestedVersion, crds, s.verbsProviderFactory)
	if len(errs) > 0 {
		warning.AddWarning(r.Context(), "", fmt.Sprintf("Some resources are temporarily unavailable: %v.", errs))
	}

	resourceListerFunc := discovery.APIResourceListerFunc(func() []metav1.APIResource {
		return apiResources
	})

	discovery.NewAPIVersionHandler(codecs, schema.GroupVersion{Group: requestedGroup, Version: requestedVersion}, resourceListerFunc).ServeHTTP(w, r)
}
