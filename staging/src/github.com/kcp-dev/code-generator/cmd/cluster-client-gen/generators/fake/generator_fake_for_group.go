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

package fake

import (
	"fmt"
	"io"
	"path"
	"strings"

	"k8s.io/gengo/v2/generator"
	"k8s.io/gengo/v2/namer"
	"k8s.io/gengo/v2/types"

	"github.com/kcp-dev/code-generator/v3/cmd/cluster-client-gen/generators/util"
)

// genFakeForGroup produces a file for a group client, e.g. ExtensionsClient for the extension group.
type genFakeForGroup struct {
	generator.GoGenerator
	outputPackage     string // must be a Go import-path
	realClientPackage string // must be a Go import-path
	version           string
	groupGoName       string
	// types in this group
	types   []*types.Type
	imports namer.ImportTracker
	// If the genGroup has been called. This generator should only execute once.
	called                     bool
	singleClusterClientPackage string
}

var _ generator.Generator = &genFakeForGroup{}

// We only want to call GenerateType() once per group.
func (g *genFakeForGroup) Filter(_ *generator.Context, _ *types.Type) bool {
	if !g.called {
		g.called = true
		return true
	}
	return false
}

func (g *genFakeForGroup) Namers(_ *generator.Context) namer.NameSystems {
	return namer.NameSystems{
		"raw": namer.NewRawNamer(g.outputPackage, g.imports),
	}
}

func groupVersionFromPackage(pkg string) string {
	version := path.Base(pkg)
	group := path.Base(path.Dir(pkg))

	return strings.ToLower(group + version)
}

func (g *genFakeForGroup) Imports(_ *generator.Context) (imports []string) {
	imports = g.imports.ImportLines()
	if len(g.types) != 0 {
		imports = append(imports,
			"github.com/kcp-dev/logicalcluster/v3",
			fmt.Sprintf("%s %q", groupVersionFromPackage(g.singleClusterClientPackage), g.singleClusterClientPackage),
			fmt.Sprintf("kcp%s %q", groupVersionFromPackage(g.realClientPackage), g.realClientPackage),
		)
	}
	return imports
}

func (g *genFakeForGroup) GenerateType(c *generator.Context, _ *types.Type, w io.Writer) error {
	sw := generator.NewSnippetWriter(w, c, "$", "$")
	gv := groupVersionFromPackage(g.realClientPackage)

	m := map[string]interface{}{
		"GroupGoName":         g.groupGoName,
		"Version":             namer.IC(g.version),
		"Fake":                c.Universe.Type(types.Name{Package: "github.com/kcp-dev/client-go/third_party/k8s.io/client-go/testing", Name: "Fake"}),
		"RESTClientInterface": c.Universe.Type(types.Name{Package: "k8s.io/client-go/rest", Name: "Interface"}),
		"RESTClient":          c.Universe.Type(types.Name{Package: "k8s.io/client-go/rest", Name: "RESTClient"}),
		"FakeClient":          c.Universe.Type(types.Name{Package: "k8s.io/client-go/gentype", Name: "FakeClient"}),
		"NewFakeClient":       c.Universe.Function(types.Name{Package: "k8s.io/client-go/gentype", Name: "NewFakeClient"}),
		"realClientPackage":   gv,
		"kcpClientPackage":    "kcp" + gv,
	}

	sw.Do(groupClusterClientTemplate, m)
	for _, t := range g.types {
		wrapper := map[string]interface{}{
			"type":              t,
			"GroupGoName":       g.groupGoName,
			"Version":           namer.IC(g.version),
			"realClientPackage": gv,
			"kcpClientPackage":  "kcp" + gv,
		}
		sw.Do(clusterGetterImpl, wrapper)
	}

	sw.Do(groupClientTemplate, m)
	for _, t := range g.types {
		tags, err := util.ParseClientGenTags(append(t.SecondClosestCommentLines, t.CommentLines...))
		if err != nil {
			return err
		}
		wrapper := map[string]interface{}{
			"type":              t,
			"GroupGoName":       g.groupGoName,
			"Version":           namer.IC(g.version),
			"realClientPackage": gv,
			"kcpClientPackage":  "kcp" + gv,
		}
		if tags.NonNamespaced {
			sw.Do(getterImplNonNamespaced, wrapper)
			continue
		}
		sw.Do(getterImplNamespaced, wrapper)
	}
	sw.Do(getRESTClient, m)
	return sw.Error()
}

var groupClusterClientTemplate = `
var _ $.kcpClientPackage$.$.GroupGoName$$.Version$ClusterInterface = (*$.GroupGoName$$.Version$ClusterClient)(nil)

type $.GroupGoName$$.Version$ClusterClient struct {
	*$.Fake|raw$
}

func (c *$.GroupGoName$$.Version$ClusterClient) Cluster(clusterPath logicalcluster.Path) $.realClientPackage$.$.GroupGoName$$.Version$Interface {
	if clusterPath == logicalcluster.Wildcard {
		panic("A specific cluster must be provided when scoping, not the wildcard.")
	}
	return &$.GroupGoName$$.Version$Client{Fake: c.Fake, ClusterPath: clusterPath}
}
`

var clusterGetterImpl = `
func (c *$.GroupGoName$$.Version$ClusterClient) $.type|publicPlural$() $.kcpClientPackage$.$.type|public$ClusterInterface {
	return newFake$.type|public$ClusterClient(c)
}
`

var groupClientTemplate = `
type $.GroupGoName$$.Version$Client struct {
	*$.Fake|raw$
	ClusterPath logicalcluster.Path
}
`

var getterImplNamespaced = `
func (c *$.GroupGoName$$.Version$Client) $.type|publicPlural$(namespace string) $.realClientPackage$.$.type|public$Interface {
	return newFake$.type|public$Client(c.Fake, namespace, c.ClusterPath)
}
`

var getterImplNonNamespaced = `
func (c *$.GroupGoName$$.Version$Client) $.type|publicPlural$() $.realClientPackage$.$.type|public$Interface {
	return newFake$.type|public$Client(c.Fake, c.ClusterPath)
}
`

var getRESTClient = `
// RESTClient returns a RESTClient that is used to communicate
// with API server by this client implementation.
func (c *$.GroupGoName$$.Version$Client) RESTClient() $.RESTClientInterface|raw$ {
	var ret *$.RESTClient|raw$
	return ret
}
`
