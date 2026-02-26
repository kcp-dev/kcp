/*
Copyright 2025 The kcp Authors.

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
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type typeMeta struct {
	Group   string
	Version string
	Kind    string

	ResourceSingular string
	ResourcePlural   string

	Scope meta.RESTScope
}

func resourceScopeToRESTScope(scope apiextensionsv1.ResourceScope) meta.RESTScope {
	if scope == apiextensionsv1.ClusterScoped {
		return meta.RESTScopeRoot
	}
	return meta.RESTScopeNamespace
}

func newTypeMeta(group, version, kind, singular, plural string, scope meta.RESTScope) typeMeta {
	m := typeMeta{
		Group:            group,
		Version:          version,
		Kind:             kind,
		ResourceSingular: singular,
		ResourcePlural:   plural,
		Scope:            scope,
	}

	if m.ResourceSingular == "" || m.ResourcePlural == "" {
		singular, plural := meta.UnsafeGuessKindToResource(m.groupVersionKind())
		m.ResourceSingular = singular.Resource
		m.ResourcePlural = plural.Resource
	}

	return m
}

func (m *typeMeta) groupVersionKind() schema.GroupVersionKind {
	return schema.GroupVersionKind{
		Group:   m.Group,
		Version: m.Version,
		Kind:    m.Kind,
	}
}

func (m *typeMeta) groupVersionResourceSingular() schema.GroupVersionResource {
	return schema.GroupVersionResource{
		Group:    m.Group,
		Version:  m.Version,
		Resource: m.ResourceSingular,
	}
}

func (m *typeMeta) groupVersionResourcePlural() schema.GroupVersionResource {
	return schema.GroupVersionResource{
		Group:    m.Group,
		Version:  m.Version,
		Resource: m.ResourcePlural,
	}
}
