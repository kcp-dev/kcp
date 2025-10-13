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

package fake

import (
	"fmt"
	"io"
	"path"
	"strings"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"

	"k8s.io/gengo/v2/generator"
	"k8s.io/gengo/v2/namer"
	"k8s.io/gengo/v2/types"

	"github.com/kcp-dev/code-generator/v3/cmd/cluster-client-gen/generators/util"
)

// genFakeForType produces a file for each top-level type.
type genFakeForType struct {
	generator.GoGenerator
	outputPackage              string // Must be a Go import-path
	realClientPackage          string // Must be a Go import-path
	version                    string
	groupGoName                string
	inputPackage               string
	typeToMatch                *types.Type
	imports                    namer.ImportTracker
	applyConfigurationPackage  string
	singleClusterClientPackage string
}

var _ generator.Generator = &genFakeForType{}

var titler = cases.Title(language.Und)

// Filter ignores all but one type because we're making a single file per type.
func (g *genFakeForType) Filter(_ *generator.Context, t *types.Type) bool {
	return t == g.typeToMatch
}

func (g *genFakeForType) Namers(_ *generator.Context) namer.NameSystems {
	return namer.NameSystems{
		"raw": namer.NewRawNamer(g.outputPackage, g.imports),
	}
}

func (g *genFakeForType) Imports(_ *generator.Context) (imports []string) {
	gvAlias := util.GroupVersionAliasFromPackage(g.realClientPackage)

	imports = append(imports, g.imports.ImportLines()...)
	imports = append(imports,
		`"github.com/kcp-dev/logicalcluster/v3"`,
		`kcpgentype "github.com/kcp-dev/client-go/third_party/k8s.io/client-go/gentype"`,
		`kcptesting "github.com/kcp-dev/client-go/third_party/k8s.io/client-go/testing"`,
		fmt.Sprintf(`%s %q`, gvAlias, g.typeToMatch.Name.Package),
		fmt.Sprintf(`typedkcp%s %q`, gvAlias, g.realClientPackage),
		fmt.Sprintf(`typed%s %q`, gvAlias, g.singleClusterClientPackage),
	)

	return imports
}

