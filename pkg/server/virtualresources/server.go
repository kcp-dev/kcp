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

type apiDef struct {
	apiGroups    map[string]metav1.APIGroup
	apiResources map[schema.GroupVersion]map[string]metav1.APIResource

	endpoints           map[schema.GroupResource]string
	apiExportIdentities map[schema.GroupResource]string
}

type Server struct {
	GenericAPIServer *genericapiserver.GenericAPIServer
	Extra            *ExtraConfig
	delegate         genericapiserver.DelegationTarget
	vwTlsConfig      *tls.Config

	groupManagers *clusterAwareGroupManager

	lock              sync.RWMutex
	apiDefs           map[logicalcluster.Name]apiDef
	wildcardEndpoints map[string]map[schema.GroupResource]string
}

func newApiDef() apiDef {
	return apiDef{
		apiGroups:           make(map[string]metav1.APIGroup),
		apiResources:        make(map[schema.GroupVersion]map[string]metav1.APIResource),
		endpoints:           make(map[schema.GroupResource]string),
		apiExportIdentities: make(map[schema.GroupResource]string),
	}
}

func (d apiDef) addGroup(apiGroup metav1.APIGroup) {
	existingApiGroup, ok := d.apiGroups[apiGroup.Name]
	if !ok {
		d.apiGroups[apiGroup.Name] = apiGroup
		return
	}

	groupVersions := sets.New[string]()
	for _, version := range existingApiGroup.Versions {
		groupVersions.Insert(version.Version)
	}
	for _, version := range apiGroup.Versions {
		groupVersions.Insert(version.Version)
	}
	apiGroup.Versions = make([]metav1.GroupVersionForDiscovery, len(groupVersions))
	for i, version := range sets.List[string](groupVersions) {
		apiGroup.Versions[i] = metav1.GroupVersionForDiscovery{
			Version: version,
			GroupVersion: schema.GroupVersion{
				Group:   apiGroup.Name,
				Version: version,
			}.String(),
		}
	}
	if len(apiGroup.Versions) > 0 {
		apiGroup.PreferredVersion = apiGroup.Versions[len(apiGroup.Versions)-1]
	}

	d.apiGroups[apiGroup.Name] = apiGroup
}

func (d apiDef) addResource(apiResource metav1.APIResource) {
	gv := schema.GroupVersion{
		Group:   apiResource.Group,
		Version: apiResource.Version,
	}

	if _, ok := d.apiResources[gv]; !ok {
		d.apiResources[gv] = make(map[string]metav1.APIResource)
	}
	d.apiResources[gv][apiResource.Name] = apiResource
}

func (d apiDef) removeResource(gr schema.GroupResource) {
	apiGroup, ok := d.apiGroups[gr.Group]
	if !ok {
		return
	}

	for _, version := range apiGroup.Versions {
		gv := schema.GroupVersion{
			Group:   gr.Group,
			Version: version.Version,
		}
		delete(d.apiResources[gv], gr.Resource)
		if len(d.apiResources[gv]) == 0 {
			delete(d.apiResources, gv)
		}
	}
}

