package apiserver

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	genericapiserver "k8s.io/apiserver/pkg/server"
	discoveryclient "k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"

	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/virtualapidefinition"
)

type virtualServingInfo struct {
	logicalClusterName logicalcluster.Name

	vwURL          string
	vwClientConfig *rest.Config
	vwTlsConfig    *tls.Config
	gr             schema.GroupResource
}

var _ virtualapidefinition.VirtualAPIDefinition = (*virtualServingInfo)(nil)

func CreateVirtualServingInfoFor(ctx context.Context, genericConfig genericapiserver.CompletedConfig, exportCluster logicalcluster.Name, gr schema.GroupResource, endpointSliceGVR schema.GroupVersionResource, endpointSliceName string, cacheDynamicClient kcpdynamic.ClusterInterface, thisShardURL string) (virtualapidefinition.VirtualAPIDefinition, error) {
	list, err := cacheDynamicClient.Cluster(logicalcluster.NewPath(exportCluster.String())).Resource(schema.GroupVersionResource{
		Group:    endpointSliceGVR.Group,
		Version:  endpointSliceGVR.Version,
		Resource: endpointSliceGVR.Resource,
	}).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	var endpointSlice *unstructured.Unstructured
	for i := range list.Items {
		if list.Items[i].GetName() == endpointSliceName {
			endpointSlice = &list.Items[i]
			break
		}
	}

	if endpointSlice == nil {
		return nil, apierrors.NewNotFound(schema.GroupResource{
			Group:    endpointSliceGVR.Group,
			Resource: endpointSliceGVR.Resource,
		}, endpointSliceName)
	}

	endpoints, found, err := unstructured.NestedSlice(endpointSlice.Object, "status", "endpoints")
	if err != nil {
		return nil, fmt.Errorf("failed to get status.endpoints: %w", err)
	}
	if !found {
		return nil, fmt.Errorf("status.endpoints not found")
	}

	var urls []string
	for i, ep := range endpoints {
		endpointMap, ok := ep.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("endpoint at index %d is not an object", i)
		}

		url, found, err := unstructured.NestedString(endpointMap, "url")
		if err != nil {
			return nil, fmt.Errorf("failed to get url from endpoint at index %d: %w", i, err)
		}
		if !found {
			return nil, fmt.Errorf("missing url in endpoint at index %d", i)
		}

		urls = append(urls, url)
	}

	var selectedEndpointURL string
	for _, url := range urls {
		if strings.HasPrefix(url, thisShardURL) {
			if selectedEndpointURL == "" {
				selectedEndpointURL = url
			} else {
				return nil, fmt.Errorf("ambiguous virtual workspace endpoints in endpoint slice: %q and %q for shard %q", selectedEndpointURL, url, thisShardURL)
			}
		}
	}
	if selectedEndpointURL == "" {
		return nil, fmt.Errorf("no suitable virtual workspace endpoint found")
	}

	tlsConfig, err := rest.TLSConfigFor(genericConfig.LoopbackClientConfig)
	if err != nil {
		return nil, err
	}

	return &virtualServingInfo{
		vwURL:          selectedEndpointURL,
		vwClientConfig: rest.CopyConfig(genericConfig.LoopbackClientConfig),
		vwTlsConfig:    tlsConfig,
		gr:             gr,
	}, nil
}

func (def *virtualServingInfo) newDiscoveryClient() (*discoveryclient.DiscoveryClient, error) {
	config := *def.vwClientConfig
	config.Host = def.vwURL + fmt.Sprintf("/clusters/%s", def.logicalClusterName.String())

	dc, err := discoveryclient.NewDiscoveryClientForConfig(&config)
	if err != nil {
		return nil, fmt.Errorf("failed to create discovery client for gr=%q, endpoint=%q: %v", def.gr, config.Host, err)
	}

	return dc, nil
}

func (def *virtualServingInfo) GetAPIGroups() ([]metav1.APIGroup, error) {
	dc, err := def.newDiscoveryClient()
	if err != nil {
		return nil, err
	}

	apiGroupList, err := dc.ServerGroups()
	if err != nil {
		return nil, fmt.Errorf("discovery client failed to list api groups for endpoint=%q: %v", fmt.Sprintf("%s/clusters/%s", def.vwURL, def.logicalClusterName), err)
	}

	return apiGroupList.Groups, nil
}

func (def *virtualServingInfo) GetAPIResources() ([]metav1.APIResource, error) {
	dc, err := def.newDiscoveryClient()
	if err != nil {
		return nil, err
	}

	apiGroupList, err := dc.ServerGroups()
	if err != nil {
		return nil, fmt.Errorf("discovery client failed to list api groups for endpoint=%q: %v", fmt.Sprintf("%s/clusters/%s", def.vwURL, def.logicalClusterName), err)
	}

	var apiGroup *metav1.APIGroup
	for _, group := range apiGroupList.Groups {
		if group.Name == def.gr.Group {
			apiGroup = group.DeepCopy()
			break
		}
	}
	if apiGroup == nil {
		return nil, fmt.Errorf("group %s not found in %s discovery", def.gr.Group, def.vwURL)
	}

	// Get all versions in the found group that are serving the bound resource.

	var apiResources []metav1.APIResource
	for _, version := range apiGroup.Versions {
		apiResourceList, err := dc.ServerResourcesForGroupVersion(version.GroupVersion)
		if err != nil {
			return nil, fmt.Errorf("discovery client failed to list resources for group/version %s in %s: %v", version.GroupVersion, def.vwURL, err)
		}

		for _, res := range apiResourceList.APIResources {
			if res.Name == def.gr.Resource {
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
		return nil, fmt.Errorf("resource %s/%s not found in %s", def.gr.Group, def.gr.Resource, def.vwURL)
	}

	return apiResources, nil
}

func (def *virtualServingInfo) GetProxy() (http.Handler, error) {
	scopedURL, err := url.Parse(urlWithCluster(def.vwURL, def.logicalClusterName))
	if err != nil {
		return nil, err
	}

	handler := httputil.NewSingleHostReverseProxy(scopedURL)
	handler.Transport = &http.Transport{
		TLSClientConfig: def.vwTlsConfig,
	}

	return handler, nil
}

func (def *virtualServingInfo) TearDown() {

}

func urlWithCluster(vwURL string, cluster logicalcluster.Name) string {
	return fmt.Sprintf("%s/clusters/%s", vwURL, cluster)
}
