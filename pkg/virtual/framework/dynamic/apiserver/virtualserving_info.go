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
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	// genericapiserver "k8s.io/apiserver/pkg/server"
	discoveryclient "k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"

	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/virtualapidefinition"
)

type virtualServingInfo struct {
	logicalClusterName logicalcluster.Name

	shardVirtualWorkspaceURL string
	vwClientConfig           *rest.Config
	vwTlsConfig              *tls.Config
	gr                       schema.GroupResource
	cacheDynamicClient       kcpdynamic.ClusterInterface
	exportCluster            logicalcluster.Name
	endpointSliceGVR         schema.GroupVersionResource
	endpointSliceName        string
}

var _ virtualapidefinition.VirtualAPIDefinition = (*virtualServingInfo)(nil)

func CreateVirtualServingInfoFor(ctx context.Context, clientConfig *rest.Config, exportCluster logicalcluster.Name, gr schema.GroupResource, endpointSliceGVR schema.GroupVersionResource, endpointSliceName string, cacheDynamicClient kcpdynamic.ClusterInterface, shardVirtualWorkspaceURL string) (virtualapidefinition.VirtualAPIDefinition, error) {
	vwClientConfig := rest.CopyConfig(clientConfig)

	tlsConfig, err := rest.TLSConfigFor(vwClientConfig)
	if err != nil {
		return nil, err
	}

	return &virtualServingInfo{
		shardVirtualWorkspaceURL: shardVirtualWorkspaceURL,
		vwClientConfig:           vwClientConfig,
		vwTlsConfig:              tlsConfig,
		gr:                       gr,
		cacheDynamicClient:       cacheDynamicClient,
		exportCluster:            exportCluster,
		endpointSliceGVR:         endpointSliceGVR,
		endpointSliceName:        endpointSliceName,
	}, nil
}

func (def *virtualServingInfo) getVWUrl(ctx context.Context) (string, bool, error) {
	list, err := def.cacheDynamicClient.Cluster(logicalcluster.NewPath(def.exportCluster.String())).Resource(schema.GroupVersionResource{
		Group:    def.endpointSliceGVR.Group,
		Version:  def.endpointSliceGVR.Version,
		Resource: def.endpointSliceGVR.Resource,
	}).List(ctx, metav1.ListOptions{})
	if err != nil {
		fmt.Printf("### CreateVirtualServingInfoFor 2 err=%v\n", err)
		return "", false, err
	}

	var endpointSlice *unstructured.Unstructured
	for i := range list.Items {
		if list.Items[i].GetName() == def.endpointSliceName {
			endpointSlice = &list.Items[i]
			fmt.Printf("### CreateVirtualServingInfoFor 3\n")
			break
		}
	}

	if endpointSlice == nil {
		fmt.Printf("### CreateVirtualServingInfoFor 4\n")
		return "", false, apierrors.NewNotFound(schema.GroupResource{
			Group:    def.endpointSliceGVR.Group,
			Resource: def.endpointSliceGVR.Resource,
		}, def.endpointSliceName)
	}

	endpoints, found, err := unstructured.NestedSlice(endpointSlice.Object, "status", "endpoints")
	if err != nil {
		fmt.Printf("### CreateVirtualServingInfoFor 5 err=%v\n", err)
		return "", false, fmt.Errorf("failed to get status.endpoints: %w ; %#v", err, endpointSlice)
	}
	if !found {
		fmt.Printf("### CreateVirtualServingInfoFor 6\n")
		return "", false, fmt.Errorf("status.endpoints not found")
	}

	var urls []string
	for i, ep := range endpoints {
		fmt.Printf("### CreateVirtualServingInfoFor 7\n")
		endpointMap, ok := ep.(map[string]interface{})
		if !ok {
			return "", false, fmt.Errorf("endpoint at index %d is not an object", i)
		}

		url, found, err := unstructured.NestedString(endpointMap, "url")
		if err != nil {
			fmt.Printf("### CreateVirtualServingInfoFor 8 err=%v\n", err)
			return "", false, fmt.Errorf("failed to get url from endpoint at index %d: %w", i, err)
		}
		if !found {
			fmt.Printf("### CreateVirtualServingInfoFor 9\n")
			return "", false, fmt.Errorf("missing url in endpoint at index %d", i)
		}

		urls = append(urls, url)
	}

	var selectedEndpointURL string
	for _, url := range urls {
		if strings.HasPrefix(url, def.shardVirtualWorkspaceURL) {
			if selectedEndpointURL == "" {
				selectedEndpointURL = url
			} else {
				fmt.Printf("### CreateVirtualServingInfoFor 10\n")
				return "", false, fmt.Errorf("ambiguous virtual workspace endpoints in endpoint slice: %q and %q for shard %q", selectedEndpointURL, url, def.shardVirtualWorkspaceURL)
			}
		}
	}
	fmt.Printf("### CreateVirtualServingInfoFor 11\n")
	if selectedEndpointURL == "" {
		fmt.Printf("### CreateVirtualServingInfoFor 12\n")
		return "", true, fmt.Errorf("no suitable virtual workspace endpoint found")
	}

	return selectedEndpointURL, false, nil
}

