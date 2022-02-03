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
	"path/filepath"
	"time"

	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apiextensionsexternalversions "k8s.io/apiextensions-apiserver/pkg/client/informers/externalversions"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apiserver/pkg/authorization/union"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/apiserver/pkg/util/webhook"
	coreexternalversions "k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/kubernetes/pkg/genericcontrolplane"

	"github.com/kcp-dev/kcp/pkg/authorization"
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

	crdServerReady chan struct{}

	kcpSharedInformerFactory           kcpexternalversions.SharedInformerFactory
	kubeSharedInformerFactory          coreexternalversions.SharedInformerFactory
	apiextensionsSharedInformerFactory apiextensionsexternalversions.SharedInformerFactory
}

// NewServer creates a new instance of Server which manages the KCP api-server.
func NewServer(o *kcpserveroptions.CompletedOptions) (*Server, error) {
	return &Server{
		options:        o,
		crdServerReady: make(chan struct{}),
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

	etcdDir := filepath.Join(s.options.Extra.RootDirectory, s.options.EmbeddedEtcd.Directory)
	if s.options.EmbeddedEtcd.Enabled {
		es := &etcd.Server{
			Dir: etcdDir,
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

	// patch the handler chain. We should do this after creating the generic apiserver config, but before CreateKubeAPIServerConfig. But
	// this needs surgery in k/k. So we do it here no, changing the already completed options. Not nice, but must be here because
	// we need to create the injector to be in-scope with the NewNonInteractiveClientConfig further down in this func.
	injector := make(chan sharding.IdentifiedConfig)
	clientLoader, err := sharding.New(s.options.Extra.ShardKubeconfigFile, injector)
	if err != nil {
		return err
	}
	s.options.GenericControlPlane.BuildHandlerChainFunc = func(apiHandler http.Handler, c *genericapiserver.Config) (secure http.Handler) {
		// we want a request to hit the chain like:
		// - lcluster handler (this package's ServeHTTP)
		// - shard proxy (sharding.ServeHTTP)
		// - original handler chain
		// the lcluster handler is a pass-through, not a delegate, so the wrapping looks weird
		if s.options.Extra.EnableSharding {
			apiHandler = http.HandlerFunc(sharding.ServeHTTP(apiHandler, clientLoader))
		}
		apiHandler = WithClusterScope(genericapiserver.DefaultBuildHandlerChain(apiHandler, c))

		return apiHandler
	}

	// TODO: get rid of this idem-potent completion call. The o.GenericControlPlane value should be completed already.
	completedGenericControlPlane, err := genericcontrolplane.Complete(&s.options.GenericControlPlane.ServerRunOptions)
	if err != nil {
		return err
	}

	apisConfig, _, pluginInitializer, err := genericcontrolplane.CreateKubeAPIServerConfig(completedGenericControlPlane)
	if err != nil {
		return err
	}

	const crossCluster = "*"

	// Setup kcp * informers
	kcpClusterClient, err := kcpclient.NewClusterForConfig(apisConfig.GenericConfig.LoopbackClientConfig)
	if err != nil {
		return err
	}
	kcpClient := kcpClusterClient.Cluster(crossCluster)
	s.kcpSharedInformerFactory = kcpexternalversions.NewSharedInformerFactoryWithOptions(kcpClient, resyncPeriod)

	// Setup kube * informers
	kubeClusterClient, err := kubernetes.NewClusterForConfig(apisConfig.GenericConfig.LoopbackClientConfig)
	if err != nil {
		return err
	}
	kubeClient := kubeClusterClient.Cluster(crossCluster)
	s.kubeSharedInformerFactory = coreexternalversions.NewSharedInformerFactoryWithOptions(kubeClient, resyncPeriod)

	// Setup apiextensions * informers
	apiextensionsClusterClient, err := apiextensionsclient.NewClusterForConfig(apisConfig.GenericConfig.LoopbackClientConfig)
	if err != nil {
		return err
	}
	apiextensionsClient := apiextensionsClusterClient.Cluster(crossCluster)
	s.apiextensionsSharedInformerFactory = apiextensionsexternalversions.NewSharedInformerFactoryWithOptions(apiextensionsClient, resyncPeriod)

	s.AddPostStartHook("start-informers", func(context genericapiserver.PostStartHookContext) error {
		if err := s.waitForCRDServer(context.StopCh); err != nil {
			return err
		}

		s.kcpSharedInformerFactory.Start(context.StopCh)
		s.kubeSharedInformerFactory.Start(context.StopCh)
		s.apiextensionsSharedInformerFactory.Start(context.StopCh)

		s.kcpSharedInformerFactory.WaitForCacheSync(context.StopCh)
		s.kubeSharedInformerFactory.WaitForCacheSync(context.StopCh)
		s.apiextensionsSharedInformerFactory.WaitForCacheSync(context.StopCh)

		return nil
	})

	s.AddPostStartHook("kcp-bootstrap-policy", bootstrappolicy.Policy().EnsureRBACPolicy())

	// If additional API servers are added, they should be gated.
	apiExtensionsConfig, err := genericcontrolplane.CreateAPIExtensionsConfig(
		*apisConfig.GenericConfig,
		apisConfig.ExtraConfig.VersionedInformers,
		pluginInitializer,
		completedGenericControlPlane.ServerRunOptions,

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

	workspaceLister := s.kcpSharedInformerFactory.Tenancy().V1alpha1().Workspaces().Lister()
	apiExtensionsConfig.ExtraConfig.NewInformerFactoryFunc = func(client apiextensionsclient.Interface, resyncPeriod time.Duration) apiextensionsexternalversions.SharedInformerFactory {
		// TODO could we use s.apiextensionsSharedInformerFactory (ignoring client & resyncPeriod) instead of creating a 2nd factory here?
		f := apiextensionsexternalversions.NewSharedInformerFactory(client, resyncPeriod)
		return &kcpAPIExtensionsSharedInformerFactory{
			SharedInformerFactory: f,
			workspaceLister:       workspaceLister,
		}
	}
	// TODO(ncdc): I thought I was going to need this, but it turns out this breaks the CRD controllers because they
	// try to issue Update() calls using the * client, which ends up with the cluster name being set to the default
	// admin cluster in handler.go. This means the update calls are likely going against the wrong logical cluster.
	//    apiExtensionsConfig.ExtraConfig.NewClientFunc = func(config *rest.Config) (apiextensionsclient.Interface, error) {
	//		crossClusterScope := controllerz.NewScope("*", controllerz.WildcardScope(true))
	//
	//		client, err := apiextensionsclient.NewScoperForConfig(config)
	//		if err != nil {
	//			return nil, err
	//		}
	//		return client.Scope(crossClusterScope), nil
	//	}

	// wire authz
	orgAuth, orgResolver := authorization.NewOrgWorkspaceAuthorizer(s.kubeSharedInformerFactory)
	localAuth, localResolver := authorization.NewLocalAuthorizer(s.kubeSharedInformerFactory)
	apisConfig.GenericConfig.RuleResolver = union.NewRuleResolvers(orgResolver, localResolver)
	apisConfig.GenericConfig.Authorization.Authorizer = authorization.NewWorkspaceContentAuthorizer(
		s.kubeSharedInformerFactory,
		workspaceLister,
		union.New(orgAuth, localAuth),
	)

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

	s.AddPostStartHook("wait-for-crd-server", func(ctx genericapiserver.PostStartHookContext) error {
		return wait.PollImmediateInfiniteWithContext(goContext(ctx), 100*time.Millisecond, func(c context.Context) (done bool, err error) {
			if serverChain.CustomResourceDefinitions.Informers.Apiextensions().V1().CustomResourceDefinitions().Informer().HasSynced() {
				close(s.crdServerReady)
				return true, nil
			}
			return false, nil
		})
	})

	//Create Client and Shared
	var clientConfig clientcmdapi.Config
	clientConfig.AuthInfos = map[string]*clientcmdapi.AuthInfo{
		"loopback": {Token: server.LoopbackClientConfig.BearerToken},
	}
	clientConfig.Clusters = map[string]*clientcmdapi.Cluster{
		// cross-cluster is the virtual cluster running by default
		"cross-cluster": {
			Server:                   server.LoopbackClientConfig.Host + "/clusters/*",
			CertificateAuthorityData: server.LoopbackClientConfig.CAData,
			TLSServerName:            server.LoopbackClientConfig.TLSClientConfig.ServerName,
		},
		// admin is the virtual cluster running by default
		"admin": {
			Server:                   server.LoopbackClientConfig.Host,
			CertificateAuthorityData: server.LoopbackClientConfig.CAData,
			TLSServerName:            server.LoopbackClientConfig.TLSClientConfig.ServerName,
		},
		// user is a virtual cluster that is lazily instantiated
		"user": {
			Server:                   server.LoopbackClientConfig.Host + "/clusters/user",
			CertificateAuthorityData: server.LoopbackClientConfig.CAData,
			TLSServerName:            server.LoopbackClientConfig.TLSClientConfig.ServerName,
		},
	}
	clientConfig.Contexts = map[string]*clientcmdapi.Context{
		"cross-cluster": {Cluster: "cross-cluster", AuthInfo: "loopback"},
		"admin":         {Cluster: "admin", AuthInfo: "loopback"},
		"user":          {Cluster: "user", AuthInfo: "loopback"},
	}
	clientConfig.CurrentContext = "admin"

	// ========================================================================================================
	// TODO: split apart everything after this line, into their own commands, optional launched in this process

	if s.options.Extra.EnableSharding {
		adminConfig, err := clientcmd.NewNonInteractiveClientConfig(clientConfig, "admin", &clientcmd.ConfigOverrides{}, nil).ClientConfig()
		if err != nil {
			return err
		}
		injector <- sharding.IdentifiedConfig{
			Identifier: server.ExternalAddress,
			Config:     adminConfig,
		}
	}

	if err := clientcmd.WriteToFile(clientConfig, filepath.Join(s.options.Extra.RootDirectory, s.options.Extra.KubeConfigPath)); err != nil {
		return err
	}

	if err := s.installKubeNamespaceController(serverChain.GenericControlPlane.GenericAPIServer.LoopbackClientConfig); err != nil {
		return err
	}

	if err := s.installClusterRoleAggregationController(serverChain.GenericControlPlane.GenericAPIServer.LoopbackClientConfig); err != nil {
		return err
	}

	enabled := sets.NewString(s.options.Controllers.IndividuallyEnabled...)

	if s.options.Controllers.EnableAll || enabled.Has("cluster") {
		if err := s.installClusterController(clientConfig, server); err != nil {
			return err
		}
	}

	if s.options.Controllers.EnableAll || enabled.Has("workspace-scheduler") {
		if err := s.installWorkspaceScheduler(ctx, clientConfig, server); err != nil {
			return err
		}
	}

	if s.options.Controllers.EnableAll || enabled.Has("namespace-scheduler") {
		if err := s.installNamespaceScheduler(ctx, workspaceLister, clientConfig, server); err != nil {
			return err
		}
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
