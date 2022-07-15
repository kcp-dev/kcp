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
	"errors"
	"net/http"
	_ "net/http/pprof"
	"time"

	apiextensionsexternalversions "k8s.io/apiextensions-apiserver/pkg/client/informers/externalversions"
	"k8s.io/apimachinery/pkg/util/wait"
	genericapiserver "k8s.io/apiserver/pkg/server"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	coreexternalversions "k8s.io/client-go/informers"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/genericcontrolplane"

	configroot "github.com/kcp-dev/kcp/config/root"
	configrootphase0 "github.com/kcp-dev/kcp/config/root-phase0"
	systemcrds "github.com/kcp-dev/kcp/config/system-crds"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	bootstrappolicy "github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
	kcpexternalversions "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	kcpfeatures "github.com/kcp-dev/kcp/pkg/features"
	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/informer"
	"github.com/kcp-dev/kcp/pkg/server/options"
	"github.com/kcp-dev/logicalcluster"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

const resyncPeriod = 10 * time.Hour

// Server manages the configuration and kcp api-server. It allows callers to easily use kcp
// as a library rather than as a single binary. Using its constructor function, you can easily
// setup a new api-server and start it:
//
//   opts := options.NewOptions()
//   completedOptions := opts.Complete()
//   conf := config.NewConfig(completedOptions)
//   completedConfig := conf.Complete()
//   srv := server.NewServer(completedConfig)
//   srv.Init()
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
	initialized     bool
	config          *CompletedConfig
	genericServer   *genericapiserver.GenericAPIServer
	genericConfig   *genericapiserver.Config
	serverChain     *genericcontrolplane.ServerChain
	preHandlerChain handlerChainMuxes

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
func NewServer(c *CompletedConfig) (*Server, error) {
	if c == nil {
		return nil, errors.New("must pass valid CompletedConfig")
	}

	kcpSharedInformerFactory, kubeSharedInformerFactory, apiextensionsSharedInformerFactory, dynamicDiscoverySharedInformerFactory, rootKcpSharedInformerFactory, rootKubeSharedInformerFactory := c.Factories()
	dynamicClusterClient := c.DynamicClusterClient()
	resolveIdentities := c.ResolveIdentitiesFunc()
	apiextensionsClusterClient := c.APIExtensionsClusterClient()
	kcpClusterClient := c.KCPClusterClient()
	genericConfig := c.GenericConfig()

	serverChain, err := genericcontrolplane.CreateServerChain(c.apisConfig, c.apiExtensionsConfig)
	if err != nil {
		return nil, err
	}
	server := serverChain.MiniAggregator.GenericAPIServer
	serverChain.GenericControlPlane.GenericAPIServer.Handler.GoRestfulContainer.Filter(
		mergeCRDsIntoCoreGroup(
			c.APIBindingAwareCRDLister(),
			serverChain.CustomResourceDefinitions.GenericAPIServer.Handler.NonGoRestfulMux.ServeHTTP,
			serverChain.GenericControlPlane.GenericAPIServer.Handler.GoRestfulContainer.ServeHTTP,
		),
	)

	dynamicDiscoverySharedInformerFactory, err = informer.NewDynamicDiscoverySharedInformerFactory(
		server.LoopbackClientConfig,
		func(obj interface{}) bool { return true },
		apiextensionsSharedInformerFactory.Apiextensions().V1().CustomResourceDefinitions(),
		indexers.NamespaceScoped(),
	)
	if err != nil {
		return nil, err
	}

	server.AddPostStartHook("kcp-bootstrap-policy", bootstrappolicy.Policy().EnsureRBACPolicy())
	server.AddPostStartHook("kcp-start-informers", generateInformerStartAndWait("kube", kubeSharedInformerFactory, apiextensionsSharedInformerFactory, rootKubeSharedInformerFactory))
	server.AddPostStartHook("kcp-bootstrap-system-crds", func(ctx genericapiserver.PostStartHookContext) error {
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
		return nil
	})
	server.AddPostStartHook("kcp-run-api-informers", func(ctx genericapiserver.PostStartHookContext) error {
		go kcpSharedInformerFactory.Apis().V1alpha1().APIExports().Informer().Run(ctx.StopCh)
		go kcpSharedInformerFactory.Apis().V1alpha1().APIBindings().Informer().Run(ctx.StopCh)
		if err := wait.PollInfiniteWithContext(goContext(ctx), time.Millisecond*100, func(ctx context.Context) (bool, error) {
			exportsSynced := kcpSharedInformerFactory.Apis().V1alpha1().APIExports().Informer().HasSynced()
			bindingsSynced := kcpSharedInformerFactory.Apis().V1alpha1().APIBindings().Informer().HasSynced()
			return exportsSynced && bindingsSynced, nil
		}); err != nil {
			klog.Errorf("failed to start APIExport and/or APIBinding informers: %v", err)
			// nolint:nilerr
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		klog.Infof("Finished starting APIExport and APIBinding informers")
		return nil
	})
	server.AddPostStartHook("kcp-bootstrap-workspace-phase-0", func(ctx genericapiserver.PostStartHookContext) error {
		// bootstrap root workspace phase 0, no APIBinding resources yet
		if err := configrootphase0.Bootstrap(goContext(ctx),
			kcpClusterClient.Cluster(tenancyv1alpha1.RootCluster),
			apiextensionsClusterClient.Cluster(tenancyv1alpha1.RootCluster).Discovery(),
			dynamicClusterClient.Cluster(tenancyv1alpha1.RootCluster),
		); err != nil {
			// nolint:nilerr
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		klog.Infof("Bootstrapped root workspace phase 0")
		return nil
	})
	server.AddPostStartHook("kcp-get-api-export-identities", func(ctx genericapiserver.PostStartHookContext) error {
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
		return nil
	})
	server.AddPostStartHook("kcp-start-remaining-informers", generateInformerStartAndWait("remaining kcp", kcpSharedInformerFactory, rootKcpSharedInformerFactory))
	server.AddPostStartHook("kcp-bootstrap-workspace-phase-1", func(ctx genericapiserver.PostStartHookContext) error {
		klog.Infof("Starting bootstrapping root workspace phase 1")
		servingCert, _ := server.SecureServingInfo.Cert.CurrentCertKeyContent()
		if err := configroot.Bootstrap(goContext(ctx),
			apiextensionsClusterClient.Cluster(tenancyv1alpha1.RootCluster).Discovery(),
			dynamicClusterClient.Cluster(tenancyv1alpha1.RootCluster),
			c.ShardName(),
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
			logicalcluster.New(c.HomeWorkspaces().HomeRootPrefix).Base(),
			c.HomeWorkspaces().HomeCreatorGroups,
		); err != nil {
			// nolint:nilerr
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}
		klog.Infof("Finished bootstrapping root workspace phase 1")
		return nil
	})

	if err := c.WriteKubeConfig(); err != nil {
		return nil, err
	}

	s := &Server{
		kcpSharedInformerFactory:              kcpSharedInformerFactory,
		kubeSharedInformerFactory:             kubeSharedInformerFactory,
		apiextensionsSharedInformerFactory:    apiextensionsSharedInformerFactory,
		dynamicDiscoverySharedInformerFactory: dynamicDiscoverySharedInformerFactory,
		config:                                c,
		genericServer:                         server,
		genericConfig:                         genericConfig,
		preHandlerChain:                       c.PreHandlerChain(),
		serverChain:                           serverChain,
		syncedCh:                              make(chan struct{}),
	}
	return s, nil
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

// Init initializes the KCP api-server. It does not start it
func (s *Server) Init() error {
	s.initialized = true
	return nil
}

// Run starts the KCP api-server. This function blocks until the api-server stops or an error.
func (s *Server) Run(ctx context.Context) error {
	if !s.initialized {
		return errors.New("must initialize server with srv.Init() prior to Run()")
	}

	// start the profiler
	if s.config.ProfilerAddress() != "" {
		// nolint:errcheck
		go http.ListenAndServe(s.config.ProfilerAddress(), nil)
	}

	// if an embedded etcd server is configured, start it
	if s.config.EtcdServer != nil {
		if err := s.config.EtcdServer.Run(ctx); err != nil {
			return err
		}
	}

	// ========================================================================================================
	// TODO: split apart everything after this line, into their own commands, optional launched in this process

	controllerConfig := s.config.IdentityConfig()
	_, certFile := s.config.ServerKeyCert()
	if err := s.installControllers(ctx, controllerConfig, s.serverChain.GenericControlPlane.GenericAPIServer.LoopbackClientConfig, s.config, s.genericServer, s.config.Controllers(), certFile); err != nil {
		return err
	}

	if s.config.VirtualEnabled() {
		if err := s.installVirtualWorkspaces(ctx, controllerConfig, s.genericServer, s.genericConfig.Authentication, s.genericConfig.ExternalAddress, s.preHandlerChain, s.config.Virtual(), s.config.Authorization()); err != nil {
			return err
		}
	} else if err := s.installVirtualWorkspacesRedirect(ctx, s.preHandlerChain, s.config.Virtual()); err != nil {
		return err
	}
	s.genericServer.AddPostStartHook("kcp-start-dynamic-informer", func(ctx genericapiserver.PostStartHookContext) error {
		klog.Infof("Starting dynamic metadata informer")
		go s.dynamicDiscoverySharedInformerFactory.StartWorker(goContext(ctx))

		klog.Infof("Synced all informers. Ready to start controllers")
		close(s.syncedCh)
		return nil
	})

	// Add our custom hooks to the underlying api server
	// this does get a little odd, in that we add hooks in the Config, but also here. it may be unavoidable.
	for _, entry := range s.postStartHooks {
		err := s.genericServer.AddPostStartHook(entry.name, entry.hook)
		if err != nil {
			return err
		}
	}
	for _, entry := range s.preShutdownHooks {
		err := s.genericServer.AddPreShutdownHook(entry.name, entry.hook)
		if err != nil {
			return err
		}
	}

	return s.genericServer.PrepareRun().Run(ctx.Done())
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

func (s *Server) installControllers(ctx context.Context, controllerConfig, clientConfig *rest.Config, kcpConfig *CompletedConfig, server *genericapiserver.GenericAPIServer, controllers options.Controllers, certFile string) error {
	if err := s.installKubeNamespaceController(ctx, controllerConfig); err != nil {
		return err
	}

	if err := s.installClusterRoleAggregationController(ctx, controllerConfig); err != nil {
		return err
	}

	if err := s.installKubeServiceAccountController(ctx, controllerConfig); err != nil {
		return err
	}

	if err := s.installKubeServiceAccountTokenController(ctx, controllerConfig, controllers); err != nil {
		return err
	}

	if err := s.installRootCAConfigMapController(ctx, clientConfig, controllers, certFile); err != nil {
		return err
	}

	if kcpConfig.IsControllerEnabled("cluster") {
		// TODO(marun) Consider enabling each controller via a separate flag

		if err := s.installApiResourceController(ctx, controllerConfig); err != nil {
			return err
		}
		if err := s.installSyncTargetHeartbeatController(ctx, controllerConfig, controllers); err != nil {
			return err
		}
		if err := s.installVirtualWorkspaceURLsController(ctx, controllerConfig, server); err != nil {
			return err
		}
	}

	if kcpConfig.IsControllerEnabled("workspace-scheduler") {
		if err := s.installWorkspaceScheduler(ctx, controllerConfig); err != nil {
			return err
		}
		if err := s.installWorkspaceDeletionController(ctx, controllerConfig); err != nil {
			return err
		}
	}

	if err := s.installHomeWorkspaces(ctx, controllerConfig); err != nil {
		return err
	}

	if kcpConfig.IsControllerEnabled("resource-scheduler") {
		if err := s.installWorkloadResourceScheduler(ctx, controllerConfig, s.dynamicDiscoverySharedInformerFactory); err != nil {
			return err
		}
	}

	if kcpConfig.IsControllerEnabled("apibinding") {
		if err := s.installAPIBindingController(ctx, controllerConfig, server, s.dynamicDiscoverySharedInformerFactory); err != nil {
			return err
		}
	}

	if kcpConfig.IsControllerEnabled("apiexport") {
		if err := s.installAPIExportController(ctx, controllerConfig, server); err != nil {
			return err
		}
	}

	if utilfeature.DefaultFeatureGate.Enabled(kcpfeatures.LocationAPI) {
		if kcpConfig.IsControllerEnabled("scheduling") {
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
	return nil
}
