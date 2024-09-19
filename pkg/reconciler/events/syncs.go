/*
Copyright 2024 The KCP Authors.

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

package events

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"

	"github.com/kcp-dev/kcp/pkg/informer"
)

// WithoutSyncs skips the sync update events.
func WithoutSyncs(h cache.ResourceEventHandler) cache.ResourceEventHandler {
	return &withoutSyncs{h}
}

type withoutSyncs struct {
	cache.ResourceEventHandler
}

func (h *withoutSyncs) OnUpdate(oldObj, newObj interface{}) {
	if oldObj == nil || newObj == nil {
		h.ResourceEventHandler.OnUpdate(oldObj, newObj)
		return
	}
	o, ok := oldObj.(metav1.Object)
	if !ok {
		h.ResourceEventHandler.OnUpdate(oldObj, newObj)
		return
	}
	n, ok := newObj.(metav1.Object)
	if !ok {
		h.ResourceEventHandler.OnUpdate(oldObj, newObj)
		return
	}
	if o.GetResourceVersion() != n.GetResourceVersion() {
		h.ResourceEventHandler.OnUpdate(oldObj, newObj)
	}
}

// WithoutGVRSyncs skips the sync update events.
func WithoutGVRSyncs(h informer.GVREventHandler) informer.GVREventHandler {
	return &withoutGVRSyncs{h}
}

type withoutGVRSyncs struct {
	informer.GVREventHandler
}

func (h *withoutGVRSyncs) OnUpdate(gvr schema.GroupVersionResource, oldObj, newObj interface{}) {
	if oldObj == nil || newObj == nil {
		h.GVREventHandler.OnUpdate(gvr, oldObj, newObj)
		return
	}
	o, ok := oldObj.(metav1.Object)
	if !ok {
		h.GVREventHandler.OnUpdate(gvr, oldObj, newObj)
		return
	}
	n, ok := newObj.(metav1.Object)
	if !ok {
		h.GVREventHandler.OnUpdate(gvr, oldObj, newObj)
		return
	}
	if o.GetResourceVersion() != n.GetResourceVersion() {
		h.GVREventHandler.OnUpdate(gvr, oldObj, newObj)
	}
}