// GenerateType makes the body of a file implementing the individual typed client for type t.
func (g *genFakeForType) GenerateType(c *generator.Context, t *types.Type, w io.Writer) error {
	sw := generator.NewSnippetWriter(w, c, "$", "$")
	tags, err := util.ParseClientGenTags(append(t.SecondClosestCommentLines, t.CommentLines...))
	if err != nil {
		return err
	}

	publicName := c.Namers["public"].Name(t)
	typedListName := publicName + "List"

	typedInterfaceName := publicName + "Interface"
	typedInterfacePkg := g.singleClusterClientPackage
	if !tags.NonNamespaced {
		typedInterfaceName = publicName + "Namespacer"
		typedInterfacePkg = g.realClientPackage
	}

	// const pkgClientGoTesting = "k8s.io/client-go/testing"
	const pkgClientGoTesting = "github.com/kcp-dev/client-go/third_party/k8s.io/client-go/testing"
	m := map[string]interface{}{
		"type":                t,
		"inputType":           t,
		"resultType":          t,
		"subresourcePath":     "",
		"namespaced":          !tags.NonNamespaced,
		"group":               t.Name.Package,
		"GroupGoName":         g.groupGoName,
		"Version":             namer.IC(g.version),
		"groupVersion":        util.GroupVersionAliasFromPackage(g.realClientPackage),
		"realClientInterface": c.Universe.Type(types.Name{Package: g.realClientPackage, Name: t.Name.Name + "Interface"}),
		"SchemeGroupVersion":  c.Universe.Type(types.Name{Package: t.Name.Package, Name: "SchemeGroupVersion"}),
		"CreateOptions":       c.Universe.Type(types.Name{Package: "k8s.io/apimachinery/pkg/apis/meta/v1", Name: "CreateOptions"}),
		"DeleteOptions":       c.Universe.Type(types.Name{Package: "k8s.io/apimachinery/pkg/apis/meta/v1", Name: "DeleteOptions"}),
		"GetOptions":          c.Universe.Type(types.Name{Package: "k8s.io/apimachinery/pkg/apis/meta/v1", Name: "GetOptions"}),
		"ListOptions":         c.Universe.Type(types.Name{Package: "k8s.io/apimachinery/pkg/apis/meta/v1", Name: "ListOptions"}),
		"PatchOptions":        c.Universe.Type(types.Name{Package: "k8s.io/apimachinery/pkg/apis/meta/v1", Name: "PatchOptions"}),
		"ApplyOptions":        c.Universe.Type(types.Name{Package: "k8s.io/apimachinery/pkg/apis/meta/v1", Name: "ApplyOptions"}),
		"UpdateOptions":       c.Universe.Type(types.Name{Package: "k8s.io/apimachinery/pkg/apis/meta/v1", Name: "UpdateOptions"}),
		"labelsEverything":    c.Universe.Type(types.Name{Package: "k8s.io/apimachinery/pkg/labels", Name: "Everything"}),
		"labelsSet":           c.Universe.Type(types.Name{Package: "k8s.io/apimachinery/pkg/labels", Name: "Set"}),
		"PatchType":           c.Universe.Type(types.Name{Package: "k8s.io/apimachinery/pkg/types", Name: "PatchType"}),
		"ApplyPatchType":      c.Universe.Type(types.Name{Package: "k8s.io/apimachinery/pkg/types", Name: "ApplyPatchType"}),
		"watchInterface":      c.Universe.Type(types.Name{Package: "k8s.io/apimachinery/pkg/watch", Name: "Interface"}),
		"jsonMarshal":         c.Universe.Type(types.Name{Package: "encoding/json", Name: "Marshal"}),
		"fmtErrorf":           c.Universe.Type(types.Name{Package: "fmt", Name: "Errorf"}),
		"contextContext":      c.Universe.Type(types.Name{Package: "context", Name: "Context"}),

		"NewRootListAction":              c.Universe.Function(types.Name{Package: pkgClientGoTesting, Name: "NewRootListAction"}),
		"NewListAction":                  c.Universe.Function(types.Name{Package: pkgClientGoTesting, Name: "NewListAction"}),
		"NewRootGetAction":               c.Universe.Function(types.Name{Package: pkgClientGoTesting, Name: "NewRootGetAction"}),
		"NewGetAction":                   c.Universe.Function(types.Name{Package: pkgClientGoTesting, Name: "NewGetAction"}),
		"NewRootDeleteActionWithOptions": c.Universe.Function(types.Name{Package: pkgClientGoTesting, Name: "NewRootDeleteActionWithOptions"}),
		"NewDeleteActionWithOptions":     c.Universe.Function(types.Name{Package: pkgClientGoTesting, Name: "NewDeleteActionWithOptions"}),
		"NewRootUpdateAction":            c.Universe.Function(types.Name{Package: pkgClientGoTesting, Name: "NewRootUpdateAction"}),
		"NewUpdateAction":                c.Universe.Function(types.Name{Package: pkgClientGoTesting, Name: "NewUpdateAction"}),
		"NewRootCreateAction":            c.Universe.Function(types.Name{Package: pkgClientGoTesting, Name: "NewRootCreateAction"}),
		"NewCreateAction":                c.Universe.Function(types.Name{Package: pkgClientGoTesting, Name: "NewCreateAction"}),
		"NewRootWatchAction":             c.Universe.Function(types.Name{Package: pkgClientGoTesting, Name: "NewRootWatchAction"}),
		"NewWatchAction":                 c.Universe.Function(types.Name{Package: pkgClientGoTesting, Name: "NewWatchAction"}),
		"NewCreateSubresourceAction":     c.Universe.Function(types.Name{Package: pkgClientGoTesting, Name: "NewCreateSubresourceAction"}),
		"NewRootCreateSubresourceAction": c.Universe.Function(types.Name{Package: pkgClientGoTesting, Name: "NewRootCreateSubresourceAction"}),
		"NewUpdateSubresourceAction":     c.Universe.Function(types.Name{Package: pkgClientGoTesting, Name: "NewUpdateSubresourceAction"}),
		"NewGetSubresourceAction":        c.Universe.Function(types.Name{Package: pkgClientGoTesting, Name: "NewGetSubresourceAction"}),
		"NewRootGetSubresourceAction":    c.Universe.Function(types.Name{Package: pkgClientGoTesting, Name: "NewRootGetSubresourceAction"}),
		"NewRootUpdateSubresourceAction": c.Universe.Function(types.Name{Package: pkgClientGoTesting, Name: "NewRootUpdateSubresourceAction"}),
		"NewRootPatchSubresourceAction":  c.Universe.Function(types.Name{Package: pkgClientGoTesting, Name: "NewRootPatchSubresourceAction"}),
		"NewPatchSubresourceAction":      c.Universe.Function(types.Name{Package: pkgClientGoTesting, Name: "NewPatchSubresourceAction"}),
		"ToPointerSlice":                 c.Universe.Type(types.Name{Package: "github.com/kcp-dev/client-go/third_party/k8s.io/client-go/gentype", Name: "ToPointerSlice"}),
		"FromPointerSlice":               c.Universe.Function(types.Name{Package: "github.com/kcp-dev/client-go/third_party/k8s.io/client-go/gentype", Name: "FromPointerSlice"}),
		"FakeClient":                     c.Universe.Type(types.Name{Package: "github.com/kcp-dev/client-go/third_party/k8s.io/client-go/gentype", Name: "FakeClient"}),
		"NewFakeClient":                  c.Universe.Function(types.Name{Package: "github.com/kcp-dev/client-go/third_party/k8s.io/client-go/gentype", Name: "NewFakeClient"}),
		"FakeClientWithApply":            c.Universe.Type(types.Name{Package: "github.com/kcp-dev/client-go/third_party/k8s.io/client-go/gentype", Name: "FakeClientWithApply"}),
		"NewFakeClientWithApply":         c.Universe.Function(types.Name{Package: "github.com/kcp-dev/client-go/third_party/k8s.io/client-go/gentype", Name: "NewFakeClientWithApply"}),
		"FakeClientWithList":             c.Universe.Type(types.Name{Package: "github.com/kcp-dev/client-go/third_party/k8s.io/client-go/gentype", Name: "FakeClientWithList"}),
		"NewFakeClientWithList":          c.Universe.Function(types.Name{Package: "github.com/kcp-dev/client-go/third_party/k8s.io/client-go/gentype", Name: "NewFakeClientWithList"}),
		"FakeClientWithListAndApply":     c.Universe.Type(types.Name{Package: "github.com/kcp-dev/client-go/third_party/k8s.io/client-go/gentype", Name: "FakeClientWithListAndApply"}),
		"NewFakeClientWithListAndApply":  c.Universe.Function(types.Name{Package: "github.com/kcp-dev/client-go/third_party/k8s.io/client-go/gentype", Name: "NewFakeClientWithListAndApply"}),
		"typedInterfaceReference":        c.Universe.Type(types.Name{Package: typedInterfacePkg, Name: typedInterfaceName}),
		"typedTypeReference":             c.Universe.Type(types.Name{Package: g.inputPackage, Name: publicName}),
		"typedListTypeReference":         c.Universe.Type(types.Name{Package: g.inputPackage, Name: typedListName}),
		"inputSchemeKind":                c.Universe.Type(types.Name{Package: g.inputPackage, Name: "Kind"}),
		"inputSchemeResource":            c.Universe.Type(types.Name{Package: g.inputPackage, Name: "Resource"}),
	}

	generateApply := len(g.applyConfigurationPackage) > 0
	if generateApply {
		// Generated apply builder type references required for generated Apply function
		_, gvString := util.ParsePathGroupVersion(g.inputPackage)
		m["inputApplyConfig"] = types.Ref(path.Join(g.applyConfigurationPackage, gvString), t.Name.Name+"ApplyConfiguration")
	}

	listableOrAppliable := noList | noApply
	var clusterListable bool

	if !tags.NoVerbs && tags.HasVerb("list") {
		listableOrAppliable |= withList
		clusterListable = true
	}

	if !tags.NoVerbs && tags.HasVerb("apply") && generateApply {
		listableOrAppliable |= withApply
	}

	if clusterListable {
		sw.Do(listableClusterClientType, m)
		sw.Do(newListableClusterClient, m)
	} else {
		sw.Do(noVerbsClusterClientType, m)
		sw.Do(newNoVerbsClusterClient, m)
	}

	if tags.NonNamespaced {
		sw.Do(rootClientScoper, m)
	} else {
		sw.Do(namespacedClientScoper, m)
	}

	sw.Do(clientStructType[listableOrAppliable], m)
	sw.Do(newClientStruct[listableOrAppliable], m)

	if tags.NoVerbs {
		return sw.Error()
	}

	_, typeGVString := util.ParsePathGroupVersion(g.inputPackage)

	// generate extended client methods
	for _, e := range tags.Extensions {
		if e.HasVerb("apply") && !generateApply {
			continue
		}
		inputType := *t
		resultType := *t
		inputGVString := typeGVString
		if len(e.InputTypeOverride) > 0 {
			if name, pkg := e.Input(); len(pkg) > 0 {
				_, inputGVString = util.ParsePathGroupVersion(pkg)
				newType := c.Universe.Type(types.Name{Package: pkg, Name: name})
				inputType = *newType
			} else {
				inputType.Name.Name = e.InputTypeOverride
			}
		}
		if len(e.ResultTypeOverride) > 0 {
			if name, pkg := e.Result(); len(pkg) > 0 {
				newType := c.Universe.Type(types.Name{Package: pkg, Name: name})
				resultType = *newType
			} else {
				resultType.Name.Name = e.ResultTypeOverride
			}
		}
		m["inputType"] = &inputType
		m["resultType"] = &resultType
		m["subresourcePath"] = e.SubResourcePath
		if e.HasVerb("apply") {
			m["inputApplyConfig"] = types.Ref(path.Join(g.applyConfigurationPackage, inputGVString), inputType.Name.Name+"ApplyConfiguration")
		}

		if e.HasVerb("get") {
			if e.IsSubresource() {
				sw.Do(adjustTemplate(e.VerbName, e.VerbType, getSubresourceTemplate), m)
			} else {
				sw.Do(adjustTemplate(e.VerbName, e.VerbType, getTemplate), m)
			}
		}

		if e.HasVerb("list") {
			sw.Do(adjustTemplate(e.VerbName, e.VerbType, listTemplate), m)
		}

		// TODO: Figure out schemantic for watching a sub-resource.
		if e.HasVerb("watch") {
			sw.Do(adjustTemplate(e.VerbName, e.VerbType, watchTemplate), m)
		}

		if e.HasVerb("create") {
			if e.IsSubresource() {
				sw.Do(adjustTemplate(e.VerbName, e.VerbType, createSubresourceTemplate), m)
			} else {
				sw.Do(adjustTemplate(e.VerbName, e.VerbType, createTemplate), m)
			}
		}

		if e.HasVerb("update") {
			if e.IsSubresource() {
				sw.Do(adjustTemplate(e.VerbName, e.VerbType, updateSubresourceTemplate), m)
			} else {
				sw.Do(adjustTemplate(e.VerbName, e.VerbType, updateTemplate), m)
			}
		}

		// TODO: Figure out schemantic for deleting a sub-resource (what arguments
		// are passed, does it need two names? etc.
		if e.HasVerb("delete") {
			sw.Do(adjustTemplate(e.VerbName, e.VerbType, deleteTemplate), m)
		}

		if e.HasVerb("patch") {
			sw.Do(adjustTemplate(e.VerbName, e.VerbType, patchTemplate), m)
		}

		if e.HasVerb("apply") && generateApply {
			if e.IsSubresource() {
				sw.Do(adjustTemplate(e.VerbName, e.VerbType, applySubresourceTemplate), m)
			} else {
				sw.Do(adjustTemplate(e.VerbName, e.VerbType, applyTemplate), m)
			}
		}
	}

	return sw.Error()
}

