package virtualresources

import (
	"context"
	"crypto/sha512"
	"encoding/json"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/discovery"
	clientopenapi3 "k8s.io/client-go/openapi3"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	"k8s.io/kube-openapi/pkg/cached"
	"k8s.io/kube-openapi/pkg/spec3"

	"github.com/kcp-dev/logicalcluster/v3"
)

// Returns map of VW URL -> Group path -> cached value for OpenAPI spec.
// The (VW URL, Group path) tuple is used as a key for the OpenAPI service.
func (s *Server) OpenAPIv3SpecGetter() func(ctx context.Context) (map[string]map[string]cached.Value[*spec3.OpenAPI], error) {
	return func(ctx context.Context) (map[string]map[string]cached.Value[*spec3.OpenAPI], error) {
		cluster, err := genericapirequest.ClusterNameFrom(ctx)
		if err != nil {
			return nil, err
		}

		log := klog.FromContext(ctx).WithName("virtualresource-openapiv3-getter").WithValues("cluster", cluster)

		trackedResourcesForEndpoint := func() map[string]map[schema.GroupVersion]sets.Set[string] {
			s.lock.RLock()
			defer s.lock.RUnlock()

			resourcesForGroupVersion := s.resourcesForGroupVersion[cluster]
			if len(resourcesForGroupVersion) == 0 {
				return nil
			}
			endpointsForGroupResource := s.endpointsForGroupResource[cluster]
			if len(endpointsForGroupResource) == 0 {
				return nil
			}

			// VW URL -> GroupVersion -> Set[ResourceName]
			m := make(map[string]map[schema.GroupVersion]sets.Set[string])

			for gr, endpoint := range endpointsForGroupResource {
				if _, ok := m[endpoint]; !ok {
					m[endpoint] = make(map[schema.GroupVersion]sets.Set[string])
				}

				foundResources := make(map[schema.GroupVersion]sets.Set[string])

				for gv, resources := range resourcesForGroupVersion {
					if resources.Has(gr.Resource) {
						if _, ok := foundResources[gv]; !ok {
							foundResources[gv] = sets.New[string]()
						}
						foundResources[gv].Insert(gr.Resource)
					}
				}

				m[endpoint] = foundResources
			}

			return m
		}()

		specsByEndpoint := make(map[string]map[string]cached.Value[*spec3.OpenAPI])

		for vwURL, resourcesForGroupVersions := range trackedResourcesForEndpoint {
			log := log.WithValues("url", vwURL)

			vwOpenAPIv3Root, err := newOpenAPIv3Root(s.Extra.VWClientConfig, vwURL, cluster)
			if err != nil {
				return nil, fmt.Errorf("failed to create discovery client for virtual workspace %s: %v", vwURL, err)
			}

			specsByEndpoint[vwURL] = make(map[string]cached.Value[*spec3.OpenAPI])
			for groupVersion, resources := range resourcesForGroupVersions {
				specsByEndpoint[vwURL][fmt.Sprintf("apis/%s", groupVersion)] = cached.Once(cached.Func[*spec3.OpenAPI](
					func() (*spec3.OpenAPI, string, error) {
						log := log.WithValues("groupVersion", groupVersion)

						spec, err := vwOpenAPIv3Root.GVSpec(groupVersion)
						if err != nil {
							log.V(4).Error(err, "Failed to get OpenAPIv3 spec")
							return nil, "", err
						}
						filterSpecForResources(spec, resources)

						bs, err := json.Marshal(spec)
						if err != nil {
							return nil, "", err
						}
						return spec, fmt.Sprintf("%X", sha512.Sum512(bs)), nil
					},
				))
			}
		}

		return specsByEndpoint, nil
	}
}

func newOpenAPIv3Root(config *rest.Config, vwURL string, cluster logicalcluster.Name) (clientopenapi3.Root, error) {
	vwConfig := *config
	vwConfig.Host = urlWithCluster(vwURL, cluster)

	vwDiscoveryClient, err := discovery.NewDiscoveryClientForConfig(&vwConfig)
	if err != nil {
		return nil, err
	}

	return clientopenapi3.NewRoot(vwDiscoveryClient.OpenAPIV3()), nil
}

func pathHasAnyResource(apiPath string, acceptedResources sets.Set[string]) bool {
	parts := strings.Split(strings.Trim(apiPath, "/"), "/")
	if len(parts) < 1 {
		return false
	}
	// last fixed segment before optional "{name}"
	resource := parts[len(parts)-1]
	if strings.HasPrefix(resource, "{") && len(parts) >= 2 {
		resource = parts[len(parts)-2]
	}
	return acceptedResources.Has(resource)
}

func filterSpecForResources(spec *spec3.OpenAPI, resources sets.Set[string]) {
	for path := range spec.Paths.Paths {
		if !pathHasAnyResource(path, resources) {
			delete(spec.Paths.Paths, path)
		}
	}
}
