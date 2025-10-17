/*
Copyright 2025 The Kubernetes Authors.
Modifications Copyright 2025 The KCP Authors.

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

package generators

import (
	"io"

	"k8s.io/gengo/v2/generator"
	"k8s.io/gengo/v2/namer"
	"k8s.io/gengo/v2/types"
	"k8s.io/klog/v2"
)

// factoryInterfaceGenerator produces a file of interfaces used to break a dependency cycle for
// informer registration.
type factoryInterfaceGenerator struct {
	generator.GoGenerator
	outputPackage                          string
	imports                                namer.ImportTracker
	clientSetPackage                       string
	filtered                               bool
	singleClusterVersionedClientSetPackage string
	singleClusterInformersPackage          string
}

var _ generator.Generator = &factoryInterfaceGenerator{}

func (g *factoryInterfaceGenerator) Filter(_ *generator.Context, _ *types.Type) bool {
	if !g.filtered {
		g.filtered = true
		return true
	}
	return false
}

func (g *factoryInterfaceGenerator) Namers(_ *generator.Context) namer.NameSystems {
	return namer.NameSystems{
		"raw": namer.NewRawNamer(g.outputPackage, g.imports),
	}
}

func (g *factoryInterfaceGenerator) Imports(_ *generator.Context) (imports []string) {
	imports = append(imports, g.imports.ImportLines()...)
	return
}

func (g *factoryInterfaceGenerator) GenerateType(c *generator.Context, t *types.Type, w io.Writer) error {
	sw := generator.NewSnippetWriter(w, c, "$", "$")

	klog.V(5).Infof("processing type %v", t)

	m := map[string]interface{}{
		"scopeableCacheSharedIndexInformer": c.Universe.Type(scopeableCacheSharedIndexInformer),
		"cacheSharedIndexInformer":          c.Universe.Type(cacheSharedIndexInformer),
		"clusterInterface":                  c.Universe.Type(types.Name{Package: g.clientSetPackage, Name: "ClusterInterface"}),
		"interface":                         c.Universe.Type(types.Name{Package: g.singleClusterVersionedClientSetPackage, Name: "Interface"}),
		"runtimeObject":                     c.Universe.Type(runtimeObject),
		"timeDuration":                      c.Universe.Type(timeDuration),
		"metav1ListOptions":                 c.Universe.Type(metav1ListOptions),
	}

	sw.Do(externalSharedInformerFactoryInterface, m)

	if g.singleClusterInformersPackage == "" {
		sw.Do(externalSharedScopedInformerFactoryInterface, m)
	}

	return sw.Error()
}

var externalSharedInformerFactoryInterface = `
// TweakListOptionsFunc is a function that transforms a $.metav1ListOptions|raw$.
type TweakListOptionsFunc func(*$.metav1ListOptions|raw$)

// NewInformerFunc takes $.clusterInterface|raw$ and $.timeDuration|raw$ to return a $.scopeableCacheSharedIndexInformer|raw$.
type NewInformerFunc func($.clusterInterface|raw$, $.timeDuration|raw$) $.scopeableCacheSharedIndexInformer|raw$

// SharedInformerFactory a small interface to allow for adding an informer without an import cycle.
type SharedInformerFactory interface {
	Start(stopCh <-chan struct{})
	InformerFor(obj $.runtimeObject|raw$, newFunc NewInformerFunc) $.scopeableCacheSharedIndexInformer|raw$
}
`

var externalSharedScopedInformerFactoryInterface = `
// NewScopedInformerFunc takes $.interface|raw$ and $.timeDuration|raw$ to return a SharedIndexInformer.
type NewScopedInformerFunc func($.interface|raw$, $.timeDuration|raw$) $.cacheSharedIndexInformer|raw$

// SharedScopedInformerFactory a small interface to allow for adding an informer without an import cycle.
type SharedScopedInformerFactory interface {
	Start(stopCh <-chan struct{})
	InformerFor(obj $.runtimeObject|raw$, newFunc NewScopedInformerFunc) $.cacheSharedIndexInformer|raw$
}
`
