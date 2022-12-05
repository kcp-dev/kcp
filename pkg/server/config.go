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

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesinformers "github.com/kcp-dev/client-go/informers"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"

	apiextensionsapiserver "k8s.io/apiextensions-apiserver/pkg/apiserver"
	kcpapiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/kcp/clientset/versioned"
	kcpapiextensionsinformers "k8s.io/apiextensions-apiserver/pkg/client/kcp/informers/externalversions"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/endpoints/filters"
	"k8s.io/apiserver/pkg/informerfactoryhack"
	"k8s.io/apiserver/pkg/quota/v1/generic"
	genericapiserver "k8s.io/apiserver/pkg/server"
	serverstorage "k8s.io/apiserver/pkg/server/storage"
	"k8s.io/apiserver/pkg/util/webhook"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/kubernetes/pkg/genericcontrolplane"
	"k8s.io/kubernetes/pkg/genericcontrolplane/aggregator"
	"k8s.io/kubernetes/pkg/genericcontrolplane/apis"
	quotainstall "k8s.io/kubernetes/pkg/quota/v1/install"

	kcpadmissioninitializers "github.com/kcp-dev/kcp/pkg/admission/initializers"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/authorization"
	bootstrappolicy "github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
	cacheclient "github.com/kcp-dev/kcp/pkg/cache/client"
	"github.com/kcp-dev/kcp/pkg/cache/client/shard"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	kcpinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	"github.com/kcp-dev/kcp/pkg/embeddedetcd"
	kcpfeatures "github.com/kcp-dev/kcp/pkg/features"
	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/informer"
	"github.com/kcp-dev/kcp/pkg/server/bootstrap"
	kcpfilters "github.com/kcp-dev/kcp/pkg/server/filters"
	kcpserveroptions "github.com/kcp-dev/kcp/pkg/server/options"
	"github.com/kcp-dev/kcp/pkg/server/options/batteries"
	"github.com/kcp-dev/kcp/pkg/tunneler"
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
	kcpAdminToken, shardAdminToken, userToken string
	shardAdminTokenHash                       []byte

	// clients
	DynamicClusterClient                kcpdynamic.ClusterInterface
	KubeClusterClient                   kcpkubernetesclientset.ClusterInterface
	DeepSARClient                       kcpkubernetesclientset.ClusterInterface
	ApiExtensionsClusterClient          kcpapiextensionsclientset.ClusterInterface
	KcpClusterClient                    kcpclientset.ClusterInterface
	RootShardKcpClusterClient           kcpclientset.ClusterInterface
	BootstrapDynamicClusterClient       kcpdynamic.ClusterInterface
	BootstrapApiExtensionsClusterClient kcpapiextensionsclientset.ClusterInterface

	CacheDynamicClient kcpdynamic.ClusterInterface

	// config from which client can be configured
	LogicalClusterAdminConfig *rest.Config

	// misc
	preHandlerChainMux   *handlerChainMuxes
	quotaAdmissionStopCh chan struct{}

	// URL getters depending on genericspiserver.ExternalAddress which is initialized on server run
	ShardBaseURL             func() string
	ShardExternalURL         func() string
	ShardVirtualWorkspaceURL func() string

	// informers
	KcpSharedInformerFactory              kcpinformers.SharedInformerFactory
	KubeSharedInformerFactory             kcpkubernetesinformers.SharedInformerFactory
	ApiExtensionsSharedInformerFactory    kcpapiextensionsinformers.SharedInformerFactory
	DynamicDiscoverySharedInformerFactory *informer.DynamicDiscoverySharedInformerFactory
	CacheKcpSharedInformerFactory         kcpinformers.SharedInformerFactory
	// TODO(p0lyn0mial):  get rid of TemporaryRootShardKcpSharedInformerFactory, in the future
	//                    we should have multi-shard aware informers
	//
	// TODO(p0lyn0mial): wire it to the root shard, this will be needed to get bindings,
	//                   eventually it will be replaced by replication
	//
	// TemporaryRootShardKcpSharedInformerFactory bring data from the root shard
	TemporaryRootShardKcpSharedInformerFactory kcpinformers.SharedInformerFactory
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

		GenericConfig:  c.GenericConfig.Complete(informerfactoryhack.Wrap(c.KubeSharedInformerFactory)),
		EmbeddedEtcd:   c.EmbeddedEtcd.Complete(),
		MiniAggregator: c.MiniAggregator.Complete(),
		Apis:           c.Apis.Complete(),
		ApiExtensions:  c.ApiExtensions.Complete(),

		ExtraConfig: c.ExtraConfig,
	}}, nil
}

