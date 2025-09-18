package endpointslice

import (
	"context"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"
)

func GetUnstructuredByLogicalClusterAndGVRAndName(ctx context.Context, cluster logicalcluster.Name, dynamicClusterClient kcpdynamic.ClusterInterface, gvr schema.GroupVersionResource, name string) (*unstructured.Unstructured, error) {
	list, err := dynamicClusterClient.Cluster(cluster.Path()).Resource(gvr).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	if len(list.Items) == 0 {
		return nil, apierrors.NewNotFound(gvr.GroupResource(), name)
	}
	if len(list.Items) > 1 {
		return nil, apierrors.NewInternalError(fmt.Errorf("multiple objects found"))
	}

	return &list.Items[0], nil
}

func ListURLsFromUnstructured(endpointSlice *unstructured.Unstructured) ([]string, error) {
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

	return urls, nil
}

func FindOneURL(prefix string, urls []string) (string, error) {
	var matches []string
	for _, url := range urls {
		if strings.HasPrefix(url, prefix) {
			matches = append(matches, url)
		}
	}

	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return "", fmt.Errorf("no URLs match prefix %q", prefix)
	default:
		return "", fmt.Errorf("ambiguous URLs %v with prefix %q", matches, prefix)
	}
}
