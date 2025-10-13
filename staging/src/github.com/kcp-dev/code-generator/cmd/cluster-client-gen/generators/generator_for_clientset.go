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
	"strings"

	"k8s.io/gengo/v2/generator"
	"k8s.io/gengo/v2/namer"
	"k8s.io/gengo/v2/types"

	clientgentypes "github.com/kcp-dev/code-generator/v3/cmd/cluster-client-gen/types"
)

// genClientset generates a package for a clientset.
type genClientset struct {
	generator.GoGenerator
	groups                 []clientgentypes.GroupVersions
	groupGoNames           map[clientgentypes.GroupVersion]string
	clientsetPackage       string // must be a Go import-path
	imports                namer.ImportTracker
	clientsetGenerated     bool
	singleClusterClientPkg string
}

var _ generator.Generator = &genClientset{}

func (g *genClientset) Namers(_ *generator.Context) namer.NameSystems {
	return namer.NameSystems{
		"raw": namer.NewRawNamer(g.clientsetPackage, g.imports),
	}
}

// We only want to call GenerateType() once.
func (g *genClientset) Filter(_ *generator.Context, _ *types.Type) bool {
	ret := !g.clientsetGenerated
	g.clientsetGenerated = true
	return ret
}

func (g *genClientset) Imports(c *generator.Context) (imports []string) {
	imports = append(imports, g.imports.ImportLines()...)
	imports = append(imports,
		fmt.Sprintf("client %q", g.singleClusterClientPkg),
		"github.com/kcp-dev/logicalcluster/v3",
	)
	for _, group := range g.groups {
		for _, version := range group.Versions {
			typedClientPath := path.Join(g.clientsetPackage, "typed", strings.ToLower(group.PackageName), strings.ToLower(version.NonEmpty()))
			groupAlias := strings.ToLower(g.groupGoNames[clientgentypes.GroupVersion{Group: group.Group, Version: version.Version}])
			imports = append(imports, fmt.Sprintf("%s%s %q", groupAlias, strings.ToLower(version.NonEmpty()), typedClientPath))
		}
	}
	return
}

func (g *genClientset) GenerateType(c *generator.Context, _ *types.Type, w io.Writer) error {
	// TODO: We actually don't need any type information to generate the clientset,
	// perhaps we can adapt the go2ild framework to this kind of usage.
	sw := generator.NewSnippetWriter(w, c, "$", "$")

	allGroups := clientgentypes.ToGroupVersionInfo(g.groups, g.groupGoNames)
	m := map[string]any{
		"allGroups":                            allGroups,
		"fmtErrorf":                            c.Universe.Type(types.Name{Package: "fmt", Name: "Errorf"}),
		"Config":                               c.Universe.Type(types.Name{Package: "k8s.io/client-go/rest", Name: "Config"}),
		"DefaultKubernetesUserAgent":           c.Universe.Function(types.Name{Package: "k8s.io/client-go/rest", Name: "DefaultKubernetesUserAgent"}),
		"RESTConfig":                           c.Universe.Type(types.Name{Package: "k8s.io/client-go/rest", Name: "Config"}),
		"RESTHTTPClientFor":                    c.Universe.Function(types.Name{Package: "k8s.io/client-go/rest", Name: "HTTPClientFor"}),
		"DiscoveryInterface":                   c.Universe.Type(types.Name{Package: "k8s.io/client-go/discovery", Name: "DiscoveryInterface"}),
		"DiscoveryClient":                      c.Universe.Type(types.Name{Package: "k8s.io/client-go/discovery", Name: "DiscoveryClient"}),
		"httpClient":                           c.Universe.Type(types.Name{Package: "net/http", Name: "Client"}),
		"NewDiscoveryClientForConfigAndClient": c.Universe.Function(types.Name{Package: "k8s.io/client-go/discovery", Name: "NewDiscoveryClientForConfigAndClient"}),
		"NewDiscoveryClientForConfigOrDie":     c.Universe.Function(types.Name{Package: "k8s.io/client-go/discovery", Name: "NewDiscoveryClientForConfigOrDie"}),
		"flowcontrolNewTokenBucketRateLimiter": c.Universe.Function(types.Name{Package: "k8s.io/client-go/util/flowcontrol", Name: "NewTokenBucketRateLimiter"}),
		"kcpclientCache":                       c.Universe.Type(types.Name{Package: "github.com/kcp-dev/apimachinery/v2/pkg/client", Name: "Cache"}),
	}

	sw.Do(clientsetInterface, m)
	sw.Do(clientsetTemplate, m)
	sw.Do(getDiscoveryTemplate, m)
	for _, g := range allGroups {
		sw.Do(clientsetInterfaceImplTemplate, g)
	}
	sw.Do(getClusterTemplate, m)
	sw.Do(newClientsetForConfigTemplate, m)
	sw.Do(newClientsetForConfigAndClientTemplate, m)
	sw.Do(newClientsetForConfigOrDieTemplate, m)
	sw.Do(newClientsetForRESTClientTemplate, m)

	return sw.Error()
}

