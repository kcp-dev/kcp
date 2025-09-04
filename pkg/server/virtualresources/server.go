package virtualresources

import (
	// "encoding/json"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"

	// "k8s.io/apimachinery/pkg/runtime/schema"
	restful "github.com/emicklei/go-restful/v3"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/endpoints/handlers/negotiation"
	"k8s.io/apiserver/pkg/endpoints/handlers/responsewriters"
	"k8s.io/apiserver/pkg/endpoints/request"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/apiserver/pkg/warning"
	discoveryclient "k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"

	"github.com/kcp-dev/logicalcluster/v3"
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
	delegate         genericapiserver.DelegationTarget
	vwTlsConfig      *tls.Config

	groupManagers *clusterAwareGroupManager

	lock                        sync.RWMutex
	groups                      map[logicalcluster.Name]map[string]metav1.APIGroup
	apiResourcesForGroupVersion map[logicalcluster.Name]map[schema.GroupVersion][]metav1.APIResource
	resourcesForGroupVersion    map[logicalcluster.Name]map[schema.GroupVersion]sets.Set[string]
	endpointsForGroupResource   map[logicalcluster.Name]map[schema.GroupResource]string
}

func NewServer(c CompletedConfig, delegationTarget genericapiserver.DelegationTarget) (*Server, error) {
	s := &Server{
		Extra:                       c.Extra,
		delegate:                    delegationTarget,
		groupManagers:               newClusterAwareGroupManager(c.Generic.DiscoveryAddresses, c.Generic.Serializer),
		groups:                      make(map[logicalcluster.Name]map[string]metav1.APIGroup),
		apiResourcesForGroupVersion: make(map[logicalcluster.Name]map[schema.GroupVersion][]metav1.APIResource),
		resourcesForGroupVersion:    make(map[logicalcluster.Name]map[schema.GroupVersion]sets.Set[string]),
		endpointsForGroupResource:   make(map[logicalcluster.Name]map[schema.GroupResource]string),
	}

	tlsConfig, err := rest.TLSConfigFor(c.Extra.VWClientConfig)
	if err != nil {
		return nil, err
	}
	s.vwTlsConfig = tlsConfig

	// c.Generic.BuildHandlerChainFunc = s.buildHandlerChain(c, delegationTarget)
	// c.Generic.ReadyzChecks = append(c.Generic.ReadyzChecks, asHealthChecks(c.Extra.VirtualWorkspaces)...)
	// apiBindings lister synced ^

	s.GenericAPIServer, err = c.Generic.New("virtual-resources-root-apiserver", delegationTarget)
	if err != nil {
		return nil, err
	}
	s.GenericAPIServer.DiscoveryGroupManager = s.groupManagers

	s.GenericAPIServer.Handler.GoRestfulContainer.Filter(func(req *restful.Request, res *restful.Response, chain *restful.FilterChain) {
		pathParts := splitPath(req.Request.URL.Path)

		ctx := req.Request.Context()
		requestInfo, ok := request.RequestInfoFrom(ctx)
		if !ok {
			responsewriters.ErrorNegotiated(
				apierrors.NewInternalError(fmt.Errorf("no RequestInfo found in the context")),
				// TODO is this the right Codecs?
				errorCodecs, schema.GroupVersion{Group: requestInfo.APIGroup, Version: requestInfo.APIVersion}, res.ResponseWriter, req.Request,
			)
			return
		}

		if requestInfo.APIGroup == "" {
			chain.ProcessFilter(req, res)
			return
		}

		if !requestInfo.IsResourceRequest {
			chain.ProcessFilter(req, res)
			return
		}

		switch len(pathParts) {
		case 3:
			s.handleAPIResourceList(res.ResponseWriter, req.Request)
			return
		default:
			s.handleResource(res.ResponseWriter, req.Request)
			return
		}
	})

	s.GenericAPIServer.Handler.NonGoRestfulMux.HandlePrefix("/apis/", s.newApisHandler())

	return s, nil
}

