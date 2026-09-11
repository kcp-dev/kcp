/*
Copyright 2026 The kcp Authors.

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

package cacheannotation

import (
	"context"
	"fmt"
	"io"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/admission"

	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
)

// PluginName is the name used to identify this admission plugin.
const PluginName = "core.kcp.io/CacheSelfAnnotation"

const (
	annotationKey   = "kcp.io/cache"
	annotationValue = ".self"
)

// Ensure interfaces are satisfied at compile time.
var (
	_ = admission.MutationInterface(&CacheSelfAnnotation{})
	_ = admission.InitializationValidator(&CacheSelfAnnotation{})
)

// Register registers the CacheSelfAnnotation admission plugin.
func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName, func(_ io.Reader) (admission.Interface, error) {
		return &CacheSelfAnnotation{
			Handler: admission.NewHandler(admission.Create, admission.Update),
		}, nil
	})
}

// CacheSelfAnnotation is a mutating admission plugin that annotates a Cache
// object with "kcp.io/cache": ".self" when its name matches the running
// cache server's --cache-name value.
type CacheSelfAnnotation struct {
	*admission.Handler
	cacheName string
}

// WantsCacheName is implemented by plugins that need the --cache-name value.
type WantsCacheName interface {
	SetCacheName(string)
}

// SetCacheName satisfies WantsCacheName.
func (p *CacheSelfAnnotation) SetCacheName(name string) { p.cacheName = name }

// ValidateInitialization ensures the required injected fields are set.
func (p *CacheSelfAnnotation) ValidateInitialization() error {
	if p.cacheName == "" {
		return fmt.Errorf("%s plugin requires a non-empty cache name", PluginName)
	}
	return nil
}

// Admit sets the self-annotation on Cache objects whose name matches the
// running cache server's name.
func (p *CacheSelfAnnotation) Admit(_ context.Context, a admission.Attributes, _ admission.ObjectInterfaces) error {
	if a.GetResource().GroupResource() != corev1alpha1.Resource("caches") ||
		a.GetKind().GroupKind() != corev1alpha1.Kind("Cache") {
		return nil
	}
	if a.GetOperation() != admission.Create && a.GetOperation() != admission.Update {
		return nil
	}

	obj, ok := a.GetObject().(metav1.Object)
	if !ok {
		return fmt.Errorf("unexpected type %T", a.GetObject())
	}

	if obj.GetName() != p.cacheName {
		return nil
	}

	annotations := obj.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}
	annotations[annotationKey] = annotationValue
	obj.SetAnnotations(annotations)
	return nil
}

// cacheNameInitializer injects the cache server's name into plugins that
// implement WantsCacheName.
type cacheNameInitializer struct{ cacheName string }

// NewCacheNameInitializer returns a PluginInitializer that injects cacheName.
func NewCacheNameInitializer(cacheName string) admission.PluginInitializer {
	return &cacheNameInitializer{cacheName: cacheName}
}

func (i *cacheNameInitializer) Initialize(plugin admission.Interface) {
	if wants, ok := plugin.(WantsCacheName); ok {
		wants.SetCacheName(i.cacheName)
	}
}
