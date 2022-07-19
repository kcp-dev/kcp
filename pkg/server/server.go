/*
Copyright 2021 The KCP Authors.

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
	"net/url"
	"time"

	"github.com/kcp-dev/logicalcluster/v2"
	etcdtypes "go.etcd.io/etcd/client/pkg/v3/types"

	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apiextensionsexternalversions "k8s.io/apiextensions-apiserver/pkg/client/informers/externalversions"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/endpoints/filters"
	genericapiserver "k8s.io/apiserver/pkg/server"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/apiserver/pkg/util/webhook"
	"k8s.io/client-go/dynamic"
	coreexternalversions "k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/genericcontrolplane"

	configroot "github.com/kcp-dev/kcp/config/root"
	configrootphase0 "github.com/kcp-dev/kcp/config/root-phase0"
	configshard "github.com/kcp-dev/kcp/config/shard"
	systemcrds "github.com/kcp-dev/kcp/config/system-crds"
	kcpadmissioninitializers "github.com/kcp-dev/kcp/pkg/admission/initializers"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	bootstrappolicy "github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kcpexternalversions "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	"github.com/kcp-dev/kcp/pkg/etcd"
	kcpfeatures "github.com/kcp-dev/kcp/pkg/features"
	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/informer"
	boostrap "github.com/kcp-dev/kcp/pkg/server/bootstrap"
	kcpserveroptions "github.com/kcp-dev/kcp/pkg/server/options"
	"github.com/kcp-dev/kcp/pkg/server/requestinfo"
)

const resyncPeriod = 10 * time.Hour

// systemShardCluster is the name of a logical cluster on every shard (including the root shard) that holds essential system resources (like the root APIs).
var systemShardCluster = logicalcluster.New("system:shard")

// Server manages the configuration and kcp api-server. It allows callers to easily use kcp
// as a library rather than as a single binary. Using its constructor function, you can easily
// setup a new api-server and start it:
//
//   srv := server.NewServer(server.BaseConfig())
//   srv.Run(ctx)
//
// You may optionally provide PostStartHookFunc and PreShutdownHookFunc hooks before starting
// the server that should be passed to the api-server itself. These hooks have access to a
// restclient.Config which allows you to easily create a client.
//
//   srv.AddPostStartHook("my-hook", func(context genericapiserver.PostStartHookContext) error {
//       client := clientset.NewForConfigOrDie(context.LoopbackClientConfig)
//   })
type Server struct {
	options *kcpserveroptions.CompletedOptions

	postStartHooks   []postStartHookEntry
	preShutdownHooks []preShutdownHookEntry

	syncedCh chan struct{}

	kcpSharedInformerFactory              kcpexternalversions.SharedInformerFactory
	kubeSharedInformerFactory             coreexternalversions.SharedInformerFactory
	apiextensionsSharedInformerFactory    apiextensionsexternalversions.SharedInformerFactory
	dynamicDiscoverySharedInformerFactory *informer.DynamicDiscoverySharedInformerFactory

	// TODO(sttts): get rid of these. We have wildcard informers already.
	rootKcpSharedInformerFactory  kcpexternalversions.SharedInformerFactory
	rootKubeSharedInformerFactory coreexternalversions.SharedInformerFactory
}

// NewServer creates a new instance of Server which manages the KCP api-server.
func NewServer(o *kcpserveroptions.CompletedOptions) (*Server, error) {
	return &Server{
		options:  o,
		syncedCh: make(chan struct{}),
	}, nil
}

// postStartHookEntry groups a PostStartHookFunc with a name. We're not storing these hooks
// in a map and are instead letting the underlying api server perform the hook validation,
// such as checking for multiple PostStartHookFunc with the same name
type postStartHookEntry struct {
	name string
	hook genericapiserver.PostStartHookFunc
}

// preShutdownHookEntry fills the same purpose as postStartHookEntry except that it handles
// the PreShutdownHookFunc
type preShutdownHookEntry struct {
	name string
	hook genericapiserver.PreShutdownHookFunc
}

// Run starts the KCP api-server. This function blocks until the api-server stops or an error.
func (s *Server) Run(ctx context.Context) error {
	if s.options.Extra.ProfilerAddress != "" {
		// nolint:errcheck
		go http.ListenAndServe(s.options.Extra.ProfilerAddress, nil)
	}
	if s.options.EmbeddedEtcd.Enabled {
		es := &etcd.Server{
			Dir: s.options.EmbeddedEtcd.Directory,
		}
		var listenMetricsURLs []url.URL
		if len(s.options.EmbeddedEtcd.ListenMetricsURLs) > 0 {
			var err error
			listenMetricsURLs, err = etcdtypes.NewURLs(s.options.EmbeddedEtcd.ListenMetricsURLs)
			if err != nil {
				return err
			}
		}
		embeddedClientInfo, err := es.Run(ctx, s.options.EmbeddedEtcd.PeerPort, s.options.EmbeddedEtcd.ClientPort, listenMetricsURLs, s.options.EmbeddedEtcd.WalSizeBytes, s.options.EmbeddedEtcd.QuotaBackendBytes, s.options.EmbeddedEtcd.ForceNewCluster, s.options.GenericControlPlane.Etcd.EnableWatchCache)
		if err != nil {
			return err
		}

		s.options.GenericControlPlane.Etcd.StorageConfig.Transport.ServerList = embeddedClientInfo.Endpoints
		s.options.GenericControlPlane.Etcd.StorageConfig.Transport.KeyFile = embeddedClientInfo.KeyFile
		s.options.GenericControlPlane.Etcd.StorageConfig.Transport.CertFile = embeddedClientInfo.CertFile
		s.options.GenericControlPlane.Etcd.StorageConfig.Transport.TrustedCAFile = embeddedClientInfo.TrustedCAFile
	}

	genericConfig, storageFactory, err := genericcontrolplane.BuildGenericConfig(s.options.GenericControlPlane)
	if err != nil {
		return err
	}

	genericConfig.RequestInfoResolver = requestinfo.NewFactory() // must be set here early to avoid a crash in the EnableMultiCluster roundtrip wrapper

	// Setup kcp * informers, but those will need the identities for the APIExports used to make the APIs available.
	// The identities are not known before we can get them from the APIExports via the loopback client, hence we postpone
	// this to getOrCreateKcpIdentities() in the kcp-start-informers post-start hook.
	// The informers here are not  used before the informers are actually started (i.e. no race).
	nonIdentityKcpClusterClient, err := kcpclient.NewClusterForConfig(genericConfig.LoopbackClientConfig) // can only used for wildcard requests of apis.kcp.dev
	if err != nil {
		return err
	}
	identityConfig, resolveIdentities := boostrap.NewConfigWithWildcardIdentities(genericConfig.LoopbackClientConfig, boostrap.KcpRootGroupExportNames, boostrap.KcpRootGroupResourceExportNames, nonIdentityKcpClusterClient.Cluster(tenancyv1alpha1.RootCluster))
	kcpClusterClient, err := kcpclient.NewClusterForConfig(identityConfig) // this is now generic to be used for all kcp API groups
	if err != nil {
		return err
	}
	kcpClient := kcpClusterClient.Cluster(logicalcluster.Wildcard)
	s.kcpSharedInformerFactory = kcpexternalversions.NewSharedInformerFactoryWithOptions(
		kcpClient,
		resyncPeriod,
		kcpexternalversions.WithExtraClusterScopedIndexers(indexers.ClusterScoped()),
		kcpexternalversions.WithExtraNamespaceScopedIndexers(indexers.NamespaceScoped()),
	)

	// Setup kube * informers
	kubeClusterClient, err := kubernetes.NewClusterForConfig(genericConfig.LoopbackClientConfig)
	if err != nil {
		return err
	}
	kubeClient := kubeClusterClient.Cluster(logicalcluster.Wildcard)
	s.kubeSharedInformerFactory = coreexternalversions.NewSharedInformerFactoryWithOptions(
		kubeClient,
		resyncPeriod,
		coreexternalversions.WithExtraClusterScopedIndexers(indexers.ClusterScoped()),
		coreexternalversions.WithExtraNamespaceScopedIndexers(indexers.NamespaceScoped()),
	)

	// Setup apiextensions * informers
	apiextensionsClusterClient, err := apiextensionsclient.NewClusterForConfig(genericConfig.LoopbackClientConfig)
	if err != nil {
		return err
	}
	apiextensionsCrossClusterClient := apiextensionsClusterClient.Cluster(logicalcluster.Wildcard)
	s.apiextensionsSharedInformerFactory = apiextensionsexternalversions.NewSharedInformerFactoryWithOptions(
		apiextensionsCrossClusterClient,
		resyncPeriod,
		apiextensionsexternalversions.WithExtraClusterScopedIndexers(indexers.ClusterScoped()),
		apiextensionsexternalversions.WithExtraNamespaceScopedIndexers(indexers.NamespaceScoped()),
	)

	// Setup root informers
	s.rootKcpSharedInformerFactory = kcpexternalversions.NewSharedInformerFactoryWithOptions(kcpClusterClient.Cluster(tenancyv1alpha1.RootCluster), resyncPeriod)
	s.rootKubeSharedInformerFactory = coreexternalversions.NewSharedInformerFactoryWithOptions(kubeClusterClient.Cluster(tenancyv1alpha1.RootCluster), resyncPeriod)

	// Setup dynamic client
	dynamicClusterClient, err := dynamic.NewClusterForConfig(genericConfig.LoopbackClientConfig)
	if err != nil {
		return err
	}

	if err := s.options.Authorization.ApplyTo(genericConfig, s.kubeSharedInformerFactory, s.kcpSharedInformerFactory); err != nil {
		return err
	}
	kcpAdminToken, shardAdminToken, shardAdminTokenHash, err := s.options.AdminAuthentication.ApplyTo(genericConfig)
	if err != nil {
		return err
	}

	if err := s.options.GenericControlPlane.Audit.ApplyTo(genericConfig); err != nil {
		return err
	}

	// preHandlerChainMux is called before the actual handler chain. Note that BuildHandlerChainFunc below
	// is called multiple times, but only one of the handler chain will actually be used. Hence, we wrap it
	// to give handlers below one mux.Handle func to call.
	var preHandlerChainMux handlerChainMuxes
	genericConfig.BuildHandlerChainFunc = func(apiHandler http.Handler, c *genericapiserver.Config) (secure http.Handler) {
		apiHandler = WithWildcardListWatchGuard(apiHandler)
		apiHandler = WithWildcardIdentity(apiHandler)

		apiHandler = genericapiserver.DefaultBuildHandlerChainFromAuthz(apiHandler, c)

		if s.options.HomeWorkspaces.Enabled {
			apiHandler = WithHomeWorkspaces(
				apiHandler,
				c.Authorization.Authorizer,
				kubeClusterClient,
				kcpClusterClient,
				s.kubeSharedInformerFactory,
				s.kcpSharedInformerFactory,
				genericConfig.ExternalAddress,
				s.options.HomeWorkspaces.CreationDelaySeconds,
				logicalcluster.New(s.options.HomeWorkspaces.HomeRootPrefix),
				s.options.HomeWorkspaces.BucketLevels,
				s.options.HomeWorkspaces.BucketSize,
			)
		}

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

	admissionPluginInitializers := []admission.PluginInitializer{
		kcpadmissioninitializers.NewKcpInformersInitializer(s.kcpSharedInformerFactory),
		kcpadmissioninitializers.NewKubeClusterClientInitializer(kubeClusterClient),
		kcpadmissioninitializers.NewKcpClusterClientInitializer(kcpClusterClient),
		kcpadmissioninitializers.NewShardBaseURLInitializer(s.options.Extra.ShardBaseURL),
		kcpadmissioninitializers.NewShardExternalURLInitializer(s.options.Extra.ShardExternalURL),
		// The external address is provided as a function, as its value may be updated
		// with the default secure port, when the config is later completed.
		kcpadmissioninitializers.NewExternalAddressInitializer(func() string { return genericConfig.ExternalAddress }),
	}

	apisConfig, err := genericcontrolplane.CreateKubeAPIServerConfig(genericConfig, s.options.GenericControlPlane, s.kubeSharedInformerFactory, admissionPluginInitializers, storageFactory)
	if err != nil {
		return err
	}

	s.AddPostStartHook("kcp-bootstrap-policy", bootstrappolicy.Policy().EnsureRBACPolicy())

	// If additional API servers are added, they should be gated.
	apiExtensionsConfig, err := genericcontrolplane.CreateAPIExtensionsConfig(
		*apisConfig.GenericConfig,
		apisConfig.ExtraConfig.VersionedInformers,
		admissionPluginInitializers,
		s.options.GenericControlPlane,

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
		return fmt.Errorf("configure api extensions: %w", err)
	}

	s.kcpSharedInformerFactory.Apis().V1alpha1().APIBindings().Informer().GetIndexer().AddIndexers(cache.Indexers{byWorkspace: indexByWorkspace})                                               // nolint: errcheck
	s.apiextensionsSharedInformerFactory.Apiextensions().V1().CustomResourceDefinitions().Informer().GetIndexer().AddIndexers(cache.Indexers{byWorkspace: indexByWorkspace})                    // nolint: errcheck
	s.apiextensionsSharedInformerFactory.Apiextensions().V1().CustomResourceDefinitions().Informer().GetIndexer().AddIndexers(cache.Indexers{byGroupResourceName: indexCRDByGroupResourceName}) // nolint: errcheck
	s.kcpSharedInformerFactory.Apis().V1alpha1().APIBindings().Informer().GetIndexer().AddIndexers(cache.Indexers{byWorkspace: indexByWorkspace})                                               // nolint: errcheck
	s.kcpSharedInformerFactory.Apis().V1alpha1().APIBindings().Informer().GetIndexer().AddIndexers(cache.Indexers{byIdentityGroupResource: indexAPIBindingByIdentityGroupResource})             // nolint: errcheck

	apiBindingAwareCRDLister := &apiBindingAwareCRDLister{
		kcpClusterClient:  kcpClusterClient,
		crdLister:         s.apiextensionsSharedInformerFactory.Apiextensions().V1().CustomResourceDefinitions().Lister(),
		crdIndexer:        s.apiextensionsSharedInformerFactory.Apiextensions().V1().CustomResourceDefinitions().Informer().GetIndexer(),
		workspaceLister:   s.kcpSharedInformerFactory.Tenancy().V1alpha1().ClusterWorkspaces().Lister(),
		apiBindingLister:  s.kcpSharedInformerFactory.Apis().V1alpha1().APIBindings().Lister(),
		apiBindingIndexer: s.kcpSharedInformerFactory.Apis().V1alpha1().APIBindings().Informer().GetIndexer(),
		apiExportIndexer:  s.kcpSharedInformerFactory.Apis().V1alpha1().APIExports().Informer().GetIndexer(),
		getAPIResourceSchema: func(clusterName logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error) {
			key := clusters.ToClusterAwareKey(clusterName, name)
			return s.kcpSharedInformerFactory.Apis().V1alpha1().APIResourceSchemas().Lister().Get(key)
		},
	}
	apiExtensionsConfig.ExtraConfig.ClusterAwareCRDLister = apiBindingAwareCRDLister

	apiExtensionsConfig.ExtraConfig.TableConverterProvider = NewTableConverterProvider()

	serverChain, err := genericcontrolplane.CreateServerChain(apisConfig.Complete(), apiExtensionsConfig.Complete())
	if err != nil {
		return err
	}
	server := serverChain.MiniAggregator.GenericAPIServer
	serverChain.GenericControlPlane.GenericAPIServer.Handler.GoRestfulContainer.Filter(
		mergeCRDsIntoCoreGroup(
			apiBindingAwareCRDLister,
			serverChain.CustomResourceDefinitions.GenericAPIServer.Handler.NonGoRestfulMux.ServeHTTP,
			serverChain.GenericControlPlane.GenericAPIServer.Handler.GoRestfulContainer.ServeHTTP,
		),
	)

	s.dynamicDiscoverySharedInformerFactory, err = informer.NewDynamicDiscoverySharedInformerFactory(
		server.LoopbackClientConfig,
		func(obj interface{}) bool { return true },
		s.apiextensionsSharedInformerFactory.Apiextensions().V1().CustomResourceDefinitions(),
		indexers.NamespaceScoped(),
	)
	if err != nil {
		return err
	}

	s.AddPostStartHook("kcp-start-informers", func(ctx genericapiserver.PostStartHookContext) error {
		s.kubeSharedInformerFactory.Start(ctx.StopCh)
		s.apiextensionsSharedInformerFactory.Start(ctx.StopCh)
		s.rootKubeSharedInformerFactory.Start(ctx.StopCh)

		s.kubeSharedInformerFactory.WaitForCacheSync(ctx.StopCh)
		s.apiextensionsSharedInformerFactory.WaitForCacheSync(ctx.StopCh)
		s.rootKubeSharedInformerFactory.WaitForCacheSync(ctx.StopCh)

		select {
		case <-ctx.StopCh:
			return nil // context closed, avoid reporting success below
		default:
		}

		klog.Infof("Finished start kube informers")

		if err := systemcrds.Bootstrap(
			goContext(ctx),
			apiextensionsClusterClient.Cluster(SystemCRDLogicalCluster),
			apiextensionsClusterClient.Cluster(SystemCRDLogicalCluster).Discovery(),
			dynamicClusterClient.Cluster(SystemCRDLogicalCluster),
		); err != nil {
			klog.Errorf("failed to bootstrap system CRDs: %v", err)
			// nolint:nilerr
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}
		klog.Infof("Finished bootstrapping system CRDs")

		if err := configshard.Bootstrap(goContext(ctx), kcpClusterClient.Cluster(systemShardCluster)); err != nil {
			// nolint:nilerr
			klog.Errorf("Failed to bootstrap the shard workspace: %v", err)
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}
		klog.Infof("Finished bootstrapping the shard workspace")

		go s.kcpSharedInformerFactory.Apis().V1alpha1().APIExports().Informer().Run(ctx.StopCh)
		go s.kcpSharedInformerFactory.Apis().V1alpha1().APIBindings().Informer().Run(ctx.StopCh)

		if err := wait.PollInfiniteWithContext(goContext(ctx), time.Millisecond*100, func(ctx context.Context) (bool, error) {
			exportsSynced := s.kcpSharedInformerFactory.Apis().V1alpha1().APIExports().Informer().HasSynced()
			bindingsSynced := s.kcpSharedInformerFactory.Apis().V1alpha1().APIBindings().Informer().HasSynced()
			return exportsSynced && bindingsSynced, nil
		}); err != nil {
			klog.Errorf("failed to start APIExport and/or APIBinding informers: %v", err)
			// nolint:nilerr
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		klog.Infof("Finished starting APIExport and APIBinding informers")

		if s.options.Extra.ShardName == tenancyv1alpha1.RootShard {
			// bootstrap root workspace phase 0 only if we are on the root shard, no APIBinding resources yet
			if err := configrootphase0.Bootstrap(goContext(ctx),
				kcpClusterClient.Cluster(tenancyv1alpha1.RootCluster),
				apiextensionsClusterClient.Cluster(tenancyv1alpha1.RootCluster).Discovery(),
				dynamicClusterClient.Cluster(tenancyv1alpha1.RootCluster),
			); err != nil {
				// nolint:nilerr
				klog.Errorf("failed to bootstrap root workspace phase 0: %w", err)
				return nil // don't klog.Fatal. This only happens when context is cancelled.
			}
			klog.Infof("Bootstrapped root workspace phase 0")
		}

		klog.Infof("Getting kcp APIExport identities")

		if err := wait.PollImmediateInfiniteWithContext(goContext(ctx), time.Millisecond*500, func(ctx context.Context) (bool, error) {
			if err := resolveIdentities(ctx); err != nil {
				klog.V(3).Infof("failed to resolve identities, keeping trying: %v", err)
				return false, nil
			}
			return true, nil
		}); err != nil {
			klog.Errorf("failed to get or create identities: %v", err)
			// nolint:nilerr
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		klog.Infof("Finished getting kcp APIExport identities")

		s.kcpSharedInformerFactory.Start(ctx.StopCh)
		s.rootKcpSharedInformerFactory.Start(ctx.StopCh)

		s.kcpSharedInformerFactory.WaitForCacheSync(ctx.StopCh)
		s.rootKcpSharedInformerFactory.WaitForCacheSync(ctx.StopCh)

		select {
		case <-ctx.StopCh:
			return nil // context closed, avoid reporting success below
		default:
		}

		klog.Infof("Finished starting (remaining) kcp informers")

		klog.Infof("Starting dynamic metadata informer worker")
		go s.dynamicDiscoverySharedInformerFactory.StartWorker(goContext(ctx))

		klog.Infof("Synced all informers. Ready to start controllers")
		close(s.syncedCh)

		if s.options.Extra.ShardName == tenancyv1alpha1.RootShard {
			// the root ws is only present on the root shard
			klog.Infof("Starting bootstrapping root workspace phase 1")
			servingCert, _ := server.SecureServingInfo.Cert.CurrentCertKeyContent()
			if err := configroot.Bootstrap(goContext(ctx),
				apiextensionsClusterClient.Cluster(tenancyv1alpha1.RootCluster).Discovery(),
				dynamicClusterClient.Cluster(tenancyv1alpha1.RootCluster),
				s.options.Extra.ShardName,
				clientcmdapi.Config{
					Clusters: map[string]*clientcmdapi.Cluster{
						// cross-cluster is the virtual cluster running by default
						"shard": {
							Server:                   "https://" + server.ExternalAddress,
							CertificateAuthorityData: servingCert, // TODO(sttts): wire controller updating this when it changes, or use CA
						},
					},
					Contexts: map[string]*clientcmdapi.Context{
						"shard": {Cluster: "shard"},
					},
					CurrentContext: "shard",
				},
				logicalcluster.New(s.options.HomeWorkspaces.HomeRootPrefix).Base(),
				s.options.HomeWorkspaces.HomeCreatorGroups,
			); err != nil {
				// nolint:nilerr
				klog.Errorf("failed to bootstrap root workspace phase 1: %w", err)
				return nil // don't klog.Fatal. This only happens when context is cancelled.
			}
			klog.Infof("Finished bootstrapping root workspace phase 1")
		}

		return nil
	})

	// ========================================================================================================
	// TODO: split apart everything after this line, into their own commands, optional launched in this process

	controllerConfig := rest.CopyConfig(identityConfig)

	if err := s.installKubeNamespaceController(ctx, controllerConfig); err != nil {
		return err
	}

	if err := s.installClusterRoleAggregationController(ctx, controllerConfig); err != nil {
		return err
	}

	if err := s.installKubeServiceAccountController(ctx, controllerConfig); err != nil {
		return err
	}

	if err := s.installKubeServiceAccountTokenController(ctx, controllerConfig); err != nil {
		return err
	}

	if err := s.installRootCAConfigMapController(ctx, serverChain.GenericControlPlane.GenericAPIServer.LoopbackClientConfig); err != nil {
		return err
	}

	enabled := sets.NewString(s.options.Controllers.IndividuallyEnabled...)
	if len(enabled) > 0 {
		klog.Infof("Starting controllers individually: %v", enabled)
	}

	if s.options.Controllers.EnableAll || enabled.Has("cluster") {
		// TODO(marun) Consider enabling each controller via a separate flag

		if err := s.installApiResourceController(ctx, controllerConfig); err != nil {
			return err
		}
		if err := s.installSyncTargetHeartbeatController(ctx, controllerConfig); err != nil {
			return err
		}
		if err := s.installVirtualWorkspaceURLsController(ctx, controllerConfig, server); err != nil {
			return err
		}
	}

	if s.options.Controllers.EnableAll || enabled.Has("workspace-scheduler") {
		if err := s.installWorkspaceScheduler(ctx, controllerConfig); err != nil {
			return err
		}
		if err := s.installWorkspaceDeletionController(ctx, controllerConfig); err != nil {
			return err
		}
	}

	if s.options.HomeWorkspaces.Enabled {
		if err := s.installHomeWorkspaces(ctx, controllerConfig); err != nil {
			return err
		}
	}

	if s.options.Controllers.EnableAll || enabled.Has("resource-scheduler") {
		if err := s.installWorkloadResourceScheduler(ctx, controllerConfig, s.dynamicDiscoverySharedInformerFactory); err != nil {
			return err
		}
	}

	if s.options.Controllers.EnableAll || enabled.Has("apibinding") {
		if err := s.installAPIBindingController(ctx, controllerConfig, server, s.dynamicDiscoverySharedInformerFactory); err != nil {
			return err
		}
	}

	if s.options.Controllers.EnableAll || enabled.Has("apiexport") {
		if err := s.installAPIExportController(ctx, controllerConfig, server); err != nil {
			return err
		}
	}

	if utilfeature.DefaultFeatureGate.Enabled(kcpfeatures.LocationAPI) {
		if s.options.Controllers.EnableAll || enabled.Has("scheduling") {
			if err := s.installWorkloadNamespaceScheduler(ctx, controllerConfig, server); err != nil {
				return err
			}
			if err := s.installSchedulingLocationStatusController(ctx, controllerConfig, server); err != nil {
				return err
			}
			if err := s.installSchedulingPlacementController(ctx, controllerConfig, server); err != nil {
				return err
			}
			if err := s.installWorkloadsAPIExportController(ctx, controllerConfig, server); err != nil {
				return err
			}
			if err := s.installWorkloadsAPIExportCreateController(ctx, controllerConfig, server); err != nil {
				return err
			}
			if err := s.installDefaultPlacementController(ctx, controllerConfig, server); err != nil {
				return err
			}
		}
	}

	if s.options.Virtual.Enabled {
		if err := s.installVirtualWorkspaces(ctx, controllerConfig, server, genericConfig.Authentication, genericConfig.ExternalAddress, preHandlerChainMux); err != nil {
			return err
		}
	} else if err := s.installVirtualWorkspacesRedirect(ctx, preHandlerChainMux); err != nil {
		return err
	}

	// Add our custom hooks to the underlying api server
	for _, entry := range s.postStartHooks {
		err := server.AddPostStartHook(entry.name, entry.hook)
		if err != nil {
			return err
		}
	}
	for _, entry := range s.preShutdownHooks {
		err := server.AddPreShutdownHook(entry.name, entry.hook)
		if err != nil {
			return err
		}
	}

	if err := s.options.AdminAuthentication.WriteKubeConfig(genericConfig, kcpAdminToken, shardAdminToken, shardAdminTokenHash); err != nil {
		return err
	}

	return server.PrepareRun().Run(ctx.Done())
}

// AddPostStartHook allows you to add a PostStartHook that gets passed to the underlying genericapiserver implementation.
func (s *Server) AddPostStartHook(name string, hook genericapiserver.PostStartHookFunc) {
	// you could potentially add duplicate or invalid post start hooks here, but we'll let
	// the genericapiserver implementation do its own validation during startup.
	s.postStartHooks = append(s.postStartHooks, postStartHookEntry{
		name: name,
		hook: hook,
	})
}

// AddPreShutdownHook allows you to add a PreShutdownHookFunc that gets passed to the underlying genericapiserver implementation.
func (s *Server) AddPreShutdownHook(name string, hook genericapiserver.PreShutdownHookFunc) {
	// you could potentially add duplicate or invalid post start hooks here, but we'll let
	// the genericapiserver implementation do its own validation during startup.
	s.preShutdownHooks = append(s.preShutdownHooks, preShutdownHookEntry{
		name: name,
		hook: hook,
	})
}

// goContext turns the PostStartHookContext into a context.Context for use in routines that may or may not
// run inside of a post-start-hook. The k8s APIServer wrote the post-start-hook context code before contexts
// were part of the Go stdlib.
func goContext(parent genericapiserver.PostStartHookContext) context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	go func(done <-chan struct{}) {
		<-done
		cancel()
	}(parent.StopCh)
	return ctx
}

type handlerChainMuxes []*http.ServeMux

func (mxs handlerChainMuxes) Handle(pattern string, handler http.Handler) {
	for _, mx := range mxs {
		mx.Handle(pattern, handler)
	}
}
