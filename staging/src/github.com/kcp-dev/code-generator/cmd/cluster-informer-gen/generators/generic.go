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
	"slices"
	"strings"

	codegennamer "k8s.io/code-generator/pkg/namer"
	"k8s.io/gengo/v2/generator"
	"k8s.io/gengo/v2/namer"
	"k8s.io/gengo/v2/types"

	clientgentypes "github.com/kcp-dev/code-generator/v3/cmd/cluster-client-gen/types"
)

// genericGenerator generates the generic informer.
type genericGenerator struct {
	generator.GoGenerator
	outputPackage             string
	imports                   namer.ImportTracker
	groupVersions             map[string]clientgentypes.GroupVersions
	groupGoNames              map[string]string
	pluralExceptions          map[string]string
	typesForGroupVersion      map[clientgentypes.GroupVersion][]*types.Type
	singleClusterInformersPkg string
	filtered                  bool
}

var _ generator.Generator = &genericGenerator{}

func (g *genericGenerator) Filter(_ *generator.Context, _ *types.Type) bool {
	if !g.filtered {
		g.filtered = true
		return true
	}
	return false
}

func (g *genericGenerator) Namers(_ *generator.Context) namer.NameSystems {
	return namer.NameSystems{
		"raw":                namer.NewRawNamer(g.outputPackage, g.imports),
		"allLowercasePlural": namer.NewAllLowercasePluralNamer(g.pluralExceptions),
		"publicPlural":       namer.NewPublicPluralNamer(g.pluralExceptions),
		"resource":           codegennamer.NewTagOverrideNamer("resourceName", namer.NewAllLowercasePluralNamer(g.pluralExceptions)),
	}
}

func (g *genericGenerator) Imports(_ *generator.Context) (imports []string) {
	imports = append(imports, g.imports.ImportLines()...)
	return
}

type group struct {
	GroupGoName string
	Name        string
	Versions    []*version
}

func (g *group) Compare(other group) int {
	return strings.Compare(strings.ToLower(g.Name), strings.ToLower(other.Name))
}

type version struct {
	Name      string
	GoName    string
	Resources []*types.Type
}

func (v *version) Compare(other *version) int {
	return strings.Compare(strings.ToLower(v.Name), strings.ToLower(other.Name))
}

func (g *genericGenerator) GenerateType(c *generator.Context, t *types.Type, w io.Writer) error {
	sw := generator.NewSnippetWriter(w, c, "{{", "}}")

	groups := []group{}
	schemeGVs := make(map[*version]*types.Type)

	orderer := namer.Orderer{Namer: namer.NewPrivateNamer(0)}
	for groupPackageName, groupVersions := range g.groupVersions {
		group := group{
			GroupGoName: g.groupGoNames[groupPackageName],
			Name:        groupVersions.Group.NonEmpty(),
			Versions:    []*version{},
		}

		for _, v := range groupVersions.Versions {
			gv := clientgentypes.GroupVersion{Group: groupVersions.Group, Version: v.Version}
			version := &version{
				Name:      v.Version.NonEmpty(),
				GoName:    namer.IC(v.Version.NonEmpty()),
				Resources: orderer.OrderTypes(g.typesForGroupVersion[gv]),
			}
			schemeGVs[version] = c.Universe.Variable(types.Name{Package: g.typesForGroupVersion[gv][0].Name.Package, Name: "SchemeGroupVersion"})
			group.Versions = append(group.Versions, version)
		}

		slices.SortFunc(group.Versions, func(a, b *version) int { return a.Compare(b) })
		groups = append(groups, group)
	}

	slices.SortFunc(groups, func(a, b group) int { return a.Compare(b) })

	genericInformerPkg := g.singleClusterInformersPkg
	generateInformerInterface := false
	if genericInformerPkg == "" {
		genericInformerPkg = g.outputPackage
		generateInformerInterface = true
	}

	m := map[string]any{
		"cacheGenericLister":                c.Universe.Type(cacheGenericLister),
		"kcpcacheGenericClusterLister":      c.Universe.Type(kcpcacheGenericClusterLister),
		"kcpcacheNewGenericClusterLister":   c.Universe.Type(kcpcacheNewGenericClusterLister),
		"cacheSharedIndexInformer":          c.Universe.Type(cacheSharedIndexInformer),
		"scopeableCacheSharedIndexInformer": c.Universe.Type(scopeableCacheSharedIndexInformer),
		"fmtErrorf":                         c.Universe.Type(fmtErrorfFunc),
		"contextContext":                    c.Universe.Type(contextContext),
		"groups":                            groups,
		"schemeGVs":                         schemeGVs,
		"schemaGroupResource":               c.Universe.Type(schemaGroupResource),
		"schemaGroupVersionResource":        c.Universe.Type(schemaGroupVersionResource),
		"genericInformer":                   c.Universe.Type(types.Name{Package: genericInformerPkg, Name: "GenericInformer"}),
		"generateInformerInterface":         generateInformerInterface,
		"logicalclusterName":                c.Universe.Type(logicalclusterName),
	}

	sw.Do(genericClusterInformer, m)
	sw.Do(genericInformer, m)
	sw.Do(forResource, m)

	if generateInformerInterface {
		sw.Do(forScopedResource, m)
	}

	return sw.Error()
}

