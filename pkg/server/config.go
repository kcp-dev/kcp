package server

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"reflect"

	kcpadmissioninitializers "github.com/kcp-dev/kcp/pkg/admission/initializers"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kcpexternalversions "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	"github.com/kcp-dev/kcp/pkg/etcd"
	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/informer"
	boostrap "github.com/kcp-dev/kcp/pkg/server/bootstrap"
	"github.com/kcp-dev/kcp/pkg/server/options"
	"github.com/kcp-dev/kcp/pkg/server/requestinfo"
	"github.com/kcp-dev/logicalcluster"
	etcdtypes "go.etcd.io/etcd/client/pkg/v3/types"
	apiextensionsapiserver "k8s.io/apiextensions-apiserver/pkg/apiserver"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apiextensionsexternalversions "k8s.io/apiextensions-apiserver/pkg/client/informers/externalversions"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/endpoints/filters"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/apiserver/pkg/util/webhook"
	"k8s.io/client-go/dynamic"
	coreexternalversions "k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/genericcontrolplane"
	"k8s.io/kubernetes/pkg/genericcontrolplane/apis"
)

type tokenAuth struct {
	kcp       string
	shard     string
	shardHash []byte
}
type Config struct {
	options                    *options.CompletedOptions
	EtcdServer                 *etcd.Server
	identityConfig             *rest.Config
	resolveIdentities          func(ctx context.Context) error
	genericConfig              *genericapiserver.Config
	apisConfig                 *apis.Config
	apiExtensionsConfig        *apiextensionsapiserver.Config
	preHandlerChain            handlerChainMuxes
	dynamicClusterClient       *dynamic.Cluster
	apiextensionsClusterClient *apiextensionsclient.Cluster
	kcpClusterClient           *kcpclient.Cluster
	kubeClusterClient          *kubernetes.Cluster
	tokenAuth
	apiBindingAwareCRDLister *apiBindingAwareCRDLister

	kcpSharedInformerFactory              kcpexternalversions.SharedInformerFactory
	kubeSharedInformerFactory             coreexternalversions.SharedInformerFactory
	apiextensionsSharedInformerFactory    apiextensionsexternalversions.SharedInformerFactory
	dynamicDiscoverySharedInformerFactory *informer.DynamicDiscoverySharedInformerFactory
	// TODO(sttts): get rid of these. We have wildcard informers already.
	rootKcpSharedInformerFactory  kcpexternalversions.SharedInformerFactory
	rootKubeSharedInformerFactory coreexternalversions.SharedInformerFactory
}

type CompletedConfig struct {
	EtcdServer            *etcd.Server
	config                *Config
	preHandlerChain       handlerChainMuxes
	profilerAddress       string
	enabledControllers    sets.String
	enabledAllControllers bool
	apiExtensionsConfig   apiextensionsapiserver.CompletedConfig
	apisConfig            apis.CompletedConfig
}

func (c *CompletedConfig) Factories() (kcpexternalversions.SharedInformerFactory, coreexternalversions.SharedInformerFactory, apiextensionsexternalversions.SharedInformerFactory, *informer.DynamicDiscoverySharedInformerFactory, kcpexternalversions.SharedInformerFactory, coreexternalversions.SharedInformerFactory) {
	return c.config.kcpSharedInformerFactory,
		c.config.kubeSharedInformerFactory,
		c.config.apiextensionsSharedInformerFactory,
		c.config.dynamicDiscoverySharedInformerFactory,
		c.config.rootKcpSharedInformerFactory,
		c.config.rootKubeSharedInformerFactory
}

func (c *CompletedConfig) IdentityConfig() *rest.Config {
	return rest.CopyConfig(c.config.identityConfig)
}

