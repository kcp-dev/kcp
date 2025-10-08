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
	"path"
	"strings"

	"k8s.io/gengo/v2/generator"
	"k8s.io/gengo/v2/namer"
	"k8s.io/gengo/v2/types"

	"github.com/kcp-dev/code-generator/v3/cmd/cluster-client-gen/generators/util"
)

// genClientForType produces a file for each top-level type.
type genClientForType struct {
	generator.GoGenerator
	outputPackage              string // must be a Go import-path
	inputPackage               string
	clientsetPackage           string // must be a Go import-path
	applyConfigurationPackage  string // must be a Go import-path
	singleClusterClientPackage string
	group                      string
	version                    string
	groupGoName                string
	typeToMatch                *types.Type
	imports                    namer.ImportTracker
}

var _ generator.Generator = &genClientForType{}

// Filter ignores all but one type because we're making a single file per type.
func (g *genClientForType) Filter(_ *generator.Context, t *types.Type) bool {
	return t == g.typeToMatch
}

func (g *genClientForType) Namers(_ *generator.Context) namer.NameSystems {
	return namer.NameSystems{
		"raw": namer.NewRawNamer(g.outputPackage, g.imports),
	}
}

func (g *genClientForType) Imports(_ *generator.Context) (imports []string) {
	imports = append(imports, g.imports.ImportLines()...)
	imports = append(imports,
		"github.com/kcp-dev/logicalcluster/v3",
	)
	return
}

