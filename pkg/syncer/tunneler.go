/*
Copyright 2022 The KCP Authors.

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

package syncer

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apiserver/pkg/endpoints/handlers/responsewriters"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/server/requestinfo"
	"github.com/kcp-dev/kcp/pkg/syncer/shared"
	"github.com/kcp-dev/kcp/pkg/tunneler"
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

type ResourceListerFunc func(gvr schema.GroupVersionResource) (cache.GenericLister, error)

// StartSyncerTunnel blocks until the context is cancelled trying to establish a tunnel against the specified target.
func StartSyncerTunnel(ctx context.Context, upstream, downstream *rest.Config, syncTargetWorkspace logicalcluster.Name, syncTargetName, syncTargetUID string, getDownstreamLister ResourceListerFunc) {
	// connect to create the reverse tunnels
	var (
		initBackoff   = 5 * time.Second
		maxBackoff    = 5 * time.Minute
		resetDuration = 1 * time.Minute
		backoffFactor = 2.0
		jitter        = 1.0
		clock         = &clock.RealClock{}
		sliding       = true
	)

	backoffMgr := wait.NewExponentialBackoffManager(initBackoff, maxBackoff, resetDuration, backoffFactor, jitter, clock)
	logger := klog.FromContext(ctx)

	go wait.BackoffUntil(func() {
		logger.V(5).Info("starting tunnel")
		err := startTunneler(ctx, upstream, downstream, syncTargetWorkspace, syncTargetName, syncTargetUID, getDownstreamLister)
		if err != nil {
			logger.Error(err, "failed to create tunnel")
		}
	}, backoffMgr, sliding, ctx.Done())
}

func startTunneler(ctx context.Context, upstream, downstream *rest.Config, syncTargetClusterName logicalcluster.Name, syncTargetName, syncTargetUID string, getDownstreamLister ResourceListerFunc) error {
	logger := klog.FromContext(ctx)

	// syncer --> kcp
	clientUpstream, err := rest.HTTPClientFor(upstream)
	if err != nil {
		return err
	}

	cfg := *downstream
	// use http/1.1 to allow SPDY tunneling: pod exec, port-forward, ...
	cfg.NextProtos = []string{"http/1.1"}
	// syncer --> local apiserver
	url, err := url.Parse(cfg.Host)
	if err != nil {
		return err
	}

	proxy := httputil.NewSingleHostReverseProxy(url)
	if err != nil {
		return err
	}

	clientDownstream, err := rest.HTTPClientFor(&cfg)
	if err != nil {
		return err
	}

	proxy.Transport = clientDownstream.Transport

	// create the reverse connection
	// virtual workspaces
	u, err := url.Parse(upstream.Host)
	if err != nil {
		return err
	}
	// strip the path
	u.Path = ""
	dst, err := tunneler.SyncerTunnelURL(u.String(), syncTargetClusterName.String(), syncTargetName)
	if err != nil {
		return err
	}

	logger = logger.WithValues("syncer-tunnel-url", dst)
	logger.Info("connecting to destination URL")
	l, err := tunneler.NewListener(clientUpstream, dst)
	if err != nil {
		return err
	}
	defer l.Close()

	// reverse proxy the request coming from the reverse connection to the p-cluster apiserver
	server := &http.Server{ReadHeaderTimeout: 30 * time.Second, Handler: withAccessAccessCheck(proxy, getDownstreamLister, syncTargetClusterName, syncTargetName, syncTargetUID)}
	defer server.Close()

	logger.V(2).Info("serving on reverse connection")
	errCh := make(chan error)
	go func() {
		errCh <- server.Serve(l)
	}()

	select {
	case err = <-errCh:
	case <-ctx.Done():
		err = server.Close()
	}
	logger.V(2).Info("stop serving on reverse connection")
	return err
}

func withAccessAccessCheck(handler http.Handler, getDownstreamLister ResourceListerFunc, synctargetClusterName logicalcluster.Name, synctargetName, syncTargetUID string) http.HandlerFunc {
	namespaceGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "namespaces"}

	return func(w http.ResponseWriter, req *http.Request) {
		resolver := requestinfo.NewKCPRequestInfoResolver()
		requestInfo, err := resolver.NewRequestInfo(req)
		if err != nil {
			responsewriters.ErrorNegotiated(
				errors.NewInternalError(fmt.Errorf("could not resolve RequestInfo: %w", err)),
				errorCodecs, schema.GroupVersion{}, w, req,
			)
			return
		}

		resource := requestInfo.Resource
		gvr := schema.GroupVersionResource{Group: "", Version: "v1", Resource: resource}

		// Ensure that requests are only for pods or services
		name := requestInfo.Name
		var expectedRequestState workloadv1alpha1.ResourceState
		switch resource {
		case "pods":
			expectedRequestState = workloadv1alpha1.ResourceStateUpsync
		case "services":
			expectedRequestState = ""
			nameParts := strings.SplitN(name, ":", 3)
			partNumber := len(nameParts)
			switch partNumber {
			case 2:
				name = nameParts[0]
			case 3:
				name = nameParts[1]
			}
		default:
			responsewriters.ErrorNegotiated(
				errors.NewForbidden(gvr.GroupResource(), requestInfo.Name, fmt.Errorf("invalid resource")),
				errorCodecs, schema.GroupVersion{}, w, req,
			)
			return
		}

		// Ensure that requests are only for pods, and we have the required information, if not, return false.
		if requestInfo.Subresource == "" || requestInfo.Name == "" || requestInfo.Namespace == "" {
			responsewriters.ErrorNegotiated(
				errors.NewForbidden(gvr.GroupResource(), requestInfo.Name, fmt.Errorf("invalid subresource")),
				errorCodecs, schema.GroupVersion{}, w, req,
			)
			return
		}

		// Ensure that requests are only for pods in a namespace owned by this syncer, if not, return false.
		downstreamNamespaceName := requestInfo.Namespace

		nsInformer, err := getDownstreamLister(namespaceGVR)
		if err != nil {
			responsewriters.ErrorNegotiated(
				errors.NewInternalError(fmt.Errorf("error while getting downstream namespace lister: %w", err)),
				errorCodecs, schema.GroupVersion{}, w, req,
			)
			return
		}

		obj, err := nsInformer.Get(downstreamNamespaceName)
		if err != nil {
			responsewriters.ErrorNegotiated(
				errors.NewForbidden(namespaceGVR.GroupResource(), requestInfo.Namespace, fmt.Errorf("forbidden")),
				errorCodecs, schema.GroupVersion{}, w, req,
			)
			return
		}

		downstreamNs, ok := obj.(*unstructured.Unstructured)
		if !ok {
			responsewriters.ErrorNegotiated(
				errors.NewInternalError(fmt.Errorf("namespace resource should be *unstructured.Unstructured but was: %T", obj)),
				errorCodecs, schema.GroupVersion{}, w, req,
			)
			return
		}

		// Ensure the referenced downstream namespace locator is correct and owned by this syncer.
		annotations := downstreamNs.GetAnnotations()
		if locator, ok, err := shared.LocatorFromAnnotations(annotations); ok {
			if err != nil || locator.SyncTarget.Name != synctargetName || string(locator.SyncTarget.UID) != syncTargetUID || locator.SyncTarget.ClusterName != string(synctargetClusterName) {
				responsewriters.ErrorNegotiated(
					errors.NewForbidden(gvr.GroupResource(), requestInfo.Name, fmt.Errorf("forbidden")),
					errorCodecs, schema.GroupVersion{}, w, req,
				)
				return
			}
		} else {
			responsewriters.ErrorNegotiated(
				errors.NewForbidden(gvr.GroupResource(), requestInfo.Name, fmt.Errorf("forbidden")),
				errorCodecs, schema.GroupVersion{}, w, req,
			)
			return
		}

		// Ensure Pod is in Upsynced state.
		informer, err := getDownstreamLister(gvr)
		if err != nil {
			responsewriters.ErrorNegotiated(
				errors.NewInternalError(fmt.Errorf("error while getting lister: %w", err)),
				errorCodecs, schema.GroupVersion{}, w, req,
			)
			return
		}
		obj, err = informer.ByNamespace(downstreamNamespaceName).Get(name)
		if err != nil {
			responsewriters.ErrorNegotiated(
				errors.NewForbidden(gvr.GroupResource(), requestInfo.Name, fmt.Errorf("forbidden")),
				errorCodecs, schema.GroupVersion{}, w, req,
			)
			return
		}
		if downstreamObj, ok := obj.(*unstructured.Unstructured); ok {
			if downstreamObj.GetLabels()[workloadv1alpha1.ClusterResourceStateLabelPrefix+workloadv1alpha1.ToSyncTargetKey(synctargetClusterName, synctargetName)] != string(expectedRequestState) {
				responsewriters.ErrorNegotiated(
					errors.NewForbidden(gvr.GroupResource(), requestInfo.Name, fmt.Errorf("forbidden")),
					errorCodecs, schema.GroupVersion{}, w, req,
				)
				return
			}
		} else {
			responsewriters.ErrorNegotiated(
				errors.NewInternalError(fmt.Errorf("resource should be *unstructured.Unstructured but was: %T", obj)),
				errorCodecs, schema.GroupVersion{}, w, req,
			)
			return
		}

		handler.ServeHTTP(w, req)
	}
}