func (c *CompletedConfig) GenericConfig() *genericapiserver.Config {
	return c.config.genericConfig
}
func (c *CompletedConfig) PreHandlerChain() handlerChainMuxes {
	return c.preHandlerChain
}
func (c *CompletedConfig) ProfilerAddress() string {
	return c.profilerAddress
}
func (c *CompletedConfig) IsControllerEnabled(name string) bool {
	return c.enabledAllControllers || c.enabledControllers.Has(name)
}
func (c *CompletedConfig) VirtualEnabled() bool {
	return c.config.options.Virtual.Enabled
}
func (c *CompletedConfig) Virtual() options.Virtual {
	return c.config.options.Virtual
}
func (c *CompletedConfig) Controllers() options.Controllers {
	return c.config.options.Controllers
}
func (c *CompletedConfig) Authorization() options.Authorization {
	return c.config.options.Authorization
}
func (c *CompletedConfig) ServerKeyCert() (keyFile string, certFile string) {
	keyFile = c.config.options.GenericControlPlane.SecureServing.SecureServingOptions.ServerCert.CertKey.KeyFile
	certFile = c.config.options.GenericControlPlane.SecureServing.SecureServingOptions.ServerCert.CertKey.CertFile
	return
}
func (c *CompletedConfig) DynamicClusterClient() *dynamic.Cluster {
	return c.config.dynamicClusterClient
}
func (c *CompletedConfig) ResolveIdentitiesFunc() func(context.Context) error {
	return c.config.resolveIdentities
}
func (c *CompletedConfig) APIExtensionsConfig() *apiextensionsapiserver.Config {
	return c.config.apiExtensionsConfig
}
func (c *CompletedConfig) APIBindingAwareCRDLister() *apiBindingAwareCRDLister {
	return c.config.apiBindingAwareCRDLister
}
func (c *CompletedConfig) APIExtensionsClusterClient() *apiextensionsclient.Cluster {
	return c.config.apiextensionsClusterClient
}
func (c *CompletedConfig) KCPClusterClient() *kcpclient.Cluster {
	return c.config.kcpClusterClient
}
func (c *CompletedConfig) KubeClusterClient() *kubernetes.Cluster {
	return c.config.kubeClusterClient
}
func (c *CompletedConfig) WriteKubeConfig() error {
	return c.config.options.AdminAuthentication.WriteKubeConfig(c.config.genericConfig, c.config.tokenAuth.kcp, c.config.tokenAuth.shard, c.config.tokenAuth.shardHash)
}
func (c *CompletedConfig) ShardName() string {
	return c.config.options.Extra.ShardName
}
func (c *CompletedConfig) HomeWorkspaces() options.HomeWorkspaces {
	return c.config.options.HomeWorkspaces
}