var genericClusterInformer = `
type GenericClusterInformer interface {
	Cluster({{.logicalclusterName|raw}}) {{.genericInformer|raw}}
	ClusterWithContext({{.contextContext|raw}}, {{.logicalclusterName|raw}}) {{.genericInformer|raw}}
	Informer() {{.scopeableCacheSharedIndexInformer|raw}}
	Lister() {{.kcpcacheGenericClusterLister|raw}}
}
{{ if .generateInformerInterface }}

type GenericInformer interface {
	Informer() {{.cacheSharedIndexInformer|raw}}
	Lister() {{.cacheGenericLister|raw}}
}
{{ end }}

type genericClusterInformer struct {
	informer {{.scopeableCacheSharedIndexInformer|raw}}
	resource {{.schemaGroupResource|raw}}
}

// Informer returns the SharedIndexInformer.
func (i *genericClusterInformer) Informer() {{.scopeableCacheSharedIndexInformer|raw}} {
	return i.informer
}

// Lister returns the GenericLister.
func (i *genericClusterInformer) Lister() {{.kcpcacheGenericClusterLister|raw}} {
	return {{.kcpcacheNewGenericClusterLister|raw}}(i.Informer().GetIndexer(), i.resource)
}

// Cluster scopes to a GenericInformer.
func (i *genericClusterInformer) Cluster(clusterName {{.logicalclusterName|raw}}) {{.genericInformer|raw}} {
	return &genericInformer{
		informer: i.Informer().Cluster(clusterName),
		lister:   i.Lister().ByCluster(clusterName),
	}
}

// ClusterWithContext scopes to a GenericInformer and unregisters all
// handles registered through it once the provided context is canceled.
func (i *genericClusterInformer) ClusterWithContext(ctx {{.contextContext|raw}}, clusterName {{.logicalclusterName|raw}}) {{.genericInformer|raw}} {
	return &genericInformer{
		informer: i.Informer().ClusterWithContext(ctx, clusterName),
		lister:   i.Lister().ByCluster(clusterName),
	}
}
`

var genericInformer = `
type genericInformer struct {
	informer {{.cacheSharedIndexInformer|raw}}
	lister   {{.cacheGenericLister|raw}}
}

// Informer returns the SharedIndexInformer.
func (i *genericInformer) Informer() {{.cacheSharedIndexInformer|raw}} {
	return i.informer
}

// Lister returns the GenericLister.
func (i *genericInformer) Lister() {{.cacheGenericLister|raw}} {
	return i.lister
}
`

var forResource = `
// ForResource gives generic access to a shared informer of the matching type
// TODO extend this to unknown resources with a client pool
func (f *sharedInformerFactory) ForResource(resource {{.schemaGroupVersionResource|raw}}) (GenericClusterInformer, error) {
	switch resource {
		{{range $group := .groups -}}{{$GroupGoName := .GroupGoName -}}
			{{range $version := .Versions -}}
	// Group={{$group.Name}}, Version={{.Name}}
				{{range .Resources -}}
	case {{index $.schemeGVs $version|raw}}.WithResource("{{.|resource}}"):
		return &genericClusterInformer{resource: resource.GroupResource(), informer: f.{{$GroupGoName}}().{{$version.GoName}}().{{.|publicPlural}}().Informer()}, nil
				{{end}}
			{{end}}
		{{end -}}
	}

	return nil, {{.fmtErrorf|raw}}("no informer found for %v", resource)
}
`

var forScopedResource = `
// ForResource gives generic access to a shared informer of the matching type
// TODO extend this to unknown resources with a client pool
func (f *sharedScopedInformerFactory) ForResource(resource {{.schemaGroupVersionResource|raw}}) ({{.genericInformer|raw}}, error) {
	switch resource {
		{{range $group := .groups -}}{{$GroupGoName := .GroupGoName -}}
			{{range $version := .Versions -}}
	// Group={{$group.Name}}, Version={{.Name}}
				{{range .Resources -}}
	case {{index $.schemeGVs $version|raw}}.WithResource("{{.|resource}}"):
		informer := f.{{$GroupGoName}}().{{$version.GoName}}().{{.|publicPlural}}().Informer()
		return &genericInformer{lister: cache.NewGenericLister(informer.GetIndexer(), resource.GroupResource()), informer: informer}, nil
				{{end}}
			{{end}}
		{{end -}}
	}

	return nil, {{.fmtErrorf|raw}}("no informer found for %v", resource)
}
`