// adjustTemplate adjust the origin verb template using the expansion name.
// TODO: Make the verbs in templates parametrized so the strings.Replace() is
// not needed.
func adjustTemplate(name, verbType, template string) string {
	return strings.ReplaceAll(template, " "+titler.String(verbType), " "+name)
}

// struct and constructor variants.
const (
	// The following values are bits in a bitmask.
	// The values which can be set indicate list support and apply support;
	// to make the declarations easier to read (like a truth table), corresponding zero-values
	// are also declared.
	noList   = 0
	noApply  = 0
	withList = 1 << iota
	withApply
)

// The following string slices are similar to maps, but with combinable keys used as indices.
// Each entry defines whether it supports lists and/or apply; each bit is then toggled:
// * noList, noApply: index 0;
// * withList, noApply: index 1;
// * noList, withApply: index 2;
// * withList, withApply: index 3.
// Go enforces index unicity in these kinds of declarations.

// cluster struct declarations.
var listableClusterClientType = `
// $.type|private$ClusterClient implements $.type|singularKind$ClusterInterface
type $.type|private$ClusterClient struct {
	*kcpgentype.FakeClusterClientWithList[*$.groupVersion$.$.type|singularKind$, *$.groupVersion$.$.type|singularKind$List]
	Fake *kcptesting.Fake
}
`

