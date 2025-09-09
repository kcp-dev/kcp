package conflict

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"

	"github.com/kcp-dev/kcp/pkg/authorization"
	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/informer"
	cachedresources "github.com/kcp-dev/kcp/pkg/reconciler/cache/cachedresources"
	cachedresourcesreplication "github.com/kcp-dev/kcp/pkg/reconciler/cache/cachedresources/replication"
	"github.com/kcp-dev/kcp/pkg/virtual/framework"
	virtualworkspacesdynamic "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apidefinition"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/apiserver"
	dynamiccontext "github.com/kcp-dev/kcp/pkg/virtual/framework/dynamic/context"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/forwardingregistry"
	"github.com/kcp-dev/kcp/pkg/virtual/framework/rootapiserver"
	"github.com/kcp-dev/kcp/pkg/virtual/replication"
	"github.com/kcp-dev/kcp/pkg/virtual/replication/apidomainkey"
	replicationauthorizer "github.com/kcp-dev/kcp/pkg/virtual/replication/authorizer"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha2"
	cachev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/cache/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/util/conditions"
	kcpinformers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions"
	apisv1alpha2informers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/apis/v1alpha2"
	apisv1alpha1listers "github.com/kcp-dev/kcp/sdk/client/listers/apis/v1alpha1"
	cachev1alpha1listers "github.com/kcp-dev/kcp/sdk/client/listers/cache/v1alpha1"
)

const conflictVWName = "test-conflict-vw"

func digestUrl(urlPath, rootPathPrefix string) (
	cluster genericapirequest.Cluster,
	domainKey dynamiccontext.APIDomainKey,
	logicalPath string,
	accepted bool,
) {
	if !strings.HasPrefix(urlPath, rootPathPrefix) {
		return genericapirequest.Cluster{}, "", "", false
	}

	// Incoming requests to this virtual workspace will look like:
	//  /services/conflict/root:org:ws/<apiexport-name>/clusters/*/api/v1/configmaps
	//                     └────────────────────────┐
	// Where the withoutRootPathPrefix starts here: ┘
	withoutRootPathPrefix := strings.TrimPrefix(urlPath, rootPathPrefix)

	parts := strings.SplitN(withoutRootPathPrefix, "/", 3)
	if len(parts) < 3 {
		return genericapirequest.Cluster{}, "", "", false
	}

	apiExportClusterName, apiExportName := parts[0], parts[1]
	if apiExportClusterName == "" {
		return genericapirequest.Cluster{}, "", "", false
	}
	if apiExportName == "" {
		return genericapirequest.Cluster{}, "", "", false
	}

	realPath := "/"
	if len(parts) > 2 {
		realPath += parts[2]
	}

	//  /services/apiexport/root:org:ws/<apiexport-name>/clusters/*/api/v1/configmaps
	//                     ┌────────────────────────────┘
	// We are now here: ───┘
	// Now, we parse out the logical cluster.
	if !strings.HasPrefix(realPath, "/clusters/") {
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
			return genericapirequest.Cluster{}, "", "", false
		}
	}

	key := fmt.Sprintf("%s/%s", apiExportClusterName, apiExportName)
	return cluster, dynamiccontext.APIDomainKey(key), strings.TrimSuffix(urlPath, realPath), true
}

type schemaNamesSetter struct {
	lock             sync.RWMutex
	singular, plural string
	kind, kindList   string
	shortNames       []string
}

func (s *schemaNamesSetter) set(
	singular, plural string,
	kind, kindList string,
	shortNames []string,
) {
	s.lock.Lock()
	defer s.lock.Unlock()

	s.singular = singular
	s.plural = plural
	s.kind = kind
	s.kindList = kindList
	shortNames = shortNames
}

