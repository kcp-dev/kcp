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

	"k8s.io/gengo/v2"
	"k8s.io/gengo/v2/generator"
	"k8s.io/gengo/v2/namer"
	"k8s.io/gengo/v2/types"
)

// genGroup produces a file for a group client, e.g. ExtensionsClient for the extension group.
type genGroup struct {
	generator.GoGenerator
	outputPackage string
	group         string
	version       string
	groupGoName   string
	apiPath       string
	// types in this group
	types                  []*types.Type
	imports                namer.ImportTracker
	inputPackage           string
	clientsetPackage       string // must be a Go import-path
	singleClusterClientPkg string
	// If the genGroup has been called. This generator should only execute once.
	called bool
}

var _ generator.Generator = &genGroup{}

// We only want to call GenerateType() once per group.
func (g *genGroup) Filter(_ *generator.Context, _ *types.Type) bool {
	if !g.called {
		g.called = true
		return true
	}
	return false
}

func (g *genGroup) Namers(_ *generator.Context) namer.NameSystems {
	return namer.NameSystems{
		"raw": namer.NewRawNamer(g.outputPackage, g.imports),
	}
}

func (g *genGroup) Imports(_ *generator.Context) (imports []string) {
	imports = append(imports, g.imports.ImportLines()...)
	imports = append(imports,
		"github.com/kcp-dev/logicalcluster/v3",
	)
	return imports
}

func (g *genGroup) GenerateType(c *generator.Context, t *types.Type, w io.Writer) error {
	sw := generator.NewSnippetWriter(w, c, "$", "$")

	// allow user to define a group name that's different from the one parsed from the directory.
	p := c.Universe.Package(g.inputPackage)
	groupName := g.group
	if override := gengo.ExtractCommentTags("+", p.Comments)["groupName"]; override != nil { //nolint:staticcheck
		groupName = override[0]
	}

	apiPath := `"` + g.apiPath + `"`
	if groupName == "" {
		apiPath = `"/api"`
	}
	schemePackage := path.Join(g.clientsetPackage, "scheme")
	typedClientName := g.groupGoName + namer.IC(g.version) + "Client"
	typedInterfaceName := g.groupGoName + namer.IC(g.version) + "Interface"

	m := map[string]interface{}{
		"version":                            g.version,
		"groupName":                          groupName,
		"GroupGoName":                        g.groupGoName,
		"Version":                            namer.IC(g.version),
		"types":                              g.types,
		"apiPath":                            apiPath,
		"httpClient":                         c.Universe.Type(types.Name{Package: "net/http", Name: "Client"}),
		"schemaGroupVersion":                 c.Universe.Type(types.Name{Package: "k8s.io/apimachinery/pkg/runtime/schema", Name: "GroupVersion"}),
		"runtimeAPIVersionInternal":          c.Universe.Variable(types.Name{Package: "k8s.io/apimachinery/pkg/runtime", Name: "APIVersionInternal"}),
		"restConfig":                         c.Universe.Type(types.Name{Package: "k8s.io/client-go/rest", Name: "Config"}),
		"restDefaultKubernetesUserAgent":     c.Universe.Function(types.Name{Package: "k8s.io/client-go/rest", Name: "DefaultKubernetesUserAgent"}),
		"restRESTClientInterface":            c.Universe.Type(types.Name{Package: "k8s.io/client-go/rest", Name: "Interface"}),
		"RESTHTTPClientFor":                  c.Universe.Function(types.Name{Package: "k8s.io/client-go/rest", Name: "HTTPClientFor"}),
		"restRESTClientFor":                  c.Universe.Function(types.Name{Package: "k8s.io/client-go/rest", Name: "RESTClientFor"}),
		"restRESTClientForConfigAndClient":   c.Universe.Function(types.Name{Package: "k8s.io/client-go/rest", Name: "RESTClientForConfigAndClient"}),
		"restCodecFactoryForGeneratedClient": c.Universe.Function(types.Name{Package: "k8s.io/client-go/rest", Name: "CodecFactoryForGeneratedClient"}),
		"SchemeGroupVersion":                 c.Universe.Variable(types.Name{Package: g.inputPackage, Name: "SchemeGroupVersion"}),
		"SchemePrioritizedVersionsForGroup":  c.Universe.Variable(types.Name{Package: schemePackage, Name: "Scheme.PrioritizedVersionsForGroup"}),
		"Codecs":                             c.Universe.Variable(types.Name{Package: schemePackage, Name: "Codecs"}),
		"Scheme":                             c.Universe.Variable(types.Name{Package: schemePackage, Name: "Scheme"}),
		"kcpNewCache":                        c.Universe.Function(types.Name{Package: "github.com/kcp-dev/apimachinery/v2/pkg/client", Name: "NewCache"}),
		"kcpClientConstructor":               c.Universe.Type(types.Name{Package: "github.com/kcp-dev/apimachinery/v2/pkg/client", Name: "Constructor"}),
		"typedClientReference":               c.Universe.Type(types.Name{Package: g.singleClusterClientPkg, Name: typedClientName}),
		"typedInterfaceReference":            c.Universe.Type(types.Name{Package: g.singleClusterClientPkg, Name: typedInterfaceName}),
		"NewForConfigAndClient":              c.Universe.Type(types.Name{Package: g.singleClusterClientPkg, Name: "NewForConfigAndClient"}),
	}
	sw.Do(groupInterfaceTemplate, m)
	sw.Do(clusterScoperTemplate, m)
	sw.Do(groupClientTemplate, m)
	for _, t := range g.types {
		wrapper := map[string]interface{}{
			"type":        t,
			"GroupGoName": g.groupGoName,
			"Version":     namer.IC(g.version),
		}
		sw.Do(getterImpl, wrapper)
	}
	sw.Do(newClientForConfigTemplate, m)
	sw.Do(newClientForConfigAndClientTemplate, m)
	sw.Do(newClientForConfigOrDieTemplate, m)
	if g.version == "" {
		sw.Do(setInternalVersionClientDefaultsTemplate, m)
	} else {
		sw.Do(setClientDefaultsTemplate, m)
	}

	return sw.Error()
}

