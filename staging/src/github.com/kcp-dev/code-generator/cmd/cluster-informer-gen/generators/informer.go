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
	"fmt"
	"io"
	"path"
	"strings"

	"k8s.io/gengo/v2/generator"
	"k8s.io/gengo/v2/namer"
	"k8s.io/gengo/v2/types"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/code-generator/v3/cmd/cluster-client-gen/generators/util"
	clientgentypes "github.com/kcp-dev/code-generator/v3/cmd/cluster-client-gen/types"
)

// informerGenerator produces a file of listers for a given GroupVersion and
// type.
type informerGenerator struct {
	generator.GoGenerator
	outputPackage                          string
	groupPkgName                           string
	groupVersion                           clientgentypes.GroupVersion
	groupGoName                            string
	typeToGenerate                         *types.Type
	imports                                namer.ImportTracker
	clientSetPackage                       string
	listersPackage                         string
	internalInterfacesPackage              string
	singleClusterVersionedClientSetPackage string
	singleClusterListersPackage            string
	singleClusterInformersPackage          string
}

var _ generator.Generator = &informerGenerator{}

func (g *informerGenerator) Filter(_ *generator.Context, t *types.Type) bool {
	return t == g.typeToGenerate
}

func (g *informerGenerator) Namers(_ *generator.Context) namer.NameSystems {
	return namer.NameSystems{
		"raw": namer.NewRawNamer(g.outputPackage, g.imports),
	}
}

func (g *informerGenerator) Imports(_ *generator.Context) (imports []string) {
	imports = append(imports, g.imports.ImportLines()...)
	return
}

func (g *informerGenerator) GenerateType(c *generator.Context, t *types.Type, w io.Writer) error {
	sw := generator.NewSnippetWriter(w, c, "$", "$")

	klog.V(5).Infof("processing type %v", t)

	clusterListersPkg := fmt.Sprintf("%s/%s/%s", g.listersPackage, g.groupPkgName, strings.ToLower(g.groupVersion.Version.NonEmpty()))
	clientSetInterface := c.Universe.Type(types.Name{Package: g.singleClusterVersionedClientSetPackage, Name: "Interface"})
	clientSetClusterInterface := c.Universe.Type(types.Name{Package: g.clientSetPackage, Name: "ClusterInterface"})

	informerPkg := g.outputPackage
	generateScopedInformer := true
	if g.singleClusterInformersPackage != "" {
		informerPkg = path.Join(g.singleClusterInformersPackage, g.groupPkgName, strings.ToLower(g.groupVersion.Version.NonEmpty()))
		generateScopedInformer = false
	}

	listersPkg := g.listersPackage
	if g.singleClusterInformersPackage != "" {
		listersPkg = g.singleClusterListersPackage
	}
	listersPkg = path.Join(listersPkg, g.groupPkgName, strings.ToLower(g.groupVersion.Version.NonEmpty()))

	tags, err := util.ParseClientGenTags(append(t.SecondClosestCommentLines, t.CommentLines...))
	if err != nil {
		return err
	}

	m := map[string]interface{}{
		"type":                                  t,
		"namespaced":                            !tags.NonNamespaced,
		"cacheIndexers":                         c.Universe.Type(cacheIndexers),
		"cacheListWatch":                        c.Universe.Type(cacheListWatch),
		"cacheMetaNamespaceIndexFunc":           c.Universe.Function(cacheMetaNamespaceIndexFunc),
		"cacheNamespaceIndex":                   c.Universe.Variable(cacheNamespaceIndex),
		"cacheNewSharedIndexInformer":           c.Universe.Function(cacheNewSharedIndexInformer),
		"cacheSharedIndexInformer":              c.Universe.Type(cacheSharedIndexInformer),
		"scopeableCacheSharedIndexInformer":     c.Universe.Type(scopeableCacheSharedIndexInformer),
		"clientSetInterface":                    clientSetInterface,
		"clientSetClusterInterface":             clientSetClusterInterface,
		"contextBackground":                     c.Universe.Function(contextBackgroundFunc),
		"contextContext":                        c.Universe.Type(contextContext),
		"group":                                 namer.IC(g.groupGoName),
		"interfacesTweakListOptionsFunc":        c.Universe.Type(types.Name{Package: g.internalInterfacesPackage, Name: "TweakListOptionsFunc"}),
		"interfacesSharedInformerFactory":       c.Universe.Type(types.Name{Package: g.internalInterfacesPackage, Name: "SharedInformerFactory"}),
		"interfacesSharedScopedInformerFactory": c.Universe.Type(types.Name{Package: g.internalInterfacesPackage, Name: "SharedScopedInformerFactory"}),
		"lister":                                c.Universe.Type(types.Name{Package: listersPkg, Name: t.Name.Name + "Lister"}),
		"clusterLister":                         c.Universe.Type(types.Name{Package: clusterListersPkg, Name: t.Name.Name + "ClusterLister"}),
		"informerInterface":                     c.Universe.Type(types.Name{Package: informerPkg, Name: t.Name.Name + "Informer"}),
		"newLister":                             c.Universe.Function(types.Name{Package: listersPkg, Name: "New" + t.Name.Name + "Lister"}),
		"newClusterLister":                      c.Universe.Function(types.Name{Package: clusterListersPkg, Name: "New" + t.Name.Name + "ClusterLister"}),
		"kcpcacheClusterIndexName":              c.Universe.Function(types.Name{Package: "github.com/kcp-dev/apimachinery/v2/pkg/cache", Name: "ClusterIndexName"}),
		"kcpcacheClusterIndexFunc":              c.Universe.Function(types.Name{Package: "github.com/kcp-dev/apimachinery/v2/pkg/cache", Name: "ClusterIndexFunc"}),
		"kcpcacheClusterAndNamespaceIndexName":  c.Universe.Function(types.Name{Package: "github.com/kcp-dev/apimachinery/v2/pkg/cache", Name: "ClusterAndNamespaceIndexName"}),
		"kcpcacheClusterAndNamespaceIndexFunc":  c.Universe.Function(types.Name{Package: "github.com/kcp-dev/apimachinery/v2/pkg/cache", Name: "ClusterAndNamespaceIndexFunc"}),
		"runtimeObject":                         c.Universe.Type(runtimeObject),
		"timeDuration":                          c.Universe.Type(timeDuration),
		"metav1ListOptions":                     c.Universe.Type(metav1ListOptions),
		"version":                               namer.IC(g.groupVersion.Version.String()),
		"watchInterface":                        c.Universe.Type(watchInterface),
		"logicalclusterName":                    c.Universe.Type(logicalclusterName),
		"kcpinformersNewSharedIndexInformer":    c.Universe.Type(kcpinformersNewSharedIndexInformer),
	}

	sw.Do(typeClusterInformerInterface, m)
	sw.Do(typeClusterInformerStruct, m)
	sw.Do(typeClusterInformerPublicConstructor, m)
	sw.Do(typeFilteredInformerPublicConstructor, m)
	sw.Do(typeInformerConstructor, m)
	sw.Do(typeInformerInformer, m)
	sw.Do(typeInformerLister, m)
	sw.Do(typeInformerCluster, m)
	sw.Do(typeInformer, m)

	if generateScopedInformer {
		sw.Do(typeScopedInformer, m)
	}

	return sw.Error()
}

