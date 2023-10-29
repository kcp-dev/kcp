//go:build !ignore_autogenerated
// +build !ignore_autogenerated

/*
Copyright The KCP Authors.

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

// Code generated by kcp code-generator. DO NOT EDIT.

package internalinterfaces

import (
	time "time"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	runtime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/cache"

	scopedclientset "github.com/kcp-dev/kcp/proxy/client/clientset/versioned"
	clientset "github.com/kcp-dev/kcp/proxy/client/clientset/versioned/cluster"
)

// NewInformerFunc takes clientset.ClusterInterface and time.Duration to return a ScopeableSharedIndexInformer.
type NewInformerFunc func(clientset.ClusterInterface, time.Duration) kcpcache.ScopeableSharedIndexInformer

// SharedInformerFactory a small interface to allow for adding an informer without an import cycle
type SharedInformerFactory interface {
	Start(stopCh <-chan struct{})
	InformerFor(obj runtime.Object, newFunc NewInformerFunc) kcpcache.ScopeableSharedIndexInformer
}

// NewScopedInformerFunc takes scopedclientset.Interface and time.Duration to return a SharedIndexInformer.
type NewScopedInformerFunc func(scopedclientset.Interface, time.Duration) cache.SharedIndexInformer

// SharedScopedInformerFactory a small interface to allow for adding an informer without an import cycle
type SharedScopedInformerFactory interface {
	Start(stopCh <-chan struct{})
	InformerFor(obj runtime.Object, newFunc NewScopedInformerFunc) cache.SharedIndexInformer
}

// TweakListOptionsFunc is a function that transforms a metav1.ListOptions.
type TweakListOptionsFunc func(*metav1.ListOptions)