func NewServer(c CompletedConfig, delegationTarget genericapiserver.DelegationTarget) (*Server, error) {
	s := &Server{
		Extra:             c.Extra,
		delegate:          delegationTarget,
		groupManagers:     newClusterAwareGroupManager(c.Generic.DiscoveryAddresses, c.Generic.Serializer),
		apiDefs:           make(map[logicalcluster.Name]apiDef),
		wildcardEndpoints: make(map[string]map[schema.GroupResource]string),
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

func (s *Server) addHandlerFor(cluster logicalcluster.Name, gr schema.GroupResource, vrEndpointURL, exportIdentity string) error {
	config := *s.Extra.VWClientConfig
	config.Host = urlWithCluster(vrEndpointURL, exportIdentity, &genericapirequest.Cluster{Name: cluster})

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
		return fmt.Errorf("group %s not found in %s discovery", gr.Group, vrEndpointURL)
	}

	// Get all versions in the found group that are serving the bound resource.

	var apiResources []metav1.APIResource
	for _, version := range apiGroup.Versions {
		apiResourceList, err := dc.ServerResourcesForGroupVersion(version.GroupVersion)
		if err != nil {
			return fmt.Errorf("discovery client failed to list resources for group/version %s in %s: %v", version.GroupVersion, vrEndpointURL, err)
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
		return fmt.Errorf("resource %s/%s not found in %s", gr.Group, gr.Resource, vrEndpointURL)
	}

	s.lock.Lock()
	defer s.lock.Unlock()

	// Store the API definitions we've found.

	s.groupManagers.AddGroupForCluster(cluster, apiGroup)

	if _, ok := s.apiDefs[cluster]; !ok {
		s.apiDefs[cluster] = newApiDef()
	}
	if _, ok := s.wildcardEndpoints[exportIdentity]; !ok {
		s.wildcardEndpoints[exportIdentity] = make(map[schema.GroupResource]string)
	}

	s.apiDefs[cluster].addGroup(*apiGroup)
	for _, apiResource := range apiResources {
		s.apiDefs[cluster].addResource(apiResource)
	}
	s.apiDefs[cluster].endpoints[gr] = vrEndpointURL
	s.apiDefs[cluster].apiExportIdentities[gr] = exportIdentity
	s.wildcardEndpoints[exportIdentity][gr] = vrEndpointURL

	return nil
}

func (s *Server) removeHandlerFor(cluster logicalcluster.Name, gr schema.GroupResource) error {
	s.lock.Lock()
	defer s.lock.Unlock()

	s.groupManagers.RemoveGroupForCluster(cluster, gr.Group)
	s.apiDefs[cluster].removeResource(gr)
	exportIdentity := s.apiDefs[cluster].apiExportIdentities[gr]
	delete(s.apiDefs[cluster].apiExportIdentities, gr)
	delete(s.wildcardEndpoints[exportIdentity], gr)

	// TODO: Handle group deletion, cluster deletion.

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
	if resources, ok := s.apiDefs[cluster.Name].apiResources[gv]; ok {
		for _, res := range resources {
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

	apiExportIdentity := kcpfilters.IdentityFromContext(ctx)

	cluster := genericapirequest.ClusterFrom(ctx)
	if cluster == nil {
		s.delegate.UnprotectedHandler().ServeHTTP(w, r)
		return
	}

	if cluster.Wildcard && apiExportIdentity == "" {
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

	// cluster may be *, check if apiexport identity is in ctx

	gr := schema.GroupResource{
		Group:    reqInfo.APIGroup,
		Resource: reqInfo.Resource,
	}

	handler := s.proxyFor(cluster, apiExportIdentity, gr)
	if handler == nil {
		s.delegate.UnprotectedHandler().ServeHTTP(w, r)
		return
	}

	handler.ServeHTTP(w, r)
}

func (s *Server) proxyFor(cluster *genericapirequest.Cluster, apiExportIdentity string, gr schema.GroupResource) http.Handler {
	s.lock.RLock()
	defer s.lock.RUnlock()

	var grEndpointMap map[schema.GroupResource]string
	if cluster.Wildcard {
		grEndpointMap = s.wildcardEndpoints[apiExportIdentity]
	} else {
		grEndpointMap = s.apiDefs[cluster.Name].endpoints
	}

	if grEndpointMap == nil {
		return nil
	}

	vrUrl := grEndpointMap[gr]
	if vrUrl == "" {
		return nil
	}

	if apiExportIdentity == "" {
		apiExportIdentity = s.apiDefs[cluster.Name].apiExportIdentities[gr]
	}

	handler, err := newProxy(cluster, vrUrl, apiExportIdentity, s.vwTlsConfig)
	if err != nil {
		return nil
	}

	return handler
}

func newProxy(cluster *genericapirequest.Cluster, vwURL, apiExportIdentity string, vwTLSConfig *tls.Config) (http.Handler, error) {
	scopedURL, err := url.Parse(urlWithCluster(vwURL, apiExportIdentity, cluster))
	if err != nil {
		return nil, err
	}

	handler := httputil.NewSingleHostReverseProxy(scopedURL)
	handler.Transport = &http.Transport{
		TLSClientConfig: vwTLSConfig,
	}

	return handler, nil
}

func urlWithCluster(vwURL, apiExportIdentity string, cluster *genericapirequest.Cluster) string {
	clusterName := cluster.Name
	if cluster.Wildcard {
		clusterName = "*"
	}
	return fmt.Sprintf("%s:%s/clusters/%s", vwURL, apiExportIdentity, clusterName)
}
