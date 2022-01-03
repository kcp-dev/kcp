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

package gvk

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
)

// GVKTranslator translates a given GroupVersionKind into a
// GroupVersionResource, using a RESTMapper.
type GVKTranslator struct {
	m *restmapper.DeferredDiscoveryRESTMapper
}

// NewGVKTranslator returns a GVKTranslator that can translate a given
// GroupVersionKind into the corresponding GroupVersionResource, using a
// RESTMapper.
func NewGVKTranslator(cfg *rest.Config) *GVKTranslator {
	return &GVKTranslator{m: restmapper.NewDeferredDiscoveryRESTMapper(
		memory.NewMemCacheClient(
			discovery.NewDiscoveryClientForConfigOrDie(cfg)))}
}

// Get returns the corresponding GroupVersionResource for the given
// GroupVersionKind.
func (t *GVKTranslator) Get(gvk *schema.GroupVersionKind) (*schema.GroupVersionResource, error) {
	m, err := t.m.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return nil, err
	}
	return &m.Resource, nil
}
