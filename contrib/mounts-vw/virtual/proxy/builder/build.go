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
	"strings"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/rootapiserver"

	proxyinformers "github.com/kcp-dev/kcp/contrib/mounts-vw/client/informers/externalversions"
)

const (
	// ProxyVirtualWorkspaceName holds the name of the virtual workspace for the proxy where we
	// expect traffic to be routed to.
	ProxyVirtualWorkspaceName string = "cluster-proxy"
	// EdgeProxyVirtualWorkspaceName holds the name of the virtual workspace for the edge proxy
	// connections. This is used for the edge clusters to connect & register to the cluster proxy.
	//EdgeProxyVirtualWorkspaceName string = "edge-proxy"
	//EdgeTunnelSuffix              string = "proxy"
	ProxyTunnelSuffix string = "proxy"
)

// BuildVirtualWorkspace builds two virtual workspaces, ProxyVirtualWorkspaceName by instantiating a DynamicVirtualWorkspace which,
// combined with a ForwardingREST REST storage implementation, serves a ProxyAPI list maintained by the APIReconciler controller.
func BuildVirtualWorkspace(
	rootPathPrefix string,
	kubeClusterClient kcpkubernetesclientset.ClusterInterface,
	dynamicClusterClient kcpdynamic.ClusterInterface,
	cachedProxyInformers proxyinformers.SharedInformerFactory,
) []rootapiserver.NamedVirtualWorkspace {
	if !strings.HasSuffix(rootPathPrefix, "/") {
		rootPathPrefix += "/"
	}

	proxyProvider := clusterProxyProvider{
		kubeClusterClient:    kubeClusterClient,
		dynamicClusterClient: dynamicClusterClient,
		cachedProxyInformers: cachedProxyInformers,
		rootPathPrefix:       rootPathPrefix,
	}

	builder := proxyProvider.newTemplate(
		clusterParameters{
			virtualWorkspaceName: ProxyVirtualWorkspaceName,
		})

	return []rootapiserver.NamedVirtualWorkspace{
		{
			Name:             ProxyVirtualWorkspaceName,
			VirtualWorkspace: builder.buildClusterProxyVirtualWorkspace(),
		},
	}

}
