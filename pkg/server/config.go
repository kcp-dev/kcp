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
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"net/http/httputil"
	_ "net/http/pprof"
	"net/url"
	"os"

	kcpapiextensionsclientset "github.com/kcp-dev/client-go/apiextensions/client"
	kcpapiextensionsinformers "github.com/kcp-dev/client-go/apiextensions/informers"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesinformers "github.com/kcp-dev/client-go/informers"
	kcpkubernetesclient "github.com/kcp-dev/client-go/kubernetes"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"

	apiextensionsapiserver "k8s.io/apiextensions-apiserver/pkg/apiserver"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/conversion"
	"k8s.io/apimachinery/pkg/runtime"
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
	"k8s.io/kubernetes/pkg/api/legacyscheme"
	"k8s.io/kubernetes/pkg/controlplane"
	controlplaneapiserver "k8s.io/kubernetes/pkg/controlplane/apiserver"
	"k8s.io/kubernetes/pkg/controlplane/apiserver/miniaggregator"
	generatedopenapi "k8s.io/kubernetes/pkg/generated/openapi"
	quotainstall "k8s.io/kubernetes/pkg/quota/v1/install"

	kcpadmissioninitializers "github.com/kcp-dev/kcp/pkg/admission/initializers"
	"github.com/kcp-dev/kcp/pkg/authorization"
	bootstrappolicy "github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
	"github.com/kcp-dev/kcp/pkg/embeddedetcd"
	"github.com/kcp-dev/kcp/pkg/informer"
	"github.com/kcp-dev/kcp/pkg/server/bootstrap"
	kcpfilters "github.com/kcp-dev/kcp/pkg/server/filters"
	kcpserveroptions "github.com/kcp-dev/kcp/pkg/server/options"
	"github.com/kcp-dev/kcp/pkg/server/options/batteries"
	"github.com/kcp-dev/kcp/pkg/server/requestinfo"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	kcpinformers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions"
)

type Config struct {
	Options kcpserveroptions.CompletedOptions

	EmbeddedEtcd *embeddedetcd.Config

	GenericConfig   *genericapiserver.Config // the config embedded into MiniAggregator, the head of the delegation chain
	MiniAggregator  *miniaggregator.MiniAggregatorConfig
	Apis            *controlplaneapiserver.Config
	ApiExtensions   *apiextensionsapiserver.Config
	OptionalVirtual *VirtualConfig

	ExtraConfig
}

type ExtraConfig struct {
	// resolveIdenties is to be called on server start until it succeeds. It injects the kcp
	// resource identities into the rest.Config used by the client. Only after it succeeds,
	// the clients can wildcard-list/watch most kcp resources.
	resolveIdentities func(ctx context.Context) error
	IdentityConfig    *rest.Config

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

	LogicalClusterAdminConfig         *rest.Config // client config connecting directly to shards, skipping the front proxy
	ExternalLogicalClusterAdminConfig *rest.Config // client config connecting to the front proxy

	// misc
	preHandlerChainMux   *handlerChainMuxes
	quotaAdmissionStopCh chan struct{}

	// URL getters depending on genericspiserver.ExternalAddress which is initialized on server run
	ShardBaseURL             func() string
	ShardExternalURL         func() string
	ShardVirtualWorkspaceURL func() string

	// informers
	KcpSharedInformerFactory                kcpinformers.SharedInformerFactory
	KubeSharedInformerFactory               kcpkubernetesinformers.SharedInformerFactory
	ApiExtensionsSharedInformerFactory      kcpapiextensionsinformers.SharedInformerFactory
	DiscoveringDynamicSharedInformerFactory *informer.DiscoveringDynamicSharedInformerFactory
	CacheKcpSharedInformerFactory           kcpinformers.SharedInformerFactory
	CacheKubeSharedInformerFactory          kcpkubernetesinformers.SharedInformerFactory
}

type completedConfig struct {
	Options kcpserveroptions.CompletedOptions

	GenericConfig   genericapiserver.CompletedConfig
	EmbeddedEtcd    embeddedetcd.CompletedConfig
	MiniAggregator  miniaggregator.CompletedMiniAggregatorConfig
	Apis            controlplaneapiserver.CompletedConfig
	ApiExtensions   apiextensionsapiserver.CompletedConfig
	OptionalVirtual CompletedVirtualConfig

	ExtraConfig
}

