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

	"k8s.io/gengo/v2/generator"
	"k8s.io/gengo/v2/namer"
	"k8s.io/gengo/v2/types"
	"k8s.io/klog/v2"

	clientgentypes "github.com/kcp-dev/code-generator/v3/cmd/cluster-client-gen/types"
)

// factoryGenerator produces a file of listers for a given GroupVersion and
// type.
type factoryGenerator struct {
	generator.GoGenerator
	outputPackage                          string
	imports                                namer.ImportTracker
	groupVersions                          map[string]clientgentypes.GroupVersions
	gvGoNames                              map[string]string
	clientSetPackage                       string
	internalInterfacesPackage              string
	filtered                               bool
	singleClusterVersionedClientSetPackage string
	singleClusterInformersPackage          string
}

var _ generator.Generator = &factoryGenerator{}

func (g *factoryGenerator) Filter(_ *generator.Context, _ *types.Type) bool {
	if !g.filtered {
		g.filtered = true
		return true
	}
	return false
}

func (g *factoryGenerator) Namers(_ *generator.Context) namer.NameSystems {
	return namer.NameSystems{
		"raw": namer.NewRawNamer(g.outputPackage, g.imports),
	}
}

func (g *factoryGenerator) Imports(_ *generator.Context) (imports []string) {
	imports = append(imports, g.imports.ImportLines()...)
	return
}

func (g *factoryGenerator) GenerateType(c *generator.Context, t *types.Type, w io.Writer) error {
	sw := generator.NewSnippetWriter(w, c, "{{", "}}")

	klog.V(5).Infof("processing type %v", t)

	gvInterfaces := make(map[string]*types.Type)
	gvClusterInterfaces := make(map[string]*types.Type)
	gvNewFuncs := make(map[string]*types.Type)
	gvNewScopedFuncs := make(map[string]*types.Type)
	for groupPkgName := range g.groupVersions {
		gvInterfaces[groupPkgName] = c.Universe.Type(types.Name{Package: path.Join(g.outputPackage, groupPkgName), Name: "Interface"})
		gvClusterInterfaces[groupPkgName] = c.Universe.Type(types.Name{Package: path.Join(g.outputPackage, groupPkgName), Name: "ClusterInterface"})
		gvNewFuncs[groupPkgName] = c.Universe.Function(types.Name{Package: path.Join(g.outputPackage, groupPkgName), Name: "New"})
		gvNewScopedFuncs[groupPkgName] = c.Universe.Function(types.Name{Package: path.Join(g.outputPackage, groupPkgName), Name: "NewScoped"})
	}

	genericInformerPkg := g.singleClusterInformersPackage
	generateScopedInformerFactory := false
	if genericInformerPkg == "" {
		genericInformerPkg = g.outputPackage
		generateScopedInformerFactory = true
	}

	m := map[string]interface{}{
		"cacheSharedIndexInformer":          c.Universe.Type(cacheSharedIndexInformer),
		"scopeableCacheSharedIndexInformer": c.Universe.Type(scopeableCacheSharedIndexInformer),
		"cacheTransformFunc":                c.Universe.Type(cacheTransformFunc),
		"groupVersions":                     g.groupVersions,
		"gvInterfaces":                      gvInterfaces,
		"gvClusterInterfaces":               gvClusterInterfaces,
		"gvNewFuncs":                        gvNewFuncs,
		"gvNewScopedFuncs":                  gvNewScopedFuncs,
		"gvGoNames":                         g.gvGoNames,
		"interfacesNewInformerFunc":         c.Universe.Type(types.Name{Package: g.internalInterfacesPackage, Name: "NewInformerFunc"}),
		"interfacesNewScopedInformerFunc":   c.Universe.Type(types.Name{Package: g.internalInterfacesPackage, Name: "NewScopedInformerFunc"}),
		"interfacesTweakListOptionsFunc":    c.Universe.Type(types.Name{Package: g.internalInterfacesPackage, Name: "TweakListOptionsFunc"}),
		"informerFactoryInterface":          c.Universe.Type(types.Name{Package: g.internalInterfacesPackage, Name: "SharedInformerFactory"}),
		"informerScopedFactoryInterface":    c.Universe.Type(types.Name{Package: g.internalInterfacesPackage, Name: "SharedScopedInformerFactory"}),
		"clientSetInterface":                c.Universe.Type(types.Name{Package: g.singleClusterVersionedClientSetPackage, Name: "Interface"}),
		"clusterClientSetInterface":         c.Universe.Type(types.Name{Package: g.clientSetPackage, Name: "ClusterInterface"}),
		"genericInformer":                   c.Universe.Type(types.Name{Package: genericInformerPkg, Name: "GenericInformer"}),
		"cacheWaitForCacheSync":             c.Universe.Function(cacheWaitForCacheSync),
		"reflectType":                       c.Universe.Type(reflectType),
		"reflectTypeOf":                     c.Universe.Type(reflectTypeOf),
		"runtimeObject":                     c.Universe.Type(runtimeObject),
		"schemaGroupVersionResource":        c.Universe.Type(schemaGroupVersionResource),
		"syncMutex":                         c.Universe.Type(syncMutex),
		"syncWaitGroup":                     c.Universe.Type(syncWaitGroup),
		"timeDuration":                      c.Universe.Type(timeDuration),
		"namespaceAll":                      c.Universe.Type(metav1NamespaceAll),
		"object":                            c.Universe.Type(metav1Object),
		"logicalclusterName":                c.Universe.Type(logicalclusterName),
	}

	sw.Do(sharedInformerFactoryStruct, m)
	sw.Do(sharedInformerFactoryInterface, m)

	if generateScopedInformerFactory {
		sw.Do(sharedScopedInformerFactoryStruct, m)
	}

	return sw.Error()
}

