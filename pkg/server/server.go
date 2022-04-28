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
	"time"

	"github.com/kcp-dev/apimachinery/pkg/logicalcluster"

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
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/genericcontrolplane"

	configroot "github.com/kcp-dev/kcp/config/root"
	systemcrds "github.com/kcp-dev/kcp/config/system-crds"
	kcpadmissioninitializers "github.com/kcp-dev/kcp/pkg/admission/initializers"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/authentication"
	bootstrappolicy "github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kcpexternalversions "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	"github.com/kcp-dev/kcp/pkg/etcd"
	kcpserveroptions "github.com/kcp-dev/kcp/pkg/server/options"
	"github.com/kcp-dev/kcp/pkg/sharding"
)

const resyncPeriod = 10 * time.Hour

// Server manages the configuration and kcp api-server. It allows callers to easily use kcp
// as a library rather than as a single binary. Using its constructor function, you can easily
// setup a new api-server and start it:
//
//   srv := server.NewServer(server.DefaultConfig())
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

	kcpSharedInformerFactory           kcpexternalversions.SharedInformerFactory
	kubeSharedInformerFactory          coreexternalversions.SharedInformerFactory
	apiextensionsSharedInformerFactory apiextensionsexternalversions.SharedInformerFactory

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
		embeddedClientInfo, err := es.Run(ctx, s.options.EmbeddedEtcd.PeerPort, s.options.EmbeddedEtcd.ClientPort, s.options.EmbeddedEtcd.WalSizeBytes)
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

	// Setup kcp * informers
	kcpClusterClient, err := kcpclient.NewClusterForConfig(genericConfig.LoopbackClientConfig)
	if err != nil {
		return err
	}
	kcpClient := kcpClusterClient.Cluster(logicalcluster.Wildcard)
	s.kcpSharedInformerFactory = kcpexternalversions.NewSharedInformerFactoryWithOptions(kcpClient, resyncPeriod)

	// Setup kube * informers
	kubeClusterClient, err := kubernetes.NewClusterForConfig(genericConfig.LoopbackClientConfig)
	if err != nil {
		return err
	}
	kubeClient := kubeClusterClient.Cluster(logicalcluster.Wildcard)
	s.kubeSharedInformerFactory = coreexternalversions.NewSharedInformerFactoryWithOptions(kubeClient, resyncPeriod)

	// Setup apiextensions * informers
	apiextensionsClusterClient, err := apiextensionsclient.NewClusterForConfig(genericConfig.LoopbackClientConfig)
	if err != nil {
		return err
	}
	apiextensionsCrossClusterClient := apiextensionsClusterClient.Cluster(logicalcluster.Wildcard)
	s.apiextensionsSharedInformerFactory = apiextensionsexternalversions.NewSharedInformerFactoryWithOptions(apiextensionsCrossClusterClient, resyncPeriod)

	// Setup root informers
	s.rootKcpSharedInformerFactory = kcpexternalversions.NewSharedInformerFactoryWithOptions(kcpClusterClient.Cluster(v1alpha1.RootCluster), resyncPeriod)
	s.rootKubeSharedInformerFactory = coreexternalversions.NewSharedInformerFactoryWithOptions(kubeClusterClient.Cluster(v1alpha1.RootCluster), resyncPeriod)

	// Setup dynamic client
	dynamicClusterClient, err := dynamic.NewClusterForConfig(genericConfig.LoopbackClientConfig)
	if err != nil {
		return err
	}

	if err := s.options.Authorization.ApplyTo(genericConfig, s.kubeSharedInformerFactory, s.kcpSharedInformerFactory.Tenancy().V1alpha1().ClusterWorkspaces().Lister()); err != nil {
		return err
	}
	newTokenOrEmpty, tokenHash, err := s.options.AdminAuthentication.ApplyTo(genericConfig)
	if err != nil {
		return err
	}

	// create service-account-only authenticator without any lookup for objects, just to extract the logical cluster name from the JWT.
	// If the request hits us at a non-/clusters URL, we will re-add the /clusters/<cluster-name> prefix to the request. This is necessary
	// because a service account used by a InCluster client does not support the /clusters/<cluster-name> prefix.
	unsafeServiceAccountPreAuth, err := authentication.NewUnsafeNonLookupServiceAccountAuthenticator(s.options.GenericControlPlane.Authentication.ServiceAccounts.KeyFiles, s.options.GenericControlPlane.Authentication.ServiceAccounts.Issuers, s.options.GenericControlPlane.Authentication.APIAudiences)
	if err != nil {
		return err
	}

	// preHandlerChainMux is called before the actual handler chain. Note that BuildHandlerChainFunc below
	// is called multiple times, but only one of the handler chain will actually be used. Hence, we wrap it
	// to give handlers below one mux.Handle func to call.
	var preHandlerChainMux handlerChainMuxes
	genericConfig.BuildHandlerChainFunc = func(apiHandler http.Handler, c *genericapiserver.Config) (secure http.Handler) {
		// we want a request to hit the chain like:
		// - lcluster handler (this package's ServeHTTP)
		// - shard proxy (sharding.ServeHTTP)
		// - original handler chain
		// the lcluster handler is a pass-through, not a delegate, so the wrapping looks weird
		if s.options.Extra.EnableSharding {
			clientLoader := sharding.NewClientLoader()
			clientLoader.Add(genericConfig.ExternalAddress, genericConfig.LoopbackClientConfig)
			apiHandler = sharding.WithSharding(apiHandler, clientLoader)
		}
		apiHandler = WithWildcardListWatchGuard(apiHandler)
		apiHandler = genericapiserver.DefaultBuildHandlerChain(apiHandler, c)

		// this will be replaced in DefaultBuildHandlerChain. So at worst we get twice as many warning.
		// But this is not harmful as the kcp warnings are not many.
		apiHandler = filters.WithWarningRecorder(apiHandler)

		// add a mux before the chain, for other handlers with their own handler chain to hook in
		mux := http.NewServeMux()
		mux.Handle("/", apiHandler)
		preHandlerChainMux = append(preHandlerChainMux, mux)
		apiHandler = mux

		apiHandler = WithWorkspaceProjection(apiHandler)
		apiHandler = WithClusterScope(apiHandler)
		apiHandler = WithInClusterServiceAccountRequestRewrite(apiHandler, unsafeServiceAccountPreAuth)
		apiHandler = WithAcceptHeader(apiHandler)

		return apiHandler
	}

	admissionPluginInitializers := []admission.PluginInitializer{
		kcpadmissioninitializers.NewKcpInformersInitializer(s.kcpSharedInformerFactory),
		kcpadmissioninitializers.NewKubeClusterClientInitializer(kubeClusterClient),
		kcpadmissioninitializers.NewKcpClusterClientInitializer(kcpClusterClient),
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
	apiExtensionsConfig.ExtraConfig.Informers = &kcpAPIExtensionsSharedInformerFactory{
		SharedInformerFactory: s.apiextensionsSharedInformerFactory,
		kcpClusterClient:      kcpClusterClient,
		workspaceLister:       s.kcpSharedInformerFactory.Tenancy().V1alpha1().ClusterWorkspaces().Lister(),
		apiBindingLister:      s.kcpSharedInformerFactory.Apis().V1alpha1().APIBindings().Lister(),
	}
	// TODO(ncdc): I thought I was going to need this, but it turns out this breaks the CRD controllers because they
	// try to issue Update() calls using the * client, which ends up with the cluster name being set to the default
	// root cluster in handler.go. This means the update calls are likely going against the wrong logical cluster.
	//    apiExtensionsConfig.ExtraConfig.NewClientFunc = func(config *rest.Config) (apiextensionsclient.Interface, error) {
	//		crossClusterScope := controllerz.NewScope("*", controllerz.WildcardScope(true))
	//
	//		client, err := apiextensionsclient.NewScoperForConfig(config)
	//		if err != nil {
	//			return nil, err
	//		}
	//		return client.Scope(crossClusterScope), nil
	//	}

	serverChain, err := genericcontrolplane.CreateServerChain(apisConfig.Complete(), apiExtensionsConfig.Complete())
	if err != nil {
		return err
	}
	server := serverChain.MiniAggregator.GenericAPIServer
	serverChain.GenericControlPlane.GenericAPIServer.Handler.GoRestfulContainer.Filter(
		mergeCRDsIntoCoreGroup(
			serverChain.CustomResourceDefinitions.Informers.Apiextensions().V1().CustomResourceDefinitions().Lister(),
			serverChain.CustomResourceDefinitions.GenericAPIServer.Handler.NonGoRestfulMux.ServeHTTP,
			serverChain.GenericControlPlane.GenericAPIServer.Handler.GoRestfulContainer.ServeHTTP,
		),
	)

	s.AddPostStartHook("kcp-start-informers", func(ctx genericapiserver.PostStartHookContext) error {
		s.kubeSharedInformerFactory.Start(ctx.StopCh)
		s.apiextensionsSharedInformerFactory.Start(ctx.StopCh)
		s.rootKubeSharedInformerFactory.Start(ctx.StopCh)

		s.kubeSharedInformerFactory.WaitForCacheSync(ctx.StopCh)
		s.apiextensionsSharedInformerFactory.WaitForCacheSync(ctx.StopCh)
		s.rootKubeSharedInformerFactory.WaitForCacheSync(ctx.StopCh)

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

		s.kcpSharedInformerFactory.Start(ctx.StopCh)
		s.rootKcpSharedInformerFactory.Start(ctx.StopCh)

		s.kcpSharedInformerFactory.WaitForCacheSync(ctx.StopCh)
		s.rootKcpSharedInformerFactory.WaitForCacheSync(ctx.StopCh)

		klog.Infof("Finished start kcp informers")

		// bootstrap root workspace with workspace shard
		servingCert, _ := server.SecureServingInfo.Cert.CurrentCertKeyContent()
		if err := configroot.Bootstrap(goContext(ctx),
			apiextensionsClusterClient.Cluster(v1alpha1.RootCluster).Discovery(),
			dynamicClusterClient.Cluster(v1alpha1.RootCluster),
			"root",

			// TODO(sttts): move away from loopback, use external advertise address, an external CA and an access header enabled client servingCert for authentication
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
			}); err != nil {
			// nolint:nilerr
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		klog.Infof("Bootstrapped resources and synced all informers. Ready to start controllers")
		close(s.syncedCh)

		return nil
	})

	// ========================================================================================================
	// TODO: split apart everything after this line, into their own commands, optional launched in this process

	controllerConfig := rest.CopyConfig(server.LoopbackClientConfig)

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
		if err := s.installWorkloadClusterHeartbeatController(ctx, controllerConfig); err != nil {
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

	if s.options.Controllers.EnableAll || enabled.Has("namespace-scheduler") {
		if err := s.installWorkloadNamespaceScheduler(ctx, controllerConfig); err != nil {
			return err
		}
	}

	if s.options.Controllers.EnableAll || enabled.Has("apibinding") {
		if err := s.installAPIBindingController(ctx, controllerConfig, server); err != nil {
			return err
		}
	}

	if s.options.Virtual.Enabled {
		if err := s.installVirtualWorkspaces(ctx, kubeClusterClient, kcpClusterClient, genericConfig.Authentication, genericConfig.ExternalAddress, preHandlerChainMux); err != nil {
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

	if err := s.options.AdminAuthentication.WriteKubeConfig(genericConfig, newTokenOrEmpty, tokenHash); err != nil {
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
