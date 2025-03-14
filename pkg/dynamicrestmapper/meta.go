/*
Copyright 2025 The KCP Authors.

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

package dynamicrestmapper

import (
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type gvkr struct {
	Group   string
	Version string
	Kind    string

	ResourceSingular string
	ResourcePlural   string
}

func newGVKR(group, version, kind, singular, plural string) gvkr {
	m := gvkr{
		Group:            group,
		Version:          version,
		Kind:             kind,
		ResourceSingular: singular,
		ResourcePlural:   plural,
	}

	if m.ResourceSingular == "" || m.ResourcePlural == "" {
		singular, plural := meta.UnsafeGuessKindToResource(m.groupVersionKind())
		m.ResourceSingular = singular.Resource
		m.ResourcePlural = plural.Resource
	}

	return m
}

func (m *gvkr) groupVersionKind() schema.GroupVersionKind {
	return schema.GroupVersionKind{
		Group:   m.Group,
		Version: m.Version,
		Kind:    m.Kind,
	}
}

func (m *gvkr) groupVersionResourceSingular() schema.GroupVersionResource {
	return schema.GroupVersionResource{
		Group:    m.Group,
		Version:  m.Version,
		Resource: m.ResourceSingular,
	}
}

func (m *gvkr) groupVersionResourcePlural() schema.GroupVersionResource {
	return schema.GroupVersionResource{
		Group:    m.Group,
		Version:  m.Version,
		Resource: m.ResourcePlural,
	}
}