var noVerbsClusterClientType = `
// $.type|private$ClusterClient implements $.type|singularKind$ClusterInterface
type $.type|private$ClusterClient struct {
	*kcpgentype.FakeClusterClient[*$.groupVersion$.$.type|singularKind$]
	Fake *kcptesting.Fake
}
`

// Constructors for the cluster struct, in all variants.
var newListableClusterClient = `
func newFake$.type|public$ClusterClient(fake *$.GroupGoName$$.Version$ClusterClient) typedkcp$.groupVersion$.$.type|singularKind$ClusterInterface {
	return &$.type|private$ClusterClient{
		kcpgentype.NewFakeClusterClientWithList[*$.groupVersion$.$.type|singularKind$, *$.groupVersion$.$.type|singularKind$List](
			fake.Fake,
			$.groupVersion$.SchemeGroupVersion.WithResource("$.type|resource$"),
			$.groupVersion$.SchemeGroupVersion.WithKind("$.type|singularKind$"),
			func() *$.groupVersion$.$.type|singularKind$ { return &$.groupVersion$.$.type|singularKind${} },
			func() *$.groupVersion$.$.type|singularKind$List { return &$.groupVersion$.$.type|singularKind$List{} },
			func(dst, src *$.groupVersion$.$.type|singularKind$List) { dst.ListMeta = src.ListMeta },
			func(list *$.groupVersion$.$.type|singularKind$List) []*$.groupVersion$.$.type|singularKind$ { return $.ToPointerSlice|raw$(list.Items) },
			func(list *$.groupVersion$.$.type|singularKind$List, items []*$.groupVersion$.$.type|singularKind$) { list.Items = $.FromPointerSlice|raw$(items) },
		),
		fake.Fake,
	}
}
`