var typeClusterInformerInterface = `
// $.type|public$ClusterInformer provides access to a shared informer and lister for
// $.type|publicPlural$.
type $.type|public$ClusterInformer interface {
	Cluster($.logicalclusterName|raw$) $.informerInterface|raw$
	ClusterWithContext($.contextContext|raw$, $.logicalclusterName|raw$) $.informerInterface|raw$
	Informer() $.scopeableCacheSharedIndexInformer|raw$
	Lister() $.clusterLister|raw$
}
`

var typeClusterInformerStruct = `
type $.type|private$ClusterInformer struct {
	factory          $.interfacesSharedInformerFactory|raw$
	tweakListOptions $.interfacesTweakListOptionsFunc|raw$
}
`

var typeClusterInformerPublicConstructor = `
// New$.type|public$ClusterInformer constructs a new informer for $.type|public$ type.
// Always prefer using an informer factory to get a shared informer instead of getting an independent
// one. This reduces memory footprint and number of connections to the server.
func New$.type|public$ClusterInformer(client $.clientSetClusterInterface|raw$, resyncPeriod $.timeDuration|raw$, indexers $.cacheIndexers|raw$) $.scopeableCacheSharedIndexInformer|raw$ {
	return NewFiltered$.type|public$ClusterInformer(client, resyncPeriod, indexers, nil)
}
`

var typeFilteredInformerPublicConstructor = `
// NewFiltered$.type|public$ClusterInformer constructs a new informer for $.type|public$ type.
// Always prefer using an informer factory to get a shared informer instead of getting an independent
// one. This reduces memory footprint and number of connections to the server.
func NewFiltered$.type|public$ClusterInformer(client $.clientSetClusterInterface|raw$, resyncPeriod $.timeDuration|raw$, indexers $.cacheIndexers|raw$, tweakListOptions $.interfacesTweakListOptionsFunc|raw$) $.scopeableCacheSharedIndexInformer|raw$ {
	return $.kcpinformersNewSharedIndexInformer|raw$(
		&$.cacheListWatch|raw${
			ListFunc: func(options $.metav1ListOptions|raw$) ($.runtimeObject|raw$, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.$.group$$.version$().$.type|publicPlural$().List($.contextBackground|raw$(), options)
			},
			WatchFunc: func(options $.metav1ListOptions|raw$) ($.watchInterface|raw$, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.$.group$$.version$().$.type|publicPlural$().Watch($.contextBackground|raw$(), options)
			},
		},
		&$.type|raw${},
		resyncPeriod,
		indexers,
	)
}
`

var typeInformerConstructor = `
func (i *$.type|private$ClusterInformer) defaultInformer(client $.clientSetClusterInterface|raw$, resyncPeriod $.timeDuration|raw$) $.scopeableCacheSharedIndexInformer|raw$ {
	return NewFiltered$.type|public$ClusterInformer(client, resyncPeriod, $.cacheIndexers|raw${
		$.kcpcacheClusterIndexName|raw$:             $.kcpcacheClusterIndexFunc|raw$,
		$.kcpcacheClusterAndNamespaceIndexName|raw$: $.kcpcacheClusterAndNamespaceIndexFunc|raw$,
	}, i.tweakListOptions)
}
`