func NewConfig(opts *options.CompletedOptions) (Config, error) {
	c := Config{
		options: opts,
	}

	// set up embedded etcd, if requested
	embedEtcd := c.options.EmbeddedEtcd
	if embedEtcd.Enabled {
		es := &etcd.Server{
			Dir: embedEtcd.Directory,
		}
		var listenMetricsURLs []url.URL
		if len(embedEtcd.ListenMetricsURLs) > 0 {
			var err error
			listenMetricsURLs, err = etcdtypes.NewURLs(embedEtcd.ListenMetricsURLs)
			if err != nil {
				return c, err
			}
		}
		clientInfo, err := es.Init(embedEtcd.PeerPort, embedEtcd.ClientPort, listenMetricsURLs, embedEtcd.WalSizeBytes, embedEtcd.QuotaBackendBytes, embedEtcd.ForceNewCluster)
		if err != nil {
			return c, err
		}
		transport := c.options.GenericControlPlane.Etcd.StorageConfig.Transport
		transport.ServerList = clientInfo.Endpoints
		transport.KeyFile = clientInfo.KeyFile
		transport.CertFile = clientInfo.CertFile
		transport.TrustedCAFile = clientInfo.TrustedCAFile
		c.EtcdServer = es
	}

	// set up kcpSharedInformerFactory
	genericConfig, storageFactory, err := genericcontrolplane.BuildGenericConfig(c.options.GenericControlPlane)
	if err != nil {
		return c, err
	}
	c.genericConfig = genericConfig

	genericConfig.RequestInfoResolver = requestinfo.NewFactory() // must be set here early to avoid a crash in the EnableMultiCluster roundtrip wrapper

	// Setup kcp * informers, but those will need the identities for the APIExports used to make the APIs available.
	// The identities are not known before we can get them from the APIExports via the loopback client, hence we postpone
	// this to getOrCreateKcpIdentities() in the kcp-start-informers post-start hook.
	// The informers here are not  used before the informers are actually started (i.e. no race).
	nonIdentityKcpClusterClient, err := kcpclient.NewClusterForConfig(genericConfig.LoopbackClientConfig) // can only used for wildcard requests of apis.kcp.dev
	if err != nil {
		return c, err
	}
	c.identityConfig, c.resolveIdentities = boostrap.NewConfigWithWildcardIdentities(genericConfig.LoopbackClientConfig, boostrap.KcpRootGroupExportNames, boostrap.KcpRootGroupResourceExportNames, nonIdentityKcpClusterClient.Cluster(tenancyv1alpha1.RootCluster))
	kcpClusterClient, err := kcpclient.NewClusterForConfig(c.identityConfig) // this is now generic to be used for all kcp API groups
	if err != nil {
		return c, err
	}
	c.kcpClusterClient = kcpClusterClient
	kcpClient := kcpClusterClient.Cluster(logicalcluster.Wildcard)
	c.kcpSharedInformerFactory = kcpexternalversions.NewSharedInformerFactoryWithOptions(
		kcpClient,
		resyncPeriod,
		kcpexternalversions.WithExtraClusterScopedIndexers(indexers.ClusterScoped()),
		kcpexternalversions.WithExtraNamespaceScopedIndexers(indexers.NamespaceScoped()),
	)

	// Setup kube * informers
	kubeClusterClient, err := kubernetes.NewClusterForConfig(genericConfig.LoopbackClientConfig)
	if err != nil {
		return c, err
	}
	kubeClient := kubeClusterClient.Cluster(logicalcluster.Wildcard)
	c.kubeSharedInformerFactory = coreexternalversions.NewSharedInformerFactoryWithOptions(
		kubeClient,
		resyncPeriod,
		coreexternalversions.WithExtraClusterScopedIndexers(indexers.ClusterScoped()),
		coreexternalversions.WithExtraNamespaceScopedIndexers(indexers.NamespaceScoped()),
	)

	// Setup apiextensions * informers
	apiextensionsClusterClient, err := apiextensionsclient.NewClusterForConfig(genericConfig.LoopbackClientConfig)
	if err != nil {
		return c, err
	}
	apiextensionsCrossClusterClient := apiextensionsClusterClient.Cluster(logicalcluster.Wildcard)
	c.apiextensionsSharedInformerFactory = apiextensionsexternalversions.NewSharedInformerFactoryWithOptions(
		apiextensionsCrossClusterClient,
		resyncPeriod,
		apiextensionsexternalversions.WithExtraClusterScopedIndexers(indexers.ClusterScoped()),
		apiextensionsexternalversions.WithExtraNamespaceScopedIndexers(indexers.NamespaceScoped()),
	)

	// Setup root informers
	c.rootKcpSharedInformerFactory = kcpexternalversions.NewSharedInformerFactoryWithOptions(kcpClusterClient.Cluster(tenancyv1alpha1.RootCluster), resyncPeriod)
	c.rootKubeSharedInformerFactory = coreexternalversions.NewSharedInformerFactoryWithOptions(kubeClusterClient.Cluster(tenancyv1alpha1.RootCluster), resyncPeriod)

	// Setup dynamic client
	dynamicClusterClient, err := dynamic.NewClusterForConfig(genericConfig.LoopbackClientConfig)
	if err != nil {
		return c, err
	}
	c.dynamicClusterClient = dynamicClusterClient

	if err := c.options.Authorization.ApplyTo(genericConfig, c.kubeSharedInformerFactory, c.kcpSharedInformerFactory); err != nil {
		return c, err
	}
	kcpAdminToken, shardAdminToken, shardAdminTokenHash, err := c.options.AdminAuthentication.ApplyTo(genericConfig)
	if err != nil {
		return c, err
	}
	c.tokenAuth = tokenAuth{
		kcp:       kcpAdminToken,
		shard:     shardAdminToken,
		shardHash: shardAdminTokenHash,
	}

	if err := c.options.GenericControlPlane.Audit.ApplyTo(genericConfig); err != nil {
		return c, err
	}

	// preHandlerChainMux is called before the actual handler chain. Note that BuildHandlerChainFunc below
	// is called multiple times, but only one of the handler chain will actually be used. Hence, we wrap it
	// to give handlers below one mux.Handle func to call.
	var preHandlerChainMux handlerChainMuxes
	kubeSharedInformerFactory, kcpSharedInformerFactory := c.kubeSharedInformerFactory, c.kcpSharedInformerFactory
	genericConfig.BuildHandlerChainFunc = func(apiHandler http.Handler, c *genericapiserver.Config) (secure http.Handler) {
		apiHandler = WithWildcardListWatchGuard(apiHandler)
		apiHandler = WithWildcardIdentity(apiHandler)

		apiHandler = genericapiserver.DefaultBuildHandlerChainFromAuthz(apiHandler, c)

		// TODO(david): Add options to drive the various Home workspace parameters.
		// For now default values are:
		//   - Creation delay (returned in the retry-afterof the http responses): 2 seconds
		//   - Home root workspace: root:users
		//   - Home bucket levels: 2
		//   - home bucket name size: 2
		apiHandler = WithHomeWorkspaces(apiHandler, c.Authorization.Authorizer, kubeClusterClient, kcpClusterClient, kubeSharedInformerFactory, kcpSharedInformerFactory, genericConfig.ExternalAddress, 2, logicalcluster.New("root:users"), 2, 2)

		apiHandler = genericapiserver.DefaultBuildHandlerChainBeforeAuthz(apiHandler, c)

		// this will be replaced in DefaultBuildHandlerChain. So at worst we get twice as many warning.
		// But this is not harmful as the kcp warnings are not many.
		apiHandler = filters.WithWarningRecorder(apiHandler)

		// add a mux before the chain, for other handlers with their own handler chain to hook in
		mux := http.NewServeMux()
		mux.Handle("/", apiHandler)
		preHandlerChainMux = append(preHandlerChainMux, mux)
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
	c.preHandlerChain = preHandlerChainMux

	admissionPluginInitializers := []admission.PluginInitializer{
		kcpadmissioninitializers.NewKcpInformersInitializer(kcpSharedInformerFactory),
		kcpadmissioninitializers.NewKubeClusterClientInitializer(kubeClusterClient),
		kcpadmissioninitializers.NewKcpClusterClientInitializer(kcpClusterClient),
		kcpadmissioninitializers.NewShardBaseURLInitializer(c.options.Extra.ShardBaseURL),
		// The external address is provided as a function, as its value may be updated
		// with the default secure port, when the config is later completed.
		kcpadmissioninitializers.NewExternalAddressInitializer(func() string { return genericConfig.ExternalAddress }),
	}

	apisConfig, err := genericcontrolplane.CreateKubeAPIServerConfig(genericConfig, c.options.GenericControlPlane, kubeSharedInformerFactory, admissionPluginInitializers, storageFactory)
	if err != nil {
		return c, err
	}
	c.apisConfig = apisConfig

	// If additional API servers are added, they should be gated.
	apiExtensionsConfig, err := genericcontrolplane.CreateAPIExtensionsConfig(
		*apisConfig.GenericConfig,
		apisConfig.ExtraConfig.VersionedInformers,
		admissionPluginInitializers,
		c.options.GenericControlPlane,

		// Wire in a ServiceResolver that always returns an error that ResolveEndpoint is not yet
		// supported. The effect is that CRD webhook conversions are not supported and will always get an
		// error.
		&unimplementedServiceResolver{},

		webhook.NewDefaultAuthenticationInfoResolverWrapper(
			nil,
			apisConfig.GenericConfig.EgressSelector,
			apisConfig.GenericConfig.LoopbackClientConfig,
			apisConfig.GenericConfig.TracerProvider,
		),
	)
	if err != nil {
		return c, fmt.Errorf("configure api extensions: %w", err)
	}
	c.apiExtensionsConfig = apiExtensionsConfig

	c.kcpSharedInformerFactory.Apis().V1alpha1().APIBindings().Informer().GetIndexer().AddIndexers(cache.Indexers{byWorkspace: indexByWorkspace})                                               // nolint: errcheck
	c.apiextensionsSharedInformerFactory.Apiextensions().V1().CustomResourceDefinitions().Informer().GetIndexer().AddIndexers(cache.Indexers{byWorkspace: indexByWorkspace})                    // nolint: errcheck
	c.apiextensionsSharedInformerFactory.Apiextensions().V1().CustomResourceDefinitions().Informer().GetIndexer().AddIndexers(cache.Indexers{byGroupResourceName: indexCRDByGroupResourceName}) // nolint: errcheck
	c.kcpSharedInformerFactory.Apis().V1alpha1().APIBindings().Informer().GetIndexer().AddIndexers(cache.Indexers{byWorkspace: indexByWorkspace})                                               // nolint: errcheck
	c.kcpSharedInformerFactory.Apis().V1alpha1().APIBindings().Informer().GetIndexer().AddIndexers(cache.Indexers{byIdentityGroupResource: indexAPIBindingByIdentityGroupResource})             // nolint: errcheck

	apiBindingAwareCRDLister := &apiBindingAwareCRDLister{
		kcpClusterClient:  kcpClusterClient,
		crdLister:         c.apiextensionsSharedInformerFactory.Apiextensions().V1().CustomResourceDefinitions().Lister(),
		crdIndexer:        c.apiextensionsSharedInformerFactory.Apiextensions().V1().CustomResourceDefinitions().Informer().GetIndexer(),
		workspaceLister:   c.kcpSharedInformerFactory.Tenancy().V1alpha1().ClusterWorkspaces().Lister(),
		apiBindingLister:  c.kcpSharedInformerFactory.Apis().V1alpha1().APIBindings().Lister(),
		apiBindingIndexer: c.kcpSharedInformerFactory.Apis().V1alpha1().APIBindings().Informer().GetIndexer(),
		apiExportIndexer:  c.kcpSharedInformerFactory.Apis().V1alpha1().APIExports().Informer().GetIndexer(),
		getAPIResourceSchema: func(clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error) {
			key := clusters.ToClusterAwareKey(clusterName, name)
			return c.kcpSharedInformerFactory.Apis().V1alpha1().APIResourceSchemas().Lister().Get(key)
		},
	}
	apiExtensionsConfig.ExtraConfig.ClusterAwareCRDLister = apiBindingAwareCRDLister
	c.apiBindingAwareCRDLister = apiBindingAwareCRDLister

	apiExtensionsConfig.ExtraConfig.TableConverterProvider = NewTableConverterProvider()

	return c, nil
}

func (c *Config) Complete() (*CompletedConfig, error) {
	cc := &CompletedConfig{}

	cc.profilerAddress = c.options.Extra.ProfilerAddress
	cc.enabledControllers = sets.NewString(c.options.Controllers.IndividuallyEnabled...)
	cc.enabledAllControllers = c.options.Controllers.EnableAll
	cc.config = c
	cc.apisConfig = c.apisConfig.Complete()
	cc.apiExtensionsConfig = c.apiExtensionsConfig.Complete()

	return cc, nil
}

type informerFactory interface {
	Start(stopCh <-chan struct{})
	WaitForCacheSync(stopCh <-chan struct{}) map[reflect.Type]bool
}

// generateInformerStartAndWait generates a postStartHook that starts and waits for cache sync for one or more informerFactories
func generateInformerStartAndWait(msg string, factories ...informerFactory) func(ctx genericapiserver.PostStartHookContext) error {
	return func(ctx genericapiserver.PostStartHookContext) error {
		for _, factory := range factories {
			factory.Start(ctx.StopCh)
		}
		for _, factory := range factories {
			factory.WaitForCacheSync(ctx.StopCh)
		}
		select {
		case <-ctx.StopCh:
			return nil // context closed, avoid reporting success below
		default:
		}

		klog.Infof("Finished start informer factories: %s", msg)
		return nil
	}
}
