package conflict

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metainternalversion "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"

	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/virtual/framework"
	virtualworkspacesdynamic "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apidefinition"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apiserver"
	dynamiccontext "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/context"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/forwardingregistry"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/rootapiserver"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha2"
	kcpinformers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions"
)

const ConflictVWName = "test-conflict-vw"

func digestURL(urlPath, rootPathPrefix string) (
	cluster genericapirequest.Cluster,
	domainKey dynamiccontext.APIDomainKey,
	logicalPath string,
	accepted bool,
) {
	if !strings.HasPrefix(urlPath, rootPathPrefix) {
		fmt.Printf("### CONFLICTSVW digestURL 0 prefix=%s\n", rootPathPrefix)
		return genericapirequest.Cluster{}, "", "", false
	}

	// Incoming requests to this virtual workspace will look like:
	//  /services/conflict/root:org:ws/<apiexport-name>/<Test name>/clusters/*/api/v1/configmaps
	//                     └────────────────────────┐
	// Where the withoutRootPathPrefix starts here: ┘
	withoutRootPathPrefix := strings.TrimPrefix(urlPath, rootPathPrefix)

	parts := strings.SplitN(withoutRootPathPrefix, "/", 4)
	fmt.Printf("### CONFLICTSVW digestURL 1 urlPath=%s,rootPathPrefix=%s,withoutRootPathPrefix=%s,parts=%v\n", urlPath, rootPathPrefix, withoutRootPathPrefix, parts)
	if len(parts) < 3 {
		fmt.Printf("### CONFLICTSVW digestURL 2\n")
		return genericapirequest.Cluster{}, "", "", false
	}

	apiExportClusterName, apiExportName, testName := parts[0], parts[1], parts[2]
	if apiExportClusterName == "" {
		fmt.Printf("### CONFLICTSVW digestURL 3 %v\n")
		return genericapirequest.Cluster{}, "", "", false
	}
	if apiExportName == "" {
		fmt.Printf("### CONFLICTSVW digestURL 4 %v\n")
		return genericapirequest.Cluster{}, "", "", false
	}
	if testName == "" {
		fmt.Printf("### CONFLICTSVW digestURL 5 %v\n")
		return genericapirequest.Cluster{}, "", "", false
	}

	realPath := "/"
	if len(parts) > 3 {
		fmt.Printf("### CONFLICTSVW digestURL 6 %v\n")
		realPath += parts[3]
	}

	//  /services/apiexport/root:org:ws/<apiexport-name>/clusters/*/api/v1/configmaps
	//                     ┌────────────────────────────┘
	// We are now here: ───┘
	// Now, we parse out the logical cluster.
	if !strings.HasPrefix(realPath, "/clusters/") {
		fmt.Printf("### CONFLICTSVW digestURL 7 realPath=%s\n", realPath)
		return genericapirequest.Cluster{}, "", "", false
	}

	withoutClustersPrefix := strings.TrimPrefix(realPath, "/clusters/")
	parts = strings.SplitN(withoutClustersPrefix, "/", 2)
	path := logicalcluster.NewPath(parts[0])
	realPath = "/"
	if len(parts) > 1 {
		realPath += parts[1]
	}

	cluster = genericapirequest.Cluster{}
	if path == logicalcluster.Wildcard {
		cluster.Wildcard = true
	} else {
		var ok bool
		cluster.Name, ok = path.Name()
		if !ok {
			fmt.Printf("### CONFLICTSVW digestURL 8 path=%s\n", path)
			return genericapirequest.Cluster{}, "", "", false
		}
	}

	key := fmt.Sprintf("%s/%s/%s", testName, apiExportClusterName, apiExportName)
	fmt.Printf("### CONFLICTSVW digestURL 9 %v\n")
	return cluster, dynamiccontext.APIDomainKey(key), strings.TrimSuffix(urlPath, realPath), true
}

func parseAPIDomainKey(key dynamiccontext.APIDomainKey) (testName, apiExportClusterName, apiExportName string) {
	parts := strings.Split(string(key), "/")
	if len(parts) != 3 {
		panic(fmt.Sprintf("wrong key %s", key))
	}
	return parts[0], parts[1], parts[2]
}

type anyAuthorizer struct{}