var clientsetInterface = `
type ClusterInterface interface {
	Cluster(logicalcluster.Path) client.Interface
	Discovery() $.DiscoveryInterface|raw$
    $range .allGroups$$.GroupGoName$$.Version$() $.PackageAlias$.$.GroupGoName$$.Version$ClusterInterface
	$end$
}
`

var clientsetTemplate = `
// ClusterClientset contains the cluster clients for groups.
type ClusterClientset struct {
	*$.DiscoveryClient|raw$
	clientCache $.kcpclientCache|raw$[*client.Clientset]
    $range .allGroups$$.LowerCaseGroupGoName$$.Version$ *$.PackageAlias$.$.GroupGoName$$.Version$ClusterClient
    $end$
}
`

var clientsetInterfaceImplTemplate = `
// $.GroupGoName$$.Version$ retrieves the $.GroupGoName$$.Version$ClusterClient.
func (c *ClusterClientset) $.GroupGoName$$.Version$() $.PackageAlias$.$.GroupGoName$$.Version$ClusterInterface {
	return c.$.LowerCaseGroupGoName$$.Version$
}
`

var getDiscoveryTemplate = `
// Discovery retrieves the DiscoveryClient.
func (c *ClusterClientset) Discovery() $.DiscoveryInterface|raw$ {
	if c == nil {
		return nil
	}
	return c.DiscoveryClient
}
`

var getClusterTemplate = `
// Cluster scopes this clientset to one cluster.
func (c *ClusterClientset) Cluster(clusterPath logicalcluster.Path) client.Interface {
	if clusterPath == logicalcluster.Wildcard {
		panic("A specific cluster must be provided when scoping, not the wildcard.")
	}
	return c.clientCache.ClusterOrDie(clusterPath)
}
`

var newClientsetForConfigTemplate = `
// NewForConfig creates a new ClusterClientset for the given config.
// If config's RateLimiter is not set and QPS and Burst are acceptable,
// NewForConfig will generate a rate-limiter in configShallowCopy.
// NewForConfig is equivalent to NewForConfigAndClient(c, httpClient),
// where httpClient was generated with rest.HTTPClientFor(c).
func NewForConfig(c *$.Config|raw$) (*ClusterClientset, error) {
	configShallowCopy := *c

	if configShallowCopy.UserAgent == "" {
		configShallowCopy.UserAgent = $.DefaultKubernetesUserAgent|raw$()
	}

	// share the transport between all clients
	httpClient, err := $.RESTHTTPClientFor|raw$(&configShallowCopy)
	if err != nil {
		return nil, err
	}

	return NewForConfigAndClient(&configShallowCopy, httpClient)
}
`

var newClientsetForConfigAndClientTemplate = `
// NewForConfigAndClient creates a new ClusterClientset for the given config and http client.
// Note the http client provided takes precedence over the configured transport values.
// If config's RateLimiter is not set and QPS and Burst are acceptable,
// NewForConfigAndClient will generate a rate-limiter in configShallowCopy.
func NewForConfigAndClient(c *$.Config|raw$, httpClient *$.httpClient|raw$) (*ClusterClientset, error) {
	configShallowCopy := *c
	if configShallowCopy.RateLimiter == nil && configShallowCopy.QPS > 0 {
		if configShallowCopy.Burst <= 0 {
			return nil, $.fmtErrorf|raw$("burst is required to be greater than 0 when RateLimiter is not set and QPS is set to greater than 0")
		}
		configShallowCopy.RateLimiter = $.flowcontrolNewTokenBucketRateLimiter|raw$(configShallowCopy.QPS, configShallowCopy.Burst)
	}

	cache := kcpclient.NewCache(c, httpClient, &kcpclient.Constructor[*client.Clientset]{
		NewForConfigAndClient: client.NewForConfigAndClient,
	})
	if _, err := cache.Cluster(logicalcluster.Name("root").Path()); err != nil {
		return nil, err
	}

	var cs ClusterClientset
	cs.clientCache = cache
	var err error
$range .allGroups$    cs.$.LowerCaseGroupGoName$$.Version$, err =$.PackageAlias$.NewForConfigAndClient(&configShallowCopy, httpClient)
	if err!=nil {
		return nil, err
	}
$end$
	cs.DiscoveryClient, err = $.NewDiscoveryClientForConfigAndClient|raw$(&configShallowCopy, httpClient)
	if err!=nil {
		return nil, err
	}
	return &cs, nil
}
`

var newClientsetForConfigOrDieTemplate = `
// NewForConfigOrDie creates a new ClusterClientset for the given config and
// panics if there is an error in the config.
func NewForConfigOrDie(c *$.Config|raw$) *ClusterClientset {
	cs, err := NewForConfig(c)
	if err!=nil {
		panic(err)
	}
	return cs
}
`

var newClientsetForRESTClientTemplate = `
// New creates a new ClusterClientset for the given RESTClient.
func New(c *$.RESTConfig|raw$) *ClusterClientset {
	var cs ClusterClientset
$range .allGroups$    cs.$.LowerCaseGroupGoName$$.Version$ = $.PackageAlias$.NewForConfigOrDie(c)
$end$
	cs.DiscoveryClient = $.NewDiscoveryClientForConfigOrDie|raw$(c)
	return &cs
}
`