// GenerateType makes the body of a file implementing the individual typed client for type t.
func (g *genClientForType) GenerateType(c *generator.Context, t *types.Type, w io.Writer) error {
	generateApply := len(g.applyConfigurationPackage) > 0
	defaultVerbTemplates := buildDefaultVerbTemplates()
	sw := generator.NewSnippetWriter(w, c, "$", "$")
	pkg := path.Base(t.Name.Package)
	tags, err := util.ParseClientGenTags(append(t.SecondClosestCommentLines, t.CommentLines...))
	if err != nil {
		return err
	}

	typedInterfaceName := c.Namers["public"].Name(t) + "Interface"
	typedClientName := g.groupGoName + namer.IC(g.version) + "Client"

	m := map[string]interface{}{
		"type":                             t,
		"inputType":                        t,
		"resultType":                       t,
		"package":                          pkg,
		"Package":                          namer.IC(pkg),
		"namespaced":                       !tags.NonNamespaced,
		"noVerbs":                          tags.NoVerbs,
		"Group":                            namer.IC(g.group),
		"subresource":                      false,
		"subresourcePath":                  "",
		"GroupGoName":                      g.groupGoName,
		"Version":                          namer.IC(g.version),
		"GetOptions":                       c.Universe.Type(types.Name{Package: "k8s.io/apimachinery/pkg/apis/meta/v1", Name: "GetOptions"}),
		"ListOptions":                      c.Universe.Type(types.Name{Package: "k8s.io/apimachinery/pkg/apis/meta/v1", Name: "ListOptions"}),
		"NamespaceAll":                     c.Universe.Type(types.Name{Package: "k8s.io/apimachinery/pkg/apis/meta/v1", Name: "NamespaceAll"}),
		"watchInterface":                   c.Universe.Type(types.Name{Package: "k8s.io/apimachinery/pkg/watch", Name: "Interface"}),
		"RESTClientInterface":              c.Universe.Type(types.Name{Package: "k8s.io/client-go/rest", Name: "Interface"}),
		"schemeParameterCodec":             c.Universe.Variable(types.Name{Package: path.Join(g.clientsetPackage, "scheme"), Name: "ParameterCodec"}),
		"fmtErrorf":                        c.Universe.Function(types.Name{Package: "fmt", Name: "Errorf"}),
		"klogWarningf":                     c.Universe.Function(types.Name{Package: "k8s.io/klog/v2", Name: "Warningf"}),
		"context":                          c.Universe.Type(types.Name{Package: "context", Name: "Context"}),
		"timeDuration":                     c.Universe.Type(types.Name{Package: "time", Name: "Duration"}),
		"timeSecond":                       c.Universe.Type(types.Name{Package: "time", Name: "Second"}),
		"resourceVersionMatchNotOlderThan": c.Universe.Type(types.Name{Package: "k8s.io/apimachinery/pkg/apis/meta/v1", Name: "ResourceVersionMatchNotOlderThan"}),
		"CheckListFromCacheDataConsistencyIfRequested":      c.Universe.Function(types.Name{Package: "k8s.io/client-go/util/consistencydetector", Name: "CheckListFromCacheDataConsistencyIfRequested"}),
		"CheckWatchListFromCacheDataConsistencyIfRequested": c.Universe.Function(types.Name{Package: "k8s.io/client-go/util/consistencydetector", Name: "CheckWatchListFromCacheDataConsistencyIfRequested"}),
		"PrepareWatchListOptionsFromListOptions":            c.Universe.Function(types.Name{Package: "k8s.io/client-go/util/watchlist", Name: "PrepareWatchListOptionsFromListOptions"}),
		"applyNewRequest":                                   c.Universe.Function(types.Name{Package: "k8s.io/client-go/util/apply", Name: "NewRequest"}),
		"Client":                                            c.Universe.Type(types.Name{Package: "k8s.io/client-go/gentype", Name: "Client"}),
		"ClientWithList":                                    c.Universe.Type(types.Name{Package: "k8s.io/client-go/gentype", Name: "ClientWithList"}),
		"ClientWithApply":                                   c.Universe.Type(types.Name{Package: "k8s.io/client-go/gentype", Name: "ClientWithApply"}),
		"ClientWithListAndApply":                            c.Universe.Type(types.Name{Package: "k8s.io/client-go/gentype", Name: "ClientWithListAndApply"}),
		"NewClient":                                         c.Universe.Function(types.Name{Package: "k8s.io/client-go/gentype", Name: "NewClient"}),
		"NewClientWithApply":                                c.Universe.Function(types.Name{Package: "k8s.io/client-go/gentype", Name: "NewClientWithApply"}),
		"NewClientWithList":                                 c.Universe.Function(types.Name{Package: "k8s.io/client-go/gentype", Name: "NewClientWithList"}),
		"NewClientWithListAndApply":                         c.Universe.Function(types.Name{Package: "k8s.io/client-go/gentype", Name: "NewClientWithListAndApply"}),
		"kcpCache":                                          c.Universe.Function(types.Name{Package: "github.com/kcp-dev/apimachinery/v2/pkg/client", Name: "Cache"}),
		"typedInterfaceReference":                           c.Universe.Type(types.Name{Package: g.singleClusterClientPackage, Name: typedInterfaceName}),
		"typedClientReference":                              c.Universe.Type(types.Name{Package: g.singleClusterClientPackage, Name: typedClientName}),
	}

	if generateApply {
		// Generated apply configuration type references required for generated Apply function
		_, gvString := util.ParsePathGroupVersion(g.inputPackage)
		m["inputApplyConfig"] = types.Ref(path.Join(g.applyConfigurationPackage, gvString), t.Name.Name+"ApplyConfiguration")
	}

	sw.Do(getterInterface, m)

	sw.Do(interfaceTemplate1, m)
	if !tags.NoVerbs {
		tags.SkipVerbs = append(tags.SkipVerbs, "updateStatus", "applyStatus")
		sw.Do(generateInterface(defaultVerbTemplates, tags), m)
	}
	sw.Do(interfaceTemplate4, m)

	sw.Do(clusterInterfaceImpl, m)

	if !tags.NonNamespaced {
		sw.Do(namespacedClusterInterfaceImpl, m)
	} else {
		sw.Do(nonNamespacedClusterInterfaceImpl, m)
	}

	if !tags.NoVerbs && tags.HasVerb("list") {
		sw.Do(listClusterInterfaceImpl, m)
	}

	if !tags.NoVerbs && tags.HasVerb("watch") {
		sw.Do(watchClusterInterfaceImpl, m)
	}

	if !tags.NonNamespaced {
		sw.Do(namespacer, m)
	}

	return sw.Error()
}

func generateInterface(defaultVerbTemplates map[string]string, tags util.Tags) string {
	// need an ordered list here to guarantee order of generated methods.
	out := []string{}
	for _, m := range util.SupportedVerbs {
		if tags.HasVerb(m) && len(defaultVerbTemplates[m]) > 0 {
			out = append(out, defaultVerbTemplates[m])
		}
	}
	return strings.Join(out, "\n")
}

