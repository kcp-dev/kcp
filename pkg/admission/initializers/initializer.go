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
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"

	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/admission/initializer"
	quota "k8s.io/apiserver/pkg/quota/v1"

	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kcpinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
)

// NewKcpInformersInitializer returns an admission plugin initializer that injects
// kcp shared informer factories into admission plugins.
func NewKcpInformersInitializer(
	kcpInformers kcpinformers.SharedInformerFactory,
) *kcpInformersInitializer {
	return &kcpInformersInitializer{
		kcpInformers: kcpInformers,
	}
}

type kcpInformersInitializer struct {
	kcpInformers kcpinformers.SharedInformerFactory
}

func (i *kcpInformersInitializer) Initialize(plugin admission.Interface) {
	if wants, ok := plugin.(WantsKcpInformers); ok {
		wants.SetKcpInformers(i.kcpInformers)
	}
}

// NewKubeClusterClientInitializer returns an admission plugin initializer that injects
// a kube cluster client into admission plugins.
func NewKubeClusterClientInitializer(
	kubeClusterClient kcpkubernetesclientset.ClusterInterface,
) *kubeClusterClientInitializer {
	return &kubeClusterClientInitializer{
		kubeClusterClient: kubeClusterClient,
	}
}

type kubeClusterClientInitializer struct {
	kubeClusterClient kcpkubernetesclientset.ClusterInterface
}

func (i *kubeClusterClientInitializer) Initialize(plugin admission.Interface) {
	if wants, ok := plugin.(WantsKubeClusterClient); ok {
		wants.SetKubeClusterClient(i.kubeClusterClient)
	}
}

// NewKcpClusterClientInitializer returns an admission plugin initializer that injects
// a kcp cluster client into admission plugins.
func NewKcpClusterClientInitializer(
	kcpClusterClient kcpclient.ClusterInterface,
) *kcpClusterClientInitializer {
	return &kcpClusterClientInitializer{
		kcpClusterClient: kcpClusterClient,
	}
}

type kcpClusterClientInitializer struct {
	kcpClusterClient kcpclient.ClusterInterface
}

func (i *kcpClusterClientInitializer) Initialize(plugin admission.Interface) {
	if wants, ok := plugin.(WantsKcpClusterClient); ok {
		wants.SetKcpClusterClient(i.kcpClusterClient)
	}
}

// NewDeepSARClientInitializer returns an admission plugin initializer that injects
// a deep SAR client into admission plugins.
func NewDeepSARClientInitializer(
	deepSARClient kcpkubernetesclientset.ClusterInterface,
) *clientConfigInitializer {
	return &clientConfigInitializer{
		deepSARClient: deepSARClient,
	}
}

type clientConfigInitializer struct {
	deepSARClient kcpkubernetesclientset.ClusterInterface
}

func (i *clientConfigInitializer) Initialize(plugin admission.Interface) {
	if wants, ok := plugin.(WantsDeepSARClient); ok {
		wants.SetDeepSARClient(i.deepSARClient)
	}
}

// NewKubeQuotaConfigurationInitializer returns an admission plugin initializer that injects quota.Configuration
// into admission plugins.
func NewKubeQuotaConfigurationInitializer(quotaConfiguration quota.Configuration) *kubeQuotaConfigurationInitializer {
	return &kubeQuotaConfigurationInitializer{
		quotaConfiguration: quotaConfiguration,
	}
}

type kubeQuotaConfigurationInitializer struct {
	quotaConfiguration quota.Configuration
}

func (i *kubeQuotaConfigurationInitializer) Initialize(plugin admission.Interface) {
	if wants, ok := plugin.(initializer.WantsQuotaConfiguration); ok {
		wants.SetQuotaConfiguration(i.quotaConfiguration)
	}
}

// NewServerShutdownInitializer returns an admission plugin initializer that injects the server's shutdown channel
// into admission plugins.
func NewServerShutdownInitializer(ch <-chan struct{}) *serverShutdownChannelInitializer {
	return &serverShutdownChannelInitializer{
		ch: ch,
	}
}

type serverShutdownChannelInitializer struct {
	ch <-chan struct{}
}

func (i *serverShutdownChannelInitializer) Initialize(plugin admission.Interface) {
	if wants, ok := plugin.(WantsServerShutdownChannel); ok {
		wants.SetServerShutdownChannel(i.ch)
	}
}
