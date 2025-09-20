package virtualresources

import (
	// "encoding/json"
	"context"
	"fmt"
	"net/http"
	"sync"

	restful "github.com/emicklei/go-restful/v3"

	// "k8s.io/apimachinery/pkg/runtime/schema"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	discoveryapi "k8s.io/apiserver/pkg/endpoints/discovery"
	"k8s.io/apiserver/pkg/endpoints/handlers/negotiation"
	"k8s.io/apiserver/pkg/endpoints/handlers/responsewriters"
	// "k8s.io/apiserver/pkg/endpoints/handlers/responsewriters"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/warning"

	"github.com/kcp-dev/logicalcluster/v3"
)

type clusterAwareGroupManager struct {
	addresses  discoveryapi.Addresses
	serializer runtime.NegotiatedSerializer

	lock          sync.RWMutex
	groupManagers map[logicalcluster.Name]discoveryapi.GroupManager
}

func newClusterAwareGroupManager(addresses discoveryapi.Addresses, serializer runtime.NegotiatedSerializer) *clusterAwareGroupManager {
	return &clusterAwareGroupManager{
		addresses:     addresses,
		serializer:    serializer,
		groupManagers: make(map[logicalcluster.Name]discoveryapi.GroupManager),
	}
}

func (m *clusterAwareGroupManager) Groups(ctx context.Context, req *http.Request) ([]metav1.APIGroup, error) {
	cluster := genericapirequest.ClusterFrom(ctx)
	if cluster == nil {
		return nil, fmt.Errorf("cluster missing in context")
	}

	m.lock.RLock()
	defer m.lock.RUnlock()
	if gm := m.groupManagers[cluster.Name]; gm != nil {
		return gm.Groups(ctx, req)
	}

	return nil, nil
}

func (m *clusterAwareGroupManager) AddGroupForCluster(cluster logicalcluster.Name, apiGroup *metav1.APIGroup) {
	m.lock.Lock()
	defer m.lock.Unlock()

	if _, found := m.groupManagers[cluster]; !found {
		m.groupManagers[cluster] = discoveryapi.NewRootAPIsHandler(m.addresses, m.serializer)
	}
	m.groupManagers[cluster].AddGroup(*apiGroup)
}

func (m *clusterAwareGroupManager) RemoveGroupForCluster(cluster logicalcluster.Name, groupName string) {
	m.lock.Lock()
	defer m.lock.Unlock()

	if _, found := m.groupManagers[cluster]; !found {
		return
	}

	m.groupManagers[cluster].RemoveGroup(groupName)

	// Groups implementation in rootAPIsHandler never returns error, ignores ctx, and we don't care about http.Request.
	groups, _ := m.groupManagers[cluster].Groups(nil, &http.Request{})
	if len(groups) == 0 {
		delete(m.groupManagers, cluster)
	}
}

func (m *clusterAwareGroupManager) AddGroup(apiGroup metav1.APIGroup) {
	panic("Use AddGroupForCluster")
}

func (m *clusterAwareGroupManager) RemoveGroup(groupName string) {
	panic("Use RemoveGroupForCluster")
}

func (m *clusterAwareGroupManager) ServeHTTP(resp http.ResponseWriter, req *http.Request) {
	cluster := genericapirequest.ClusterFrom(req.Context())
	if cluster == nil {
		warning.AddWarning(req.Context(), "", "cluster missing in context")
		responsewriters.ErrorNegotiated(
			apierrors.NewInternalError(fmt.Errorf("cluster missing in context when serving %s", req.URL.Path)),
			errorCodecs, schema.GroupVersion{},
			resp, req)
		return
	}

	m.lock.RLock()
	defer m.lock.RUnlock()

	if gm := m.groupManagers[cluster.Name]; gm != nil {
		gm.ServeHTTP(resp, req)
	}
}

func (m *clusterAwareGroupManager) WebService() *restful.WebService {
	mediaTypes, _ := negotiation.MediaTypesForSerializer(m.serializer)
	ws := new(restful.WebService)
	ws.Path(discoveryapi.APIGroupPrefix)
	ws.Doc("get available API versions")
	ws.Route(ws.GET("/").To(m.restfulHandle).
		Doc("get available API versions").
		Operation("getAPIVersions").
		Produces(mediaTypes...).
		Consumes(mediaTypes...).
		Writes(metav1.APIGroupList{}))
	return ws
}

func (m *clusterAwareGroupManager) restfulHandle(req *restful.Request, resp *restful.Response) {
	m.ServeHTTP(resp.ResponseWriter, req.Request)
}
