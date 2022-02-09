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

package ingress

import (
	"sync"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	k8scache "k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
)

// tracker is used to track the relationship between services and ingresses.
// It is used to determine which ingresses are affected by a service change and
// trigger a reconciliation of the affected ingresses.
type tracker struct {
	mu               sync.Mutex
	ingress2services map[string]map[string]*networkingv1.Ingress
}

// newTracker creates a new tracker.
func newTracker() tracker {
	return tracker{
		ingress2services: make(map[string]map[string]*networkingv1.Ingress),
	}
}

// getIngress returns the list of ingresses that are related to a given service, or
// an empty list, and a boolean, false, indicating if the list is empty.
func (t *tracker) getIngress(key string) ([]*networkingv1.Ingress, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	ingresses, ok := convertMap(t.ingress2services)[key]
	return ingresses, ok
}

// Adds a service to an ingress (key) to be tracked.
func (t *tracker) add(ingress *networkingv1.Ingress, s *corev1.Service) {
	t.mu.Lock()
	defer t.mu.Unlock()
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

	if _, ok := t.ingress2services[ingressKey]; !ok {
		t.ingress2services[ingressKey] = make(map[string]*networkingv1.Ingress)
	}

	t.ingress2services[ingressKey][serviceKey] = ingress
}

// deleteIngress deletes an ingress from all the tracked services
func (t *tracker) deleteIngress(ingressKey string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	delete(t.ingress2services, ingressKey)
}

func convertMap(m map[string]map[string]*networkingv1.Ingress) map[string][]*networkingv1.Ingress {
	inv := make(map[string][]*networkingv1.Ingress)
	for _, v := range m {
		for k2, v2 := range v {
			inv[k2] = append(inv[k2], v2)
		}
	}
	return inv
}