var sharedInformerFactoryStruct = `
// SharedInformerOption defines the functional option type for SharedInformerFactory.
type SharedInformerOption func(*SharedInformerOptions) *SharedInformerOptions

type SharedInformerOptions struct {
	customResync     map[{{.reflectType|raw}}]{{.timeDuration|raw}}
	tweakListOptions {{.interfacesTweakListOptionsFunc|raw}}
	transform        {{.cacheTransformFunc|raw}}
	namespace        string
}

type sharedInformerFactory struct {
	client           {{.clusterClientSetInterface|raw}}
	tweakListOptions {{.interfacesTweakListOptionsFunc|raw}}
	lock             {{.syncMutex|raw}}
	defaultResync    {{.timeDuration|raw}}
	customResync     map[{{.reflectType|raw}}]{{.timeDuration|raw}}
	transform        {{.cacheTransformFunc|raw}}

	informers map[{{.reflectType|raw}}]{{.scopeableCacheSharedIndexInformer|raw}}
	// startedInformers is used for tracking which informers have been started.
	// This allows Start() to be called multiple times safely.
	startedInformers map[{{.reflectType|raw}}]bool
	// wg tracks how many goroutines were started.
	wg {{.syncWaitGroup|raw}}
	// shuttingDown is true when Shutdown has been called. It may still be running
	// because it needs to wait for goroutines.
	shuttingDown bool
}

// WithCustomResyncConfig sets a custom resync period for the specified informer types.
func WithCustomResyncConfig(resyncConfig map[{{.object|raw}}]{{.timeDuration|raw}}) SharedInformerOption {
	return func(opts *SharedInformerOptions) *SharedInformerOptions {
		for k, v := range resyncConfig {
			opts.customResync[{{.reflectTypeOf|raw}}(k)] = v
		}
		return opts
	}
}

// WithTweakListOptions sets a custom filter on all listers of the configured SharedInformerFactory.
func WithTweakListOptions(tweakListOptions {{.interfacesTweakListOptionsFunc|raw}}) SharedInformerOption {
	return func(opts *SharedInformerOptions) *SharedInformerOptions {
		opts.tweakListOptions = tweakListOptions
		return opts
	}
}

// WithTransform sets a transform on all informers.
func WithTransform(transform {{.cacheTransformFunc|raw}}) SharedInformerOption {
	return func(opts *SharedInformerOptions) *SharedInformerOptions {
		opts.transform = transform
		return opts
	}
}

// NewSharedInformerFactory constructs a new instance of sharedInformerFactory for all namespaces.
func NewSharedInformerFactory(client {{.clusterClientSetInterface|raw}}, defaultResync {{.timeDuration|raw}}) SharedInformerFactory {
	return NewSharedInformerFactoryWithOptions(client, defaultResync)
}

// NewFilteredSharedInformerFactory constructs a new instance of sharedInformerFactory.
// Listers obtained via this SharedInformerFactory will be subject to the same filters
// as specified here.
// Deprecated: Please use NewSharedInformerFactoryWithOptions instead
func NewFilteredSharedInformerFactory(client {{.clusterClientSetInterface|raw}}, defaultResync {{.timeDuration|raw}}, tweakListOptions {{.interfacesTweakListOptionsFunc|raw}}) SharedInformerFactory {
	return NewSharedInformerFactoryWithOptions(client, defaultResync, WithTweakListOptions(tweakListOptions))
}

// NewSharedInformerFactoryWithOptions constructs a new instance of a SharedInformerFactory with additional options.
func NewSharedInformerFactoryWithOptions(client {{.clusterClientSetInterface|raw}}, defaultResync {{.timeDuration|raw}}, options ...SharedInformerOption) SharedInformerFactory {
	factory := &sharedInformerFactory{
		client:           client,
		defaultResync:    defaultResync,
		informers:        make(map[{{.reflectType|raw}}]{{.scopeableCacheSharedIndexInformer|raw}}),
		startedInformers: make(map[{{.reflectType|raw}}]bool),
		customResync:     make(map[{{.reflectType|raw}}]{{.timeDuration|raw}}),
	}

	opts := &SharedInformerOptions{
		customResync: make(map[{{.reflectType|raw}}]{{.timeDuration|raw}}),
	}

	// Apply all options
	for _, opt := range options {
		opts = opt(opts)
	}

	// Forward options to the factory
	factory.customResync = opts.customResync
	factory.tweakListOptions = opts.tweakListOptions
	factory.transform = opts.transform

	return factory
}

func (f *sharedInformerFactory) Start(stopCh <-chan struct{}) {
	f.lock.Lock()
	defer f.lock.Unlock()

	if f.shuttingDown {
		return
	}

	for informerType, informer := range f.informers {
		if !f.startedInformers[informerType] {
			f.wg.Add(1)
			// We need a new variable in each loop iteration,
			// otherwise the goroutine would use the loop variable
			// and that keeps changing.
			informer := informer
			go func() {
				defer f.wg.Done()
				informer.Run(stopCh)
			}()
			f.startedInformers[informerType] = true
		}
	}
}

func (f *sharedInformerFactory) Shutdown() {
	f.lock.Lock()
	f.shuttingDown = true
	f.lock.Unlock()

	// Will return immediately if there is nothing to wait for.
	f.wg.Wait()
}

func (f *sharedInformerFactory) WaitForCacheSync(stopCh <-chan struct{}) map[{{.reflectType|raw}}]bool {
	informers := func()map[{{.reflectType|raw}}]{{.scopeableCacheSharedIndexInformer|raw}}{
		f.lock.Lock()
		defer f.lock.Unlock()

		informers := map[{{.reflectType|raw}}]{{.scopeableCacheSharedIndexInformer|raw}}{}
		for informerType, informer := range f.informers {
			if f.startedInformers[informerType] {
				informers[informerType] = informer
			}
		}
		return informers
	}()

	res := map[{{.reflectType|raw}}]bool{}
	for informType, informer := range informers {
		res[informType] = {{.cacheWaitForCacheSync|raw}}(stopCh, informer.HasSynced)
	}

	return res
}

// InformerFor returns the ScopeableSharedIndexInformer for obj using an internal client.
func (f *sharedInformerFactory) InformerFor(obj {{.runtimeObject|raw}}, newFunc {{.interfacesNewInformerFunc|raw}}) {{.scopeableCacheSharedIndexInformer|raw}} {
	f.lock.Lock()
	defer f.lock.Unlock()

	informerType := {{.reflectTypeOf|raw}}(obj)
	informer, exists := f.informers[informerType]
	if exists {
		return informer
	}

	resyncPeriod, exists := f.customResync[informerType]
	if !exists {
		resyncPeriod = f.defaultResync
	}

	informer = newFunc(f.client, resyncPeriod)
	informer.SetTransform(f.transform)
	f.informers[informerType] = informer

	return informer
}
`

