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
	"fmt"
	"io"
	"path"
	"path/filepath"
	"strings"

	"k8s.io/gengo/v2"
	"k8s.io/gengo/v2/generator"
	"k8s.io/gengo/v2/namer"
	"k8s.io/gengo/v2/types"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/code-generator/v3/cmd/cluster-client-gen/generators/util"
	clientgentypes "github.com/kcp-dev/code-generator/v3/cmd/cluster-client-gen/types"
	"github.com/kcp-dev/code-generator/v3/cmd/cluster-lister-gen/args"
	"github.com/kcp-dev/code-generator/v3/pkg/imports"
)

// NameSystems returns the name system used by the generators in this package.
func NameSystems(pluralExceptions map[string]string) namer.NameSystems {
	return namer.NameSystems{
		"public":            namer.NewPublicNamer(0),
		"private":           namer.NewPrivateNamer(0),
		"raw":               namer.NewRawNamer("", nil),
		"publicPlural":      namer.NewPublicPluralNamer(pluralExceptions),
		"lowercaseSingular": &lowercaseSingularNamer{},
	}
}

// lowercaseSingularNamer implements Namer.
type lowercaseSingularNamer struct{}

// Name returns t's name in all lowercase.
func (n *lowercaseSingularNamer) Name(t *types.Type) string {
	return strings.ToLower(t.Name.Name)
}

// DefaultNameSystem returns the default name system for ordering the types to be
// processed by the generators in this package.
func DefaultNameSystem() string {
	return "public"
}

// GetTargets makes the client target definition.
func GetTargets(context *generator.Context, args *args.Args) []generator.Target {
	boilerplate, err := gengo.GoBoilerplate(args.GoHeaderFile, "", gengo.StdGeneratedBy)
	if err != nil {
		klog.Fatalf("Failed loading boilerplate: %v", err)
	}

	var targetList []generator.Target
	for _, inputPkg := range context.Inputs {
		p := context.Universe.Package(inputPkg)

		objectMeta, internal, err := objectMetaForPackage(p)
		if err != nil {
			klog.Fatal(err)
		}
		if objectMeta == nil {
			// no types in this package had genclient
			continue
		}

		var gv clientgentypes.GroupVersion

		if internal {
			lastSlash := strings.LastIndex(p.Path, "/")
			if lastSlash == -1 {
				klog.Fatalf("error constructing internal group version for package %q", p.Path)
			}
			gv.Group = clientgentypes.Group(p.Path[lastSlash+1:])
		} else {
			parts := strings.Split(p.Path, "/")
			gv.Group = clientgentypes.Group(parts[len(parts)-2])
			gv.Version = clientgentypes.Version(parts[len(parts)-1])
		}
		groupPackageName := strings.ToLower(gv.Group.NonEmpty())

		// If there's a comment of the form "// +groupName=somegroup" or
		// "// +groupName=somegroup.foo.bar.io", use the first field (somegroup) as the name of the
		// group when generating.
		if override := gengo.ExtractCommentTags("+", p.Comments)["groupName"]; override != nil { //nolint:staticcheck
			gv.Group = clientgentypes.Group(strings.SplitN(override[0], ".", 2)[0])
		}

		var typesToGenerate []*types.Type
		for _, t := range p.Types {
			tags := util.MustParseClientGenTags(append(t.SecondClosestCommentLines, t.CommentLines...))
			if !tags.GenerateClient || !tags.HasVerb("list") || !tags.HasVerb("get") {
				continue
			}
			typesToGenerate = append(typesToGenerate, t)
		}
		if len(typesToGenerate) == 0 {
			continue
		}
		orderer := namer.Orderer{Namer: namer.NewPrivateNamer(0)}
		typesToGenerate = orderer.OrderTypes(typesToGenerate)

		subdir := []string{groupPackageName, strings.ToLower(gv.Version.NonEmpty())}
		outputDir := filepath.Join(args.OutputDir, filepath.Join(subdir...))
		outputPkg := path.Join(args.OutputPackage, path.Join(subdir...))

		targetList = append(targetList, &generator.SimpleTarget{
			PkgName:       strings.ToLower(gv.Version.NonEmpty()),
			PkgPath:       outputPkg,
			PkgDir:        outputDir,
			HeaderComment: boilerplate,
			FilterFunc: func(_ *generator.Context, t *types.Type) bool {
				tags := util.MustParseClientGenTags(append(t.SecondClosestCommentLines, t.CommentLines...))
				return tags.GenerateClient && tags.HasVerb("list") && tags.HasVerb("get")
			},
			GeneratorsFunc: func(_ *generator.Context) (generators []generator.Generator) {
				var singleClusterListersPkg string
				if args.SingleClusterListersPackage != "" {
					singleClusterListersPkg = path.Join(args.SingleClusterListersPackage, groupPackageName, string(gv.Version))
				}

				generators = append(generators, &expansionGenerator{
					GoGenerator: generator.GoGenerator{
						OutputFilename: "expansion_generated.go",
					},
					outputPath:              outputDir,
					types:                   typesToGenerate,
					singleClusterListersPkg: singleClusterListersPkg,
				})

				for _, t := range typesToGenerate {
					generators = append(generators, &listerGenerator{
						GoGenerator: generator.GoGenerator{
							OutputFilename: strings.ToLower(t.Name.Name) + ".go",
						},
						outputPackage:           outputPkg,
						groupVersion:            gv,
						typeToGenerate:          t,
						imports:                 imports.NewImportTrackerForPackage(outputPkg),
						singleClusterListersPkg: singleClusterListersPkg,
					})
				}

				return generators
			},
		})
	}

	return targetList
}