var newNoVerbsClusterClient = `
func newFake$.type|public$ClusterClient(fake *$.GroupGoName$$.Version$ClusterClient) typedkcp$.groupVersion$.$.type|singularKind$ClusterInterface {
	return &$.type|private$ClusterClient{
		kcpgentype.NewFakeClusterClient[*$.groupVersion$.$.type|singularKind$](
			fake.Fake,
			$.groupVersion$.SchemeGroupVersion.WithResource("$.type|resource$"),
			$.groupVersion$.SchemeGroupVersion.WithKind("$.type|singularKind$"),
			func() *$.groupVersion$.$.type|singularKind$ { return &$.groupVersion$.$.type|singularKind${} },
		),
		fake.Fake,
	}
}
`

var rootClientScoper = `
func (c *$.type|private$ClusterClient) Cluster(cluster logicalcluster.Path) typed$.groupVersion$.$.type|singularKind$Interface {
	return newFake$.type|public$Client(c.Fake, cluster)
}
`

var namespacedClientScoper = `
func (c *$.type|private$ClusterClient) Cluster(cluster logicalcluster.Path) typedkcp$.groupVersion$.$.type|publicPlural$Namespacer {
	return &$.type|private$Namespacer{Fake: c.Fake, ClusterPath: cluster}
}

type $.type|private$Namespacer struct {
	*kcptesting.Fake
	ClusterPath logicalcluster.Path
}

func (n *$.type|private$Namespacer) Namespace(namespace string) typed$.groupVersion$.$.type|singularKind$Interface {
	return newFake$.type|public$Client(n.Fake, namespace, n.ClusterPath)
}
`

// scoped struct declarations.
var clientStructType = []string{
	noList | noApply: `
	// $.type|private$ScopedClient implements $.type|public$Interface
	type $.type|private$ScopedClient struct {
		*$.FakeClient|raw$[*$.groupVersion$.$.type|singularKind$]
		Fake *kcptesting.Fake
		ClusterPath logicalcluster.Path
	}
	`,
	withList | noApply: `
	// $.type|private$ScopedClient implements $.type|public$Interface
	type $.type|private$ScopedClient struct {
		*$.FakeClientWithList|raw$[*$.groupVersion$.$.type|singularKind$, *$.groupVersion$.$.type|singularKind$List]
		Fake *kcptesting.Fake
		ClusterPath logicalcluster.Path
	}
	`,
	noList | withApply: `
	// $.type|private$ScopedClient implements $.type|public$Interface
	type $.type|private$ScopedClient struct {
		*$.FakeClientWithApply|raw$[*$.groupVersion$.$.type|singularKind$, *$.inputApplyConfig|raw$]
		Fake *kcptesting.Fake
		ClusterPath logicalcluster.Path
	}
	`,
	withList | withApply: `
	// $.type|private$ScopedClient implements $.type|public$Interface
	type $.type|private$ScopedClient struct {
		*$.FakeClientWithListAndApply|raw$[*$.groupVersion$.$.type|singularKind$, *$.groupVersion$.$.type|singularKind$List, *$.inputApplyConfig|raw$]
		Fake *kcptesting.Fake
		ClusterPath logicalcluster.Path
	}
	`,
}

