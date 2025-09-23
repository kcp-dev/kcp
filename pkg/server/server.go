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
	"net/url"
	"os"
	"time"

	extensionsapiserver "k8s.io/apiextensions-apiserver/pkg/apiserver"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/apimachinery/pkg/util/wait"
	genericapifilters "k8s.io/apiserver/pkg/endpoints/filters"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/apiserver/pkg/util/notfoundhandler"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/klog/v2"
	controlplaneapiserver "k8s.io/kubernetes/pkg/controlplane/apiserver"
	"k8s.io/kubernetes/pkg/controlplane/apiserver/miniaggregator"
	autoscalingrest "k8s.io/kubernetes/pkg/registry/autoscaling/rest"
	flowcontrolrest "k8s.io/kubernetes/pkg/registry/flowcontrol/rest"

	"github.com/kcp-dev/logicalcluster/v3"

	configroot "github.com/kcp-dev/kcp/config/root"
	configrootidentities "github.com/kcp-dev/kcp/config/root-identities"
	configrootidentitiesns "github.com/kcp-dev/kcp/config/root-identities/namespace"
	configrootphase0 "github.com/kcp-dev/kcp/config/root-phase0"
	configshard "github.com/kcp-dev/kcp/config/shard"
	systemcrds "github.com/kcp-dev/kcp/config/system-crds"
	bootstrappolicy "github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
	"github.com/kcp-dev/kcp/pkg/informer"
	metadataclient "github.com/kcp-dev/kcp/pkg/metadata"
	"github.com/kcp-dev/kcp/pkg/reconciler/cache/replication"
	"github.com/kcp-dev/kcp/pkg/reconciler/dynamicrestmapper"
	"github.com/kcp-dev/kcp/pkg/reconciler/kubequota"
	"github.com/kcp-dev/kcp/pkg/server/options/batteries"
	"github.com/kcp-dev/kcp/pkg/server/virtualresources"
	virtualrootapiserver "github.com/kcp-dev/kcp/pkg/virtual/framework/rootapiserver"
	"github.com/kcp-dev/kcp/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"

	_ "net/http/pprof"
)

const resyncPeriod = 10 * time.Hour

type Server struct {
	CompletedConfig

	ApiExtensions    *extensionsapiserver.CustomResourceDefinitions
	Apis             *controlplaneapiserver.Server
	VirtualResources *virtualresources.Server
	MiniAggregator   *miniaggregator.MiniAggregatorServer
	virtual          *virtualrootapiserver.Server
	// DynRESTMapper is a workspace-aware REST mapper, backed by a reconciler,
	// which dynamically loads all bound resources through every type associated
	// with an APIBinding in the workspace into the mapper. Another controller can
	// use this to resolve the Kind/Resource of the objects.
	DynRESTMapper *dynamicrestmapper.DynamicRESTMapper

	syncedCh             chan struct{}
	rootPhase1FinishedCh chan struct{}

	controllers map[string]*controllerWrapper
}

func (s *Server) AddPostStartHook(name string, hook genericapiserver.PostStartHookFunc) error {
	return s.MiniAggregator.GenericAPIServer.AddPostStartHook(name, hook)
}

func (s *Server) AddPreShutdownHook(name string, hook genericapiserver.PreShutdownHookFunc) error {
	return s.MiniAggregator.GenericAPIServer.AddPreShutdownHook(name, hook)
}