var groupInterfaceTemplate = `
type $.GroupGoName$$.Version$ClusterInterface interface {
	$.GroupGoName$$.Version$ClusterScoper
	$range .types$ $.|publicPlural$ClusterGetter
	$end$
}
`

var clusterScoperTemplate = `
type $.GroupGoName$$.Version$ClusterScoper interface {
	Cluster(logicalcluster.Path) $.typedInterfaceReference|raw$
}
`

var groupClientTemplate = `
// $.GroupGoName$$.Version$ClusterClient is used to interact with features provided by the $.groupName$ group.
type $.GroupGoName$$.Version$ClusterClient struct {
	clientCache kcpclient.Cache[*$.typedClientReference|raw$]
}

func (c *$.GroupGoName$$.Version$ClusterClient) Cluster(clusterPath logicalcluster.Path) $.typedInterfaceReference|raw$ {
	if clusterPath == logicalcluster.Wildcard {
		panic("A specific cluster must be provided when scoping, not the wildcard.")
	}
	return c.clientCache.ClusterOrDie(clusterPath)
}
`

var getterImpl = `
func (c *$.GroupGoName$$.Version$ClusterClient) $.type|publicPlural$() $.type|public$ClusterInterface {
	return &$.type|privatePlural$ClusterInterface{clientCache: c.clientCache}
}
`

var newClientForConfigTemplate = `
// NewForConfig creates a new $.GroupGoName$$.Version$ClusterClient for the given config.
// NewForConfig is equivalent to NewForConfigAndClient(c, httpClient),
// where httpClient was generated with rest.HTTPClientFor(c).
func NewForConfig(c *$.restConfig|raw$) (*$.GroupGoName$$.Version$ClusterClient, error) {
	config := *c
	setConfigDefaults(&config)
	httpClient, err := $.RESTHTTPClientFor|raw$(&config)
	if err != nil {
		return nil, err
	}
	return NewForConfigAndClient(&config, httpClient)
}
`

var newClientForConfigAndClientTemplate = `
// NewForConfigAndClient creates a new $.GroupGoName$$.Version$ClusterClient for the given config and http client.
// Note the http client provided takes precedence over the configured transport values.
func NewForConfigAndClient(c *$.restConfig|raw$, h *$.httpClient|raw$) (*$.GroupGoName$$.Version$ClusterClient, error) {
	cache := $.kcpNewCache|raw$(c, h, &$.kcpClientConstructor|raw$[*$.typedClientReference|raw$]{
		NewForConfigAndClient: $.NewForConfigAndClient|raw$,
	})
	if _, err := cache.Cluster(logicalcluster.Name("root").Path()); err != nil {
		return nil, err
	}

	return &$.GroupGoName$$.Version$ClusterClient{clientCache: cache}, nil
}
`

var newClientForConfigOrDieTemplate = `
// NewForConfigOrDie creates a new $.GroupGoName$$.Version$ClusterClient for the given config and
// panics if there is an error in the config.
func NewForConfigOrDie(c *$.restConfig|raw$) *$.GroupGoName$$.Version$ClusterClient {
	client, err := NewForConfig(c)
	if err != nil {
		panic(err)
	}
	return client
}
`

var setInternalVersionClientDefaultsTemplate = `
func setConfigDefaults(config *$.restConfig|raw$) {
	config.APIPath = $.apiPath$
	if config.UserAgent == "" {
		config.UserAgent = $.restDefaultKubernetesUserAgent|raw$()
	}
	if config.GroupVersion == nil || config.GroupVersion.Group != $.SchemePrioritizedVersionsForGroup|raw$("$.groupName$")[0].Group {
		gv := $.SchemePrioritizedVersionsForGroup|raw$("$.groupName$")[0]
		config.GroupVersion = &gv
	}
	config.NegotiatedSerializer = $.restCodecFactoryForGeneratedClient|raw$($.Scheme|raw$, $.Codecs|raw$)

	if config.QPS == 0 {
		config.QPS = 5
	}
	if config.Burst == 0 {
		config.Burst = 10
	}
}
`

var setClientDefaultsTemplate = `
func setConfigDefaults(config *$.restConfig|raw$) {
	gv := $.SchemeGroupVersion|raw$
	config.GroupVersion =  &gv
	config.APIPath = $.apiPath$
	config.NegotiatedSerializer = $.restCodecFactoryForGeneratedClient|raw$($.Scheme|raw$, $.Codecs|raw$).WithoutConversion()

	if config.UserAgent == "" {
		config.UserAgent = $.restDefaultKubernetesUserAgent|raw$()
	}
}
`
