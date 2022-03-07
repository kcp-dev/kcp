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
	"os"
	"time"

	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apiextensionsexternalversions "k8s.io/apiextensions-apiserver/pkg/client/informers/externalversions"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/admission"
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
	kcpadmissioninitializers "github.com/kcp-dev/kcp/pkg/admission/initializers"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1/helper"
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
	rootKcpSharedInformerFactory       kcpexternalversions.SharedInformerFactory
	kubeSharedInformerFactory          coreexternalversions.SharedInformerFactory
	rootKubeSharedInformerFactory      coreexternalversions.SharedInformerFactory
	apiextensionsSharedInformerFactory apiextensionsexternalversions.SharedInformerFactory
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

	if dir := s.options.Extra.RootDirectory; len(dir) != 0 {
		if fi, err := os.Stat(dir); err != nil {
			if !os.IsNotExist(err) {
				return err
			}
			if err := os.MkdirAll(dir, 0755); err != nil {
				return err
			}
		} else {
			if !fi.IsDir() {
				return fmt.Errorf("%q is a file, please delete or select another location", dir)
			}
		}
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

	const crossCluster = "*"

	// Setup kcp * informers
	kcpClusterClient, err := kcpclient.NewClusterForConfig(genericConfig.LoopbackClientConfig)
	if err != nil {
		return err
	}
	kcpClient := kcpClusterClient.Cluster(crossCluster)
	s.kcpSharedInformerFactory = kcpexternalversions.NewSharedInformerFactoryWithOptions(kcpClient, resyncPeriod)

	// Setup kube * informers
	kubeClusterClient, err := kubernetes.NewClusterForConfig(genericConfig.LoopbackClientConfig)
	if err != nil {
		return err
	}
	kubeClient := kubeClusterClient.Cluster(crossCluster)
	s.kubeSharedInformerFactory = coreexternalversions.NewSharedInformerFactoryWithOptions(kubeClient, resyncPeriod)

	// Setup apiextensions * informers
	apiextensionsClusterClient, err := apiextensionsclient.NewClusterForConfig(genericConfig.LoopbackClientConfig)
	if err != nil {
		return err
	}
	apiextensionsCrossClusterClient := apiextensionsClusterClient.Cluster(crossCluster)
	s.apiextensionsSharedInformerFactory = apiextensionsexternalversions.NewSharedInformerFactoryWithOptions(apiextensionsCrossClusterClient, resyncPeriod)

	// Setup root informers
	s.rootKcpSharedInformerFactory = kcpexternalversions.NewSharedInformerFactoryWithOptions(kcpClusterClient.Cluster(helper.RootCluster), resyncPeriod)
	s.rootKubeSharedInformerFactory = coreexternalversions.NewSharedInformerFactoryWithOptions(kubeClusterClient.Cluster(helper.RootCluster), resyncPeriod)

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
	servingOpts := s.options.GenericControlPlane.SecureServing
	if err := s.options.AdminAuthentication.WriteKubeConfig(genericConfig, newTokenOrEmpty, tokenHash, s.options.GenericControlPlane.GenericServerRunOptions.ExternalHost, servingOpts.BindPort); err != nil {
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
			clientLoader.Add(s.options.GenericControlPlane.GenericServerRunOptions.ExternalHost, genericConfig.LoopbackClientConfig)
			apiHandler = sharding.WithSharding(apiHandler, clientLoader)
		}
		apiHandler = WithWildcardListWatchGuard(apiHandler)
		apiHandler = WithClusterScope(genericapiserver.DefaultBuildHandlerChain(apiHandler, c))

		// add a mux before the chain, for other handlers with their own handler chain to hook in
		mux := http.NewServeMux()
		mux.Handle("/", apiHandler)
		preHandlerChainMux = append(preHandlerChainMux, mux)
		return mux
	}

	admissionPluginInitializers := []admission.PluginInitializer{
		kcpadmissioninitializers.NewKcpInformersInitializer(s.kcpSharedInformerFactory),
		kcpadmissioninitializers.NewKubeClusterClientInitializer(kubeClusterClient),
		kcpadmissioninitializers.NewKcpClusterClientInitializer(kcpClusterClient),
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
	apiExtensionsConfig.ExtraConfig.NewInformerFactoryFunc = func(client apiextensionsclient.Interface, resyncPeriod time.Duration) apiextensionsexternalversions.SharedInformerFactory {
		// TODO could we use s.apiextensionsSharedInformerFactory (ignoring client & resyncPeriod) instead of creating a 2nd factory here?
		f := apiextensionsexternalversions.NewSharedInformerFactory(client, resyncPeriod)
		return &kcpAPIExtensionsSharedInformerFactory{
			SharedInformerFactory: f,
			workspaceLister:       s.kcpSharedInformerFactory.Tenancy().V1alpha1().ClusterWorkspaces().Lister(),
		}
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
		s.kcpSharedInformerFactory.Start(ctx.StopCh)
		s.rootKubeSharedInformerFactory.Start(ctx.StopCh)
		s.rootKcpSharedInformerFactory.Start(ctx.StopCh)

		s.apiextensionsSharedInformerFactory.WaitForCacheSync(ctx.StopCh)
		// wait for CRD inheritance work through the custom informer
		// TODO: merge with upper s.apiextensionsSharedInformerFactory
		serverChain.CustomResourceDefinitions.Informers.WaitForCacheSync(ctx.StopCh)

		// bootstrap root workspace with workspace shard
		servingCert, _ := server.SecureServingInfo.Cert.CurrentCertKeyContent()
		if err := configroot.Bootstrap(goContext(ctx),
			apiextensionsClusterClient.Cluster(helper.RootCluster),
			dynamicClusterClient.Cluster(helper.RootCluster),
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

		s.kubeSharedInformerFactory.WaitForCacheSync(ctx.StopCh)
		s.kcpSharedInformerFactory.WaitForCacheSync(ctx.StopCh)
		s.rootKubeSharedInformerFactory.WaitForCacheSync(ctx.StopCh)
		s.rootKcpSharedInformerFactory.WaitForCacheSync(ctx.StopCh)

		klog.Infof("Bootstrapped CRDs and synced all informers. Ready to start controllers")
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

		if err := s.installApiImportController(ctx, controllerConfig); err != nil {
			return err
		}

		// TODO(sttts): this is a hack, using the loopback config as a blueprint. Syncer should never use a loopback connection.
		if err := s.installSyncerController(ctx, controllerConfig, CreateLoopbackUpstreamKubeConfig(server)); err != nil {
			return err
		}
		if err := s.installApiResourceController(ctx, controllerConfig); err != nil {
			return err
		}
	}

	if s.options.Controllers.EnableAll || enabled.Has("workspace-scheduler") {
		if err := s.installWorkspaceScheduler(ctx, controllerConfig); err != nil {
			return err
		}
	}

	if s.options.Controllers.EnableAll || enabled.Has("namespace-scheduler") {
		if err := s.installNamespaceScheduler(ctx, controllerConfig); err != nil {
			return err
		}
	}

	if s.options.Controllers.EnableAll || enabled.Has("apibinding") {
		if err := s.installAPIBindingController(ctx, controllerConfig, server); err != nil {
			return err
		}
	}

	if s.options.Virtual.Enabled {
		if err := s.installVirtualWorkspaces(ctx, kubeClusterClient, kcpClusterClient, genericConfig.Authentication, preHandlerChainMux); err != nil {
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
