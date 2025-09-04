package apiserver

import (
	"context"
	"crypto/tls"
	"fmt"
	"strings"

	apiextensionsinternal "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsapiserver "k8s.io/apiextensions-apiserver/pkg/apiserver"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	structuraldefaulting "k8s.io/apiextensions-apiserver/pkg/apiserver/schema/defaulting"
	apiservervalidation "k8s.io/apiextensions-apiserver/pkg/apiserver/validation"
	"k8s.io/apiextensions-apiserver/pkg/controller/openapi/builder"
	"k8s.io/apiextensions-apiserver/pkg/crdserverscheme"
	"k8s.io/apiextensions-apiserver/pkg/registry/customresource/tableconvertor"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/conversion"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/managedfields"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apiserver/pkg/endpoints/handlers"
	"k8s.io/apiserver/pkg/endpoints/openapi"
	genericapiserver "k8s.io/apiserver/pkg/server"
	utilopenapi "k8s.io/apiserver/pkg/util/openapi"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	"k8s.io/kube-openapi/pkg/validation/spec"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"

	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apidefinition"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/virtualapidefinition"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
)

type virtualServingInfo struct {
	logicalClusterName logicalcluster.Name

	vwURL       string
	vwTlsConfig *tls.Config
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
		vwURL:       selectedEndpointURL,
		vwTlsConfig: tlsConfig,
	}, nil
}

func (def *virtualServingInfo) GetAPIGroups() ([]metav1.APIGroup, error) {

}

func (def *virtualServingInfo) GetAPIResources() ([]metav1.APIResource, error) {

}

func (def *virtualServingInfo) TearDown() {

}