func (_ anyAuthorizer) Authorize(ctx context.Context, attr authorizer.Attributes) (authorizer.Decision, string, error) {
	return authorizer.DecisionAllow, "", nil
}

func BuildConflictVirtualWorkspace(
	cfg *rest.Config,
	rootPathPrefix string,
	localKcpInformers kcpinformers.SharedInformerFactory,
	globalKcpInformers kcpinformers.SharedInformerFactory,
) ([]rootapiserver.NamedVirtualWorkspace, error) {
	fmt.Printf("### CONFLICTSVW: BuildConflictVirtualWorkspace\n")

	if !strings.HasSuffix(rootPathPrefix, "/") {
		rootPathPrefix += "/"
	}

	readyCh := make(chan struct{})

	content := &virtualworkspacesdynamic.DynamicVirtualWorkspace{
		RootPathResolver: framework.RootPathResolverFunc(func(urlPath string, ctx context.Context) (accepted bool, prefixToStrip string, completedContext context.Context) {
			cluster, apiDomain, prefixToStrip, ok := digestURL(urlPath, rootPathPrefix)
			fmt.Printf("### CONFLICTSVW: RootPathResolver urlPath=%s\n", urlPath)
			if !ok {
				fmt.Printf("### CONFLICTSVW: RootPathResolver urlPath=%s failed\n", urlPath)
				return false, "", ctx
			}
			fmt.Printf("### CONFLICTSVW: RootPathResolver key=%s\n", apiDomain)

			completedContext = genericapirequest.WithCluster(ctx, cluster)
			completedContext = dynamiccontext.WithAPIDomainKey(completedContext, apiDomain)
			return true, prefixToStrip, completedContext
		}),

		Authorizer: anyAuthorizer{},

		ReadyChecker: framework.ReadyFunc(func() error {
			select {
			case <-readyCh:
				return nil
			default:
				return errors.New("test-conflict-vw controllers are not started")
			}
		}),

		BootstrapAPISetManagement: func(mainConfig genericapiserver.CompletedConfig) (apidefinition.APIDefinitionSetGetter, error) {

			globalInformers := map[string]cache.SharedIndexInformer{
				"cachedobjects":      globalKcpInformers.Cache().V1alpha1().CachedObjects().Informer(),
				"cachedresources":    globalKcpInformers.Cache().V1alpha1().CachedResources().Informer(),
				"apiexports":         globalKcpInformers.Apis().V1alpha2().APIExports().Informer(),
				"apiresourceschemas": globalKcpInformers.Apis().V1alpha1().APIResourceSchemas().Informer(),
			}

			localInformers := map[string]cache.SharedIndexInformer{
				"cachedresources":    localKcpInformers.Cache().V1alpha1().CachedResources().Informer(),
				"apiexports":         localKcpInformers.Apis().V1alpha2().APIExports().Informer(),
				"apibindings":        localKcpInformers.Apis().V1alpha2().APIBindings().Informer(),
				"apiresourceschemas": localKcpInformers.Apis().V1alpha1().APIResourceSchemas().Informer(),
			}

			// APIExport indexers.

			indexers.AddIfNotPresentOrDie(
				globalKcpInformers.Apis().V1alpha2().APIExports().Informer().GetIndexer(),
				cache.Indexers{
					indexers.ByLogicalClusterPathAndName: indexers.IndexByLogicalClusterPathAndName,
				},
			)
			indexers.AddIfNotPresentOrDie(
				localKcpInformers.Apis().V1alpha2().APIExports().Informer().GetIndexer(),
				cache.Indexers{
					indexers.ByLogicalClusterPathAndName: indexers.IndexByLogicalClusterPathAndName,
				},
			)
			indexers.AddIfNotPresentOrDie(
				globalKcpInformers.Apis().V1alpha2().APIExports().Informer().GetIndexer(),
				cache.Indexers{
					indexers.APIExportByVirtualResourceIdentitiesAndGRs: indexers.IndexAPIExportByVirtualResourceIdentitiesAndGRs,
				},
			)
			indexers.AddIfNotPresentOrDie(
				localKcpInformers.Apis().V1alpha2().APIExports().Informer().GetIndexer(),
				cache.Indexers{
					indexers.APIExportByVirtualResourceIdentitiesAndGRs: indexers.IndexAPIExportByVirtualResourceIdentitiesAndGRs,
				},
			)

			// APIBinding indexers.

			indexers.AddIfNotPresentOrDie(localKcpInformers.Apis().V1alpha2().APIBindings().Informer().GetIndexer(), cache.Indexers{
				indexers.APIBindingsByAPIExport: indexers.IndexAPIBindingByAPIExport,
			})

			if err := mainConfig.AddPostStartHook(ConflictVWName, func(hookContext genericapiserver.PostStartHookContext) error {
				defer close(readyCh)

				// Wait for caches to be synced.

				for name, informer := range globalInformers {
					if !cache.WaitForNamedCacheSync(name, hookContext.Done(), informer.HasSynced) {
						klog.Background().Error(nil, "global informer not synced")
						return nil
					}
				}

				for name, informer := range localInformers {
					if !cache.WaitForNamedCacheSync(name, hookContext.Done(), informer.HasSynced) {
						klog.Background().Error(nil, "local informer not synced")
						return nil
					}
				}

				return nil
			}); err != nil {
				return nil, err
			}

			return &singleResourceAPIDefinitionSetProvider{
				localKcpInformers:  localKcpInformers,
				globalKcpInformers: globalKcpInformers,

				/*
					getAPIBinding: func(cluster logicalcluster.Name, name string) (*apisv1alpha2.APIBinding, error) {
						return localKcpInformers.Apis().V1alpha2().APIBindings().Cluster(cluster).Lister().Get(name)
					},
				*/

				getAPIExportByPath: func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error) {
					return indexers.ByPathAndNameWithFallback[*apisv1alpha2.APIExport](
						apisv1alpha2.Resource("apiexports"),
						localKcpInformers.Apis().V1alpha2().APIExports().Informer().GetIndexer(),
						globalKcpInformers.Apis().V1alpha2().APIExports().Informer().GetIndexer(),
						path,
						name,
					)
				},

				config: mainConfig,
				storageProvider: func(ctx context.Context, schDesc *SchemaDescription) (apiserver.RestProviderFunc, error) {
					return forwardingregistry.ProvideReadOnlyRestStorage(
						ctx,
						func(ctx context.Context) (kcpdynamic.ClusterInterface, error) { return nil, nil }, // The client is never used.
						synthethicStorageWrapper(schDesc),
						nil,
					)
				},
			}, nil
		},
	}

	return []rootapiserver.NamedVirtualWorkspace{
		{Name: ConflictVWName, VirtualWorkspace: content},
	}, nil
}