const kcpBootstrapperUserName = "system:kcp:bootstrapper"

func NewConfig(opts *kcpserveroptions.CompletedOptions) (*Config, error) {
	c := &Config{
		Options: opts,
	}

	if opts.Extra.ProfilerAddress != "" {
		//nolint:errcheck
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
	c.GenericConfig, storageFactory, c.KubeSharedInformerFactory, c.KubeClusterClient, err = genericcontrolplane.BuildGenericConfig(opts.GenericControlPlane)
	if err != nil {
		return nil, err
	}

	if c.Options.Cache.Enabled {
		var cacheClientConfig *rest.Config
		if len(c.Options.Cache.KubeconfigFile) > 0 {
			cacheClientConfig, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(&clientcmd.ClientConfigLoadingRules{ExplicitPath: c.Options.Cache.KubeconfigFile}, nil).ClientConfig()
			if err != nil {
				return nil, fmt.Errorf("failed to load the kubeconfig from: %s, for a cache client, err: %w", c.Options.Cache.KubeconfigFile, err)
			}
		} else {
			cacheClientConfig = rest.CopyConfig(c.GenericConfig.LoopbackClientConfig)
		}
		rt := cacheclient.WithCacheServiceRoundTripper(cacheClientConfig)
		rt = cacheclient.WithShardNameFromContextRoundTripper(rt)
		rt = cacheclient.WithDefaultShardRoundTripper(rt, shard.Wildcard)

		cacheKcpClusterClient, err := kcpclientset.NewForConfig(rt)
		if err != nil {
			return nil, err
		}
		c.CacheKcpSharedInformerFactory = kcpinformers.NewSharedInformerFactoryWithOptions(
			cacheKcpClusterClient,
			resyncPeriod,
		)
		c.CacheDynamicClient, err = kcpdynamic.NewForConfig(rt)
		if err != nil {
			return nil, err
		}
	}

	// Setup kcp * informers, but those will need the identities for the APIExports used to make the APIs available.
	// The identities are not known before we can get them from the APIExports via the loopback client or from the root shard in case this is a non-root shard,
	// hence we postpone this to getOrCreateKcpIdentities() in the kcp-start-informers post-start hook.
	// The informers here are not used before the informers are actually started (i.e. no race).
	if len(c.Options.Extra.RootShardKubeconfigFile) > 0 {
		// TODO(p0lyn0mial): use kcp-admin instead of system:admin
		nonIdentityRootKcpShardSystemAdminConfig, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(&clientcmd.ClientConfigLoadingRules{ExplicitPath: c.Options.Extra.RootShardKubeconfigFile}, &clientcmd.ConfigOverrides{CurrentContext: "system:admin"}).ClientConfig()
		if err != nil {
			return nil, fmt.Errorf("failed to load the kubeconfig from: %s, for the root shard, err: %w", c.Options.Extra.RootShardKubeconfigFile, err)
		}

		var kcpShardIdentityRoundTripper func(rt http.RoundTripper) http.RoundTripper
		kcpShardIdentityRoundTripper, c.resolveIdentities = bootstrap.NewWildcardIdentitiesWrappingRoundTripper(bootstrap.KcpRootGroupExportNames, bootstrap.KcpRootGroupResourceExportNames, nonIdentityRootKcpShardSystemAdminConfig, c.KubeClusterClient)
		rootKcpShardIdentityConfig := rest.CopyConfig(nonIdentityRootKcpShardSystemAdminConfig)
		rootKcpShardIdentityConfig.Wrap(kcpShardIdentityRoundTripper)
		c.RootShardKcpClusterClient, err = kcpclientset.NewForConfig(rootKcpShardIdentityConfig)
		if err != nil {
			return nil, err
		}
		c.TemporaryRootShardKcpSharedInformerFactory = kcpinformers.NewSharedInformerFactoryWithOptions(
			c.RootShardKcpClusterClient,
			resyncPeriod,
		)

		c.identityConfig = rest.CopyConfig(c.GenericConfig.LoopbackClientConfig)
		c.identityConfig.Wrap(kcpShardIdentityRoundTripper)
		c.KcpClusterClient, err = kcpclientset.NewForConfig(c.identityConfig) // this is now generic to be used for all kcp API groups
		if err != nil {
			return nil, err
		}
	} else {
		// create an empty non-functional factory so that code that uses it but doesn't need it, doesn't have to check against the nil value
		c.TemporaryRootShardKcpSharedInformerFactory = kcpinformers.NewSharedInformerFactory(nil, resyncPeriod)

		// The informers here are not used before the informers are actually started (i.e. no race).

		c.identityConfig, c.resolveIdentities = bootstrap.NewConfigWithWildcardIdentities(c.GenericConfig.LoopbackClientConfig, bootstrap.KcpRootGroupExportNames, bootstrap.KcpRootGroupResourceExportNames, nil)
		c.KcpClusterClient, err = kcpclientset.NewForConfig(c.identityConfig) // this is now generic to be used for all kcp API groups
		if err != nil {
			return nil, err
		}
		c.RootShardKcpClusterClient = c.KcpClusterClient
	}

	informerConfig := rest.CopyConfig(c.identityConfig)
	informerConfig.UserAgent = "kcp-informers"
	informerKcpClient, err := kcpclientset.NewForConfig(informerConfig)
	if err != nil {
		return nil, err
	}
	c.KcpSharedInformerFactory = kcpinformers.NewSharedInformerFactoryWithOptions(
		informerKcpClient,
		resyncPeriod,
	)
	c.DeepSARClient, err = kcpkubernetesclientset.NewForConfig(authorization.WithDeepSARConfig(rest.CopyConfig(c.GenericConfig.LoopbackClientConfig)))
	if err != nil {
		return nil, err
	}

	c.LogicalClusterAdminConfig = rest.CopyConfig(c.GenericConfig.LoopbackClientConfig)
	if len(c.Options.Extra.LogicalClusterAdminKubeconfig) > 0 {
		c.LogicalClusterAdminConfig, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(&clientcmd.ClientConfigLoadingRules{ExplicitPath: c.Options.Extra.LogicalClusterAdminKubeconfig}, nil).ClientConfig()
		if err != nil {
			return nil, fmt.Errorf("failed to load the kubeconfig from: %s, for a logical cluster client, err: %w", c.Options.Extra.LogicalClusterAdminKubeconfig, err)
		}
	}

	// Setup apiextensions * informers
	c.ApiExtensionsClusterClient, err = kcpapiextensionsclientset.NewForConfig(c.GenericConfig.LoopbackClientConfig)
	if err != nil {
		return nil, err
	}
	c.ApiExtensionsSharedInformerFactory = kcpapiextensionsinformers.NewSharedInformerFactoryWithOptions(
		c.ApiExtensionsClusterClient,
		resyncPeriod,
	)

	// Setup dynamic client
	c.DynamicClusterClient, err = kcpdynamic.NewForConfig(c.GenericConfig.LoopbackClientConfig)
	if err != nil {
		return nil, err
	}

	if err := opts.Authorization.ApplyTo(c.GenericConfig, c.KubeSharedInformerFactory, c.KcpSharedInformerFactory); err != nil {
		return nil, err
	}
	var userToken string
	c.kcpAdminToken, c.shardAdminToken, userToken, c.shardAdminTokenHash, err = opts.AdminAuthentication.ApplyTo(c.GenericConfig)
	if err != nil {
		return nil, err
	}
	if sets.NewString(opts.Extra.BatteriesIncluded...).Has(batteries.User) {
		c.userToken = userToken
	}

	bootstrapConfig := rest.CopyConfig(c.GenericConfig.LoopbackClientConfig)
	bootstrapConfig.Impersonate.UserName = kcpBootstrapperUserName
	bootstrapConfig.Impersonate.Groups = []string{bootstrappolicy.SystemKcpWorkspaceBootstrapper}
	bootstrapConfig = rest.AddUserAgent(bootstrapConfig, "kcp-bootstrapper")

	c.BootstrapApiExtensionsClusterClient, err = kcpapiextensionsclientset.NewForConfig(bootstrapConfig)
	if err != nil {
		return nil, err
	}

	c.BootstrapDynamicClusterClient, err = kcpdynamic.NewForConfig(bootstrapConfig)
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
		apiHandler = WithRequestIdentity(apiHandler)
		apiHandler = authorization.WithDeepSubjectAccessReview(apiHandler)

		apiHandler = genericapiserver.DefaultBuildHandlerChainFromAuthz(apiHandler, genericConfig)

		if opts.HomeWorkspaces.Enabled {
			apiHandler, err = WithHomeWorkspaces(
				apiHandler,
				genericConfig.Authorization.Authorizer,
				c.KubeClusterClient,
				c.KcpClusterClient,
				c.KubeSharedInformerFactory,
				c.KcpSharedInformerFactory,
				c.GenericConfig.ExternalAddress,
				opts.HomeWorkspaces.CreationDelaySeconds,
				logicalcluster.New(opts.HomeWorkspaces.HomeRootPrefix),
				opts.HomeWorkspaces.BucketLevels,
				opts.HomeWorkspaces.BucketSize,
			)
			if err != nil {
				panic(err) // shouldn't happen due to flag validation
			}
		}

		apiHandler = genericapiserver.DefaultBuildHandlerChainBeforeAuthz(apiHandler, genericConfig)

		// this will be replaced in DefaultBuildHandlerChain. So at worst we get twice as many warning.
		// But this is not harmful as the kcp warnings are not many.
		apiHandler = filters.WithWarningRecorder(apiHandler)

		// Add a mux before the chain, for other handlers with their own handler chain to hook in. For example, when
		// the virtual workspace server is running as part of kcp, it registers /services with the mux so it can handle
		// that path itself, instead of the rest of the handler chain above handling it.
		mux := http.NewServeMux()
		mux.Handle("/", apiHandler)
		*c.preHandlerChainMux = append(*c.preHandlerChainMux, mux)
		apiHandler = mux

		if kcpfeatures.DefaultFeatureGate.Enabled(kcpfeatures.SyncerTunnel) {
			apiHandler = tunneler.WithSyncerTunnel(apiHandler)
		}

		apiHandler = WithClusterWorkspaceProjection(apiHandler)
		apiHandler = kcpfilters.WithAuditEventClusterAnnotation(apiHandler)
		apiHandler = WithAuditAnnotation(apiHandler) // Must run before any audit annotation is made
		apiHandler = WithLocalProxy(apiHandler, opts.Extra.ShardName, opts.Extra.ShardBaseURL, c.KcpSharedInformerFactory.Tenancy().V1beta1().Workspaces(), c.KcpSharedInformerFactory.Tenancy().V1alpha1().ThisWorkspaces())
		apiHandler = kcpfilters.WithClusterScope(apiHandler)
		apiHandler = WithInClusterServiceAccountRequestRewrite(apiHandler)
		apiHandler = kcpfilters.WithAcceptHeader(apiHandler)
		apiHandler = WithUserAgent(apiHandler)

		return apiHandler
	}

	// TODO(ncdc): find a way to support the default configuration. For now, don't use it, because it is difficult
	// to get support for the special evaluators for pods/services/pvcs.
	// quotaConfiguration := quotainstall.NewQuotaConfigurationForAdmission()
	quotaConfiguration := generic.NewConfiguration(nil, quotainstall.DefaultIgnoredResources())

	c.ExtraConfig.quotaAdmissionStopCh = make(chan struct{})

	admissionPluginInitializers := []admission.PluginInitializer{
		kcpadmissioninitializers.NewKcpInformersInitializer(c.KcpSharedInformerFactory),
		kcpadmissioninitializers.NewKubeClusterClientInitializer(c.KubeClusterClient),
		kcpadmissioninitializers.NewKcpClusterClientInitializer(c.KcpClusterClient),
		kcpadmissioninitializers.NewDeepSARClientInitializer(c.DeepSARClient),
		// The external address is provided as a function, as its value may be updated
		// with the default secure port, when the config is later completed.
		kcpadmissioninitializers.NewKubeQuotaConfigurationInitializer(quotaConfiguration),
		kcpadmissioninitializers.NewServerShutdownInitializer(c.quotaAdmissionStopCh),
	}

	c.ShardBaseURL = func() string {
		if opts.Extra.ShardBaseURL != "" {
			return opts.Extra.ShardBaseURL
		}
		return "https://" + c.GenericConfig.ExternalAddress
	}
	c.ShardExternalURL = func() string {
		if opts.Extra.ShardExternalURL != "" {
			return opts.Extra.ShardExternalURL
		}
		return "https://" + c.GenericConfig.ExternalAddress
	}
	c.ShardVirtualWorkspaceURL = func() string {
		if opts.Extra.ShardVirtualWorkspaceURL != "" {
			return opts.Extra.ShardVirtualWorkspaceURL
		}
		return "https://" + c.GenericConfig.ExternalAddress
	}

	c.Apis, err = genericcontrolplane.CreateKubeAPIServerConfig(c.GenericConfig, opts.GenericControlPlane, c.KubeSharedInformerFactory, admissionPluginInitializers, storageFactory)
	if err != nil {
		return nil, err
	}

	// If additional API servers are added, they should be gated.
	c.ApiExtensions, err = genericcontrolplane.CreateAPIExtensionsConfig(
		*c.Apis.GenericConfig,
		informerfactoryhack.Wrap(c.Apis.ExtraConfig.VersionedInformers),
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

	c.ApiExtensionsSharedInformerFactory.Apiextensions().V1().CustomResourceDefinitions().Informer().GetIndexer().AddIndexers(cache.Indexers{byGroupResourceName: indexCRDByGroupResourceName})       //nolint:errcheck
	c.KcpSharedInformerFactory.Apis().V1alpha1().APIBindings().Informer().GetIndexer().AddIndexers(cache.Indexers{byIdentityGroupResource: indexAPIBindingByIdentityGroupResource})                   //nolint:errcheck
	c.KcpSharedInformerFactory.Workload().V1alpha1().SyncTargets().Informer().GetIndexer().AddIndexers(cache.Indexers{indexers.SyncTargetsBySyncTargetKey: indexers.IndexSyncTargetsBySyncTargetKey}) //nolint:errcheck

	c.ApiExtensions.ExtraConfig.ClusterAwareCRDLister = &apiBindingAwareCRDClusterLister{
		kcpClusterClient:  c.KcpClusterClient,
		crdLister:         c.ApiExtensionsSharedInformerFactory.Apiextensions().V1().CustomResourceDefinitions().Lister(),
		crdIndexer:        c.ApiExtensionsSharedInformerFactory.Apiextensions().V1().CustomResourceDefinitions().Informer().GetIndexer(),
		workspaceLister:   c.KcpSharedInformerFactory.Tenancy().V1beta1().Workspaces().Lister(),
		apiBindingLister:  c.KcpSharedInformerFactory.Apis().V1alpha1().APIBindings().Lister(),
		apiBindingIndexer: c.KcpSharedInformerFactory.Apis().V1alpha1().APIBindings().Informer().GetIndexer(),
		apiExportIndexer:  c.KcpSharedInformerFactory.Apis().V1alpha1().APIExports().Informer().GetIndexer(),
		getAPIResourceSchema: func(clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error) {
			return c.KcpSharedInformerFactory.Apis().V1alpha1().APIResourceSchemas().Lister().Cluster(clusterName).Get(name)
		},
	}
	c.ApiExtensions.ExtraConfig.Client = c.ApiExtensionsClusterClient
	c.ApiExtensions.ExtraConfig.Informers = c.ApiExtensionsSharedInformerFactory
	c.ApiExtensions.ExtraConfig.TableConverterProvider = NewTableConverterProvider()

	c.MiniAggregator = &aggregator.MiniAggregatorConfig{
		GenericConfig: c.GenericConfig,
	}

	return c, nil
}