func (def *virtualServingInfo) newDiscoveryClient(vwURL string, cluster genericapirequest.Cluster) (*discoveryclient.DiscoveryClient, error) {
	config := *def.vwClientConfig
	config.Host = urlWithCluster(vwURL, cluster)

	dc, err := discoveryclient.NewDiscoveryClientForConfig(&config)
	if err != nil {
		return nil, fmt.Errorf("failed to create discovery client for gr=%q, endpoint=%q: %v", def.gr, config.Host, err)
	}

	return dc, nil
}

func (def *virtualServingInfo) GetAPIGroups(ctx context.Context) ([]metav1.APIGroup, error) {
	vwURL, shouldRetry, err := def.getVWUrl(ctx)
	if err != nil {
		if shouldRetry {
			return nil, err
		}
		return nil, nil
	}

	dc, err := def.newDiscoveryClient(vwURL, *genericapirequest.ClusterFrom(ctx))
	if err != nil {
		return nil, err
	}

	apiGroupList, err := dc.ServerGroups()
	if err != nil {
		return nil, fmt.Errorf("discovery client failed to list api groups for endpoint=%q: %v", fmt.Sprintf("%s/clusters/%s", vwURL, def.logicalClusterName), err)
	}

	return apiGroupList.Groups, nil
}

func (def *virtualServingInfo) GetAPIResources(ctx context.Context) ([]metav1.APIResource, error) {
	vwURL, shouldRetry, err := def.getVWUrl(ctx)
	if err != nil {
		if shouldRetry {
			return nil, err
		}
		return nil, nil
	}

	dc, err := def.newDiscoveryClient(vwURL, *genericapirequest.ClusterFrom(ctx))
	if err != nil {
		return nil, err
	}

	apiGroupList, err := dc.ServerGroups()
	if err != nil {
		return nil, fmt.Errorf("discovery client failed to list api groups for endpoint=%q: %v", fmt.Sprintf("%s/clusters/%s", vwURL, def.logicalClusterName), err)
	}

	var apiGroup *metav1.APIGroup
	for _, group := range apiGroupList.Groups {
		if group.Name == def.gr.Group {
			apiGroup = group.DeepCopy()
			break
		}
	}
	if apiGroup == nil {
		return nil, fmt.Errorf("group %s not found in %s discovery", def.gr.Group, vwURL)
	}

	// Get all versions in the found group that are serving the bound resource.

	var apiResources []metav1.APIResource
	for _, version := range apiGroup.Versions {
		apiResourceList, err := dc.ServerResourcesForGroupVersion(version.GroupVersion)
		if err != nil {
			return nil, fmt.Errorf("discovery client failed to list resources for group/version %s in %s: %v", version.GroupVersion, vwURL, err)
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
		return nil, fmt.Errorf("resource %s/%s not found in %s", def.gr.Group, def.gr.Resource, vwURL)
	}

	return apiResources, nil
}

func (def *virtualServingInfo) GetProxy(ctx context.Context) (http.Handler, error) {
	vwURL, shouldRetry, err := def.getVWUrl(ctx)
	if err != nil {
		if shouldRetry {
			return nil, err
		}
		return nil, nil
	}

	scopedURL, err := url.Parse(urlWithCluster(vwURL, *genericapirequest.ClusterFrom(ctx)))
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

func urlWithCluster(vwURL string, cluster genericapirequest.Cluster) string {
	if cluster.Wildcard {
		return fmt.Sprintf("%s/clusters/*", vwURL)
	}
	return fmt.Sprintf("%s/clusters/%s", vwURL, cluster.Name)
}