type singleResourceAPIDefinitionSetProvider struct {
	config          genericapiserver.CompletedConfig
	storageProvider func(ctx context.Context, schDesc *SchemaDescription) (apiserver.RestProviderFunc, error)

	getAPIExportByPath func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error)

	localKcpInformers  kcpinformers.SharedInformerFactory
	globalKcpInformers kcpinformers.SharedInformerFactory
}

func (a *singleResourceAPIDefinitionSetProvider) GetAPIDefinitionSet(ctx context.Context, key dynamiccontext.APIDomainKey) (apis apidefinition.APIDefinitionSet, apisExist bool, err error) {
	fmt.Printf("### CONFLICTSVW: GetAPIDefinitionSet 1 key=%s failed\n", key)

	testName, apiExportCluster, apiExportName := parseAPIDomainKey(key)
	apiExport, err := a.getAPIExportByPath(logicalcluster.NewPath(apiExportCluster), apiExportName)
	if err != nil {
		fmt.Printf("### CONFLICTSVW: GetAPIDefinitionSet 2 key=%s failed\n", key)
		return nil, false, err
	}

	schDesc, err := getSchemaDescriptionFromAnnotation(testName, apiExport)
	if err != nil {
		fmt.Printf("### CONFLICTSVW: GetAPIDefinitionSet 3 key=%s failed\n", key)
		return nil, false, err
	}
	sch := &apisv1alpha1.APIResourceSchema{
		Spec: apisv1alpha1.APIResourceSchemaSpec{
			Group: schDesc.Group,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural:     schDesc.Plural,
				Singular:   schDesc.Singular,
				ShortNames: schDesc.ShortNames,
				Kind:       schDesc.Kind,
				ListKind:   schDesc.ListKind,
			},
			Scope: apiextensionsv1.ClusterScoped,
			Versions: []apisv1alpha1.APIResourceVersion{
				{
					Name:    schDesc.Version,
					Served:  true,
					Storage: true,
					Schema: runtime.RawExtension{
						Raw: []byte(`
{
	"description": "synthethic-test-object",
	"type": "object"
}
								`),
					},
				},
			},
		},
	}

	storageProvider, err := a.storageProvider(ctx, &schDesc)
	if err != nil {
		fmt.Printf("### CONFLICTSVW: GetAPIDefinitionSet 4 key=%s failed\n", key)
		return nil, false, err
	}

	apiDefinition, err := apiserver.CreateServingInfoFor(
		a.config,
		sch,
		schDesc.Version,
		storageProvider,
	)
	if err != nil {
		fmt.Printf("### CONFLICTSVW: GetAPIDefinitionSet 5 key=%s failed\n", key)
		return nil, false, fmt.Errorf("failed to create serving info: %w", err)
	}
	fmt.Printf("### CONFLICTSVW: GetAPIDefinitionSet 6 key=%s failed\n", key)

	return apidefinition.APIDefinitionSet{
		schDesc.GroupVersionResource(): apiDefinition,
	}, true, nil
}