func buildDefaultVerbTemplates() map[string]string {
	m := map[string]string{
		"list":  `List(ctx $.context|raw$, opts $.ListOptions|raw$) (*$.resultType|raw$List, error)`,
		"watch": `Watch(ctx $.context|raw$, opts $.ListOptions|raw$) ($.watchInterface|raw$, error)`,
	}
	return m
}

// group client will implement this interface.
var getterInterface = `
// $.type|publicPlural$ClusterGetter has a method to return a $.type|public$ClusterInterface.
// A group's cluster client should implement this interface.
type $.type|publicPlural$ClusterGetter interface {
	$.type|publicPlural$() $.type|public$ClusterInterface
}
`

// this type's interface, typed client will implement this interface.
var interfaceTemplate1 = `
$- if .noVerbs $
// $.type|public$ClusterInterface can scope down to one cluster and return a $if .namespaced$$.type|publicPlural$Namespacer$else$$.typedInterfaceReference|raw$$end$.
$ else $
// $.type|public$ClusterInterface can operate on $.type|publicPlural$ across all clusters,
// or scope down to one cluster and return a $if .namespaced$$.type|publicPlural$Namespacer$else$$.typedInterfaceReference|raw$$end$.
$ end -$
type $.type|public$ClusterInterface interface {
	Cluster(logicalcluster.Path) $if .namespaced$$.type|publicPlural$Namespacer$else$$.typedInterfaceReference|raw$$end$
`

var interfaceTemplate4 = `
	$.type|public$ClusterExpansion
}
`

var clusterInterfaceImpl = `
type $.type|privatePlural$ClusterInterface struct {
	clientCache $.kcpCache|raw$[*$.typedClientReference|raw$]
}
`

var namespacedClusterInterfaceImpl = `
// Cluster scopes the client down to a particular cluster.
func (c *$.type|privatePlural$ClusterInterface) Cluster(clusterPath logicalcluster.Path) $if .namespaced$$.type|publicPlural$Namespacer$else$$.typedInterfaceReference|raw$$end$ {
	if clusterPath == logicalcluster.Wildcard {
		panic("A specific cluster must be provided when scoping, not the wildcard.")
	}

	return &$.type|privatePlural$Namespacer{clientCache: c.clientCache, clusterPath: clusterPath}
}
`

var nonNamespacedClusterInterfaceImpl = `
// Cluster scopes the client down to a particular cluster.
func (c *$.type|privatePlural$ClusterInterface) Cluster(clusterPath logicalcluster.Path) $if .namespaced$$.type|publicPlural$Namespacer$else$$.typedInterfaceReference|raw$$end$ {
	if clusterPath == logicalcluster.Wildcard {
		panic("A specific cluster must be provided when scoping, not the wildcard.")
	}

	return c.clientCache.ClusterOrDie(clusterPath).$.type|publicPlural$()
}
`

var listClusterInterfaceImpl = `
// List returns the entire collection of all $.type|publicPlural$ across all clusters.
func (c *$.type|privatePlural$ClusterInterface) List(ctx context.Context, opts $.ListOptions|raw$) (*$.resultType|raw$List, error) {
	return c.clientCache.ClusterOrDie(logicalcluster.Wildcard).$.type|publicPlural$($if .namespaced$$.NamespaceAll|raw$$end$).List(ctx, opts)
}
`

var watchClusterInterfaceImpl = `
// Watch begins to watch all $.type|publicPlural$ across all clusters.
func (c *$.type|privatePlural$ClusterInterface) Watch(ctx context.Context, opts $.ListOptions|raw$) (watch.Interface, error) {
	return c.clientCache.ClusterOrDie(logicalcluster.Wildcard).$.type|publicPlural$($if .namespaced$$.NamespaceAll|raw$$end$).Watch(ctx, opts)
}
`

var namespacer = `
// $.type|publicPlural$Namespacer can scope to objects within a namespace, returning a $.typedInterfaceReference|raw$.
type $.type|publicPlural$Namespacer interface {
	Namespace(string) $.typedInterfaceReference|raw$
}

type $.type|privatePlural$Namespacer struct {
	clientCache $.kcpCache|raw$[*$.typedClientReference|raw$]
	clusterPath logicalcluster.Path
}

func (n *$.type|privatePlural$Namespacer) Namespace(namespace string) $.typedInterfaceReference|raw$ {
	return n.clientCache.ClusterOrDie(n.clusterPath).$.type|publicPlural$(namespace)
}
`
