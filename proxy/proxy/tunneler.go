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

package proxy

import (
	"context"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"github.com/kcp-dev/logicalcluster/v3"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"

	"github.com/kcp-dev/kcp/proxy/tunneler"
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

// StartProxyTunnel blocks until the context is cancelled trying to establish a tunnel against the specified target.
func StartProxyTunnel(ctx context.Context, upstream, downstream *rest.Config, proxyTargetWorkspace logicalcluster.Name, proxyTargetName, proxyTargetUID string) {
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
		err := startTunneler(ctx, upstream, downstream, proxyTargetWorkspace, proxyTargetName, proxyTargetUID)
		if err != nil {
			logger.Error(err, "failed to create tunnel")
		}
	}, backoffMgr, sliding, ctx.Done())
}

func startTunneler(ctx context.Context, upstream, downstream *rest.Config, proxyTargetClusterName logicalcluster.Name, proxyTargetName, proxyTargetUID string) error {
	logger := klog.FromContext(ctx)

	// proxy --> kcp
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

	logger = logger.WithValues("proxy-tunnel-url", upstream.Host)
	logger.Info("connecting to destination URL")
	l, err := tunneler.NewListener(clientUpstream, upstream.Host)
	if err != nil {
		return err
	}
	defer l.Close()
	logger.Info("connected to destination URL")

	// reverse proxy the request coming from the reverse connection to the p-cluster apiserver
	server := &http.Server{ReadHeaderTimeout: 30 * time.Second, Handler: withAccessCheck(proxy, proxyTargetClusterName, proxyTargetName, proxyTargetUID)}
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

func withAccessCheck(handler http.Handler, proxyTargetClusterName logicalcluster.Name, proxyTargetName, proxyTargetUID string) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		// TODO: filtering, checking, etc goes here

		handler.ServeHTTP(w, req)
	}
}