// objectMetaForPackage returns the type of ObjectMeta used by package p.
func objectMetaForPackage(p *types.Package) (*types.Type, bool, error) {
	generatingForPackage := false
	for _, t := range p.Types {
		// filter out types which don't have genclient.
		if !util.MustParseClientGenTags(append(t.SecondClosestCommentLines, t.CommentLines...)).GenerateClient {
			continue
		}
		generatingForPackage = true
		for _, member := range t.Members {
			if member.Name == "ObjectMeta" {
				return member.Type, isInternal(member), nil
			}
		}
	}
	if generatingForPackage {
		return nil, false, fmt.Errorf("unable to find ObjectMeta for any types in package %s", p.Path)
	}
	return nil, false, nil
}

// isInternal returns true if the tags for a member do not contain a json tag.
func isInternal(m types.Member) bool {
	return !strings.Contains(m.Tags, "json")
}

// listerGenerator produces a file of listers for a given GroupVersion and type.
type listerGenerator struct {
	generator.GoGenerator
	outputPackage           string
	groupVersion            clientgentypes.GroupVersion
	typeToGenerate          *types.Type
	imports                 namer.ImportTracker
	singleClusterListersPkg string
}

var _ generator.Generator = &listerGenerator{}

func (g *listerGenerator) Filter(_ *generator.Context, t *types.Type) bool {
	return t == g.typeToGenerate
}

func (g *listerGenerator) Namers(_ *generator.Context) namer.NameSystems {
	return namer.NameSystems{
		"raw": namer.NewRawNamer(g.outputPackage, g.imports),
	}
}

func (g *listerGenerator) Imports(_ *generator.Context) (imports []string) {
	imports = append(imports, g.imports.ImportLines()...)
	imports = append(imports,
		`"github.com/kcp-dev/logicalcluster/v3"`,
		`kcplisters "github.com/kcp-dev/client-go/third_party/k8s.io/client-go/listers"`,
		`"k8s.io/apimachinery/pkg/labels"`,
		`"k8s.io/client-go/tools/cache"`,
	)
	return
}