type CompletedConfig struct {
	// Embed a private pointer that cannot be instantiated outside of this package.
	*completedConfig
}

// Complete fills in any fields not set that are required to have valid data. It's mutating the receiver.
func (c *Config) Complete() (CompletedConfig, error) {
	miniAggregator := c.MiniAggregator.Complete()
	return CompletedConfig{&completedConfig{
		Options: c.Options,

		GenericConfig:  c.GenericConfig.Complete(informerfactoryhack.Wrap(c.KubeSharedInformerFactory)),
		EmbeddedEtcd:   c.EmbeddedEtcd.Complete(),
		MiniAggregator: miniAggregator,
		Apis:           c.Apis.Complete(),
		ApiExtensions:  c.ApiExtensions.Complete(),
		OptionalVirtual: c.OptionalVirtual.Complete(
			miniAggregator.GenericConfig.Authentication,
			miniAggregator.GenericConfig.AuditPolicyRuleEvaluator,
			miniAggregator.GenericConfig.AuditBackend,
			c.GenericConfig.ExternalAddress,
		),

		ExtraConfig: c.ExtraConfig,
	}}, nil
}

const KcpBootstrapperUserName = "system:kcp:bootstrapper"

func NewConfig(opts kcpserveroptions.CompletedOptions) (*Config, error) {
	c := &Config{
		Options: opts,
	}

	if opts.Extra.ProfilerAddress != "" {
		//nolint:errcheck,gosec
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
	c.GenericConfig, c.KubeSharedInformerFactory, storageFactory, err = controlplaneapiserver.BuildGenericConfig(
		opts.GenericControlPlane,
		[]*runtime.Scheme{legacyscheme.Scheme, apiextensionsapiserver.Scheme},
		controlplane.DefaultAPIResourceConfigSource(),
		generatedopenapi.GetOpenAPIDefinitions,
	)
	if err != nil {
		return nil, err
	}

	c.KubeClusterClient, err = kcpkubernetesclient.NewForConfig(rest.CopyConfig(c.GenericConfig.LoopbackClientConfig))
	if err != nil {
		return nil, err
	}
	cacheClientConfig, err := c.Options.Cache.Client.RestConfig(rest.CopyConfig(c.GenericConfig.LoopbackClientConfig))
	if err != nil {
		return nil, err
	}
	cacheKcpClusterClient, err := kcpclientset.NewForConfig(cacheClientConfig)
	if err != nil {
		return nil, err
	}
	cacheKubeClusterClient, err := kcpkubernetesclientset.NewForConfig(cacheClientConfig)
	if err != nil {
		return nil, err
	}
	c.CacheKcpSharedInformerFactory = kcpinformers.NewSharedInformerFactoryWithOptions(
		cacheKcpClusterClient,
		resyncPeriod,
	)
	c.CacheKubeSharedInformerFactory = kcpkubernetesinformers.NewSharedInformerFactoryWithOptions(
		cacheKubeClusterClient,
		resyncPeriod,
	)
	c.CacheDynamicClient, err = kcpdynamic.NewForConfig(cacheClientConfig)
	if err != nil {
		return nil, err
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

		c.IdentityConfig = rest.CopyConfig(c.GenericConfig.LoopbackClientConfig)
		c.IdentityConfig.Wrap(kcpShardIdentityRoundTripper)
		c.KcpClusterClient, err = kcpclientset.NewForConfig(c.IdentityConfig) // this is now generic to be used for all kcp API groups
		if err != nil {
			return nil, err
		}
	} else {
		// The informers here are not used before the informers are actually started (i.e. no race).

		c.IdentityConfig, c.resolveIdentities = bootstrap.NewConfigWithWildcardIdentities(c.GenericConfig.LoopbackClientConfig, bootstrap.KcpRootGroupExportNames, bootstrap.KcpRootGroupResourceExportNames, nil)
		c.KcpClusterClient, err = kcpclientset.NewForConfig(c.IdentityConfig) // this is now generic to be used for all kcp API groups
		if err != nil {
			return nil, err
		}
		c.RootShardKcpClusterClient = c.KcpClusterClient
	}

	informerConfig := rest.CopyConfig(c.IdentityConfig)
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
			return nil, fmt.Errorf("failed to load the logical cluster admin kubeconfig from %q: %w", c.Options.Extra.LogicalClusterAdminKubeconfig, err)
		}
	}

	c.ExternalLogicalClusterAdminConfig = rest.CopyConfig(c.GenericConfig.LoopbackClientConfig)
	if len(c.Options.Extra.ExternalLogicalClusterAdminKubeconfig) > 0 {
		c.ExternalLogicalClusterAdminConfig, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(&clientcmd.ClientConfigLoadingRules{ExplicitPath: c.Options.Extra.ExternalLogicalClusterAdminKubeconfig}, nil).ClientConfig()
		if err != nil {
			return nil, fmt.Errorf("failed to load the external logical cluster admin kubeconfig from %q: %w", c.Options.Extra.ExternalLogicalClusterAdminKubeconfig, err)
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

	if err := opts.Authorization.ApplyTo(c.GenericConfig, c.KubeSharedInformerFactory, c.CacheKubeSharedInformerFactory, c.KcpSharedInformerFactory, c.CacheKcpSharedInformerFactory); err != nil {
		return nil, err
	}
	var userToken string
	c.kcpAdminToken, c.shardAdminToken, userToken, c.shardAdminTokenHash, err = opts.AdminAuthentication.ApplyTo(c.GenericConfig)
	if err != nil {
		return nil, err
	}
	if sets.New[string](opts.Extra.BatteriesIncluded...).Has(batteries.User) {
		c.userToken = userToken
	}

	bootstrapConfig := rest.CopyConfig(c.GenericConfig.LoopbackClientConfig)
	bootstrapConfig.Impersonate.UserName = KcpBootstrapperUserName
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

	var shardVirtualWorkspaceURL *url.URL
	if !opts.Virtual.Enabled && opts.Extra.ShardVirtualWorkspaceURL != "" {
		shardVirtualWorkspaceURL, err = url.Parse(opts.Extra.ShardVirtualWorkspaceURL)
		if err != nil {
			return nil, err
		}
	}

	var virtualWorkspaceServerProxyTransport http.RoundTripper
	if opts.Extra.ShardClientCertFile != "" && opts.Extra.ShardClientKeyFile != "" && opts.Extra.ShardVirtualWorkspaceCAFile != "" {
		caCert, err := os.ReadFile(opts.Extra.ShardVirtualWorkspaceCAFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read CA file %q: %w", opts.Extra.ShardVirtualWorkspaceCAFile, err)
		}

		caCertPool := x509.NewCertPool()
		caCertPool.AppendCertsFromPEM(caCert)

		cert, err := tls.LoadX509KeyPair(opts.Extra.ShardClientCertFile, opts.Extra.ShardClientKeyFile)
		if err != nil {
			return nil, fmt.Errorf("failed to load client certificate %q or key %q: %w", opts.Extra.ShardClientCertFile, opts.Extra.ShardClientKeyFile, err)
		}

		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.TLSClientConfig = &tls.Config{
			Certificates: []tls.Certificate{cert},
			RootCAs:      caCertPool,
		}
		virtualWorkspaceServerProxyTransport = transport
	}

	// Make sure to set our RequestInfoResolver that is capable of populating a RequestInfo even for /services/... URLs.
	c.GenericConfig.RequestInfoResolver = requestinfo.NewKCPRequestInfoResolver()

	// preHandlerChainMux is called before the actual handler chain. Note that BuildHandlerChainFunc below
	// is called multiple times, but only one of the handler chain will actually be used. Hence, we wrap it
	// to give handlers below one mux.Handle func to call.
	c.preHandlerChainMux = &handlerChainMuxes{}
	c.GenericConfig.BuildHandlerChainFunc = func(apiHandler http.Handler, genericConfig *genericapiserver.Config) (secure http.Handler) {
		apiHandler = WithWildcardListWatchGuard(apiHandler)
		apiHandler = WithRequestIdentity(apiHandler)
		apiHandler = authorization.WithSubjectAccessReviewAuditAnnotations(apiHandler)
		apiHandler = authorization.WithDeepSubjectAccessReview(apiHandler)

		// The following ensures that only the default main api handler chain executes authorizers which log audit messages.
		// All other invocations of the same authorizer chain still work but do not produce audit log entries.
		// This compromises audit log size and information overflow vs. having audit reasons for the main api handler only.
		// First, remember authorizer chain with audit logging disabled.
		authorizerWithoutAudit := genericConfig.Authorization.Authorizer
		// configure audit logging enabled authorizer chain and build the apiHandler using this configuration.
		genericConfig.Authorization.Authorizer = authorization.WithAuditLogging("request.auth.kcp.io", genericConfig.Authorization.Authorizer)
		apiHandler = genericapiserver.DefaultBuildHandlerChainFromAuthz(apiHandler, genericConfig)
		// reset authorizer chain with audit logging disabled.
		genericConfig.Authorization.Authorizer = authorizerWithoutAudit

		if opts.HomeWorkspaces.Enabled {
			apiHandler, err = WithHomeWorkspaces(
				apiHandler,
				genericConfig.Authorization.Authorizer,
				c.KubeClusterClient,
				c.KcpClusterClient,
				c.KubeSharedInformerFactory,
				c.KcpSharedInformerFactory,
				c.GenericConfig.ExternalAddress,
			)
			if err != nil {
				panic(err) // shouldn't happen due to flag validation
			}
		}

		// Only include this when the virtual workspace server is external
		if shardVirtualWorkspaceURL != nil && virtualWorkspaceServerProxyTransport != nil {
			proxy := &httputil.ReverseProxy{
				Director: func(r *http.Request) {
					r.URL.Scheme = shardVirtualWorkspaceURL.Scheme
					r.URL.Host = shardVirtualWorkspaceURL.Host
					delete(r.Header, "X-Forwarded-For")
				},
			}
			// Note: this has to come after DefaultBuildHandlerChainBeforeAuthz because it needs the user info, which
			// is only available after DefaultBuildHandlerChainBeforeAuthz.
			apiHandler = WithVirtualWorkspacesProxy(apiHandler, shardVirtualWorkspaceURL, virtualWorkspaceServerProxyTransport, proxy)
		}

		apiHandler = genericapiserver.DefaultBuildHandlerChainBeforeAuthz(apiHandler, genericConfig)

		// this will be replaced in DefaultBuildHandlerChain. So at worst we get twice as many warning.
		// But this is not harmful as the kcp warnings are not many.
		apiHandler = filters.WithWarningRecorder(apiHandler)

		apiHandler = kcpfilters.WithAuditEventClusterAnnotation(apiHandler)

		// Add a mux before the chain, for other handlers with their own handler chain to hook in. For example, when
		// the virtual workspace server is running as part of kcp, it registers /services with the mux so it can handle
		// that path itself, instead of the rest of the handler chain above handling it.
		mux := http.NewServeMux()
		mux.Handle("/", apiHandler)
		*c.preHandlerChainMux = append(*c.preHandlerChainMux, mux)
		apiHandler = mux

		apiHandler = filters.WithAuditInit(apiHandler) // Must run before any audit annotation is made
		apiHandler = WithLocalProxy(apiHandler, opts.Extra.ShardName, opts.Extra.ShardBaseURL, c.KcpSharedInformerFactory.Tenancy().V1alpha1().Workspaces(), c.KcpSharedInformerFactory.Core().V1alpha1().LogicalClusters())
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
		kcpadmissioninitializers.NewKcpInformersInitializer(c.KcpSharedInformerFactory, c.CacheKcpSharedInformerFactory),
		kcpadmissioninitializers.NewKubeInformersInitializer(c.KubeSharedInformerFactory, c.CacheKubeSharedInformerFactory),
		kcpadmissioninitializers.NewKubeClusterClientInitializer(c.KubeClusterClient),
		kcpadmissioninitializers.NewKcpClusterClientInitializer(c.KcpClusterClient),
		kcpadmissioninitializers.NewDeepSARClientInitializer(c.DeepSARClient),
		// The external address is provided as a function, as its value may be updated
		// with the default secure port, when the config is later completed.
		kcpadmissioninitializers.NewKubeQuotaConfigurationInitializer(quotaConfiguration),
		kcpadmissioninitializers.NewServerShutdownInitializer(c.quotaAdmissionStopCh),
		kcpadmissioninitializers.NewDynamicClusterClientInitializer(c.DynamicClusterClient),
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

	serviceResolver := webhook.NewDefaultServiceResolver()
	kubeControlPlane, kubePluginInitializer, err := controlplaneapiserver.CreateConfig(
		opts.GenericControlPlane,
		c.GenericConfig,
		c.KubeSharedInformerFactory,
		storageFactory,
		serviceResolver,
		admissionPluginInitializers,
	)
	if err != nil {
		return nil, err
	}
	c.Apis = kubeControlPlane
	admissionPluginInitializers = append(admissionPluginInitializers, kubePluginInitializer...)

	authInfoResolver := webhook.NewDefaultAuthenticationInfoResolverWrapper(kubeControlPlane.ProxyTransport, kubeControlPlane.Generic.EgressSelector, kubeControlPlane.Generic.LoopbackClientConfig, kubeControlPlane.Generic.TracerProvider)
	conversionFactory, err := conversion.NewCRConverterFactory(serviceResolver, authInfoResolver)
	if err != nil {
		return nil, err
	}
	c.ApiExtensions, err = controlplaneapiserver.CreateAPIExtensionsConfig(
		*c.GenericConfig,
		informerfactoryhack.Wrap(c.KubeSharedInformerFactory),
		admissionPluginInitializers,
		opts.GenericControlPlane,
		3,
		conversionFactory)
	if err != nil {
		return nil, fmt.Errorf("error configuring api extensions: %w", err)
	}

	// make sure the informer gets started, otherwise conversions will not work!
	_ = c.KcpSharedInformerFactory.Apis().V1alpha1().APIConversions().Informer()

	c.ApiExtensionsSharedInformerFactory.Apiextensions().V1().CustomResourceDefinitions().Informer().GetIndexer().AddIndexers(cache.Indexers{byGroupResourceName: indexCRDByGroupResourceName}) //nolint:errcheck
	c.KcpSharedInformerFactory.Apis().V1alpha1().APIBindings().Informer().GetIndexer().AddIndexers(cache.Indexers{byIdentityGroupResource: indexAPIBindingByIdentityGroupResource})             //nolint:errcheck

	c.ApiExtensions.ExtraConfig.ClusterAwareCRDLister = &apiBindingAwareCRDClusterLister{
		kcpClusterClient:  c.KcpClusterClient,
		crdLister:         c.ApiExtensionsSharedInformerFactory.Apiextensions().V1().CustomResourceDefinitions().Lister(),
		crdIndexer:        c.ApiExtensionsSharedInformerFactory.Apiextensions().V1().CustomResourceDefinitions().Informer().GetIndexer(),
		workspaceLister:   c.KcpSharedInformerFactory.Tenancy().V1alpha1().Workspaces().Lister(),
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

	c.MiniAggregator = &miniaggregator.MiniAggregatorConfig{
		GenericConfig: *c.GenericConfig,
	}
	// same as in kube-aggregator, remove hooks and OpenAPI
	c.MiniAggregator.GenericConfig.PostStartHooks = map[string]genericapiserver.PostStartHookConfigEntry{}
	c.MiniAggregator.GenericConfig.SkipOpenAPIInstallation = true

	if opts.Virtual.Enabled {
		virtualWorkspacesConfig := rest.CopyConfig(c.GenericConfig.LoopbackClientConfig)
		virtualWorkspacesConfig = rest.AddUserAgent(virtualWorkspacesConfig, "virtual-workspaces")

		c.OptionalVirtual, err = newVirtualConfig(
			opts,
			virtualWorkspacesConfig,
			c.KubeSharedInformerFactory,
			c.KcpSharedInformerFactory,
			c.CacheKcpSharedInformerFactory,
			c.ShardExternalURL,
		)
		if err != nil {
			return nil, err
		}
	}

	return c, nil
}