func (s *Server) addHandlerFor(cluster logicalcluster.Name, gr schema.GroupResource, vwEndpointURL string) error {
	config := *s.Extra.VWClientConfig
	config.Host = vwEndpointURL + fmt.Sprintf("/clusters/%s", cluster.String())

	dc, err := discoveryclient.NewDiscoveryClientForConfig(&config)
	if err != nil {
		return fmt.Errorf("failed to create discovery client for gr=%q, endpoint=%q: %v", gr, config.Host, err)
	}

	// Get API groups from the VW.

	apiGroupList, err := dc.ServerGroups()
	if err != nil {
		return fmt.Errorf("discovery client failed to list api groups for endpoint=%q: %v", config.Host, err)
	}

	// Find the group we want to bind.

	var apiGroup *metav1.APIGroup
	for _, group := range apiGroupList.Groups {
		if group.Name == gr.Group {
			apiGroup = group.DeepCopy()
			break
		}
	}
	if apiGroup == nil {
		return fmt.Errorf("group %s not found in %s discovery", gr.Group, vwEndpointURL)
	}

	// Get all versions in the found group that are serving the bound resource.

	var apiResources []metav1.APIResource
	for _, version := range apiGroup.Versions {
		apiResourceList, err := dc.ServerResourcesForGroupVersion(version.GroupVersion)
		if err != nil {
			return fmt.Errorf("discovery client failed to list resources for group/version %s in %s: %v", version.GroupVersion, vwEndpointURL, err)
		}

		for _, res := range apiResourceList.APIResources {
			if res.Name == gr.Resource {
				res = *res.DeepCopy()
				if res.Group == "" {
					res.Group = apiGroup.Name
				}
				if res.Version == "" {
					res.Version = version.Version
				}
				apiResources = append(apiResources, res)
				break
			}
		}
	}

	if apiResources == nil {
		return fmt.Errorf("resource %s/%s not found in %s", gr.Group, gr.Resource, vwEndpointURL)
	}

	s.lock.Lock()
	defer s.lock.Unlock()

	// Store the group we've found.

	s.groupManagers.AddGroupForCluster(cluster, apiGroup)

	if _, ok := s.groups[cluster]; !ok {
		s.groups[cluster] = make(map[string]metav1.APIGroup)
	}
	s.groups[cluster][gr.Group] = *apiGroup

	// Store resource's gv.

	if _, ok := s.apiResourcesForGroupVersion[cluster]; !ok {
		s.apiResourcesForGroupVersion[cluster] = make(map[schema.GroupVersion][]metav1.APIResource)
	}
	if _, ok := s.resourcesForGroupVersion[cluster]; !ok {
		s.resourcesForGroupVersion[cluster] = make(map[schema.GroupVersion]sets.Set[string])
	}
	scopedApiResources := s.apiResourcesForGroupVersion[cluster]
	scopedResources := s.resourcesForGroupVersion[cluster]
	for _, res := range apiResources {
		gv := schema.GroupVersion{
			Group:   res.Group,
			Version: res.Version,
		}
		scopedApiResources[gv] = append(scopedApiResources[gv], res)
		if _, ok := scopedResources[gv]; !ok {
			scopedResources[gv] = sets.New[string]()
		}
		scopedResources[gv].Insert(res.Name)
	}

	// Store the vw url.

	if _, ok := s.endpointsForGroupResource[cluster][gr]; !ok {
		s.endpointsForGroupResource[cluster] = make(map[schema.GroupResource]string)
	}
	s.endpointsForGroupResource[cluster][gr] = vwEndpointURL

	return nil
}

func (s *Server) removeHandlerFor(cluster logicalcluster.Name, gr schema.GroupResource, vwEndpointURL string) error {
	return nil
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
		pathParts := splitPath(r.URL.Path)
		switch len(pathParts) {
		case 3:
			s.handleAPIResourceList(w, r)
			return
		default:
			s.handleResource(w, r)
			return
		}
	}
}