func synthethicStorageWrapper(schDesc *SchemaDescription) forwardingregistry.StorageWrapper {
	return forwardingregistry.StorageWrapperFunc(func(groupResource schema.GroupResource, storage *forwardingregistry.StoreFuncs) {
		storage.GetterFunc = func(ctx context.Context, name string, options *metav1.GetOptions) (runtime.Object, error) {
			return &metav1.PartialObjectMetadata{
				ObjectMeta: metav1.ObjectMeta{
					Name: name,
				},
				TypeMeta: metav1.TypeMeta{
					APIVersion: schDesc.APIVersion(),
					Kind:       schDesc.Kind,
				},
			}, nil
		}

		storage.ListerFunc = func(ctx context.Context, options *metainternalversion.ListOptions) (runtime.Object, error) {
			return &metav1.PartialObjectMetadataList{
				TypeMeta: metav1.TypeMeta{
					APIVersion: schDesc.APIVersion(),
					Kind:       schDesc.ListKind,
				},
				Items: []metav1.PartialObjectMetadata{
					metav1.PartialObjectMetadata{
						ObjectMeta: metav1.ObjectMeta{
							Name: "synthethic-item-1",
						},
						TypeMeta: metav1.TypeMeta{
							APIVersion: schDesc.APIVersion(),
							Kind:       schDesc.Kind,
						},
					},
					metav1.PartialObjectMetadata{
						ObjectMeta: metav1.ObjectMeta{
							Name: "synthethic-item-2",
						},
						TypeMeta: metav1.TypeMeta{
							APIVersion: schDesc.APIVersion(),
							Kind:       schDesc.Kind,
						},
					},
					metav1.PartialObjectMetadata{
						ObjectMeta: metav1.ObjectMeta{
							Name: "synthethic-item-3",
						},
						TypeMeta: metav1.TypeMeta{
							APIVersion: schDesc.APIVersion(),
							Kind:       schDesc.Kind,
						},
					},
				},
			}, nil
		}

		storage.WatcherFunc = func(ctx context.Context, options *metainternalversion.ListOptions) (watch.Interface, error) {
			return newSynthethicWatch(ctx), nil
		}
	})
}

type synthethicWatch struct {
	lock       sync.Mutex
	doneChan   chan struct{}
	resultChan chan watch.Event
}

func newSynthethicWatch(ctx context.Context) *synthethicWatch {
	w := &synthethicWatch{
		doneChan:   make(chan struct{}),
		resultChan: make(chan watch.Event),
	}

	go func() {
		for {
			select {
			case <-w.doneChan:
				// Watch was stopped externally via Stop().
				return
			case <-ctx.Done():
				// Watch was stopped due to context. We also clean up with Stop().
				w.Stop()
				return
			}
		}
	}()

	return w
}

func (w *synthethicWatch) Stop() {
	w.lock.Lock()
	defer w.lock.Unlock()

	select {
	case <-w.doneChan:
	default:
		close(w.doneChan)
		close(w.resultChan)
	}
}

func (w *synthethicWatch) ResultChan() <-chan watch.Event {
	return w.resultChan
}
