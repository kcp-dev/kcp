/*
Copyright 2023 The KCP Authors.

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

package builder

import (
	"errors"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"

	"github.com/davecgh/go-spew/spew"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/kcp/pkg/virtual/framework"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/handler"
	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/klog/v2"

	proxyv1alpha1 "github.com/kcp-dev/kcp/contrib/mounts-vw/apis/proxy/v1alpha1"
	proxyinformers "github.com/kcp-dev/kcp/contrib/mounts-vw/client/informers/externalversions"
)

type clusterProxyProvider struct {
	kubeClusterClient    kcpkubernetesclientset.ClusterInterface
	dynamicClusterClient kcpdynamic.ClusterInterface
	cachedProxyInformers proxyinformers.SharedInformerFactory
	rootPathPrefix       string
}

type clusterParameters struct {
	virtualWorkspaceName string
}

func (c *clusterProxyProvider) newTemplate(clusterParameters clusterParameters) *clusterProxy {
	return &clusterProxy{
		clusterProxyProvider: *c,
		clusterParameters:    clusterParameters,
		readyClusterCh:       make(chan struct{}),
	}
}

type controlMsg struct {
	Command  string `json:"command,omitempty"`  // "keep-alive", "conn-ready", "pickup-failed"
	ConnPath string `json:"connPath,omitempty"` // conn pick-up URL path for "conn-url", "pickup-failed"
	Err      string `json:"err,omitempty"`
}

type key struct {
	clusterName logicalcluster.Name
	proxyName   string
}

type clusterProxy struct {
	clusterProxyProvider

	clusterParameters

	readyClusterCh chan struct{}

	// Proxy dialer pool
	mu   sync.Mutex
	pool map[key]*Dialer
}

func (p *clusterProxy) readyCluster() error {
	select {
	case <-p.readyClusterCh:
		return nil
	default:
		return errors.New("proxy virtual workspace controllers are not started")
	}
}

func (p *clusterProxy) buildClusterProxyVirtualWorkspace() *handler.VirtualWorkspace {
	defer close(p.readyClusterCh)
	return &handler.VirtualWorkspace{
		RootPathResolver: framework.RootPathResolverFunc(p.resolveClusterProxyRootPath),
		Authorizer:       authorizer.AuthorizerFunc(p.authorizeCluster),
		ReadyChecker:     framework.ReadyFunc(p.readyCluster),
		HandlerFactory: handler.HandlerFactory(func(rootAPIServerConfig genericapiserver.CompletedConfig) (http.Handler, error) {
			return http.HandlerFunc(
				func(w http.ResponseWriter, r *http.Request) {
					ctx := r.Context()
					spew.Dump(r.RequestURI)
					logger := klog.FromContext(ctx)

					ri, exists := genericapirequest.RequestInfoFrom(ctx)
					if !exists {
						http.Error(w, "could not determine resource", http.StatusInternalServerError)
						return
					}

					if !ri.IsResourceRequest ||
						ri.Resource != "kubeclusters" ||
						ri.Subresource != "proxy" ||
						ri.APIGroup != proxyv1alpha1.SchemeGroupVersion.Group ||
						ri.APIVersion != proxyv1alpha1.SchemeGroupVersion.Version ||
						ri.Name == "" {
						http.Error(w, "could not determine resource", http.StatusInternalServerError)
						return
					}

					cluster, err := genericapirequest.ValidClusterFrom(ctx)
					if err != nil {
						http.Error(w, "could not determine resource", http.StatusInternalServerError)
						return
					}

					clusterName := cluster.Name
					proxyName := ri.Name

					// TODO: verify proxy exists here

					logger = logger.WithValues("cluster", clusterName, "proxyName", proxyName, "action", "proxy")
					logger.V(5).Info("tunnel connection done", "remoteAddr", r.RemoteAddr)

					// TODO: Get client from existing kubeconfig pool and use it to dial.

					target, err := url.Parse("http://" + proxyName)
					if err != nil {
						http.Error(w, err.Error(), http.StatusInternalServerError)
						return
					}

					parts := strings.SplitN(r.URL.Path, "/", 8)

					resourceURL := parts[7]

					r.URL.Path = resourceURL

					proxy := httputil.NewSingleHostReverseProxy(target)
					proxy.Transport = &http.Transport{
						Proxy: nil, // no proxies
						//DialContext:        // dialer
						ForceAttemptHTTP2:   false, // this is a tunneled connection
						DisableKeepAlives:   true,  // one connection per reverse connection
						MaxIdleConnsPerHost: -1,
					}

					proxy.ServeHTTP(w, r)

				}), nil
		}),
	}
}
