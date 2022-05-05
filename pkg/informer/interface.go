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

// copy of client-go/dynamic/dynamic/dynamicinformer. DO NOT TOUCH!

package informer

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/informers"
)

// DynamicSharedInformerFactory provides access to a shared informer and lister for dynamic client
type DynamicSharedInformerFactory interface {
	Start(stopCh <-chan struct{})
	ForResource(gvr schema.GroupVersionResource) informers.GenericInformer
	WaitForCacheSync(stopCh <-chan struct{}) map[schema.GroupVersionResource]bool

	// Remove deregisters the given informer for the gvr, and does nothing if another informer for the same gvr is registered.
	Remove(gvr schema.GroupVersionResource, informer informers.GenericInformer)
}

// TweakListOptionsFunc defines the signature of a helper function
// that wants to provide more listing options to API
type TweakListOptionsFunc func(*metav1.ListOptions)
