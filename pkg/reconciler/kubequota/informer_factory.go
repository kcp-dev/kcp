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

package kubequota

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/informers"
	"k8s.io/controller-manager/pkg/informerfactory"

	"github.com/kcp-dev/kcp/pkg/informer"
)

// NewInformerFactory returns a new *informerFactory. This is primarily meant to be used by the resource quota
// controller.
func NewInformerFactory(
	kubeInformerFactory informers.SharedInformerFactory,
	dynamicDiscoverySharedInformerFactory *informer.DynamicDiscoverySharedInformerFactory,
) *informerFactory {
	return &informerFactory{
		kubeInformerFactory:                   kubeInformerFactory,
		dynamicDiscoverySharedInformerFactory: dynamicDiscoverySharedInformerFactory,
	}
}

var _ informerfactory.InformerFactory = &informerFactory{}

type informerFactory struct {
	kubeInformerFactory                   informers.SharedInformerFactory
	dynamicDiscoverySharedInformerFactory *informer.DynamicDiscoverySharedInformerFactory
}

var (
	podsGVR                   = corev1.SchemeGroupVersion.WithResource("pods")
	servicesGVR               = corev1.SchemeGroupVersion.WithResource("services")
	persistentVolumeClaimsGVR = corev1.SchemeGroupVersion.WithResource("persistentvolumeclaims")
)

// ForResource returns an informers.GenericInformer for resource. For Pods, Services, and PersistentVolumeClaims, the
// returned informer is backed by a strongly typed informer. All other resources are backed by dynamic partial metadata
// informers.
func (i *informerFactory) ForResource(resource schema.GroupVersionResource) (informers.GenericInformer, error) {
	switch resource {
	case podsGVR, servicesGVR, persistentVolumeClaimsGVR:
		return i.kubeInformerFactory.ForResource(resource)
	}

	return i.dynamicDiscoverySharedInformerFactory.InformerForResource(resource)
}

// Start starts all unstarted but desired shared informers in both the underlying kube and dynamic informer factories.
func (i *informerFactory) Start(stopCh <-chan struct{}) {
	i.kubeInformerFactory.Start(stopCh)
	i.dynamicDiscoverySharedInformerFactory.Start(stopCh)
}
