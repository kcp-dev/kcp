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

package builder

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"

	"github.com/davecgh/go-spew/spew"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/virtual/framework"
	virtualworkspacesdynamic "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apidefinition"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/handler"
	proxyv1alpha1 "github.com/kcp-dev/kcp/proxy/apis/proxy/v1alpha1"
	proxyinformers "github.com/kcp-dev/kcp/proxy/client/informers/externalversions"
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

type edgeParameters struct {
	virtualWorkspaceName string
}

func (c *clusterProxyProvider) newTemplate(clusterParameters clusterParameters, edgeParameters edgeParameters) *clusterProxy {
	return &clusterProxy{
		clusterProxyProvider: *c,
		clusterParameters:    clusterParameters,
		edgeParameters:       edgeParameters,
		readyEdgeCh:          make(chan struct{}),
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
	edgeParameters

	readyEdgeCh    chan struct{}
	readyClusterCh chan struct{}

	// Proxy dialer pool
	mu   sync.Mutex
	pool map[key]*Dialer
}

func (p *clusterProxy) readyEdge() error {
	select {
	case <-p.readyEdgeCh:
		return nil
	default:
		return errors.New("proxy virtual workspace controllers are not started")
	}
}

func (p *clusterProxy) readyCluster() error {
	select {
	case <-p.readyClusterCh:
		return nil
	default:
		return errors.New("proxy virtual workspace controllers are not started")
	}
}

func (p *clusterProxy) bootstrapClusterManagement(mainConfig genericapiserver.CompletedConfig) (apidefinition.APIDefinitionSetGetter, error) {
	defer close(p.readyClusterCh)
	defer close(p.readyEdgeCh)
	// TBC

	return nil, nil
}

func (p *clusterProxy) buildEdgeProxyVirtualWorkspace() *handler.VirtualWorkspace {
	return &handler.VirtualWorkspace{
		RootPathResolver: framework.RootPathResolverFunc(p.resolveEdgeRootPath),
		Authorizer:       authorizer.AuthorizerFunc(p.authorizeEdge),
		ReadyChecker:     framework.ReadyFunc(p.readyEdge),
		HandlerFactory: handler.HandlerFactory(func(rootAPIServerConfig genericapiserver.CompletedConfig) (http.Handler, error) {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ctx := r.Context()
				spew.Dump(genericapirequest.RequestInfoFrom(ctx))
				spew.Dump(ctx)
				logger := klog.FromContext(ctx)
				logger.Info("edge proxy virtual workspace handler start")

				ri, exists := genericapirequest.RequestInfoFrom(ctx)
				if !exists {
					http.Error(w, "could not determine resource", http.StatusInternalServerError)
					return
				}
				spew.Dump(ri)
				os.Exit(1)

				if !ri.IsResourceRequest ||
					ri.Resource != "workspaceproxies" ||
					ri.Subresource != "tunnel" ||
					ri.APIGroup != proxyv1alpha1.SchemeGroupVersion.Group ||
					ri.APIVersion != proxyv1alpha1.SchemeGroupVersion.Version ||
					ri.Name == "" {
					spew.Dump(ri)
					http.Error(w, "could not determine resource type", http.StatusInternalServerError)
					return
				}

				cluster, err := genericapirequest.ValidClusterFrom(ctx)
				if err != nil {
					http.Error(w, fmt.Sprintf("could not determine cluster for request: %v", err), http.StatusInternalServerError)
					return
				}

				logger.Info("edge proxy virtual workspace handler end")

				spew.Dump(cluster)
				spew.Dump("edge proxy virtual workspace handler")

			}), nil
		}),
	}
}

func (p *clusterProxy) buildClusterProxyVirtualWorkspace() *virtualworkspacesdynamic.DynamicVirtualWorkspace {
	return &virtualworkspacesdynamic.DynamicVirtualWorkspace{
		RootPathResolver:          framework.RootPathResolverFunc(p.resolveClusterProxyRootPath),
		Authorizer:                authorizer.AuthorizerFunc(p.authorizeCluster),
		ReadyChecker:              framework.ReadyFunc(p.readyEdge),
		BootstrapAPISetManagement: p.bootstrapClusterManagement,
	}
}
