/*
Copyright 2025 The KCP Authors.
Copyright 2025 The Kubernetes Authors.

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

	"github.com/kcp-dev/code-generator/v3/cmd/cluster-client-gen/generators/util"
)

// versionInterfaceGenerator generates the per-version interface file.
type versionInterfaceGenerator struct {
	generator.GoGenerator
	outputPackage                 string
	imports                       namer.ImportTracker
	types                         []*types.Type
	filtered                      bool
	internalInterfacesPackage     string
	singleClusterInformersPackage string
}

var _ generator.Generator = &versionInterfaceGenerator{}

func (g *versionInterfaceGenerator) Filter(_ *generator.Context, _ *types.Type) bool {
	if !g.filtered {
		g.filtered = true
		return true
	}
	return false
}

func (g *versionInterfaceGenerator) Namers(_ *generator.Context) namer.NameSystems {
	return namer.NameSystems{
		"raw": namer.NewRawNamer(g.outputPackage, g.imports),
	}
}

func (g *versionInterfaceGenerator) Imports(_ *generator.Context) (imports []string) {
	imports = append(imports, g.imports.ImportLines()...)
	return
}

func (g *versionInterfaceGenerator) GenerateType(c *generator.Context, _ *types.Type, w io.Writer) error {
	sw := generator.NewSnippetWriter(w, c, "$", "$")

	m := map[string]any{
		"interfacesTweakListOptionsFunc":        c.Universe.Type(types.Name{Package: g.internalInterfacesPackage, Name: "TweakListOptionsFunc"}),
		"interfacesSharedInformerFactory":       c.Universe.Type(types.Name{Package: g.internalInterfacesPackage, Name: "SharedInformerFactory"}),
		"interfacesSharedScopedInformerFactory": c.Universe.Type(types.Name{Package: g.internalInterfacesPackage, Name: "SharedScopedInformerFactory"}),
		"types":                                 g.types,
	}

	sw.Do(clusterVersionTemplate, m)
	for _, typeDef := range g.types {
		m["type"] = typeDef
		sw.Do(clusterVersionFuncTemplate, m)
	}

	if g.singleClusterInformersPackage == "" {
		sw.Do(versionTemplate, m)
		for _, typeDef := range g.types {
			tags, err := util.ParseClientGenTags(append(typeDef.SecondClosestCommentLines, typeDef.CommentLines...))
			if err != nil {
				return err
			}
			m["namespaced"] = !tags.NonNamespaced
			m["type"] = typeDef
			sw.Do(versionFuncTemplate, m)
		}
	}

	return sw.Error()
}

var clusterVersionTemplate = `
type ClusterInterface interface {
	$range .types -$
	// $.|publicPlural$ returns a $.|public$ClusterInformer.
	$.|publicPlural$() $.|public$ClusterInformer
	$end$
}

type version struct {
	factory          $.interfacesSharedInformerFactory|raw$
	tweakListOptions $.interfacesTweakListOptionsFunc|raw$
}

// New returns a new Interface.
func New(f $.interfacesSharedInformerFactory|raw$, tweakListOptions $.interfacesTweakListOptionsFunc|raw$) ClusterInterface {
	return &version{factory: f, tweakListOptions: tweakListOptions}
}
`

var clusterVersionFuncTemplate = `
// $.type|publicPlural$ returns a $.type|public$ClusterInformer.
func (v *version) $.type|publicPlural$() $.type|public$ClusterInformer {
	return &$.type|private$ClusterInformer{factory: v.factory, tweakListOptions: v.tweakListOptions}
}
`

var versionTemplate = `
type Interface interface {
	$range .types -$
	// $.|publicPlural$ returns a $.|public$Informer.
	$.|publicPlural$() $.|public$Informer
	$end$
}

type scopedVersion struct {
	factory          $.interfacesSharedScopedInformerFactory|raw$
	tweakListOptions $.interfacesTweakListOptionsFunc|raw$
	namespace        string
}

// New returns a new Interface.
func NewScoped(f $.interfacesSharedScopedInformerFactory|raw$, namespace string, tweakListOptions $.interfacesTweakListOptionsFunc|raw$) Interface {
	return &scopedVersion{factory: f, tweakListOptions: tweakListOptions}
}
`

var versionFuncTemplate = `
// $.type|publicPlural$ returns a $.type|public$Informer.
func (v *scopedVersion) $.type|publicPlural$() $.type|public$Informer {
	return &$.type|private$ScopedInformer{factory: v.factory$if .namespaced$, namespace: v.namespace$end$, tweakListOptions: v.tweakListOptions}
}
`