var sharedInformerFactoryInterface = `
type ScopedDynamicSharedInformerFactory interface {
	// ForResource gives generic access to a shared informer of the matching type.
	ForResource(resource {{.schemaGroupVersionResource|raw}}) ({{.genericInformer|raw}}, error)

	// Start initializes all requested informers. They are handled in goroutines
	// which run until the stop channel gets closed.
	Start(stopCh <-chan struct{})
}

// SharedInformerFactory provides shared informers for resources in all known
// API group versions.
//
// It is typically used like this:
//
//   ctx, cancel := context.Background()
//   defer cancel()
//   factory := NewSharedInformerFactory(client, resyncPeriod)
//   defer factory.WaitForStop()    // Returns immediately if nothing was started.
//   genericInformer := factory.ForResource(resource)
//   typedInformer := factory.SomeAPIGroup().V1().SomeType()
//   factory.Start(ctx.Done())      // Start processing these informers.
//   synced := factory.WaitForCacheSync(ctx.Done())
//   for v, ok := range synced {
//       if !ok {
//           fmt.Fprintf(os.Stderr, "caches failed to sync: %v", v)
//           return
//       }
//   }
//
//   // Creating informers can also be created after Start, but then
//   // Start must be called again:
//   anotherGenericInformer := factory.ForResource(resource)
//   factory.Start(ctx.Done())
type SharedInformerFactory interface {
	{{.informerFactoryInterface|raw}}

	Cluster({{.logicalclusterName|raw}}) ScopedDynamicSharedInformerFactory

	// Start initializes all requested informers. They are handled in goroutines
	// which run until the stop channel gets closed.
	// Warning: Start does not block. When run in a go-routine, it will race with a later WaitForCacheSync.
	Start(stopCh <-chan struct{})

	// Shutdown marks a factory as shutting down. At that point no new
	// informers can be started anymore and Start will return without
	// doing anything.
	//
	// In addition, Shutdown blocks until all goroutines have terminated. For that
	// to happen, the close channel(s) that they were started with must be closed,
	// either before Shutdown gets called or while it is waiting.
	//
	// Shutdown may be called multiple times, even concurrently. All such calls will
	// block until all goroutines have terminated.
	Shutdown()

	// WaitForCacheSync blocks until all started informers' caches were synced
	// or the stop channel gets closed.
	WaitForCacheSync(stopCh <-chan struct{}) map[{{.reflectType|raw}}]bool

	// ForResource gives generic access to a shared informer of the matching type.
	ForResource(resource {{.schemaGroupVersionResource|raw}}) (GenericClusterInformer, error)

	// InformerFor returns the SharedIndexInformer for obj using an internal
	// client.
	InformerFor(obj {{.runtimeObject|raw}}, newFunc {{.interfacesNewInformerFunc|raw}}) {{.scopeableCacheSharedIndexInformer|raw}}

	{{range $groupName, $group := .groupVersions}}{{index $.gvGoNames $groupName}}() {{index $.gvClusterInterfaces $groupName|raw}}
	{{end}}
}

{{range $groupPkgName, $group := .groupVersions}}
func (f *sharedInformerFactory) {{index $.gvGoNames $groupPkgName}}() {{index $.gvClusterInterfaces $groupPkgName|raw}} {
  return {{index $.gvNewFuncs $groupPkgName|raw}}(f, f.tweakListOptions)
}
{{end}}

func (f *sharedInformerFactory) Cluster(clusterName {{.logicalclusterName|raw}}) ScopedDynamicSharedInformerFactory {
	return &scopedDynamicSharedInformerFactory{
		sharedInformerFactory: f,
		clusterName:           clusterName,
	}
}

type scopedDynamicSharedInformerFactory struct {
	*sharedInformerFactory
	clusterName {{.logicalclusterName|raw}}
}

func (f *scopedDynamicSharedInformerFactory) ForResource(resource {{.schemaGroupVersionResource|raw}}) ({{.genericInformer|raw}}, error) {
	clusterInformer, err := f.sharedInformerFactory.ForResource(resource)
	if err != nil {
		return nil, err
	}
	return clusterInformer.Cluster(f.clusterName), nil
}

func (f *scopedDynamicSharedInformerFactory) Start(stopCh <-chan struct{}) {
	f.sharedInformerFactory.Start(stopCh)
}
`

