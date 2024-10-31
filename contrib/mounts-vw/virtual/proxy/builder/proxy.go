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

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/kcp/pkg/virtual/framework"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/handler"
	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/rest"

	targetsv1alpha1 "github.com/kcp-dev/kcp/contrib/mounts-vw/apis/targets/v1alpha1"
	mountsinformers "github.com/kcp-dev/kcp/contrib/mounts-vw/client/informers/externalversions"
	"github.com/kcp-dev/kcp/contrib/mounts-vw/state"
)

type clusterProxyProvider struct {
	kubeClusterClient    kcpkubernetesclientset.ClusterInterface
	dynamicClusterClient kcpdynamic.ClusterInterface
	cachedProxyInformers mountsinformers.SharedInformerFactory
	rootPathPrefix       string
	state                state.ClientSetStoreInterface
}

type clusterParameters struct {
	virtualWorkspaceName string
}

func (c *clusterProxyProvider) newTemplate(clusterParameters clusterParameters, store state.ClientSetStoreInterface) *clusterProxy {
	return &clusterProxy{
		clusterProxyProvider: *c,
		clusterParameters:    clusterParameters,
		readyClusterCh:       make(chan struct{}),
		store:                store,
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
	mu    sync.Mutex
	store state.ClientSetStoreInterface
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
					//	logger := klog.FromContext(ctx)

					ri, exists := genericapirequest.RequestInfoFrom(ctx)
					if !exists {
						http.Error(w, "could not determine resource", http.StatusInternalServerError)
						return
					}

					if !ri.IsResourceRequest ||
						ri.Resource != "kubeclusters" ||
						ri.Subresource != "proxy" ||
						ri.APIGroup != targetsv1alpha1.SchemeGroupVersion.Group ||
						ri.APIVersion != targetsv1alpha1.SchemeGroupVersion.Version ||
						ri.Name == "" {
						http.Error(w, "could not determine resource", http.StatusInternalServerError)
						return
					}

					// Get secrets from url and strip it away:
					// https://localhost:6444/services/cluster-proxy/1t1mlh6tgdzuelpg/apis/targets.contrib.kcp.io/v1alpha1/kubeclusters/proxy-cluster/proxy/secret/cFI-tl0Qvwqnln4N
					//                  ┌─────────────────────────────────────────────────────────────────────────────────────────────────────────────────────┘
					// We are now here: ┘
					parts := strings.SplitN(r.URL.Path, "/", 8)
					secrets := strings.TrimPrefix(parts[7], "secret/")
					parts = strings.SplitN(secrets, "/", 2)

					value, found := p.store.Get(state.KindKubeClusters, parts[0])
					if !found {
						http.Error(w, "Unauthorized", http.StatusInternalServerError)
						return
					}

					// Parse the API server URL from the config
					apiServerURL, err := url.Parse(value.Config.Host)
					if err != nil {
						http.Error(w, err.Error(), http.StatusInternalServerError)
						return
					}

					// Create a reverse proxy that targets the Kubernetes API server
					proxy := httputil.NewSingleHostReverseProxy(apiServerURL)
					transport, err := rest.TransportFor(value.Config)
					if err != nil {
						http.Error(w, err.Error(), http.StatusInternalServerError)
						return
					}
					proxy.Transport = transport

					r.URL.Path = parts[1]
					proxy.ServeHTTP(w, r)

				}), nil
		}),
	}
}