func NewServer(c CompletedConfig) (*Server, error) {
	s := &Server{
		CompletedConfig:      c,
		syncedCh:             make(chan struct{}),
		rootPhase1FinishedCh: make(chan struct{}),
		controllers:          make(map[string]*controllerWrapper),
	}

	notFoundHandler := notfoundhandler.New(c.GenericConfig.Serializer, genericapifilters.NoMuxAndDiscoveryIncompleteKey)
	var err error
	s.ApiExtensions, err = c.ApiExtensions.New(genericapiserver.NewEmptyDelegateWithCustomHandler(notFoundHandler))
	if err != nil {
		return nil, fmt.Errorf("create api extensions: %v", err)
	}

	s.VirtualResources, err = virtualresources.NewServer(c.VirtualResources, s.ApiExtensions.GenericAPIServer)
	if err != nil {
		return nil, fmt.Errorf("failed to create virtual resources server: %v", err)
	}

	s.Apis, err = c.Apis.New("generic-control-plane", s.VirtualResources.GenericAPIServer)
	if err != nil {
		return nil, fmt.Errorf("failed to create generic controlplane apiserver: %w", err)
	}

	// TODO(sttts): that discovery client is used for CEL admission. It looks up
	//              resources to admit I believe. So that probably must be scoped.
	allStorageProviders, err := c.Apis.GenericStorageProviders(s.KcpClusterClient.Cluster(controlplaneapiserver.LocalAdminCluster.Path()).Discovery())
	if err != nil {
		return nil, fmt.Errorf("failed to create storage providers: %w", err)
	}
	var storageProviders []controlplaneapiserver.RESTStorageProvider
	for i := range allStorageProviders {
		switch allStorageProviders[i].(type) {
		case flowcontrolrest.RESTStorageProvider:
		// TODO(sttts): remove this code when P&F is wired
		case autoscalingrest.RESTStorageProvider:
		default:
			storageProviders = append(storageProviders, allStorageProviders[i])
		}
	}
	if err := s.Apis.InstallAPIs(storageProviders...); err != nil {
		return nil, fmt.Errorf("failed to install APIs: %w", err)
	}

	if err := s.openAPIv3ServiceCache.RegisterStaticAPIs(s.Apis.GenericAPIServer.Handler.GoRestfulContainer); err != nil {
		return nil, err
	}
	s.openAPIv3ServiceCache.RegisterVRSpecsGetter(s.VirtualResources.OpenAPIv3SpecGetter())

	s.MiniAggregator, err = c.MiniAggregator.New(s.Apis.GenericAPIServer, s.Apis, s.ApiExtensions, s.VirtualResources.GenericAPIServer)
	if err != nil {
		return nil, err
	}

	s.Apis.GenericAPIServer.Handler.GoRestfulContainer.Filter(
		mergeCRDsIntoCoreGroup(
			s.ApiExtensions.ClusterAwareCRDLister,
			s.ApiExtensions.GenericAPIServer.Handler.NonGoRestfulMux.ServeHTTP,
			s.Apis.GenericAPIServer.Handler.GoRestfulContainer.ServeHTTP,
		),
	)

	metadataClusterClient, err := metadataclient.NewDynamicMetadataClusterClientForConfig(
		rest.AddUserAgent(rest.CopyConfig(s.MiniAggregator.GenericAPIServer.LoopbackClientConfig), "kcp-partial-metadata-informers"))
	if err != nil {
		return nil, err
	}

	crdGVRSource, err := informer.NewCRDGVRSource(s.ApiExtensionsSharedInformerFactory.Apiextensions().V1().CustomResourceDefinitions().Informer())
	if err != nil {
		return nil, err
	}

	s.DiscoveringDynamicSharedInformerFactory, err = informer.NewDiscoveringDynamicSharedInformerFactory(
		metadataClusterClient,
		func(obj interface{}) bool { return true },
		nil,
		crdGVRSource,
		cache.Indexers{},
	)
	if err != nil {
		return nil, err
	}

	if c.Options.Virtual.Enabled {
		s.virtual, err = c.OptionalVirtual.NewServer(s.preHandlerChainMux)
		if err != nil {
			return nil, err
		}
		if err := s.AddPostStartHook("kcp-start-virtual-workspaces", func(ctx genericapiserver.PostStartHookContext) error {
			s.virtual.GenericAPIServer.RunPostStartHooks(ctx)
			return nil
		}); err != nil {
			return nil, err
		}
	}

	// Controllers are free to pull in DynRESTMapper and query for types.
	s.DynRESTMapper = dynamicrestmapper.NewDynamicRESTMapper(nil)

	return s, nil
}

/* Deregister the controllers when the leader election is lost. */
func (s *Server) uninstallControllers() {
	for key := range s.controllers {
		delete(s.controllers, key)
	}
}

