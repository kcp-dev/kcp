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
// the external address of the shard into the admission plugin.
func NewExternalAddressInitializer(
	externalAddress string,
) *externalAddressInitializer {
	return &externalAddressInitializer{
		externalAddress: externalAddress,
	}
}

type externalAddressInitializer struct {
	externalAddress string
}

func (i *externalAddressInitializer) Initialize(plugin admission.Interface) {
	if wants, ok := plugin.(WantExternalAddress); ok {
		wants.SetExternalAddress(i.externalAddress)
	}
}