// Constructors for the scoped struct, in all variants.
var newClientStruct = []string{
	noList | noApply: `
	func newFake$.type|public$Client(fake *kcptesting.Fake$if .namespaced$, namespace string$end$, clusterPath logicalcluster.Path) typed$.groupVersion$.$.type|singularKind$Interface {
		return &$.type|private$ScopedClient{
			$.NewFakeClient|raw$[*$.groupVersion$.$.type|singularKind$](
				fake,
				clusterPath,
				$if .namespaced$namespace$else$""$end$,
				$.groupVersion$.SchemeGroupVersion.WithResource("$.type|resource$"),
				$.groupVersion$.SchemeGroupVersion.WithKind("$.type|singularKind$"),
				func() *$.groupVersion$.$.type|singularKind$ {return &$.groupVersion$.$.type|singularKind${}},
			),
			fake,
			clusterPath,
		}
	}
	`,
	noList | withApply: `
	func newFake$.type|public$Client(fake *kcptesting.Fake$if .namespaced$, namespace string$end$, clusterPath logicalcluster.Path) typed$.groupVersion$.$.type|singularKind$Interface {
		return &$.type|private$ScopedClient{
			$.NewFakeClientWithApply|raw$[*$.groupVersion$.$.type|singularKind$, *$.inputApplyConfig|raw$](
				fake,
				clusterPath,
				$if .namespaced$namespace$else$""$end$,
				$.groupVersion$.SchemeGroupVersion.WithResource("$.type|resource$"),
				$.groupVersion$.SchemeGroupVersion.WithKind("$.type|singularKind$"),
				func() *$.groupVersion$.$.type|singularKind$ {return &$.groupVersion$.$.type|singularKind${}},
			),
			fake,
			clusterPath,
		}
	}
	`,
	withList | noApply: `
	func newFake$.type|public$Client(fake *kcptesting.Fake$if .namespaced$, namespace string$end$, clusterPath logicalcluster.Path) typed$.groupVersion$.$.type|singularKind$Interface {
		return &$.type|private$ScopedClient{
			$.NewFakeClientWithList|raw$[*$.groupVersion$.$.type|singularKind$, *$.groupVersion$.$.type|singularKind$List](
				fake,
				clusterPath,
				$if .namespaced$namespace$else$""$end$,
				$.groupVersion$.SchemeGroupVersion.WithResource("$.type|resource$"),
				$.groupVersion$.SchemeGroupVersion.WithKind("$.type|singularKind$"),
				func() *$.groupVersion$.$.type|singularKind$ {return &$.groupVersion$.$.type|singularKind${}},
				func() *$.groupVersion$.$.type|singularKind$List {return &$.groupVersion$.$.type|singularKind$List{}},
				func(dst, src *$.groupVersion$.$.type|singularKind$List) {dst.ListMeta = src.ListMeta},
				func(list *$.groupVersion$.$.type|singularKind$List) []*$.groupVersion$.$.type|singularKind$ {return $.ToPointerSlice|raw$(list.Items)},
				func(list *$.groupVersion$.$.type|singularKind$List, items []*$.groupVersion$.$.type|singularKind$) {list.Items = $.FromPointerSlice|raw$(items)},
			),
			fake,
			clusterPath,
		}
	}
	`,
	withList | withApply: `
	func newFake$.type|public$Client(fake *kcptesting.Fake$if .namespaced$, namespace string$end$, clusterPath logicalcluster.Path) typed$.groupVersion$.$.type|singularKind$Interface {
		return &$.type|private$ScopedClient{
			$.NewFakeClientWithListAndApply|raw$[*$.groupVersion$.$.type|singularKind$, *$.groupVersion$.$.type|singularKind$List, *$.inputApplyConfig|raw$](
				fake,
				clusterPath,
				$if .namespaced$namespace$else$""$end$,
				$.groupVersion$.SchemeGroupVersion.WithResource("$.type|resource$"),
				$.groupVersion$.SchemeGroupVersion.WithKind("$.type|singularKind$"),
				func() *$.groupVersion$.$.type|singularKind$ {return &$.groupVersion$.$.type|singularKind${}},
				func() *$.groupVersion$.$.type|singularKind$List {return &$.groupVersion$.$.type|singularKind$List{}},
				func(dst, src *$.groupVersion$.$.type|singularKind$List) {dst.ListMeta = src.ListMeta},
				func(list *$.groupVersion$.$.type|singularKind$List) []*$.groupVersion$.$.type|singularKind$ {return $.ToPointerSlice|raw$(list.Items)},
				func(list *$.groupVersion$.$.type|singularKind$List, items []*$.groupVersion$.$.type|singularKind$) {list.Items = $.FromPointerSlice|raw$(items)},
			),
			fake,
			clusterPath,
		}
	}
	`,
}

var listTemplate = `
// List takes label and field selectors, and returns the list of $.type|publicPlural$ that match those selectors.
func (c *$.type|private$ScopedClient) List(ctx $.contextContext|raw$, opts $.ListOptions|raw$) (result *$.type|raw$List, err error) {
	emptyResult := &$.type|raw$List{}
	obj, err := c.Fake.
		$- if .namespaced$Invokes($.NewListAction|raw$(c.Resource(), c.ClusterPath, c.Kind(), c.Namespace(), opts), emptyResult)
		$- else$Invokes($.NewRootListAction|raw$(c.Resource(), c.ClusterPath, c.Kind(), opts), emptyResult)$end$
	if obj == nil {
		return emptyResult, err
	}
	return obj.(*$.type|raw$List), err
}
`

var getTemplate = `
// Get takes name of the $.type|private$, and returns the corresponding $.resultType|private$ object, and an error if there is any.
func (c *$.type|private$ScopedClient) Get(ctx $.contextContext|raw$, name string, _ $.GetOptions|raw$) (result *$.resultType|raw$, err error) {
	emptyResult := &$.resultType|raw${}
	obj, err := c.Fake.
		$- if .namespaced$Invokes($.NewGetAction|raw$(c.Resource(), c.ClusterPath, c.Namespace(), name), emptyResult)
		$- else$Invokes($.NewRootGetAction|raw$(c.Resource(), c.ClusterPath, name), emptyResult)$end$
	if obj == nil {
		return emptyResult, err
	}
	return obj.(*$.resultType|raw$), err
}
`

