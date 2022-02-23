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

package ingresssplitter

import (
	"sync"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	k8scache "k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
)

// tracker is used to track the relationship between services and ingresses.
// It is used to determine which ingresses are affected by a service change and
// trigger a reconciliation of the affected ingresses.
type tracker struct {
	lock               sync.Mutex
	serviceToIngresses map[string]sets.String
	ingressToServices  map[string]sets.String
}

// newTracker creates a new tracker.
func newTracker() tracker {
	return tracker{
		serviceToIngresses: make(map[string]sets.String),
		ingressToServices:  make(map[string]sets.String),
	}
}

// getIngressesForService returns the list of ingresses that are related to a given service.
func (t *tracker) getIngressesForService(key string) sets.String {
	t.lock.Lock()
	defer t.lock.Unlock()

	ingresses, ok := t.serviceToIngresses[key]
	if !ok {
		return sets.String{}
	}

	return ingresses
}

// Adds a service to an ingress (key) to be tracked.
func (t *tracker) add(ingress *networkingv1.Ingress, s *corev1.Service) {
	t.lock.Lock()
	defer t.lock.Unlock()

	klog.Infof("tracking service %q for ingress %q", s.Name, ingress.Name)

	ingressKey, err := k8scache.MetaNamespaceKeyFunc(ingress)
	if err != nil {
		klog.Errorf("Failed to get ingress key: %v", err)
		return
	}

	serviceKey, err := k8scache.MetaNamespaceKeyFunc(s)
	if err != nil {
		klog.Errorf("Failed to get service key: %v", err)
		return
	}

	if t.serviceToIngresses[serviceKey] == nil {
		t.serviceToIngresses[serviceKey] = sets.NewString()
	}

	t.serviceToIngresses[serviceKey].Insert(ingressKey)

	if t.ingressToServices[ingressKey] == nil {
		t.ingressToServices[ingressKey] = sets.NewString()
	}

	t.ingressToServices[ingressKey].Insert(serviceKey)
}

// deleteIngress deletes an ingress from all the tracked services
func (t *tracker) deleteIngress(ingressKey string) {
	t.lock.Lock()
	defer t.lock.Unlock()

	services, ok := t.ingressToServices[ingressKey]
	if !ok {
		return
	}

	for _, serviceKey := range services.List() {
		t.serviceToIngresses[serviceKey].Delete(ingressKey)
	}

	delete(t.ingressToServices, ingressKey)
}
