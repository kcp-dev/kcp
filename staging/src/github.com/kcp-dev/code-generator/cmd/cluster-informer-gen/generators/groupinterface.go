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
	"path"
	"strings"

	"k8s.io/gengo/v2/generator"
	"k8s.io/gengo/v2/namer"
	"k8s.io/gengo/v2/types"

	clientgentypes "github.com/kcp-dev/code-generator/v3/cmd/cluster-client-gen/types"
)

// groupInterfaceGenerator generates the per-group interface file.
type groupInterfaceGenerator struct {
	generator.GoGenerator
	outputPackage                 string
	imports                       namer.ImportTracker
	groupVersions                 clientgentypes.GroupVersions
	filtered                      bool
	internalInterfacesPackage     string
	singleClusterInformersPackage string
}

var _ generator.Generator = &groupInterfaceGenerator{}

func (g *groupInterfaceGenerator) Filter(_ *generator.Context, _ *types.Type) bool {
	if !g.filtered {
		g.filtered = true
		return true
	}
	return false
}

func (g *groupInterfaceGenerator) Namers(_ *generator.Context) namer.NameSystems {
	return namer.NameSystems{
		"raw": namer.NewRawNamer(g.outputPackage, g.imports),
	}
}

func (g *groupInterfaceGenerator) Imports(_ *generator.Context) (imports []string) {
	imports = append(imports, g.imports.ImportLines()...)
	return
}

type versionData struct {
	Name             string
	Interface        *types.Type
	ClusterInterface *types.Type
	New              *types.Type
	NewScoped        *types.Type
}

func (g *groupInterfaceGenerator) GenerateType(c *generator.Context, _ *types.Type, w io.Writer) error {
	sw := generator.NewSnippetWriter(w, c, "$", "$")

	versions := make([]versionData, 0, len(g.groupVersions.Versions))
	for _, version := range g.groupVersions.Versions {
		versionPackage := path.Join(g.outputPackage, strings.ToLower(version.Version.NonEmpty()))

		versions = append(versions, versionData{
			Name:             namer.IC(version.Version.NonEmpty()),
			Interface:        c.Universe.Type(types.Name{Package: versionPackage, Name: "Interface"}),
			ClusterInterface: c.Universe.Type(types.Name{Package: versionPackage, Name: "ClusterInterface"}),
			New:              c.Universe.Function(types.Name{Package: versionPackage, Name: "New"}),
			NewScoped:        c.Universe.Function(types.Name{Package: versionPackage, Name: "NewScoped"}),
		})
	}

	m := map[string]any{
		"interfacesTweakListOptionsFunc":        c.Universe.Type(types.Name{Package: g.internalInterfacesPackage, Name: "TweakListOptionsFunc"}),
		"interfacesSharedInformerFactory":       c.Universe.Type(types.Name{Package: g.internalInterfacesPackage, Name: "SharedInformerFactory"}),
		"interfacesSharedScopedInformerFactory": c.Universe.Type(types.Name{Package: g.internalInterfacesPackage, Name: "SharedScopedInformerFactory"}),
		"versions":                              versions,
	}

	sw.Do(clusterGroupTemplate, m)

	if g.singleClusterInformersPackage == "" {
		sw.Do(groupTemplate, m)
	}

	return sw.Error()
}

var clusterGroupTemplate = `
// ClusterInterface provides access to each of this group's versions.
type ClusterInterface interface {
	$range .versions -$
	// $.Name$ provides access to shared informers for resources in $.Name$.
	$.Name$() $.ClusterInterface|raw$
	$end$
}

type group struct {
	factory          $.interfacesSharedInformerFactory|raw$
	tweakListOptions $.interfacesTweakListOptionsFunc|raw$
}

// New returns a new ClusterInterface.
func New(f $.interfacesSharedInformerFactory|raw$, tweakListOptions $.interfacesTweakListOptionsFunc|raw$) ClusterInterface {
	return &group{factory: f, tweakListOptions: tweakListOptions}
}

$range .versions$
// $.Name$ returns a new $.ClusterInterface|raw$.
func (g *group) $.Name$() $.ClusterInterface|raw$ {
	return $.New|raw$(g.factory, g.tweakListOptions)
}
$end$
`

var groupTemplate = `
// Interface provides access to each of this group's versions.
type Interface interface {
	$range .versions -$
	// $.Name$ provides access to shared informers for resources in $.Name$.
	$.Name$() $.Interface|raw$
	$end$
}

type scopedGroup struct {
	factory          $.interfacesSharedScopedInformerFactory|raw$
	tweakListOptions $.interfacesTweakListOptionsFunc|raw$
	namespace        string
}

// New returns a new Interface.
func NewScoped(f $.interfacesSharedScopedInformerFactory|raw$, namespace string, tweakListOptions $.interfacesTweakListOptionsFunc|raw$) Interface {
	return &scopedGroup{factory: f, namespace: namespace, tweakListOptions: tweakListOptions}
}

$range .versions$
// $.Name$ returns a new $.Interface|raw$.
func (g *scopedGroup) $.Name$() $.Interface|raw$ {
	return $.NewScoped|raw$(g.factory, g.namespace, g.tweakListOptions)
}
$end$
`