var getSubresourceTemplate = `
// Get takes name of the $.type|private$, and returns the corresponding $.resultType|private$ object, and an error if there is any.
func (c *$.type|private$ScopedClient) Get(ctx $.contextContext|raw$, $.type|private$Name string, _ $.GetOptions|raw$) (result *$.resultType|raw$, err error) {
	emptyResult := &$.resultType|raw${}
	obj, err := c.Fake.
		$- if .namespaced$Invokes($.NewGetSubresourceAction|raw$(c.Resource(), c.ClusterPath, c.Namespace(), "$.subresourcePath$", $.type|private$Name), emptyResult)
		$- else$Invokes($.NewRootGetSubresourceAction|raw$(c.Resource(), c.ClusterPath, "$.subresourcePath$", $.type|private$Name), emptyResult)$end $
	if obj == nil {
		return emptyResult, err
	}
	return obj.(*$.resultType|raw$), err
}
`

var deleteTemplate = `
// Delete takes name of the $.type|private$ and deletes it. Returns an error if one occurs.
func (c *$.type|private$ScopedClient) Delete(ctx $.contextContext|raw$, name string, opts $.DeleteOptions|raw$) error {
	_, err := c.Fake.
		$- if .namespaced$Invokes($.NewDeleteActionWithOptions|raw$(c.Resource(), c.ClusterPath, c.Namespace(), name, opts), &$.type|raw${})
		$- else$Invokes($.NewRootDeleteActionWithOptions|raw$(c.Resource(), c.ClusterPath, name, opts), &$.type|raw${})$end$
	return err
}
`

var createTemplate = `
// Create takes the representation of a $.inputType|private$ and creates it. Returns the server's representation of the $.resultType|private$, and an error, if there is any.
func (c *$.type|private$ScopedClient) Create(ctx $.contextContext|raw$, $.inputType|private$ *$.inputType|raw$, _ $.CreateOptions|raw$) (result *$.resultType|raw$, err error) {
	emptyResult := &$.resultType|raw${}
	obj, err := c.Fake.
		$- if .namespaced$Invokes($.NewCreateAction|raw$(c.Resource(), c.ClusterPath, c.Namespace(), $.inputType|private$), emptyResult)
		$- else$Invokes($.NewRootCreateAction|raw$(c.Resource(), c.ClusterPath, $.inputType|private$), emptyResult)$end$
	if obj == nil {
		return emptyResult, err
	}
	return obj.(*$.resultType|raw$), err
}
`

var createSubresourceTemplate = `
// Create takes the representation of a $.inputType|private$ and creates it. Returns the server's representation of the $.resultType|private$, and an error, if there is any.
func (c *$.type|private$ScopedClient) Create(ctx $.contextContext|raw$, $.type|private$Name string, $.inputType|private$ *$.inputType|raw$, _ $.CreateOptions|raw$) (result *$.resultType|raw$, err error) {
	emptyResult := &$.resultType|raw${}
	obj, err := c.Fake.
		$- if .namespaced$Invokes($.NewCreateSubresourceAction|raw$(c.Resource(), c.ClusterPath, $.type|private$Name, "$.subresourcePath$", c.Namespace(), $.inputType|private$), emptyResult)
		$- else$Invokes($.NewRootCreateSubresourceAction|raw$(c.Resource(), c.ClusterPath, $.type|private$Name, "$.subresourcePath$", $.inputType|private$), emptyResult)$end$
	if obj == nil {
		return emptyResult, err
	}
	return obj.(*$.resultType|raw$), err
}
`

var updateTemplate = `
// Update takes the representation of a $.inputType|private$ and updates it. Returns the server's representation of the $.resultType|private$, and an error, if there is any.
func (c *$.type|private$ScopedClient) Update(ctx $.contextContext|raw$, $.inputType|private$ *$.inputType|raw$, _ $.UpdateOptions|raw$) (result *$.resultType|raw$, err error) {
	emptyResult := &$.resultType|raw${}
	obj, err := c.Fake.
		$- if .namespaced$Invokes($.NewUpdateAction|raw$(c.Resource(), c.ClusterPath, c.Namespace(), $.inputType|private$), emptyResult)
		$- else$Invokes($.NewRootUpdateAction|raw$(c.Resource(), c.ClusterPath, $.inputType|private$), emptyResult)$end$
	if obj == nil {
		return emptyResult, err
	}
	return obj.(*$.resultType|raw$), err
}
`