func (g *listerGenerator) GenerateType(c *generator.Context, t *types.Type, w io.Writer) error {
	sw := generator.NewSnippetWriter(w, c, "$", "$")

	interfacesPkg := g.singleClusterListersPkg
	if interfacesPkg == "" {
		// This will make gengo not output an import statement at all for any references.
		interfacesPkg = g.outputPackage
	}

	tags, err := util.ParseClientGenTags(append(t.SecondClosestCommentLines, t.CommentLines...))
	if err != nil {
		return err
	}

	klog.V(5).Infof("processing type %v", t)
	m := map[string]interface{}{
		"type":                     t,
		"namespaced":               !tags.NonNamespaced,
		"Resource":                 c.Universe.Function(types.Name{Package: t.Name.Package, Name: "Resource"}),
		"listerInterface":          c.Universe.Type(types.Name{Package: interfacesPkg, Name: t.Name.Name + "Lister"}),
		"namespaceListerInterface": c.Universe.Type(types.Name{Package: interfacesPkg, Name: t.Name.Name + "NamespaceLister"}),
	}

	sw.Do(typeClusterListerInterface, m)
	sw.Do(typeListerStruct, m)

	// no external interfaces provided, so we generate our own
	if g.singleClusterListersPkg == "" {
		sw.Do(typeListerInterface, m)
	}

	if !tags.NonNamespaced {
		sw.Do(typeListerNamespaceLister, m)
		sw.Do(namespaceListerStruct, m)

		if g.singleClusterListersPkg == "" {
			sw.Do(namespaceListerInterface, m)
		}
	}

	sw.Do(scopedLister, m)

	return sw.Error()
}

var typeClusterListerInterface = `
// $.type|public$ClusterLister helps list $.type|publicPlural$ across all workspaces,
// or scope down to a $.type|public$Lister for one workspace.
// All objects returned here must be treated as read-only.
type $.type|public$ClusterLister interface {
	// List lists all $.type|publicPlural$ in the indexer.
	// Objects returned here must be treated as read-only.
	List(selector labels.Selector) (ret []*$.type|raw$, err error)
	// Cluster returns a lister that can list and get $.type|publicPlural$ in one workspace.
	Cluster(clusterName logicalcluster.Name) $.listerInterface|raw$
	$.type|public$ClusterListerExpansion
}

// $.type|private$ClusterLister implements the $.type|public$ClusterLister interface.
type $.type|private$ClusterLister struct {
	kcplisters.ResourceClusterIndexer[*$.type|raw$]
}

var _ $.type|public$ClusterLister = new($.type|private$ClusterLister)

// New$.type|public$ClusterLister returns a new $.type|public$ClusterLister.
// We assume that the indexer:
// - is fed by a cross-workspace LIST+WATCH
// - uses kcpcache.MetaClusterNamespaceKeyFunc as the key function
// - has the kcpcache.ClusterIndex as an index
$if .namespaced -$
// - has the kcpcache.ClusterAndNamespaceIndex as an index
$end -$
func New$.type|public$ClusterLister(indexer cache.Indexer) $.type|public$ClusterLister {
	return &$.type|private$ClusterLister{
		kcplisters.NewCluster[*$.type|raw$](indexer, $.Resource|raw$("$.type|lowercaseSingular$")),
	}
}

// Cluster scopes the lister to one workspace, allowing users to list and get $.type|publicPlural$.
func (l *$.type|private$ClusterLister) Cluster(clusterName logicalcluster.Name) $.listerInterface|raw$ {
	return &$.type|private$Lister{
		l.ResourceClusterIndexer.WithCluster(clusterName),
	}
}
`

