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

	clientgentypes "github.com/kcp-dev/code-generator/v3/cmd/cluster-client-gen/types"
)

// genClientset generates a package for a clientset.
type genClientset struct {
	generator.GoGenerator
	groups               []clientgentypes.GroupVersions
	groupGoNames         map[clientgentypes.GroupVersion]string
	fakeClientsetPackage string // must be a Go import-path
	imports              namer.ImportTracker
	clientsetGenerated   bool
	// the import path of the generated real clientset.
	realClientsetPackage       string // must be a Go import-path
	singleClusterClientPackage string
	applyConfigurationPackage  string
}

var _ generator.Generator = &genClientset{}

func (g *genClientset) Namers(_ *generator.Context) namer.NameSystems {
	return namer.NameSystems{
		"raw": namer.NewRawNamer(g.fakeClientsetPackage, g.imports),
	}
}

// We only want to call GenerateType() once.
func (g *genClientset) Filter(_ *generator.Context, _ *types.Type) bool {
	ret := !g.clientsetGenerated
	g.clientsetGenerated = true
	return ret
}

func (g *genClientset) Imports(_ *generator.Context) (imports []string) {
	imports = append(imports, g.imports.ImportLines()...)
	for _, group := range g.groups {
		for _, version := range group.Versions {
			groupClientPackage := path.Join(g.fakeClientsetPackage, "typed", strings.ToLower(group.PackageName), strings.ToLower(version.NonEmpty()))
			singleClusterGroupClientPackage := path.Join(g.singleClusterClientPackage, "typed", strings.ToLower(group.PackageName), strings.ToLower(version.NonEmpty()))
			fakeGroupClientPackage := path.Join(groupClientPackage, "fake")

			groupAlias := strings.ToLower(g.groupGoNames[clientgentypes.GroupVersion{Group: group.Group, Version: version.Version}])
			imports = append(imports, fmt.Sprintf("%s%s %q", groupAlias, strings.ToLower(version.NonEmpty()), singleClusterGroupClientPackage),
				fmt.Sprintf("kcp%s%s %q", groupAlias, strings.ToLower(version.NonEmpty()), groupClientPackage),
				fmt.Sprintf("kcpfake%s%s %q", groupAlias, strings.ToLower(version.NonEmpty()), fakeGroupClientPackage))
		}
	}

	// the package that has the clientset Interface and imports for the code in commonTemplate
	imports = append(imports,
		fmt.Sprintf("clientset %q", g.singleClusterClientPackage),
		fmt.Sprintf("kcpclientset %q", g.realClientsetPackage),
		fmt.Sprintf("kcpclientscheme %q", path.Join(g.realClientsetPackage, "scheme")),

		"github.com/kcp-dev/logicalcluster/v3",
		"kcptesting \"github.com/kcp-dev/client-go/third_party/k8s.io/client-go/testing\"",
		"kcpfakediscovery \"github.com/kcp-dev/client-go/third_party/k8s.io/client-go/discovery/fake\"",
		"k8s.io/client-go/discovery",
		"k8s.io/apimachinery/pkg/runtime",
	)

	return
}

func (g *genClientset) GenerateType(c *generator.Context, _ *types.Type, w io.Writer) error {
	generateApply := len(g.applyConfigurationPackage) > 0

	// TODO: We actually don't need any type information to generate the clientset,
	// perhaps we can adapt the go2ild framework to this kind of usage.
	sw := generator.NewSnippetWriter(w, c, "$", "$")

	allGroups := clientgentypes.ToGroupVersionInfo(g.groups, g.groupGoNames)

	sw.Do(clusterCommon, nil)

	for _, group := range allGroups {
		m := map[string]interface{}{
			"group":        group.Group,
			"version":      group.Version,
			"PackageAlias": group.PackageAlias,
			"GroupGoName":  group.GroupGoName,
			"Version":      namer.IC(group.Version.String()),
		}

		sw.Do(clusterClientsetInterfaceImplTemplate, m)
	}

	sw.Do(singleCommon, nil)

	if generateApply {
		sw.Do(managedFieldsClientset, map[string]any{
			"newTypeConverter": types.Ref(g.applyConfigurationPackage, "NewTypeConverter"),
		})
	}

	for _, group := range allGroups {
		m := map[string]interface{}{
			"group":        group.Group,
			"version":      group.Version,
			"PackageAlias": group.PackageAlias,
			"GroupGoName":  group.GroupGoName,
			"Version":      namer.IC(group.Version.String()),
		}

		sw.Do(clientsetInterfaceImplTemplate, m)
	}

	return sw.Error()
}

// This part of code is version-independent, unchanging.