var updateSubresourceTemplate = `
// Update takes the representation of a $.inputType|private$ and updates it. Returns the server's representation of the $.resultType|private$, and an error, if there is any.
func (c *$.type|private$ScopedClient) Update(ctx $.contextContext|raw$, $.type|private$Name string, $.inputType|private$ *$.inputType|raw$, _ $.UpdateOptions|raw$) (result *$.resultType|raw$, err error) {
	emptyResult := &$.resultType|raw${}
	obj, err := c.Fake.
		$- if .namespaced$Invokes($.NewUpdateSubresourceAction|raw$(c.Resource(), c.ClusterPath, "$.subresourcePath$", c.Namespace(), $.inputType|private$), &$.inputType|raw${})
		$- else$Invokes($.NewRootUpdateSubresourceAction|raw$(c.Resource(), c.ClusterPath, "$.subresourcePath$", $.inputType|private$), emptyResult)$end$
	if obj == nil {
		return emptyResult, err
	}
	return obj.(*$.resultType|raw$), err
}
`

var watchTemplate = `
// Watch returns a $.watchInterface|raw$ that watches the requested $.type|privatePlural$.
func (c *$.type|private$ScopedClient) Watch(ctx $.contextContext|raw$, opts $.ListOptions|raw$) ($.watchInterface|raw$, error) {
	return c.Fake.
		$- if .namespaced$InvokesWatch($.NewWatchAction|raw$(c.Resource(), c.ClusterPath, c.Namespace(), opts))
		$- else$InvokesWatch($.NewRootWatchAction|raw$(c.Resource(), c.ClusterPath, opts))$end$
}
`

var patchTemplate = `
// Patch applies the patch and returns the patched $.resultType|private$.
func (c *$.type|private$ScopedClient) Patch(ctx $.contextContext|raw$, name string, pt $.PatchType|raw$, data []byte, _ $.PatchOptions|raw$, subresources ...string) (result *$.resultType|raw$, err error) {
	emptyResult := &$.resultType|raw${}
	obj, err := c.Fake.
		$- if .namespaced$Invokes($.NewPatchSubresourceAction|raw$(c.Resource(), c.ClusterPath, c.Namespace(), name, pt, data, subresources... ), emptyResult)
		$- else$Invokes($.NewRootPatchSubresourceAction|raw$(c.Resource(), c.ClusterPath, name, pt, data, subresources...), emptyResult)$end$
	if obj == nil {
		return emptyResult, err
	}
	return obj.(*$.resultType|raw$), err
}
`

var applyTemplate = `
// Apply takes the given apply declarative configuration, applies it and returns the applied $.resultType|private$.
func (c *$.type|private$ScopedClient) Apply(ctx $.contextContext|raw$, $.inputType|private$ *$.inputApplyConfig|raw$, _ $.ApplyOptions|raw$) (result *$.resultType|raw$, err error) {
	if $.inputType|private$ == nil {
		return nil, $.fmtErrorf|raw$("$.inputType|private$ provided to Apply must not be nil")
	}
	data, err := $.jsonMarshal|raw$($.inputType|private$)
	if err != nil {
		return nil, err
	}
	name := $.inputType|private$.Name
	if name == nil {
		return nil, $.fmtErrorf|raw$("$.inputType|private$.Name must be provided to Apply")
	}
	emptyResult := &$.resultType|raw${}
	obj, err := c.Fake.
		$- if .namespaced$Invokes($.NewPatchSubresourceAction|raw$(c.Resource(), c.ClusterPath, c.Namespace(), *name, $.ApplyPatchType|raw$, data), emptyResult)
		$- else$Invokes($.NewRootPatchSubresourceAction|raw$(c.Resource(), c.ClusterPath, *name, $.ApplyPatchType|raw$, data), emptyResult)$end$
	if obj == nil {
		return emptyResult, err
	}
	return obj.(*$.resultType|raw$), err
}
`

var applySubresourceTemplate = `
// Apply takes top resource name and the apply declarative configuration for $.subresourcePath$,
// applies it and returns the applied $.resultType|private$, and an error, if there is any.
func (c *$.type|private$ScopedClient) Apply(ctx $.contextContext|raw$, $.type|private$Name string, $.inputType|private$ *$.inputApplyConfig|raw$, _ $.ApplyOptions|raw$) (result *$.resultType|raw$, err error) {
	if $.inputType|private$ == nil {
		return nil, $.fmtErrorf|raw$("$.inputType|private$ provided to Apply must not be nil")
	}
	data, err := $.jsonMarshal|raw$($.inputType|private$)
	if err != nil {
		return nil, err
	}
	emptyResult := &$.resultType|raw${}
	obj, err := c.Fake.
		$- if .namespaced$Invokes($.NewPatchSubresourceAction|raw$(c.Resource(), c.ClusterPath, c.Namespace(), $.type|private$Name, $.ApplyPatchType|raw$, data, "$.inputType|private$"), emptyResult)
		$- else$Invokes($.NewRootPatchSubresourceAction|raw$(c.Resource(), c.ClusterPath, $.type|private$Name, $.ApplyPatchType|raw$, data, "$.inputType|private$"), emptyResult)$end$
	if obj == nil {
		return emptyResult, err
	}
	return obj.(*$.resultType|raw$), err
}
`