var typeListerInterface = `
$if .namespaced -$
// $.listerInterface|raw$ can list $.type|publicPlural$ across all namespaces, or scope down to a $.namespaceListerInterface|raw$ for one namespace.
$else -$
// $.listerInterface|raw$ can list all $.type|publicPlural$, or get one in particular.
$end -$
// All objects returned here must be treated as read-only.
type $.listerInterface|raw$ interface {
	// List lists all $.type|publicPlural$ in the indexer.
	// Objects returned here must be treated as read-only.
	List(selector labels.Selector) (ret []*$.type|raw$, err error)
$if .namespaced -$
	// $.type|publicPlural$ returns a lister that can list and get $.type|publicPlural$ in one workspace and namespace.
	$.type|publicPlural$(namespace string) $.namespaceListerInterface|raw$
$else -$
	// Get retrieves the $.type|public$ from the indexer for a given workspace and name.
	// Objects returned here must be treated as read-only.
	Get(name string) (*$.type|raw$, error)
$end -$
	$.type|public$ListerExpansion
}
`

var typeListerStruct = `
// $.type|private$Lister can list all $.type|publicPlural$ inside a workspace
// or scope down to a $.namespaceListerInterface|raw$ for one namespace.
type $.type|private$Lister struct {
	kcplisters.ResourceIndexer[*$.type|raw$]
}

var _ $.listerInterface|raw$ = new($.type|private$Lister)
`

var typeListerNamespaceLister = `
// $.type|publicPlural$ returns an object that can list and get $.type|publicPlural$ in one namespace.
func (l *$.type|private$Lister) $.type|publicPlural$(namespace string) $.namespaceListerInterface|raw$ {
	return &$.type|private$NamespaceLister{
		l.ResourceIndexer.WithNamespace(namespace),
	}
}
`

var namespaceListerInterface = `
// $.namespaceListerInterface|raw$ can list all $.type|publicPlural$, or get one in particular.
// All objects returned here must be treated as read-only.
type $.namespaceListerInterface|raw$ interface {
	// List lists all $.type|publicPlural$ in the indexer.
	// Objects returned here must be treated as read-only.
	List(selector labels.Selector) (ret []*$.type|raw$, err error)
	// Get retrieves the $.type|public$ from the indexer for a given workspace, namespace and name.
	// Objects returned here must be treated as read-only.
	Get(name string) (*$.type|raw$, error)
	$.type|public$NamespaceListerExpansion
}
`

var namespaceListerStruct = `
// $.type|private$NamespaceLister implements the $.namespaceListerInterface|raw$
// interface.
type $.type|private$NamespaceLister struct {
	kcplisters.ResourceIndexer[*$.type|raw$]
}

var _ $.namespaceListerInterface|raw$ = new($.type|private$NamespaceLister)
`

var scopedLister = `
// New$.type|public$Lister returns a new $.type|public$Lister.
// We assume that the indexer:
// - is fed by a cross-workspace LIST+WATCH
// - uses kcpcache.MetaClusterNamespaceKeyFunc as the key function
// - has the kcpcache.ClusterIndex as an index
$if .namespaced -$
// - has the kcpcache.ClusterAndNamespaceIndex as an index
$end -$
func New$.type|public$Lister(indexer cache.Indexer) $.listerInterface|raw$ {
	return &$.type|private$Lister{
		kcplisters.New[*$.type|raw$](indexer, $.Resource|raw$("$.type|lowercaseSingular$")),
	}
}

// $.type|private$ScopedLister can list all $.type|publicPlural$ inside a workspace
// or scope down to a $.namespaceListerInterface|raw$$if .namespaced$ for one namespace$end$.
type $.type|private$ScopedLister struct {
	kcplisters.ResourceIndexer[*$.type|raw$]
}

$if .namespaced -$
// $.type|publicPlural$ returns an object that can list and get $.type|publicPlural$ in one namespace.
func (l *$.type|private$ScopedLister) $.type|publicPlural$(namespace string) $.listerInterface|raw$ {
	return &$.type|private$Lister{
		l.ResourceIndexer.WithNamespace(namespace),
	}
}
$end -$
`