var typeInformerInformer = `
func (i *$.type|private$ClusterInformer) Informer() $.scopeableCacheSharedIndexInformer|raw$ {
	return i.factory.InformerFor(&$.type|raw${}, i.defaultInformer)
}
`

var typeInformerLister = `
func (i *$.type|private$ClusterInformer) Lister() $.clusterLister|raw$ {
	return $.newClusterLister|raw$(i.Informer().GetIndexer())
}
`

var typeInformerCluster = `
func (i *$.type|private$ClusterInformer) Cluster(clusterName $.logicalclusterName|raw$) $.informerInterface|raw$ {
	return &$.type|private$Informer{
		informer: i.Informer().Cluster(clusterName),
		lister:   i.Lister().Cluster(clusterName),
	}
}

func (i *$.type|private$ClusterInformer) ClusterWithContext(ctx $.contextContext|raw$, clusterName $.logicalclusterName|raw$) $.informerInterface|raw$ {
	return &$.type|private$Informer{
		informer: i.Informer().ClusterWithContext(ctx, clusterName),
		lister:   i.Lister().Cluster(clusterName),
	}
}
`

var typeInformer = `
type $.type|private$Informer struct {
	informer $.cacheSharedIndexInformer|raw$
	lister   $.lister|raw$
}

func (i *$.type|private$Informer) Informer() $.cacheSharedIndexInformer|raw$ {
	return i.informer
}

func (i *$.type|private$Informer) Lister() $.lister|raw$ {
	return i.lister
}
`

var typeScopedInformer = `
// $.informerInterface|raw$ provides access to a shared informer and lister for
// $.type|publicPlural$.
type $.informerInterface|raw$ interface {
	Informer() $.cacheSharedIndexInformer|raw$
	Lister() $.lister|raw$
}

type $.type|private$ScopedInformer struct {
	factory          $.interfacesSharedScopedInformerFactory|raw$
	tweakListOptions $.interfacesTweakListOptionsFunc|raw$
	$if .namespaced -$
	namespace        string
	$end -$
}

// New$.type|public$Informer constructs a new informer for $.type|public$ type.
// Always prefer using an informer factory to get a shared informer instead of getting an independent
// one. This reduces memory footprint and number of connections to the server.
func New$.type|public$Informer(client $.clientSetInterface|raw$, resyncPeriod $.timeDuration|raw$$if .namespaced$, namespace string$end$, indexers $.cacheIndexers|raw$) $.cacheSharedIndexInformer|raw$ {
	return NewFiltered$.type|public$Informer(client, resyncPeriod$if .namespaced$, namespace$end$, indexers, nil)
}

// NewFiltered$.type|public$Informer constructs a new informer for $.type|public$ type.
// Always prefer using an informer factory to get a shared informer instead of getting an independent
// one. This reduces memory footprint and number of connections to the server.
func NewFiltered$.type|public$Informer(client $.clientSetInterface|raw$, resyncPeriod $.timeDuration|raw$$if .namespaced$, namespace string$end$, indexers $.cacheIndexers|raw$, tweakListOptions $.interfacesTweakListOptionsFunc|raw$) $.cacheSharedIndexInformer|raw$ {
	return $.cacheNewSharedIndexInformer|raw$(
		&$.cacheListWatch|raw${
			ListFunc: func(options $.metav1ListOptions|raw$) ($.runtimeObject|raw$, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.$.group$$.version$().$.type|publicPlural$($if .namespaced$namespace$end$).List($.contextBackground|raw$(), options)
			},
			WatchFunc: func(options $.metav1ListOptions|raw$) ($.watchInterface|raw$, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.$.group$$.version$().$.type|publicPlural$($if .namespaced$namespace$end$).Watch($.contextBackground|raw$(), options)
			},
		},
		&$.type|raw${},
		resyncPeriod,
		indexers,
	)
}

func (i *$.type|private$ScopedInformer) Informer() $.cacheSharedIndexInformer|raw$ {
	return i.factory.InformerFor(&$.type|raw${}, i.defaultInformer)
}

func (i *$.type|private$ScopedInformer) Lister() $.lister|raw$ {
	return $.newLister|raw$(i.Informer().GetIndexer())
}

func (i *$.type|private$ScopedInformer) defaultInformer(client $.clientSetInterface|raw$, resyncPeriod $.timeDuration|raw$) $.cacheSharedIndexInformer|raw$ {
$if .namespaced -$
	return NewFiltered$.type|public$Informer(client, resyncPeriod, i.namespace, $.cacheIndexers|raw${
		$.cacheNamespaceIndex|raw$: $.cacheMetaNamespaceIndexFunc|raw$,
	}, i.tweakListOptions)
$else -$
	return NewFiltered$.type|public$Informer(client, resyncPeriod, $.cacheIndexers|raw${}, i.tweakListOptions)
$end -$
}
`