/* Registering all controllers and informers before starting informers. */
func (s *Server) installControllers(ctx context.Context, controllerConfig *rest.Config, gvrs map[schema.GroupVersionResource]replication.ReplicatedGVR) error {
	logger := klog.FromContext(ctx).WithValues("component", "kcp")
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

	if err := s.installRootCAConfigMapController(ctx, s.Apis.GenericAPIServer.LoopbackClientConfig); err != nil {
		return err
	}

	if err := s.installKubeValidatingAdmissionPolicyStatusController(ctx, controllerConfig); err != nil {
		return err
	}

	if err := s.installApiExportIdentityController(ctx, controllerConfig); err != nil {
		return err
	}
	if err := s.installReplicationController(ctx, controllerConfig, gvrs); err != nil {
		return err
	}

	enabled := sets.New[string](s.Options.Controllers.IndividuallyEnabled...)
	if len(enabled) > 0 {
		logger.WithValues("controllers", enabled).Info("starting controllers individually")
	}

	if s.Options.Controllers.EnableAll || enabled.Has("workspace-scheduler") {
		if err := s.installWorkspaceScheduler(ctx, controllerConfig, s.LogicalClusterAdminConfig, s.ExternalLogicalClusterAdminConfig); err != nil {
			return err
		}
		if err := s.installWorkspaceMountsScheduler(ctx, controllerConfig); err != nil {
			return err
		}
		if err := s.installTenancyLogicalClusterController(ctx, controllerConfig); err != nil {
			return err
		}
		if err := s.installLogicalClusterDeletionController(ctx, controllerConfig, s.LogicalClusterAdminConfig, s.ExternalLogicalClusterAdminConfig); err != nil {
			return err
		}
		if err := s.installLogicalCluster(ctx, controllerConfig); err != nil {
			return err
		}
	}

	if s.Options.Controllers.EnableAll || enabled.Has("apibinding") {
		if err := s.installAPIBindingController(ctx, controllerConfig, s.DiscoveringDynamicSharedInformerFactory); err != nil {
			return err
		}
		if err := s.installCRDCleanupController(ctx, controllerConfig); err != nil {
			return err
		}
		if err := s.installLogicalClusterCleanupController(ctx, controllerConfig); err != nil {
			return err
		}
		if err := s.installExtraAnnotationSyncController(ctx, controllerConfig); err != nil {
			return err
		}
	}

	if s.Options.Controllers.EnableAll || enabled.Has("apiexport") {
		if err := s.installAPIExportController(ctx, controllerConfig); err != nil {
			return err
		}
	}

	if s.Options.Controllers.EnableAll || enabled.Has("apisreplicateclusterrole") {
		if err := s.installApisReplicateClusterRoleControllers(ctx, controllerConfig); err != nil {
			return err
		}
	}

	if s.Options.Controllers.EnableAll || enabled.Has("apisreplicateclusterrolebinding") {
		if err := s.installApisReplicateClusterRoleBindingControllers(ctx, controllerConfig); err != nil {
			return err
		}
	}

	if s.Options.Controllers.EnableAll || enabled.Has("apisreplicatelogicalcluster") {
		if err := s.installApisReplicateLogicalClusterControllers(ctx, controllerConfig); err != nil {
			return err
		}
	}

	if s.Options.Controllers.EnableAll || enabled.Has("tenancyreplicatelogicalcluster") {
		if err := s.installTenancyReplicateLogicalClusterControllers(ctx, controllerConfig); err != nil {
			return err
		}
	}

	if s.Options.Controllers.EnableAll || enabled.Has("corereplicateclusterrole") {
		if err := s.installCoreReplicateClusterRoleControllers(ctx, controllerConfig); err != nil {
			return err
		}
	}

	if s.Options.Controllers.EnableAll || enabled.Has("corereplicateclusterrolebinding") {
		if err := s.installCoreReplicateClusterRoleBindingControllers(ctx, controllerConfig); err != nil {
			return err
		}
	}

	if s.Options.Controllers.EnableAll || enabled.Has("tenancyreplicateclusterrole") {
		if err := s.installTenancyReplicateClusterRoleControllers(ctx, controllerConfig); err != nil {
			return err
		}
	}
	if s.Options.Controllers.EnableAll || enabled.Has("tenancyreplicationclusterrolebinding") {
		if err := s.installTenancyReplicateClusterRoleBindingControllers(ctx, controllerConfig); err != nil {
			return err
		}
	}

	if s.Options.Controllers.EnableAll || enabled.Has("apiexportendpointslice") {
		if err := s.installAPIExportEndpointSliceController(ctx, controllerConfig); err != nil {
			return err
		}
		if err := s.installAPIExportEndpointSliceURLsController(ctx, controllerConfig); err != nil {
			return err
		}
	}

	if s.Options.Controllers.EnableAll || enabled.Has("apibinder") {
		if err := s.installAPIBinderController(ctx, controllerConfig); err != nil {
			return err
		}
	}

	if s.Options.Controllers.EnableAll || enabled.Has("defaultapibindinglifecycle") {
		if err := s.installDefaultAPIBindingController(ctx, controllerConfig); err != nil {
			return err
		}
	}

	if s.Options.Controllers.EnableAll || enabled.Has("partition") {
		if err := s.installPartitionSetController(ctx, controllerConfig); err != nil {
			return err
		}
	}

	if s.Options.Controllers.EnableAll || enabled.Has("quota") {
		if err := s.installKubeQuotaController(ctx, controllerConfig); err != nil {
			return err
		}
	}

	if s.Options.Controllers.EnableAll || enabled.Has("garbagecollector") {
		if err := s.installGarbageCollectorController(ctx, controllerConfig); err != nil {
			return err
		}
	}

	if s.Options.Controllers.EnableAll || enabled.Has("dynamicrestmapper") {
		if err := s.installDynamicRESTMapper(ctx, controllerConfig); err != nil {
			return err
		}
	}

	if s.Options.Controllers.EnableAll || enabled.Has("cache") {
		if err := s.installCacheController(ctx, controllerConfig); err != nil {
			return err
		}
	}

	if s.Options.Controllers.EnableAll || enabled.Has("cachedresourcendpointslice") {
		if err := s.installCachedResourceEndpointSliceController(ctx, controllerConfig); err != nil {
			return err
		}
		if err := s.installCachedResourceEndpointSliceURLsController(ctx, s.ExternalLogicalClusterAdminConfig); err != nil {
			return err
		}
	}

	if s.Options.Controllers.EnableAll || enabled.Has("virtualresourceapibinding") {
		if err := s.installVirtualResourcesAPIBindingController(ctx, controllerConfig); err != nil {
			return err
		}
	}

	return nil
}