var managedFieldsClientset = `
// NewClientset returns a clientset that will respond with the provided objects.
// It's backed by a very simple object tracker that processes creates, updates and deletions as-is,
// without applying any validations and/or defaults. It shouldn't be considered a replacement
// for a real clientset and is mostly useful in simple unit tests.
func NewClientset(objects ...runtime.Object) *ClusterClientset {
	o := kcptesting.NewFieldManagedObjectTracker(
		kcpclientscheme.Scheme,
		kcpclientscheme.Codecs.UniversalDecoder(),
		$.newTypeConverter|raw$(kcpclientscheme.Scheme),
	)
	o.AddAll(objects...)

	cs := &ClusterClientset{Fake: kcptesting.Fake{}, tracker: o}
	cs.discovery = &kcpfakediscovery.FakeDiscovery{Fake: &cs.Fake, ClusterPath: logicalcluster.Wildcard}
	cs.AddReactor("*", "*", kcptesting.ObjectReaction(o))
	cs.AddWatchReactor("*", kcptesting.WatchReaction(o))

	return cs
}
`

var clusterCommon = `
// NewSimpleClientset returns a clientset that will respond with the provided objects.
// It's backed by a very simple object tracker that processes creates, updates and deletions as-is,
// without applying any field management, validations and/or defaults. It shouldn't be considered a replacement
// for a real clientset and is mostly useful in simple unit tests.
//
// DEPRECATED: NewClientset replaces this with support for field management, which significantly improves
// server side apply testing. NewClientset is only available when apply configurations are generated (e.g.
// via --with-applyconfig).
func NewSimpleClientset(objects ...runtime.Object) *ClusterClientset {
	o := kcptesting.NewObjectTracker(kcpclientscheme.Scheme, kcpclientscheme.Codecs.UniversalDecoder())
	o.AddAll(objects...)

	cs := &ClusterClientset{Fake: kcptesting.Fake{}, tracker: o}
	cs.discovery = &kcpfakediscovery.FakeDiscovery{Fake: &cs.Fake, ClusterPath: logicalcluster.Wildcard}
	cs.AddReactor("*", "*", kcptesting.ObjectReaction(o))
	cs.AddWatchReactor("*", kcptesting.WatchReaction(o))

	return cs
}

// ClusterClientset contains the clients for groups.
type ClusterClientset struct {
	kcptesting.Fake
	discovery *kcpfakediscovery.FakeDiscovery
	tracker   kcptesting.ObjectTracker
}

var _ kcpclientset.ClusterInterface = (*ClusterClientset)(nil)

// Discovery retrieves the DiscoveryClient
func (c *ClusterClientset) Discovery() discovery.DiscoveryInterface {
	return c.discovery
}

func (c *ClusterClientset) Tracker() kcptesting.ObjectTracker {
	return c.tracker
}

// Cluster scopes this clientset to one cluster.
func (c *ClusterClientset) Cluster(clusterPath logicalcluster.Path) clientset.Interface {
	if clusterPath == logicalcluster.Wildcard {
		panic("A specific cluster must be provided when scoping, not the wildcard.")
	}
	return &Clientset{
		Fake:        &c.Fake,
		discovery:   &kcpfakediscovery.FakeDiscovery{Fake: &c.Fake, ClusterPath: clusterPath},
		tracker:     c.tracker.Cluster(clusterPath),
		clusterPath: clusterPath,
	}
}
`

var clusterClientsetInterfaceImplTemplate = `
// $.GroupGoName$$.Version$ retrieves the $.GroupGoName$$.Version$ClusterClient
func (c *ClusterClientset) $.GroupGoName$$.Version$() kcp$.PackageAlias$.$.GroupGoName$$.Version$ClusterInterface {
	return &kcpfake$.PackageAlias$.$.GroupGoName$$.Version$ClusterClient{Fake: &c.Fake}
}
`

var singleCommon = `
// Clientset implements clientset.Interface. Meant to be embedded into a
// struct to get a default implementation. This makes faking out just the method
// you want to test easier.
type Clientset struct {
	*kcptesting.Fake
	discovery   *kcpfakediscovery.FakeDiscovery
	tracker     kcptesting.ScopedObjectTracker
	clusterPath logicalcluster.Path
}

var (
	_ clientset.Interface         = &Clientset{}
	_ kcptesting.FakeScopedClient = &Clientset{}
)

func (c *Clientset) Discovery() discovery.DiscoveryInterface {
	return c.discovery
}

func (c *Clientset) Tracker() kcptesting.ScopedObjectTracker {
	return c.tracker
}
`

var clientsetInterfaceImplTemplate = `
// $.GroupGoName$$.Version$ retrieves the $.GroupGoName$$.Version$Client
func (c *Clientset) $.GroupGoName$$.Version$() $.PackageAlias$.$.GroupGoName$$.Version$Interface {
	return &kcpfake$.PackageAlias$.$.GroupGoName$$.Version$Client{Fake: c.Fake, ClusterPath: c.clusterPath}
}
`