func buildConflictVirtualWorkspace(
	cfg *rest.Config,
	rootPathPrefix string,
	localKcpInformers kcpinformers.SharedInformerFactory,
	globalKcpInformers kcpinformers.SharedInformerFactory,
	nameSetter *schemaNamesSetter,
) ([]rootapiserver.NamedVirtualWorkspace, error) {
	if !strings.HasSuffix(rootPathPrefix, "/") {
		rootPathPrefix += "/"
	}

	readyCh := make(chan struct{})

	content := &virtualworkspacesdynamic.DynamicVirtualWorkspace{
		RootPathResolver: framework.RootPathResolverFunc(func(urlPath string, ctx context.Context) (accepted bool, prefixToStrip string, completedContext context.Context) {
			cluster, apiDomain, prefixToStrip, ok := digestUrl(urlPath, rootPathPrefix)
			if !ok {
				return false, "", ctx
			}

			completedContext = genericapirequest.WithCluster(ctx, cluster)
			completedContext = dynamiccontext.WithAPIDomainKey(completedContext, apiDomain)
			return true, prefixToStrip, completedContext
		}),

		ReadyChecker: framework.ReadyFunc(func() error {
			select {
			case <-readyCh:
				return nil
			default:
				return errors.New("test-conflict-vw controllers are not started")
			}
		}),

		BootstrapAPISetManagement: func(mainConfig genericapiserver.CompletedConfig) (apidefinition.APIDefinitionSetGetter, error) {
			return &singleResourceAPIDefinitionSetProvider{
				localKcpInformers:  localKcpInformers,
				globalKcpInformers: globalKcpInformers,

				getLogicalCluster: func(cluster logicalcluster.Name, name string) (*corev1alpha1.LogicalCluster, error) {
					return localKcpInformers.Core().V1alpha1().LogicalClusters().Cluster(cluster).Lister().Get(name)
				},

				getAPIBinding: func(cluster logicalcluster.Name, name string) (*apisv1alpha2.APIBinding, error) {
					return localKcpInformers.Apis().V1alpha2().APIBindings().Cluster(cluster).Lister().Get(name)
				},

				getAPIExportByPath: func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error) {
					return indexers.ByPathAndNameWithFallback[*apisv1alpha2.APIExport](
						apisv1alpha2.Resource("apiexports"),
						localKcpInformers.Apis().V1alpha2().APIExports().Informer().GetIndexer(),
						globalKcpInformers.Apis().V1alpha2().APIExports().Informer().GetIndexer(),
						path,
						name,
					)
				},

				getAPIResourceSchema: informer.NewScopedGetterWithFallback[*apisv1alpha1.APIResourceSchema, apisv1alpha1listers.APIResourceSchemaLister](localKcpInformers.Apis().V1alpha1().APIResourceSchemas().Lister(), globalKcpInformers.Apis().V1alpha1().APIResourceSchemas().Lister()),

				getCachedResource: informer.NewScopedGetterWithFallback[*cachev1alpha1.CachedResource, cachev1alpha1listers.CachedResourceLister](localKcpInformers.Cache().V1alpha1().CachedResources().Lister(), globalKcpInformers.Cache().V1alpha1().CachedResources().Lister()),

				config:               mainConfig,
				dynamicClusterClient: dynamicClusterClient,
				storageProvider: func(ctx context.Context, dynamicClusterClientFunc forwardingregistry.DynamicClusterClientFunc, apiResourceSchema *apisv1alpha1.APIResourceSchema, version string, cr *cachev1alpha1.CachedResource) (apiserver.RestProviderFunc, error) {
					return forwardingregistry.ProvideReadOnlyRestStorage(
						ctx,
						dynamicClusterClientFunc,
						withUnwrapping(apiResourceSchema, version, localKcpInformers, globalKcpInformers, cr),
						nil,
					)
				},
			}, nil
		},
	}

	return []rootapiserver.NamedVirtualWorkspace{
		{Name: conflictVWName, VirtualWorkspace: content},
	}, nil

}

type singleResourceAPIDefinitionSetProvider struct {
	storageProvider func(ctx context.Context, dynamicClusterClientFunc forwardingregistry.DynamicClusterClientFunc, apiResourceSchema *apisv1alpha1.APIResourceSchema, version string, cr *cachev1alpha1.CachedResource) (apiserver.RestProviderFunc, error)

	localKcpInformers  kcpinformers.SharedInformerFactory
	globalKcpInformers kcpinformers.SharedInformerFactory
}

func (a *singleResourceAPIDefinitionSetProvider) GetAPIDefinitionSet(ctx context.Context, key dynamiccontext.APIDomainKey) (apis apidefinition.APIDefinitionSet, apisExist bool, err error) {
	parsedKey, err := apidomainkey.Parse(key)
	if err != nil {
		return nil, false, err
	}

	storage, err := forwardingregistry.ProvideReadOnlyRestStorage(
		ctx,
		dynamicClusterClientFunc,
		withUnwrapping(apiResourceSchema, version, localKcpInformers, globalKcpInformers, cr),
		nil,
	)

	apiDefinition, err := apiserver.CreateServingInfoFor(
		a.config,
		wrappedSch,
		wrappedGVR.Version,
		storage,
	)
	if err != nil {
		return nil, false, fmt.Errorf("failed to create serving info: %w", err)
	}

	return apidefinition.APIDefinitionSet{
		wrappedGVR: apiDefinition,
	}, true, nil
}
