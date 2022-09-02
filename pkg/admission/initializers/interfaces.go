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

package initializers

import (
	kubernetesclient "k8s.io/client-go/kubernetes"

	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kcpinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
)

// WantsKcpInformers interface should be implemented by admission plugins
// that want to have a kcp informer factory injected.
type WantsKcpInformers interface {
	SetKcpInformers(kcpinformers.SharedInformerFactory)
}

// WantsKubeClusterClient interface should be implemented by admission plugins
// that want to have a kube cluster client injected.
type WantsKubeClusterClient interface {
	SetKubeClusterClient(kubernetesclient.ClusterInterface)
}

// WantsKcpClusterClient interface should be implemented by admission plugins
// that want to have a kcp cluster client injected.
type WantsKcpClusterClient interface {
	SetKcpClusterClient(kcpclient.ClusterInterface)
}

// WantsDeepSARClient interface should be implemented by admission plugins
// that want to have a client capable of deep SAR handling.
// See pkg/authorization.WithDeepSARConfig for details.
type WantsDeepSARClient interface {
	SetDeepSARClient(kubernetesclient.ClusterInterface)
}

// WantsExternalAddressProvider interface should be implemented by admission plugins
// that want to have an external address provider injected.
type WantsExternalAddressProvider interface {
	SetExternalAddressProvider(externalAddressProvider func() string)
}

// WantsShardBaseURL interface should be implemented by admission plugins
// that want to have the default shard base url injected.
type WantsShardBaseURL interface {
	SetShardBaseURL(string)
}

// WantsShardExternalURL interface should be implemented by admission plugins
// that want to have the shard external url injected.
type WantsShardExternalURL interface {
	SetShardExternalURL(string)
}

// WantsServerShutdownChannel interface should be implemented by admission plugins that want to perform cleanup
// activities when the main server context/channel is done.
type WantsServerShutdownChannel interface {
	SetServerShutdownChannel(<-chan struct{})
}