func (s *Server) Run(ctx context.Context) error {
	logger := klog.FromContext(ctx).WithValues("component", "kcp")
	ctx = klog.NewContext(ctx, logger)

	if err := s.AddPostStartHook("kcp-bootstrap-policy", bootstrappolicy.Policy().EnsureRBACPolicy()); err != nil {
		return err
	}

	hookName := "kcp-start-informers"
	if err := s.AddPostStartHook(hookName, func(hookContext genericapiserver.PostStartHookContext) error {
		logger = logger.WithValues("postStartHook", hookName)
		hookCtx := klog.NewContext(hookContext, logger)

		logger.Info("starting kube informers")
		s.KubeSharedInformerFactory.Start(hookCtx.Done())
		s.ApiExtensionsSharedInformerFactory.Start(hookCtx.Done())
		s.CacheKubeSharedInformerFactory.Start(hookCtx.Done())

		s.KubeSharedInformerFactory.WaitForCacheSync(hookCtx.Done())
		s.ApiExtensionsSharedInformerFactory.WaitForCacheSync(hookCtx.Done())
		s.CacheKubeSharedInformerFactory.WaitForCacheSync(hookCtx.Done())

		select {
		case <-hookCtx.Done():
			return nil // context closed, avoid reporting success below
		default:
		}

		logger.Info("finished starting kube informers")

		logger.Info("bootstrapping system CRDs")
		if err := wait.PollUntilContextCancel(hookCtx, time.Second, true, func(ctx context.Context) (bool, error) {
			if err := systemcrds.Bootstrap(ctx,
				s.ApiExtensionsClusterClient.Cluster(SystemCRDClusterName.Path()),
				s.ApiExtensionsClusterClient.Cluster(SystemCRDClusterName.Path()).Discovery(),
				s.DynamicClusterClient.Cluster(SystemCRDClusterName.Path()),
				sets.New(s.Options.Extra.BatteriesIncluded...),
			); err != nil {
				logger.Error(err, "failed to bootstrap system CRDs, retrying")
				return false, nil // keep trying
			}
			return true, nil
		}); err != nil {
			logger.Error(err, "failed to bootstrap system CRDs")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}
		logger.Info("finished bootstrapping system CRDs")

		logger.Info("bootstrapping the shard workspace")
		if err := wait.PollUntilContextCancel(hookCtx, time.Second, true, func(ctx context.Context) (bool, error) {
			if err := configshard.Bootstrap(ctx,
				s.ApiExtensionsClusterClient.Cluster(configshard.SystemShardCluster.Path()).Discovery(),
				s.DynamicClusterClient.Cluster(configshard.SystemShardCluster.Path()),
				sets.New(s.Options.Extra.BatteriesIncluded...),
				s.KcpClusterClient.Cluster(configshard.SystemShardCluster.Path())); err != nil {
				logger.Error(err, "failed to bootstrap the shard workspace")
				return false, nil // keep trying
			}
			return true, nil
		}); err != nil {
			logger.Error(err, "failed to bootstrap the shard workspace")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}
		logger.Info("finished bootstrapping the shard workspace")

		go s.KcpSharedInformerFactory.Apis().V1alpha2().APIExports().Informer().Run(hookContext.Done())
		go s.KcpSharedInformerFactory.Apis().V1alpha1().APIExportEndpointSlices().Informer().Run(hookContext.Done())
		go s.CacheKcpSharedInformerFactory.Apis().V1alpha2().APIExports().Informer().Run(hookContext.Done())
		go s.CacheKcpSharedInformerFactory.Cache().V1alpha1().CachedResources().Informer().Run(hookContext.Done())
		go s.CacheKcpSharedInformerFactory.Cache().V1alpha1().CachedResourceEndpointSlices().Informer().Run(hookContext.Done())
		go s.KcpSharedInformerFactory.Core().V1alpha1().LogicalClusters().Informer().Run(hookContext.Done())
		go s.KcpSharedInformerFactory.Cache().V1alpha1().CachedResources().Informer().Run(hookContext.Done())
		go s.KcpSharedInformerFactory.Cache().V1alpha1().CachedResourceEndpointSlices().Informer().Run(hookContext.Done())

		logger.Info("starting APIExport, APIBinding and LogicalCluster informers")
		if err := wait.PollUntilContextCancel(hookCtx, time.Millisecond*100, true, func(ctx context.Context) (bool, error) {
			exportsSynced := s.KcpSharedInformerFactory.Apis().V1alpha2().APIExports().Informer().HasSynced()
			cacheExportsSynced := s.KcpSharedInformerFactory.Apis().V1alpha2().APIExports().Informer().HasSynced()
			logicalClusterSynced := s.KcpSharedInformerFactory.Core().V1alpha1().LogicalClusters().Informer().HasSynced()
			return exportsSynced && cacheExportsSynced && logicalClusterSynced, nil
		}); err != nil {
			logger.Error(err, "failed to start some of APIExport, APIBinding and LogicalCluster informers")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}
		logger.Info("finished starting APIExport, APIBinding and LogicalCluster informers")

		if s.Options.Extra.ShardName == corev1alpha1.RootShard {
			logger.Info("bootstrapping root workspace phase 0")
			s.RootShardKcpClusterClient = s.KcpClusterClient

			if s.Options.Extra.RootIdentitiesFile != "" {
				logger.Info("bootstrapping identities into root workspace", "file", s.Options.Extra.RootIdentitiesFile)
				logger.Info("creating system namespace in root workspace")
				if err := configrootidentitiesns.Bootstrap(hookCtx,
					s.BootstrapApiExtensionsClusterClient.Cluster(core.RootCluster.Path()).Discovery(),
					s.DynamicClusterClient.Cluster(core.RootCluster.Path()),
					s.KubeClusterClient.Cluster(core.RootCluster.Path()),
				); err != nil {
					logger.Error(err, "failed to bootstrap identity secrets namespace")
					return nil // don't klog.Fatal. This only happens when context is cancelled.
				}
				logger.Info("created system namespace in root workspace")
				logger.Info("bootstrapping root workspace with identities", "file", s.Options.Extra.RootIdentitiesFile)
				identitiesBytes, err := os.ReadFile(s.Options.Extra.RootIdentitiesFile)
				if err != nil {
					logger.Error(err, "failed to read root identities file")
					return nil // don't klog.Fatal. This only happens when context is cancelled.
				}
				if err := configrootidentities.Bootstrap(hookCtx,
					s.BootstrapApiExtensionsClusterClient.Cluster(core.RootCluster.Path()).Discovery(),
					s.DynamicClusterClient.Cluster(core.RootCluster.Path()),
					s.KubeClusterClient.Cluster(core.RootCluster.Path()),
					identitiesBytes,
				); err != nil {
					logger.Error(err, "failed to bootstrap root workspace with identities")
					return nil // don't klog.Fatal. This only happens when context is cancelled.
				}
				logger.Info("bootstrapped root workspace with identities", "file", s.Options.Extra.RootIdentitiesFile)
			}

			// bootstrap root workspace phase 0 only if we are on the root shard, no APIBinding resources yet
			if err := configrootphase0.Bootstrap(hookCtx,
				s.KcpClusterClient.Cluster(core.RootCluster.Path()),
				s.ApiExtensionsClusterClient.Cluster(core.RootCluster.Path()).Discovery(),
				s.DynamicClusterClient.Cluster(core.RootCluster.Path()),
				sets.New(s.Options.Extra.BatteriesIncluded...),
			); err != nil {
				logger.Error(err, "failed to bootstrap root workspace phase 0")
				return nil // don't klog.Fatal. This only happens when context is cancelled.
			}
			logger.Info("bootstrapped root workspace phase 0")

			logger.Info("getting kcp APIExport identities")
			if err := wait.PollUntilContextCancel(hookCtx, time.Millisecond*500, true, func(ctx context.Context) (bool, error) {
				if err := s.resolveIdentities(ctx); err != nil {
					logger.V(3).Info("failed to resolve identities, keeping trying", "err", err)
					return false, nil
				}
				return true, nil
			}); err != nil {
				logger.Error(err, "failed to get or create identities")
				return nil // don't klog.Fatal. This only happens when context is cancelled.
			}
			logger.Info("finished getting kcp APIExport identities")
		} else if len(s.Options.Extra.RootShardKubeconfigFile) > 0 {
			logger.Info("getting kcp APIExport identities for the root shard")
			if err := wait.PollUntilContextCancel(hookContext, time.Millisecond*500, true, func(ctx context.Context) (bool, error) {
				if err := s.resolveIdentities(ctx); err != nil {
					logger.V(3).Info("failed to resolve identities for the root shard, keeping trying", "err", err)
					return false, nil
				}
				return true, nil
			}); err != nil {
				logger.Error(err, "failed to get or create identities for the root shard")
				return nil // don't klog.Fatal. This only happens when context is cancelled.
			}
			logger.Info("finished getting kcp APIExport identities for the root shard")
		}

		s.KcpSharedInformerFactory.Start(hookCtx.Done())
		s.CacheKcpSharedInformerFactory.Start(hookCtx.Done())

		s.KcpSharedInformerFactory.WaitForCacheSync(hookCtx.Done())
		s.CacheKcpSharedInformerFactory.WaitForCacheSync(hookCtx.Done())

		// create or update shard
		shard := &corev1alpha1.Shard{
			ObjectMeta: metav1.ObjectMeta{
				Name:        s.Options.Extra.ShardName,
				Annotations: map[string]string{logicalcluster.AnnotationKey: core.RootCluster.String()},
				Labels: map[string]string{
					"name": s.Options.Extra.ShardName,
				},
			},
			Spec: corev1alpha1.ShardSpec{
				BaseURL:             s.CompletedConfig.ShardBaseURL(),
				ExternalURL:         s.CompletedConfig.ShardExternalURL(),
				VirtualWorkspaceURL: s.CompletedConfig.ShardVirtualWorkspaceURL(),
			},
		}
		logger.Info("Creating or updating Shard", "shard", s.Options.Extra.ShardName)
		if err := wait.PollUntilContextCancel(hookCtx, time.Second, true, func(ctx context.Context) (bool, error) {
			existingShard, err := s.RootShardKcpClusterClient.Cluster(core.RootCluster.Path()).CoreV1alpha1().Shards().Get(ctx, shard.Name, metav1.GetOptions{})
			if err != nil && !errors.IsNotFound(err) {
				logger.Error(err, "failed getting Shard from the root workspace")
				return false, nil
			} else if errors.IsNotFound(err) {
				if _, err := s.RootShardKcpClusterClient.Cluster(core.RootCluster.Path()).CoreV1alpha1().Shards().Create(ctx, shard, metav1.CreateOptions{}); err != nil {
					logger.Error(err, "failed creating Shard in the root workspace")
					return false, nil
				}
				logger.Info("Created Shard", "shard", s.Options.Extra.ShardName)
				return true, nil
			}
			existingShard.Spec.BaseURL = shard.Spec.BaseURL
			existingShard.Spec.ExternalURL = shard.Spec.ExternalURL
			existingShard.Spec.VirtualWorkspaceURL = shard.Spec.VirtualWorkspaceURL
			if _, err := s.RootShardKcpClusterClient.Cluster(core.RootCluster.Path()).CoreV1alpha1().Shards().Update(hookCtx, existingShard, metav1.UpdateOptions{}); err != nil {
				logger.Error(err, "failed updating Shard in the root workspace")
				return false, nil
			}
			logger.Info("Updated Shard", "shard", s.Options.Extra.ShardName)
			return true, nil
		}); err != nil {
			logger.Error(err, "failed reconciling Shard resource in the root workspace")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		select {
		case <-hookCtx.Done():
			return nil // context closed, avoid reporting success below
		default:
		}

		logger.Info("finished starting (remaining) kcp informers")

		logger.Info("starting dynamic metadata informer worker")
		go s.DiscoveringDynamicSharedInformerFactory.StartWorker(hookCtx)

		logger.Info("synced all informers, ready to start controllers")
		close(s.syncedCh)

		if s.Options.Extra.ShardName == corev1alpha1.RootShard {
			// the root ws is only present on the root shard
			logger.Info("starting bootstrapping root workspace phase 1")
			if err := configroot.Bootstrap(
				hookCtx,
				s.BootstrapApiExtensionsClusterClient.Cluster(core.RootCluster.Path()).Discovery(),
				s.BootstrapDynamicClusterClient.Cluster(core.RootCluster.Path()),
				s.Options.HomeWorkspaces.HomeCreatorGroups,
				sets.New(s.Options.Extra.BatteriesIncluded...),
			); err != nil {
				logger.Error(err, "failed to bootstrap root workspace phase 1")
				return nil // don't klog.Fatal. This only happens when context is cancelled.
			}
			logger.Info("finished bootstrapping root workspace phase 1")
			close(s.rootPhase1FinishedCh)
		}

		return nil
	}); err != nil {
		return err
	}

	// ========================================================================================================
	// TODO: split apart everything after this line, into their own commands, optional launched in this process

	controllerConfig := s.IdentityConfig

	gvrs := s.addIndexersToInformers(ctx)
	if err := s.installControllers(ctx, controllerConfig, gvrs); err != nil {
		return err
	}

	// Adding this to bootup sequence to not cause re-initialization errors
	if err := s.AddPreShutdownHook(kubequota.ControllerName, func() error {
		close(s.quotaAdmissionStopCh)
		return nil
	}); err != nil {
		return err
	}
	if len(s.Options.Cache.Client.KubeconfigFile) == 0 {
		if err := s.installCacheServer(ctx); err != nil {
			return err
		}
	}

	if sets.New[string](s.Options.Extra.BatteriesIncluded...).Has(batteries.Admin) {
		if err := s.Options.AdminAuthentication.WriteKubeConfig(s.GenericConfig, s.kcpAdminToken, s.shardAdminToken, s.userToken, s.shardAdminTokenHash); err != nil {
			return err
		}
	}
	if err := s.AddPostStartHook("kcp-start-controllers", func(hookContext genericapiserver.PostStartHookContext) error {
		logger := klog.FromContext(ctx).WithValues("postStartHook", "kcp-start-controllers")
		controllerCtx := klog.NewContext(hookContext, logger)

		if s.Options.Controllers.EnableLeaderElection {
			hostname, err := os.Hostname()
			if err != nil {
				return fmt.Errorf("failed to determine hostname: %w", err)
			}
			id := fmt.Sprintf("%s_%s", hostname, string(uuid.NewUUID()))

			// set up a special *rest.Config that points to the (shard-)local admin cluster.
			leaderElectionConfig := rest.CopyConfig(s.GenericConfig.LoopbackClientConfig)
			leaderElectionConfig = rest.AddUserAgent(leaderElectionConfig, "kcp-leader-election")
			leaderElectionConfig.Host, err = url.JoinPath(leaderElectionConfig.Host, controlplaneapiserver.LocalAdminCluster.Path().RequestPath())
			if err != nil {
				return fmt.Errorf("failed to determine local shard admin workspace: %w", err)
			}

			go s.leaderElectAndStartControllers(controllerCtx, id, leaderElectionConfig, controllerConfig, gvrs)
		} else {
			s.startControllers(controllerCtx)
		}

		return nil
	}); err != nil {
		return err
	}

	// add post start hook that starts the OpenAPIv3 controller.
	if err := s.AddPostStartHook("kcp-start-openapiv3-controller", func(srvcontext genericapiserver.PostStartHookContext) error {
		if err := wait.PollUntilContextCancel(srvcontext, time.Second, true, func(ctx context.Context) (bool, error) {
			return s.ApiExtensionsSharedInformerFactory.Apiextensions().V1().CustomResourceDefinitions().Informer().HasSynced(), nil
		}); err != nil {
			return err
		}
		go s.openAPIv3Controller.Run(ctx)
		return nil
	}); err != nil {
		return err
	}

	return s.MiniAggregator.GenericAPIServer.PrepareRun().RunWithContext(ctx)
}

type handlerChainMuxes []*http.ServeMux

func (mxs *handlerChainMuxes) Handle(pattern string, handler http.Handler) {
	for _, mx := range *mxs {
		mx.Handle(pattern, handler)
	}
}

func (s *Server) WaitForPhase1Finished() {
	<-s.rootPhase1FinishedCh
}

func (s *Server) leaderElectAndStartControllers(ctx context.Context, id string, leaseconfig, controllerConfig *rest.Config, gvrs map[schema.GroupVersionResource]replication.ReplicatedGVR) {
	logger := klog.FromContext(ctx).WithValues("identity", id)

	rl, err := resourcelock.NewFromKubeconfig("leases",
		s.Options.Controllers.LeaderElectionNamespace,
		s.Options.Controllers.LeaderElectionName,
		resourcelock.ResourceLockConfig{
			Identity: id,
		},
		leaseconfig,
		time.Second*30,
	)

	if err != nil {
		logger.Error(err, "failed to set up resource lock")
		return
	}

	electionLogger := logger.WithValues("namespace", s.Options.Controllers.LeaderElectionNamespace, "name", s.Options.Controllers.LeaderElectionName)

	// for the whole runtime of the kcp process, we want to try becoming the leader to run
	// the kcp controllers. Even if we once were leader and lost that lock, we want to
	// restart the leader election (thus the loop) until the complete process terminates.
loop:
	for {
		select {
		case <-ctx.Done():
			electionLogger.Info("context canceled, will not retry leader election")
			break loop
		default:
			electionLogger.Info("(re-)starting leader election")
			leaderelection.RunOrDie(ctx, leaderelection.LeaderElectionConfig{
				Lock:          rl,
				LeaseDuration: time.Second * 60,
				RenewDeadline: time.Second * 5,
				RetryPeriod:   time.Second * 2,
				Callbacks: leaderelection.LeaderCallbacks{
					OnStartedLeading: func(leaderElectionCtx context.Context) {
						electionLogger.Info("started leading")
						if len(s.controllers) == 0 {
							if err = s.installControllers(ctx, controllerConfig, gvrs); err != nil {
								logger.Error(err, "error in re-registering controllers")
								klog.FlushAndExit(klog.ExitFlushTimeout, 1)
							}
						}
						s.startControllers(leaderElectionCtx)
					},
					OnStoppedLeading: func() {
						s.uninstallControllers()
						electionLogger.Info("stopped leading")
					},
					OnNewLeader: func(current_id string) {
						if current_id == id {
							// we already log when we start leading, no reason to also log something here.
							return
						}

						electionLogger.WithValues("leader", current_id).Info("leader is someone else")
					},
				},
				WatchDog: leaderelection.NewLeaderHealthzAdaptor(time.Second * 5),
				Name:     s.Options.Controllers.LeaderElectionName,
			})
		}
	}

	electionLogger.Info("leader election loop has been terminated")
}
