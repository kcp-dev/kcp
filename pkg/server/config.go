/*
Copyright 2022 The KCP Authors.

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

package server

import (
	"context"
	"fmt"
	"net/http"
	_ "net/http/pprof"

	kcpclienthelper "github.com/kcp-dev/apimachinery/pkg/client"
	"github.com/kcp-dev/logicalcluster/v2"

	apiextensionsapiserver "k8s.io/apiextensions-apiserver/pkg/apiserver"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apiextensionsexternalversions "k8s.io/apiextensions-apiserver/pkg/client/informers/externalversions"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/endpoints/filters"
	genericapiserver "k8s.io/apiserver/pkg/server"
	serverstorage "k8s.io/apiserver/pkg/server/storage"
	"k8s.io/apiserver/pkg/util/webhook"
	"k8s.io/client-go/dynamic"
	coreexternalversions "k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/kubernetes/pkg/genericcontrolplane"
	"k8s.io/kubernetes/pkg/genericcontrolplane/aggregator"
	"k8s.io/kubernetes/pkg/genericcontrolplane/apis"

	kcpadmissioninitializers "github.com/kcp-dev/kcp/pkg/admission/initializers"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kcpexternalversions "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	"github.com/kcp-dev/kcp/pkg/embeddedetcd"
	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/informer"
	boostrap "github.com/kcp-dev/kcp/pkg/server/bootstrap"
	kcpserveroptions "github.com/kcp-dev/kcp/pkg/server/options"
	"github.com/kcp-dev/kcp/pkg/server/requestinfo"
)

type Config struct {
	Options *kcpserveroptions.CompletedOptions

	EmbeddedEtcd *embeddedetcd.Config

	GenericConfig  *genericapiserver.Config // the config embedded into MiniAggregator, the head of the delegation chain
	MiniAggregator *aggregator.MiniAggregatorConfig
	Apis           *apis.Config
	ApiExtensions  *apiextensionsapiserver.Config

	ExtraConfig
}

type ExtraConfig struct {
	// resolveIdenties is to be called on server start until it succeeds. It injects the kcp
	// resource identities into the rest.Config used by the client. Only after it succeeds,
	// the clients can wildcard-list/watch most kcp resources.
	resolveIdentities func(ctx context.Context) error
	identityConfig    *rest.Config

	// authentication
	kcpAdminToken, shardAdminToken string
	shardAdminTokenHash            []byte

	// clients
	DynamicClusterClient       dynamic.ClusterInterface
	KubeClusterClient          kubernetes.ClusterInterface
	ApiExtensionsClusterClient apiextensionsclient.ClusterInterface
	KcpClusterClient           kcpclient.ClusterInterface

	// misc
	preHandlerChainMux *handlerChainMuxes

	// informers
	KcpSharedInformerFactory              kcpexternalversions.SharedInformerFactory
	KubeSharedInformerFactory             coreexternalversions.SharedInformerFactory
	ApiExtensionsSharedInformerFactory    apiextensionsexternalversions.SharedInformerFactory
	DynamicDiscoverySharedInformerFactory *informer.DynamicDiscoverySharedInformerFactory

	// TODO(p0lyn0mial):  get rid of TemporaryRootShardKcpSharedInformerFactory, in the future
	//                    we should have multi-shard aware informers
	//
	// TemporaryRootShardKcpSharedInformerFactory bring data from the root shard
	TemporaryRootShardKcpSharedInformerFactory kcpexternalversions.SharedInformerFactory
}

type completedConfig struct {
	Options *kcpserveroptions.CompletedOptions

	GenericConfig  genericapiserver.CompletedConfig
	EmbeddedEtcd   embeddedetcd.CompletedConfig
	MiniAggregator aggregator.CompletedMiniAggregatorConfig
	Apis           apis.CompletedConfig
	ApiExtensions  apiextensionsapiserver.CompletedConfig

	ExtraConfig
}

type CompletedConfig struct {
	// Embed a private pointer that cannot be instantiated outside of this package.
	*completedConfig
}

// Complete fills in any fields not set that are required to have valid data. It's mutating the receiver.
func (c *Config) Complete() (CompletedConfig, error) {
	return CompletedConfig{&completedConfig{
		Options: c.Options,

		GenericConfig:  c.GenericConfig.Complete(c.KubeSharedInformerFactory),
		EmbeddedEtcd:   c.EmbeddedEtcd.Complete(),
		MiniAggregator: c.MiniAggregator.Complete(c.KubeSharedInformerFactory),
		Apis:           c.Apis.Complete(),
		ApiExtensions:  c.ApiExtensions.Complete(),

		ExtraConfig: c.ExtraConfig,
	}}, nil
}

func NewConfig(opts *kcpserveroptions.CompletedOptions) (*Config, error) {
	c := &Config{
		Options: opts,
	}

	if opts.Extra.ProfilerAddress != "" {
		// nolint:errcheck
		go http.ListenAndServe(opts.Extra.ProfilerAddress, nil)
	}

	if opts.EmbeddedEtcd.Enabled {
		var err error
		c.EmbeddedEtcd, err = embeddedetcd.NewConfig(opts.EmbeddedEtcd, opts.GenericControlPlane.Etcd.EnableWatchCache)
		if err != nil {
			return nil, err
		}
	}

	var err error
	var storageFactory *serverstorage.DefaultStorageFactory
	c.GenericConfig, storageFactory, err = genericcontrolplane.BuildGenericConfig(opts.GenericControlPlane)
	if err != nil {
		return nil, err
	}

	c.GenericConfig.RequestInfoResolver = requestinfo.NewFactory() // must be set here early to avoid a crash in the EnableMultiCluster roundtrip wrapper

	// Setup kcp * informers, but those will need the identities for the APIExports used to make the APIs available.
	// The identities are not known before we can get them from the APIExports via the loopback client, hence we postpone
	// this to getOrCreateKcpIdentities() in the kcp-start-informers post-start hook.
	// The informers here are not  used before the informers are actually started (i.e. no race).
	nonIdentityKcpClusterClient, err := kcpclient.NewClusterForConfig(c.GenericConfig.LoopbackClientConfig) // can only used for wildcard requests of apis.kcp.dev
	if err != nil {
		return nil, err
	}
	c.identityConfig, c.resolveIdentities = boostrap.NewConfigWithWildcardIdentities(c.GenericConfig.LoopbackClientConfig, boostrap.KcpRootGroupExportNames, boostrap.KcpRootGroupResourceExportNames, nonIdentityKcpClusterClient.Cluster(tenancyv1alpha1.RootCluster))
	c.KcpClusterClient, err = kcpclient.NewClusterForConfig(c.identityConfig) // this is now generic to be used for all kcp API groups
	if err != nil {
		return nil, err
	}
	c.KcpSharedInformerFactory = kcpexternalversions.NewSharedInformerFactoryWithOptions(
		c.KcpClusterClient.Cluster(logicalcluster.Wildcard),
		resyncPeriod,
		kcpexternalversions.WithExtraClusterScopedIndexers(indexers.ClusterScoped()),
		kcpexternalversions.WithExtraNamespaceScopedIndexers(indexers.NamespaceScoped()),
	)

	// create an empty non-functional factory so that code that uses it but doesn't need it, doesn't have to check against the nil value
	c.TemporaryRootShardKcpSharedInformerFactory = kcpexternalversions.NewSharedInformerFactory(nil, resyncPeriod)

	// Setup kube * informers
	c.KubeClusterClient, err = kubernetes.NewClusterForConfig(c.GenericConfig.LoopbackClientConfig)
	if err != nil {
		return nil, err
	}

	scopedClusterKubeClient, err := kubernetes.NewForConfig(kcpclienthelper.NewClusterConfig(c.GenericConfig.LoopbackClientConfig))
	if err != nil {
		return nil, err
	}

	c.KubeSharedInformerFactory = coreexternalversions.NewSharedInformerFactoryWithOptions(
		c.KubeClusterClient.Cluster(logicalcluster.Wildcard),
		resyncPeriod,
		coreexternalversions.WithExtraClusterScopedIndexers(indexers.ClusterScoped()),
		coreexternalversions.WithExtraNamespaceScopedIndexers(indexers.NamespaceScoped()),
	)

	// Setup apiextensions * informers
	c.ApiExtensionsClusterClient, err = apiextensionsclient.NewClusterForConfig(c.GenericConfig.LoopbackClientConfig)
	if err != nil {
		return nil, err
	}
	c.ApiExtensionsSharedInformerFactory = apiextensionsexternalversions.NewSharedInformerFactoryWithOptions(
		c.ApiExtensionsClusterClient.Cluster(logicalcluster.Wildcard),
		resyncPeriod,
		apiextensionsexternalversions.WithExtraClusterScopedIndexers(indexers.ClusterScoped()),
		apiextensionsexternalversions.WithExtraNamespaceScopedIndexers(indexers.NamespaceScoped()),
	)

	// Setup dynamic client
	c.DynamicClusterClient, err = dynamic.NewClusterForConfig(c.GenericConfig.LoopbackClientConfig)
	if err != nil {
		return nil, err
	}

	if err := opts.Authorization.ApplyTo(c.GenericConfig, c.KubeSharedInformerFactory, c.KcpSharedInformerFactory); err != nil {
		return nil, err
	}
	c.kcpAdminToken, c.shardAdminToken, c.shardAdminTokenHash, err = opts.AdminAuthentication.ApplyTo(c.GenericConfig)
	if err != nil {
		return nil, err
	}

	if err := opts.GenericControlPlane.Audit.ApplyTo(c.GenericConfig); err != nil {
		return nil, err
	}

	// preHandlerChainMux is called before the actual handler chain. Note that BuildHandlerChainFunc below
	// is called multiple times, but only one of the handler chain will actually be used. Hence, we wrap it
	// to give handlers below one mux.Handle func to call.
	c.preHandlerChainMux = &handlerChainMuxes{}
	c.GenericConfig.BuildHandlerChainFunc = func(apiHandler http.Handler, genericConfig *genericapiserver.Config) (secure http.Handler) {
		apiHandler = WithWildcardListWatchGuard(apiHandler)
		apiHandler = WithWildcardIdentity(apiHandler)

		apiHandler = genericapiserver.DefaultBuildHandlerChainFromAuthz(apiHandler, genericConfig)

		if opts.HomeWorkspaces.Enabled {
			apiHandler = WithHomeWorkspaces(
				apiHandler,
				genericConfig.Authorization.Authorizer,
				scopedClusterKubeClient,
				c.KcpClusterClient,
				c.KubeSharedInformerFactory,
				c.KcpSharedInformerFactory,
				c.GenericConfig.ExternalAddress,
				opts.HomeWorkspaces.CreationDelaySeconds,
				logicalcluster.New(opts.HomeWorkspaces.HomeRootPrefix),
				opts.HomeWorkspaces.BucketLevels,
				opts.HomeWorkspaces.BucketSize,
			)
		}

		apiHandler = genericapiserver.DefaultBuildHandlerChainBeforeAuthz(apiHandler, genericConfig)

		// this will be replaced in DefaultBuildHandlerChain. So at worst we get twice as many warning.
		// But this is not harmful as the kcp warnings are not many.
		apiHandler = filters.WithWarningRecorder(apiHandler)

		// add a mux before the chain, for other handlers with their own handler chain to hook in
		mux := http.NewServeMux()
		mux.Handle("/", apiHandler)
		*c.preHandlerChainMux = append(*c.preHandlerChainMux, mux)
		apiHandler = mux

		apiHandler = WithWorkspaceProjection(apiHandler)
		apiHandler = WithClusterAnnotation(apiHandler)
		apiHandler = WithAuditAnnotation(apiHandler) // Must run before any audit annotation is made
		apiHandler = WithClusterScope(apiHandler)
		apiHandler = WithInClusterServiceAccountRequestRewrite(apiHandler)
		apiHandler = WithAcceptHeader(apiHandler)
		apiHandler = WithUserAgent(apiHandler)

		return apiHandler
	}

	admissionPluginInitializers := []admission.PluginInitializer{
		kcpadmissioninitializers.NewKcpInformersInitializer(c.KcpSharedInformerFactory),
		kcpadmissioninitializers.NewKubeClusterClientInitializer(scopedClusterKubeClient),
		kcpadmissioninitializers.NewKcpClusterClientInitializer(c.KcpClusterClient),
		kcpadmissioninitializers.NewShardBaseURLInitializer(opts.Extra.ShardBaseURL),
		kcpadmissioninitializers.NewShardExternalURLInitializer(opts.Extra.ShardExternalURL),
		// The external address is provided as a function, as its value may be updated
		// with the default secure port, when the config is later completed.
		kcpadmissioninitializers.NewExternalAddressInitializer(func() string { return c.GenericConfig.ExternalAddress }),
	}

	c.Apis, err = genericcontrolplane.CreateKubeAPIServerConfig(c.GenericConfig, opts.GenericControlPlane, c.KubeSharedInformerFactory, admissionPluginInitializers, storageFactory)
	if err != nil {
		return nil, err
	}

	// If additional API servers are added, they should be gated.
	c.ApiExtensions, err = genericcontrolplane.CreateAPIExtensionsConfig(
		*c.Apis.GenericConfig,
		c.Apis.ExtraConfig.VersionedInformers,
		admissionPluginInitializers,
		opts.GenericControlPlane,

		// Wire in a ServiceResolver that always returns an error that ResolveEndpoint is not yet
		// supported. The effect is that CRD webhook conversions are not supported and will always get an
		// error.
		&unimplementedServiceResolver{},

		webhook.NewDefaultAuthenticationInfoResolverWrapper(
			nil,
			c.Apis.GenericConfig.EgressSelector,
			c.Apis.GenericConfig.LoopbackClientConfig,
			c.Apis.GenericConfig.TracerProvider,
		),
	)
	if err != nil {
		return nil, fmt.Errorf("configure api extensions: %w", err)
	}

	c.KcpSharedInformerFactory.Apis().V1alpha1().APIBindings().Informer().GetIndexer().AddIndexers(cache.Indexers{byWorkspace: indexByWorkspace})                                               // nolint: errcheck
	c.ApiExtensionsSharedInformerFactory.Apiextensions().V1().CustomResourceDefinitions().Informer().GetIndexer().AddIndexers(cache.Indexers{byWorkspace: indexByWorkspace})                    // nolint: errcheck
	c.ApiExtensionsSharedInformerFactory.Apiextensions().V1().CustomResourceDefinitions().Informer().GetIndexer().AddIndexers(cache.Indexers{byGroupResourceName: indexCRDByGroupResourceName}) // nolint: errcheck
	c.KcpSharedInformerFactory.Apis().V1alpha1().APIBindings().Informer().GetIndexer().AddIndexers(cache.Indexers{byWorkspace: indexByWorkspace})                                               // nolint: errcheck
	c.KcpSharedInformerFactory.Apis().V1alpha1().APIBindings().Informer().GetIndexer().AddIndexers(cache.Indexers{byIdentityGroupResource: indexAPIBindingByIdentityGroupResource})             // nolint: errcheck

	c.ApiExtensions.ExtraConfig.ClusterAwareCRDLister = &apiBindingAwareCRDLister{
		kcpClusterClient:  c.KcpClusterClient,
		crdLister:         c.ApiExtensionsSharedInformerFactory.Apiextensions().V1().CustomResourceDefinitions().Lister(),
		crdIndexer:        c.ApiExtensionsSharedInformerFactory.Apiextensions().V1().CustomResourceDefinitions().Informer().GetIndexer(),
		workspaceLister:   c.KcpSharedInformerFactory.Tenancy().V1alpha1().ClusterWorkspaces().Lister(),
		apiBindingLister:  c.KcpSharedInformerFactory.Apis().V1alpha1().APIBindings().Lister(),
		apiBindingIndexer: c.KcpSharedInformerFactory.Apis().V1alpha1().APIBindings().Informer().GetIndexer(),
		apiExportIndexer:  c.KcpSharedInformerFactory.Apis().V1alpha1().APIExports().Informer().GetIndexer(),
		getAPIResourceSchema: func(clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error) {
			key := clusters.ToClusterAwareKey(clusterName, name)
			return c.KcpSharedInformerFactory.Apis().V1alpha1().APIResourceSchemas().Lister().Get(key)
		},
	}
	c.ApiExtensions.ExtraConfig.TableConverterProvider = NewTableConverterProvider()

	c.MiniAggregator = &aggregator.MiniAggregatorConfig{
		GenericConfig: c.GenericConfig,
	}

	return c, nil
}
