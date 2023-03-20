/*
Copyright 2021 The KCP Authors.

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

package tunneler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apiserver/pkg/endpoints/handlers/responsewriters"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/klog/v2"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	kcpinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/syncer/shared"
)

var (
	errorScheme = runtime.NewScheme()
	errorCodecs = serializer.NewCodecFactory(errorScheme)
)

// WithSubresourceTunnelling tunnels the subresource requests using the syncer tunneler.
func (tn *tunneler) WithSubresourceTunnelling(apiHandler http.Handler, kcpclient dynamic.ClusterInterface, kcpInformer kcpinformers.SharedInformerFactory) http.Handler {
	syncTargetInformer, err := kcpInformer.ForResource(workloadv1alpha1.SchemeGroupVersion.WithResource("synctargets"))
	if err != nil {
		panic(err)
	}

	return &subresourceTunnelHandler{
		proxyFunc:  tn.Proxy,
		apiHandler: apiHandler,
		getByName: func(ctx context.Context, resource string, cluster logicalcluster.Name, namespace, name string) (*unstructured.Unstructured, error) {
			unstr, err := kcpclient.Cluster(cluster.Path()).Resource(corev1.SchemeGroupVersion.WithResource(resource)).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return nil, err
			}
			return unstr, nil
		},
		getSyncTargetBySynctargetKey: func(ctx context.Context, synctargetKey string) (*workloadv1alpha1.SyncTarget, error) {
			synctargets, err := syncTargetInformer.Informer().GetIndexer().ByIndex(indexers.SyncTargetsBySyncTargetKey, synctargetKey)
			if err != nil {
				return nil, err
			}
			if len(synctargets) != 1 {
				return nil, fmt.Errorf("expected 1 synctarget for key %q, got %d", synctargetKey, len(synctargets))
			}
			synctarget, ok := synctargets[0].(*workloadv1alpha1.SyncTarget)
			if !ok {
				return nil, fmt.Errorf("expected synctarget to be of type %T, got %T", &workloadv1alpha1.SyncTarget{}, synctargets[0])
			}
			return synctarget, nil
		},
	}
}

type subresourceTunnelHandler struct {
	proxyFunc                    func(clusterName logicalcluster.Name, syncerName string, rw http.ResponseWriter, req *http.Request)
	apiHandler                   http.Handler
	getByName                    func(ctx context.Context, resource string, cluster logicalcluster.Name, namespace, name string) (*unstructured.Unstructured, error)
	getSyncTargetBySynctargetKey func(ctx context.Context, synctargetKey string) (*workloadv1alpha1.SyncTarget, error)
}

func (b *subresourceTunnelHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	logger := klog.FromContext(req.Context())
	cluster := request.ClusterFrom(req.Context())
	ctx := req.Context()
	requestInfo, ok := request.RequestInfoFrom(ctx)
	// If the requestInfo is not present, just return.
	if !ok {
		b.apiHandler.ServeHTTP(w, req)
		return
	}

	if !requestInfo.IsResourceRequest {
		b.apiHandler.ServeHTTP(w, req)
		return
	}

	if cluster.Name.Empty() {
		b.apiHandler.ServeHTTP(w, req)
		return
	}

	if requestInfo.Subresource == "" {
		b.apiHandler.ServeHTTP(w, req)
		return
	}

	resource := requestInfo.Resource
	namespace := requestInfo.Namespace
	name := requestInfo.Name
	fullName := name
	subresource := requestInfo.Subresource

	// If something is empty, just return..
	if namespace == "" || name == "" || subresource == "" {
		b.apiHandler.ServeHTTP(w, req)
		return
	}

	var expectedRequestState workloadv1alpha1.ResourceState
	switch resource {
	case "pods":
		// Check if the subresource is valid, for Pods we support exec, log, portforward, proxy , attach and ephemeralcontainers.
		if subresource != "exec" && subresource != "log" && subresource != "portforward" && subresource != "proxy" && subresource != "attach" && subresource != "ephemeralcontainers" {
			responsewriters.ErrorNegotiated(
				apierrors.NewBadRequest(fmt.Sprintf("invalid subresource or not implemented %q", subresource)),
				errorCodecs, schema.GroupVersion{}, w, req,
			)
			b.apiHandler.ServeHTTP(w, req)
			return
		}
		expectedRequestState = workloadv1alpha1.ResourceStateUpsync
	case "services":
		// Check if the subresource is valid, for Services we support only proxy.
		if subresource != "proxy" {
			responsewriters.ErrorNegotiated(
				apierrors.NewBadRequest(fmt.Sprintf("invalid subresource or not implemented %q", subresource)),
				errorCodecs, schema.GroupVersion{}, w, req,
			)
			b.apiHandler.ServeHTTP(w, req)
			return
		}
		expectedRequestState = workloadv1alpha1.ResourceStateSync
		nameParts := strings.SplitN(name, ":", 3)
		partNumber := len(nameParts)
		switch partNumber {
		case 2:
			name = nameParts[0]
		case 3:
			name = nameParts[1]
		}
	default:
		b.apiHandler.ServeHTTP(w, req)
		return
	}

	// Now let's start the proxying
	logger.Info("proxying subresource", "resource", resource, "namespace", namespace, "name", name, "subresource", subresource)

	object, err := b.getByName(req.Context(), resource, cluster.Name, namespace, name)
	if apierrors.IsNotFound(err) {
		responsewriters.ErrorNegotiated(
			apierrors.NewNotFound(schema.GroupResource{Group: "", Resource: resource}, name),
			errorCodecs, schema.GroupVersion{}, w, req,
		)
		b.apiHandler.ServeHTTP(w, req)
		return
	}
	if err != nil {
		responsewriters.ErrorNegotiated(
			apierrors.NewInternalError(err),
			errorCodecs, schema.GroupVersion{}, w, req,
		)
		b.apiHandler.ServeHTTP(w, req)
		return
	}

	// Let's get the synctargetKey
	var synctargetKey string
	for k, v := range object.GetLabels() {
		if strings.HasPrefix(k, workloadv1alpha1.ClusterResourceStateLabelPrefix) {
			if v == string(expectedRequestState) {
				synctargetKey = strings.TrimPrefix(k, workloadv1alpha1.ClusterResourceStateLabelPrefix)
				break
			}
		}
	}
	if synctargetKey == "" {
		responsewriters.ErrorNegotiated(
			apierrors.NewBadRequest(fmt.Sprintf("%s %q is not upsynced", resource, name)),
			errorCodecs, schema.GroupVersion{}, w, req,
		)
		b.apiHandler.ServeHTTP(w, req)
		return
	}

	synctarget, err := b.getSyncTargetBySynctargetKey(req.Context(), synctargetKey)
	if apierrors.IsNotFound(err) {
		logger.Error(err, "synctarget not found when trying to proxy subresource", "synctargetKey", synctargetKey, "resource", resource, "subresource", subresource, "name", name)
		responsewriters.ErrorNegotiated(
			apierrors.NewServiceUnavailable(fmt.Sprintf("subresource %q is not available right now for %s %q", subresource, resource, name)),
			errorCodecs, schema.GroupVersion{}, w, req,
		)
		b.apiHandler.ServeHTTP(w, req)
		return
	}
	if err != nil {
		responsewriters.ErrorNegotiated(
			apierrors.NewInternalError(err),
			errorCodecs, schema.GroupVersion{}, w, req,
		)
		b.apiHandler.ServeHTTP(w, req)
		return
	}

	// Let's find the downstream namespace for the object
	// TODO(jmprusi): This should rely on an annotation in the resource instead of calculating the downstreamNamespace as
	//                there's a possibility that the namespace name is different from the calculated one (migrations, etc).
	downstreamNamespace, err := shared.PhysicalClusterNamespaceName(shared.NamespaceLocator{
		SyncTarget: shared.SyncTargetLocator{
			ClusterName: logicalcluster.From(synctarget).String(),
			Name:        synctarget.GetName(),
			UID:         synctarget.GetUID(),
		},
		ClusterName: cluster.Name,
		Namespace:   namespace,
	})
	if err != nil {
		logger.Error(err, "unable to find downstream namespace", "resource", resource, "namespace", namespace, "name", name)
		responsewriters.ErrorNegotiated(
			apierrors.NewInternalError(err),
			errorCodecs, schema.GroupVersion{}, w, req,
		)
		b.apiHandler.ServeHTTP(w, req)
		return
	}

	var additionalParts []string
	if len(requestInfo.Parts) > 3 && requestInfo.Parts[2] == subresource {
		additionalParts = requestInfo.Parts[3:]
	}

	// Rewrite the path to point to the SyncerTunnel proxy path.
	downstreamURL, err := subresourceURL(resource, downstreamNamespace, fullName, subresource, additionalParts...)
	if err != nil {
		logger.Error(err, "unable to get syncer tunnel proxy path")
		responsewriters.ErrorNegotiated(
			apierrors.NewInternalError(err),
			errorCodecs, schema.GroupVersion{}, w, req,
		)
		b.apiHandler.ServeHTTP(w, req)
		return
	}
	// Set the URL path to the calculated
	req.URL.Path = downstreamURL.Path

	// TODO(davidfestal): the proxy handler of the APIServer seems to respond with 301 if the trailing slash is missing.
	// Redirections are currently not well supported by the tunneler.
	if subresource == "proxy" && len(additionalParts) == 0 {
		req.URL.Path += "/"
	}

	b.proxyFunc(logicalcluster.From(synctarget), synctarget.GetName(), w, req)
}

func subresourceURL(resource, downstreamNamespaceName, name, subresource string, additionalParts ...string) (*url.URL, error) {
	if downstreamNamespaceName == "" || name == "" || subresource == "" {
		return nil, fmt.Errorf("invalid tunnel path: resource=%q, downstreamNamespaceName=%q, name=%q, subresource=%q", resource, downstreamNamespaceName, name, subresource)
	}
	proxyPath, err := url.JoinPath("/api/v1/namespaces", downstreamNamespaceName, resource, name, subresource)
	if err != nil {
		return nil, err
	}
	if len(additionalParts) > 0 {
		proxyPath, err = url.JoinPath(proxyPath, additionalParts...)
		if err != nil {
			return nil, err
		}
	}

	return url.Parse(proxyPath)
}

// Proxy proxies the request to the syncer identified by the cluster and syncername.
func (tn *tunneler) Proxy(clusterName logicalcluster.Name, syncerName string, rw http.ResponseWriter, req *http.Request) {
	d := tn.getDialer(clusterName, syncerName)
	if d == nil || isClosedChan(d.Done()) {
		rw.Header().Set("Retry-After", "1")
		http.Error(rw, "syncer tunnels: tunnel closed", http.StatusServiceUnavailable)
		return
	}

	target, err := url.Parse("http://" + syncerName)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = &http.Transport{
		Proxy:               nil,    // no proxies
		DialContext:         d.Dial, // use a reverse connection
		ForceAttemptHTTP2:   false,  // this is a tunneled connection
		DisableKeepAlives:   true,   // one connection per reverse connection
		MaxIdleConnsPerHost: -1,
	}

	proxy.ServeHTTP(rw, req)
}
