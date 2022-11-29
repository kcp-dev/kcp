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

package server

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/endpoints/handlers/responsewriters"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/klog/v2"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	kcpinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/syncer/shared"
	"github.com/kcp-dev/kcp/pkg/tunneler"
)

// WithPodSubresourceProxying proxies the POD subresource requests using the syncer tunneler.
func WithPodSubresourceProxying(apiHandler http.Handler, kcpclient dynamic.ClusterInterface, kcpInformer kcpinformers.SharedInformerFactory) http.Handler {
	syncTargetInformer, err := kcpInformer.ForResource(schema.GroupVersionResource{Group: "workload.kcp.io", Resource: "synctargets", Version: "v1alpha1"})
	if err != nil {
		panic(err)
	}

	return &podSubresourceProxyHandler{
		apiHandler: apiHandler,

		getPodByName: func(ctx context.Context, cluster logicalcluster.Name, namespace, podName string) (*corev1.Pod, error) {
			unstr, err := kcpclient.Cluster(cluster.Path()).Resource(schema.GroupVersionResource{Group: "", Resource: "pods", Version: "v1"}).Namespace(namespace).Get(ctx, podName, metav1.GetOptions{})
			if err != nil {
				return nil, err
			}
			pod := &corev1.Pod{}
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(unstr.Object, pod); err != nil {
				return nil, err
			}
			return pod, nil
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

type podSubresourceProxyHandler struct {
	apiHandler                   http.Handler
	getPodByName                 func(ctx context.Context, cluster logicalcluster.Name, namespace, podName string) (*corev1.Pod, error)
	getSyncTargetBySynctargetKey func(ctx context.Context, synctargetKey string) (*workloadv1alpha1.SyncTarget, error)
}

func (b *podSubresourceProxyHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
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
		logger.Error(nil, "no cluster found in the context")
		b.apiHandler.ServeHTTP(w, req)
		return
	}

	if requestInfo.Resource != "pods" || requestInfo.Subresource == "" {
		b.apiHandler.ServeHTTP(w, req)
		return
	}

	namespace := requestInfo.Namespace
	podName := requestInfo.Name
	subresource := requestInfo.Subresource

	// If something is empty, just return..
	if namespace == "" || podName == "" || subresource == "" {
		b.apiHandler.ServeHTTP(w, req)
		return
	}

	// Check if the subresource is valid, we only support exec, log, portforward, proxy and attach.
	if subresource != "exec" && subresource != "log" && subresource != "portforward" && subresource != "proxy" && subresource != "attach" {
		responsewriters.ErrorNegotiated(
			apierrors.NewBadRequest(fmt.Sprintf("invalid subresource or not implemented %q", subresource)),
			errorCodecs, schema.GroupVersion{}, w, req,
		)
		b.apiHandler.ServeHTTP(w, req)
		return
	}

	// Now let's start the proxying
	logger.Info("proxying pod subresource", "namespace", namespace, "podName", podName, "subresource", subresource)

	pod, err := b.getPodByName(req.Context(), cluster.Name, namespace, podName)
	if apierrors.IsNotFound(err) {
		responsewriters.ErrorNegotiated(
			apierrors.NewNotFound(schema.GroupResource{Group: "", Resource: "pods"}, podName),
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
	for k, v := range pod.GetLabels() {
		if strings.HasPrefix(k, workloadv1alpha1.ClusterResourceStateLabelPrefix) {
			if v == string(workloadv1alpha1.ResourceStateUpsync) {
				synctargetKey = strings.TrimPrefix(k, workloadv1alpha1.ClusterResourceStateLabelPrefix)
				break
			}
		}
	}
	if synctargetKey == "" {
		responsewriters.ErrorNegotiated(
			apierrors.NewBadRequest(fmt.Sprintf("pod %q is not upsynced", podName)),
			errorCodecs, schema.GroupVersion{}, w, req,
		)
		b.apiHandler.ServeHTTP(w, req)
		return
	}

	synctarget, err := b.getSyncTargetBySynctargetKey(req.Context(), synctargetKey)
	if apierrors.IsNotFound(err) {
		logger.Error(err, "synctarget not found when trying to proxy subresource", "synctargetKey", synctargetKey, "subresource", subresource, "podName", podName)
		responsewriters.ErrorNegotiated(
			apierrors.NewServiceUnavailable(fmt.Sprintf("subresource %q is not available right now for pod %q", subresource, podName)),
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

	// Let's find the downstream namespace for the POD
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
		logger.Error(err, "unable to find downstream namespace for pod", "namespace", namespace, "podName", podName)
		responsewriters.ErrorNegotiated(
			apierrors.NewInternalError(err),
			errorCodecs, schema.GroupVersion{}, w, req,
		)
		b.apiHandler.ServeHTTP(w, req)
		return
	}

	// Rewrite the path to point to the SyncerTunnel proxy path.
	proxyPathURL, err := syncerTunnelProxyPath(logicalcluster.From(synctarget), synctarget.GetName(), downstreamNamespace, podName, subresource)
	if err != nil {
		logger.Error(err, "unable to get syncer tunnel proxy path")
		responsewriters.ErrorNegotiated(
			apierrors.NewInternalError(err),
			errorCodecs, schema.GroupVersion{}, w, req,
		)
		b.apiHandler.ServeHTTP(w, req)
		return
	}

	req.URL.Path = proxyPathURL.Path
	req.RequestURI = proxyPathURL.Path
	b.apiHandler.ServeHTTP(w, req)
}

func syncerTunnelProxyPath(syncTargetWorkspaceName logicalcluster.Name, syncTargetName, downstreamNamespaceName, podName, subresource string) (url.URL, error) {
	if syncTargetWorkspaceName.String() == "" || syncTargetName == "" || downstreamNamespaceName == "" || podName == "" || subresource == "" {
		return url.URL{}, fmt.Errorf("invalid tunnel path: workspaceName=%q, syncTargetName=%q, downstreamNamespaceName=%q, podName=%q, subresource=%q", syncTargetWorkspaceName.String(), syncTargetName, downstreamNamespaceName, podName, subresource)
	}

	proxyPath := fmt.Sprintf("/clusters/%s/syncer-tunnels/apis/%s/synctargets/%s/%s/api/v1/namespaces/%s/pods/%s/%s", syncTargetWorkspaceName.String(), workloadv1alpha1.SchemeGroupVersion.String(), syncTargetName, tunneler.CmdTunnelProxy, downstreamNamespaceName, podName, subresource)
	parse, err := url.Parse(proxyPath)
	if err != nil {
		return url.URL{}, err
	}
	return *parse, nil
}
