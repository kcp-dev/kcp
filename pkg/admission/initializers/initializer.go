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
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/admission/initializer"
	quota "k8s.io/apiserver/pkg/quota/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"

	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
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

// NewKubeInformersInitializer returns an admission plugin initializer that injects
// kube shared informer factories into admission plugins.
func NewKubeInformersInitializer(
	kubeInformers informers.SharedInformerFactory,
) *kubeInformersInitializer {
	return &kubeInformersInitializer{
		kubeInformers: kubeInformers,
	}
}

type kubeInformersInitializer struct {
	kubeInformers informers.SharedInformerFactory
}

func (i *kubeInformersInitializer) Initialize(plugin admission.Interface) {
	if wants, ok := plugin.(initializer.WantsExternalKubeInformerFactory); ok {
		wants.SetExternalKubeInformerFactory(i.kubeInformers)
	}
}

// NewKubeClusterClientInitializer returns an admission plugin initializer that injects
// a kube cluster client into admission plugins.
func NewKubeClusterClientInitializer(
	kubeClusterClient *kubernetes.Cluster,
) *kubeClusterClientInitializer {
	return &kubeClusterClientInitializer{
		kubeClusterClient: kubeClusterClient,
	}
}

type kubeClusterClientInitializer struct {
	kubeClusterClient *kubernetes.Cluster
}

func (i *kubeClusterClientInitializer) Initialize(plugin admission.Interface) {
	if wants, ok := plugin.(WantsKubeClusterClient); ok {
		wants.SetKubeClusterClient(i.kubeClusterClient)
	}
}

// NewKcpClusterClientInitializer returns an admission plugin initializer that injects
// a kcp cluster client into admission plugins.
func NewKcpClusterClientInitializer(
	kcpClusterClient *kcpclientset.Cluster,
) *kcpClusterClientInitializer {
	return &kcpClusterClientInitializer{
		kcpClusterClient: kcpClusterClient,
	}
}

type kcpClusterClientInitializer struct {
	kcpClusterClient *kcpclientset.Cluster
}

func (i *kcpClusterClientInitializer) Initialize(plugin admission.Interface) {
	if wants, ok := plugin.(WantsKcpClusterClient); ok {
		wants.SetKcpClusterClient(i.kcpClusterClient)
	}
}

// NewExternalAddressInitializer returns an admission plugin initializer that injects
// an external address provider into the admission plugin.
func NewExternalAddressInitializer(
	externalAddressProvider func() string,
) *externalAddressInitializer {
	return &externalAddressInitializer{
		externalAddressProvider: externalAddressProvider,
	}
}

type externalAddressInitializer struct {
	externalAddressProvider func() string
}

func (i *externalAddressInitializer) Initialize(plugin admission.Interface) {
	if wants, ok := plugin.(WantsExternalAddressProvider); ok {
		wants.SetExternalAddressProvider(i.externalAddressProvider)
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