func (s *Server) handleAPIResourceList(w http.ResponseWriter, r *http.Request) {
	pathParts := splitPath(r.URL.Path)
	if len(pathParts) != 3 {
		s.delegate.UnprotectedHandler().ServeHTTP(w, r)
		return
	}

	ctx := r.Context()

	cluster := genericapirequest.ClusterFrom(ctx)
	if cluster == nil {
		warning.AddWarning(ctx, "", "cluster missing in context")
		s.delegate.UnprotectedHandler().ServeHTTP(w, r)
		return
	}

	_, hasReqInfo := genericapirequest.RequestInfoFrom(ctx)
	if !hasReqInfo {
		warning.AddWarning(ctx, "", "request info missing in context")
		s.delegate.UnprotectedHandler().ServeHTTP(w, r)
		return
	}

	gv := schema.GroupVersion{
		Group:   pathParts[1],
		Version: pathParts[2],
	}

	var knownVersionedResources []metav1.APIResource
	s.lock.RLock()
	if gvResources := s.apiResourcesForGroupVersion[cluster.Name]; gvResources != nil {
		for _, res := range gvResources[gv] {
			knownVersionedResources = append(knownVersionedResources, *res.DeepCopy())
		}
	}
	s.lock.RUnlock()

	if len(knownVersionedResources) == 0 {
		s.delegate.UnprotectedHandler().ServeHTTP(w, r)
		return
	}

	apiResourceList := &metav1.APIResourceList{}
	apiResourceList.GroupVersion = gv.String()
	apiResourceList.APIResources = knownVersionedResources

	responsewriters.WriteObjectNegotiated(s.GenericAPIServer.Serializer, negotiation.DefaultEndpointRestrictions, schema.GroupVersion{}, w, r, http.StatusOK, apiResourceList, false)
}

func (s *Server) handleResource(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	cluster := genericapirequest.ClusterFrom(ctx)
	if cluster == nil {
		warning.AddWarning(ctx, "", "cluster missing in context")
		s.delegate.UnprotectedHandler().ServeHTTP(w, r)
		return
	}

	reqInfo, hasReqInfo := genericapirequest.RequestInfoFrom(ctx)
	if !hasReqInfo {
		warning.AddWarning(ctx, "", "request info missing in context")
		s.delegate.UnprotectedHandler().ServeHTTP(w, r)
		return
	}

	if !reqInfo.IsResourceRequest {
		s.delegate.UnprotectedHandler().ServeHTTP(w, r)
		return
	}

	handler := s.proxyFor(cluster.Name, schema.GroupResource{Group: reqInfo.APIGroup, Resource: reqInfo.Resource})
	if handler == nil {
		s.delegate.UnprotectedHandler().ServeHTTP(w, r)
		return
	}

	handler.ServeHTTP(w, r)
}

func (s *Server) proxyFor(cluster logicalcluster.Name, gr schema.GroupResource) http.Handler {
	s.lock.RLock()
	defer s.lock.RUnlock()

	endpoints := s.endpointsForGroupResource[cluster]
	if endpoints == nil {
		return nil
	}

	vwURL := endpoints[gr]
	if vwURL == "" {
		return nil
	}

	handler, err := newProxy(cluster, vwURL, s.vwTlsConfig)
	if err != nil {
		return nil
	}

	return handler
}

func (s *Server) getEndpointsForCluster(cluster logicalcluster.Name) map[schema.GroupResource]string {
	m := make(map[schema.GroupResource]string)

	s.lock.RLock()
	defer s.lock.RUnlock()

	if grEndpoints := s.endpointsForGroupResource[cluster]; grEndpoints != nil {
		for k, v := range s.endpointsForGroupResource[cluster] {
			m[k] = v
		}
	}

	return m
}

func newProxy(cluster logicalcluster.Name, vwURL string, vwTLSConfig *tls.Config) (http.Handler, error) {
	scopedURL, err := url.Parse(urlWithCluster(vwURL, cluster))
	if err != nil {
		return nil, err
	}

	handler := httputil.NewSingleHostReverseProxy(scopedURL)
	handler.Transport = &http.Transport{
		TLSClientConfig: vwTLSConfig,
	}

	return handler, nil
}

func urlWithCluster(vwURL string, cluster logicalcluster.Name) string {
	return fmt.Sprintf("%s/clusters/%s", vwURL, cluster)
}