var sharedScopedInformerFactoryStruct = `
// WithNamespace limits the SharedInformerFactory to the specified namespace.
func WithNamespace(namespace string) SharedInformerOption {
	return func(opts *SharedInformerOptions) *SharedInformerOptions {
		opts.namespace = namespace
		return opts
	}
}

type sharedScopedInformerFactory struct {
	client           {{.clientSetInterface|raw}}
	tweakListOptions {{.interfacesTweakListOptionsFunc|raw}}
	lock             {{.syncMutex|raw}}
	defaultResync    {{.timeDuration|raw}}
	customResync     map[{{.reflectType|raw}}]{{.timeDuration|raw}}
	transform        {{.cacheTransformFunc|raw}}
	namespace        string

	informers map[{{.reflectType|raw}}]{{.cacheSharedIndexInformer|raw}}
	// startedInformers is used for tracking which informers have been started.
	// This allows Start() to be called multiple times safely.
	startedInformers map[{{.reflectType|raw}}]bool
}

// NewSharedScopedInformerFactory constructs a new instance of SharedInformerFactory for some or all namespaces.
func NewSharedScopedInformerFactory(client {{.clientSetInterface|raw}}, defaultResync {{.timeDuration|raw}}, namespace string) SharedScopedInformerFactory {
	return NewSharedScopedInformerFactoryWithOptions(client, defaultResync, WithNamespace(namespace))
}

// NewSharedScopedInformerFactoryWithOptions constructs a new instance of a SharedInformerFactory with additional options.
func NewSharedScopedInformerFactoryWithOptions(client {{.clientSetInterface|raw}}, defaultResync {{.timeDuration|raw}}, options ...SharedInformerOption) SharedScopedInformerFactory {
	factory := &sharedScopedInformerFactory{
		client:           client,
		defaultResync:    defaultResync,
		informers:        make(map[{{.reflectType|raw}}]{{.cacheSharedIndexInformer|raw}}),
		startedInformers: make(map[{{.reflectType|raw}}]bool),
		customResync:     make(map[{{.reflectType|raw}}]{{.timeDuration|raw}}),
	}

	opts := &SharedInformerOptions{
		customResync: make(map[{{.reflectType|raw}}]{{.timeDuration|raw}}),
	}

	// Apply all options
	for _, opt := range options {
		opts = opt(opts)
	}

	// Forward options to the factory
	factory.customResync = opts.customResync
	factory.tweakListOptions = opts.tweakListOptions
	factory.namespace = opts.namespace

	return factory
}

// Start initializes all requested informers.
func (f *sharedScopedInformerFactory) Start(stopCh <-chan struct{}) {
	f.lock.Lock()
	defer f.lock.Unlock()

	for informerType, informer := range f.informers {
		if !f.startedInformers[informerType] {
			go informer.Run(stopCh)
			f.startedInformers[informerType] = true
		}
	}
}

// WaitForCacheSync waits for all started informers' cache were synced.
func (f *sharedScopedInformerFactory) WaitForCacheSync(stopCh <-chan struct{}) map[{{.reflectType|raw}}]bool {
	informers := func() map[{{.reflectType|raw}}]{{.cacheSharedIndexInformer|raw}} {
		f.lock.Lock()
		defer f.lock.Unlock()

		informers := map[{{.reflectType|raw}}]{{.cacheSharedIndexInformer|raw}}{}
		for informerType, informer := range f.informers {
			if f.startedInformers[informerType] {
				informers[informerType] = informer
			}
		}
		return informers
	}()

	res := map[{{.reflectType|raw}}]bool{}
	for informType, informer := range informers {
		res[informType] = {{.cacheWaitForCacheSync|raw}}(stopCh, informer.HasSynced)
	}
	return res
}

// InformerFor returns the SharedIndexInformer for obj.
func (f *sharedScopedInformerFactory) InformerFor(obj {{.runtimeObject|raw}}, newFunc {{.interfacesNewScopedInformerFunc|raw}}) {{.cacheSharedIndexInformer|raw}} {
	f.lock.Lock()
	defer f.lock.Unlock()

	informerType := {{.reflectTypeOf|raw}}(obj)
	informer, exists := f.informers[informerType]
	if exists {
		return informer
	}

	resyncPeriod, exists := f.customResync[informerType]
	if !exists {
		resyncPeriod = f.defaultResync
	}

	informer = newFunc(f.client, resyncPeriod)
	informer.SetTransform(f.transform)
	f.informers[informerType] = informer

	return informer
}

// SharedScopedInformerFactory provides shared informers for resources in all known
// API group versions, scoped to one workspace.
type SharedScopedInformerFactory interface {
	{{.informerScopedFactoryInterface|raw}}
	ForResource(resource {{.schemaGroupVersionResource|raw}}) (GenericInformer, error)
	WaitForCacheSync(stopCh <-chan struct{}) map[{{.reflectType|raw}}]bool

	{{range $groupName, $group := .groupVersions}}{{index $.gvGoNames $groupName}}() {{index $.gvInterfaces $groupName|raw}}
	{{end}}
}

{{range $groupPkgName, $group := .groupVersions}}
func (f *sharedScopedInformerFactory) {{index $.gvGoNames $groupPkgName}}() {{index $.gvInterfaces $groupPkgName|raw}} {
  return {{index $.gvNewScopedFuncs $groupPkgName|raw}}(f, f.namespace, f.tweakListOptions)
}
{{end}}
`
