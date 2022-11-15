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
	"net/http"
	_ "net/http/pprof"
	"time"

	"github.com/kcp-dev/logicalcluster/v2"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/genericcontrolplane"

	configroot "github.com/kcp-dev/kcp/config/root"
	configrootphase0 "github.com/kcp-dev/kcp/config/root-phase0"
	configrootcompute "github.com/kcp-dev/kcp/config/rootcompute"
	configshard "github.com/kcp-dev/kcp/config/shard"
	systemcrds "github.com/kcp-dev/kcp/config/system-crds"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	bootstrappolicy "github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
	kcpfeatures "github.com/kcp-dev/kcp/pkg/features"
	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/informer"
)

const resyncPeriod = 10 * time.Hour

type Server struct {
	CompletedConfig

	*genericcontrolplane.ServerChain

	syncedCh             chan struct{}
	syncedOptionalCh     chan struct{}
	rootPhase1FinishedCh chan struct{}
}

func (s *Server) AddPostStartHook(name string, hook genericapiserver.PostStartHookFunc) error {
	return s.MiniAggregator.GenericAPIServer.AddPostStartHook(name, hook)
}

func NewServer(c CompletedConfig) (*Server, error) {
	s := &Server{
		CompletedConfig:      c,
		syncedCh:             make(chan struct{}),
		syncedOptionalCh:     make(chan struct{}),
		rootPhase1FinishedCh: make(chan struct{}),
	}

	var err error
	s.ServerChain, err = genericcontrolplane.CreateServerChain(c.MiniAggregator, c.Apis, c.ApiExtensions)
	if err != nil {
		return nil, err
	}

	s.GenericControlPlane.GenericAPIServer.Handler.GoRestfulContainer.Filter(
		mergeCRDsIntoCoreGroup(
			s.ApiExtensions.ExtraConfig.ClusterAwareCRDLister,
			s.CustomResourceDefinitions.GenericAPIServer.Handler.NonGoRestfulMux.ServeHTTP,
			s.GenericControlPlane.GenericAPIServer.Handler.GoRestfulContainer.ServeHTTP,
		),
	)

	s.DynamicDiscoverySharedInformerFactory, err = informer.NewDynamicDiscoverySharedInformerFactory(
		s.MiniAggregator.GenericAPIServer.LoopbackClientConfig,
		func(obj interface{}) bool { return true },
		s.ApiExtensionsSharedInformerFactory.Apiextensions().V1().CustomResourceDefinitions(),
		indexers.AppendOrDie(
			cache.Indexers{
				indexers.BySyncerFinalizerKey:           indexers.IndexBySyncerFinalizerKey,
				indexers.ByClusterResourceStateLabelKey: indexers.IndexByClusterResourceStateLabelKey,
			},
		),
	)
	if err != nil {
		return nil, err
	}

	return s, nil
}

func (s *Server) Run(ctx context.Context) error {
	logger := klog.FromContext(ctx).WithValues("component", "kcp")
	ctx = klog.NewContext(ctx, logger)
	delegationChainHead := s.MiniAggregator.GenericAPIServer

	if err := s.AddPostStartHook("kcp-bootstrap-policy", bootstrappolicy.Policy().EnsureRBACPolicy()); err != nil {
		return err
	}

	hookName := "kcp-start-informers"
	if err := s.AddPostStartHook(hookName, func(hookContext genericapiserver.PostStartHookContext) error {
		logger := logger.WithValues("postStartHook", hookName)
		ctx = klog.NewContext(ctx, logger)

		logger.Info("starting kube informers")
		s.KubeSharedInformerFactory.Start(hookContext.StopCh)
		s.ApiExtensionsSharedInformerFactory.Start(hookContext.StopCh)

		s.KubeSharedInformerFactory.WaitForCacheSync(hookContext.StopCh)
		s.ApiExtensionsSharedInformerFactory.WaitForCacheSync(hookContext.StopCh)

		select {
		case <-hookContext.StopCh:
			return nil // context closed, avoid reporting success below
		default:
		}

		logger.Info("finished starting kube informers")

		logger.Info("bootstrapping system CRDs")
		if err := wait.PollInfiniteWithContext(goContext(hookContext), time.Second, func(ctx context.Context) (bool, error) {
			if err := systemcrds.Bootstrap(ctx,
				s.ApiExtensionsClusterClient.Cluster(SystemCRDLogicalCluster),
				s.ApiExtensionsClusterClient.Cluster(SystemCRDLogicalCluster).Discovery(),
				s.DynamicClusterClient.Cluster(SystemCRDLogicalCluster),
				sets.NewString(s.Options.Extra.BatteriesIncluded...),
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
		if err := wait.PollInfiniteWithContext(goContext(hookContext), time.Second, func(ctx context.Context) (bool, error) {
			if err := configshard.Bootstrap(ctx,
				s.ApiExtensionsClusterClient.Cluster(configshard.SystemShardCluster).Discovery(),
				s.DynamicClusterClient.Cluster(configshard.SystemShardCluster),
				sets.NewString(s.Options.Extra.BatteriesIncluded...),
				s.KcpClusterClient.Cluster(configshard.SystemShardCluster)); err != nil {
				logger.Error(err, "failed to bootstrap the shard workspace")
				return false, nil // keep trying
			}
			return true, nil
		}); err != nil {
			logger.Error(err, "failed to bootstrap the shard workspace")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}
		logger.Info("finished bootstrapping the shard workspace")

		go s.KcpSharedInformerFactory.Apis().V1alpha1().APIExports().Informer().Run(hookContext.StopCh)
		go s.KcpSharedInformerFactory.Apis().V1alpha1().APIBindings().Informer().Run(hookContext.StopCh)

		logger.Info("starting APIExport and APIBinding informers")
		if err := wait.PollInfiniteWithContext(goContext(hookContext), time.Millisecond*100, func(ctx context.Context) (bool, error) {
			exportsSynced := s.KcpSharedInformerFactory.Apis().V1alpha1().APIExports().Informer().HasSynced()
			bindingsSynced := s.KcpSharedInformerFactory.Apis().V1alpha1().APIBindings().Informer().HasSynced()
			return exportsSynced && bindingsSynced, nil
		}); err != nil {
			logger.Error(err, "failed to start APIExport and/or APIBinding informers")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}
		logger.Info("finished starting APIExport and APIBinding informers")

		if s.Options.Extra.ShardName == tenancyv1alpha1.RootShard {
			logger.Info("bootstrapping root workspace phase 0")
			s.RootShardKcpClusterClient = s.KcpClusterClient

			// bootstrap root workspace phase 0 only if we are on the root shard, no APIBinding resources yet
			if err := configrootphase0.Bootstrap(goContext(hookContext),
				s.KcpClusterClient.Cluster(tenancyv1alpha1.RootCluster),
				s.ApiExtensionsClusterClient.Cluster(tenancyv1alpha1.RootCluster).Discovery(),
				s.DynamicClusterClient.Cluster(tenancyv1alpha1.RootCluster),
				sets.NewString(s.Options.Extra.BatteriesIncluded...),
			); err != nil {
				logger.Error(err, "failed to bootstrap root workspace phase 0")
				return nil // don't klog.Fatal. This only happens when context is cancelled.
			}
			logger.Info("bootstrapped root workspace phase 0")

			logger.Info("getting kcp APIExport identities")
			if err := wait.PollImmediateInfiniteWithContext(goContext(hookContext), time.Millisecond*500, func(ctx context.Context) (bool, error) {
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
			logger.Info("starting setting up kcp informers for the root shard")

			go s.TemporaryRootShardKcpSharedInformerFactory.Apis().V1alpha1().APIExports().Informer().Run(hookContext.StopCh)
			go s.TemporaryRootShardKcpSharedInformerFactory.Apis().V1alpha1().APIBindings().Informer().Run(hookContext.StopCh)

			if err := wait.PollInfiniteWithContext(goContext(hookContext), time.Millisecond*100, func(ctx context.Context) (bool, error) {
				exportsSynced := s.TemporaryRootShardKcpSharedInformerFactory.Apis().V1alpha1().APIExports().Informer().HasSynced()
				bindingsSynced := s.TemporaryRootShardKcpSharedInformerFactory.Apis().V1alpha1().APIBindings().Informer().HasSynced()
				return exportsSynced && bindingsSynced, nil
			}); err != nil {
				logger.Error(err, "failed to start APIExport and/or APIBinding informers for the root shard")
				return nil // don't klog.Fatal. This only happens when context is cancelled.
			}
			logger.Info("finished starting APIExport and APIBinding informers for the root shard")

			logger.Info("getting kcp APIExport identities for the root shard")
			if err := wait.PollImmediateInfiniteWithContext(goContext(hookContext), time.Millisecond*500, func(ctx context.Context) (bool, error) {
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

			s.TemporaryRootShardKcpSharedInformerFactory.Start(hookContext.StopCh)
			s.TemporaryRootShardKcpSharedInformerFactory.WaitForCacheSync(hookContext.StopCh)

			select {
			case <-hookContext.StopCh:
				return nil // context closed, avoid reporting success below
			default:
			}
			logger.Info("finished starting kcp informers for the root shard")
		}

		s.KcpSharedInformerFactory.Start(hookContext.StopCh)
		s.KcpSharedInformerFactory.WaitForCacheSync(hookContext.StopCh)

		// create or update shard
		shard := &tenancyv1alpha1.ClusterWorkspaceShard{
			ObjectMeta: metav1.ObjectMeta{
				Name:        s.Options.Extra.ShardName,
				Annotations: map[string]string{logicalcluster.AnnotationKey: tenancyv1alpha1.RootCluster.String()},
				Labels: map[string]string{
					"name": s.Options.Extra.ShardName,
				},
			},
			Spec: tenancyv1alpha1.ClusterWorkspaceShardSpec{
				BaseURL:             s.CompletedConfig.ShardBaseURL(),
				ExternalURL:         s.CompletedConfig.ShardExternalURL(),
				VirtualWorkspaceURL: s.CompletedConfig.ShardVirtualWorkspaceURL(),
			},
		}
		logger.Info("Creating or updating ClusterWorkspaceShard", "shard", s.Options.Extra.ShardName)
		if err := wait.PollInfiniteWithContext(goContext(hookContext), time.Second, func(ctx context.Context) (bool, error) {
			existingShard, err := s.RootShardKcpClusterClient.Cluster(tenancyv1alpha1.RootCluster).TenancyV1alpha1().ClusterWorkspaceShards().Get(ctx, shard.Name, metav1.GetOptions{})
			if err != nil && !errors.IsNotFound(err) {
				logger.Error(err, "failed getting ClusterWorkspaceShard from the root workspace")
				return false, nil
			} else if errors.IsNotFound(err) {
				if _, err := s.RootShardKcpClusterClient.Cluster(tenancyv1alpha1.RootCluster).TenancyV1alpha1().ClusterWorkspaceShards().Create(ctx, shard, metav1.CreateOptions{}); err != nil {
					logger.Error(err, "failed creating ClusterWorkspaceShard in the root workspace")
					return false, nil
				}
				logger.Info("Created ClusterWorkspaceShard", "shard", s.Options.Extra.ShardName)
				return true, nil
			}
			existingShard.Spec.BaseURL = shard.Spec.BaseURL
			existingShard.Spec.ExternalURL = shard.Spec.ExternalURL
			existingShard.Spec.VirtualWorkspaceURL = shard.Spec.VirtualWorkspaceURL
			if _, err := s.RootShardKcpClusterClient.Cluster(tenancyv1alpha1.RootCluster).TenancyV1alpha1().ClusterWorkspaceShards().Update(ctx, existingShard, metav1.UpdateOptions{}); err != nil {
				logger.Error(err, "failed updating ClusterWorkspaceShard in the root workspace")
				return false, nil
			}
			logger.Info("Updated ClusterWorkspaceShard", "shard", s.Options.Extra.ShardName)
			return true, nil
		}); err != nil {
			logger.Error(err, "failed reconciling ClusterWorkspaceShard resource in the root workspace")
			return nil // don't klog.Fatal. This only happens when context is cancelled.
		}

		select {
		case <-hookContext.StopCh:
			return nil // context closed, avoid reporting success below
		default:
		}

		logger.Info("finished starting (remaining) kcp informers")

		logger.Info("starting dynamic metadata informer worker")
		go s.DynamicDiscoverySharedInformerFactory.StartWorker(goContext(hookContext))

		logger.Info("synced all informers, ready to start controllers")
		close(s.syncedCh)

		if s.Options.Extra.ShardName == tenancyv1alpha1.RootShard {
			// the root ws is only present on the root shard
			logger.Info("starting bootstrapping root workspace phase 1")
			if err := configroot.Bootstrap(goContext(hookContext),
				s.BootstrapApiExtensionsClusterClient.Cluster(tenancyv1alpha1.RootCluster).Discovery(),
				s.BootstrapDynamicClusterClient.Cluster(tenancyv1alpha1.RootCluster),
				s.Options.HomeWorkspaces.HomeCreatorGroups,
				sets.NewString(s.Options.Extra.BatteriesIncluded...),
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

	if s.Options.Cache.Enabled {
		if err := s.AddPostStartHook("kcp-start-optional-informers", func(hookContext genericapiserver.PostStartHookContext) error {
			// TODO(p0lyn0mial): failing the optional hook should not render the main server unhealthy
			logger := logger.WithValues("postStartHook", "kcp-start-optional-informers")
			s.CacheKcpSharedInformerFactory.Start(hookContext.StopCh)
			s.CacheKcpSharedInformerFactory.WaitForCacheSync(hookContext.StopCh)

			select {
			case <-hookContext.StopCh:
				return nil // context closed, avoid reporting success below
			default:
			}

			logger.Info("finished starting optional cache informers, ready to start controllers")
			close(s.syncedOptionalCh)
			return nil
		}); err != nil {
			return err
		}
	}

	// ========================================================================================================
	// TODO: split apart everything after this line, into their own commands, optional launched in this process

	controllerConfig := rest.CopyConfig(s.identityConfig)

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

	if err := s.installRootCAConfigMapController(ctx, s.GenericControlPlane.GenericAPIServer.LoopbackClientConfig); err != nil {
		return err
	}

	if err := s.installApiExportIdentityController(ctx, controllerConfig, delegationChainHead); err != nil {
		return err
	}
	if err := s.installReplicationController(ctx, controllerConfig, delegationChainHead); err != nil {
		return err
	}

	enabled := sets.NewString(s.Options.Controllers.IndividuallyEnabled...)
	if len(enabled) > 0 {
		logger.WithValues("controllers", enabled).Info("starting controllers individually")
	}

	if s.Options.Controllers.EnableAll || enabled.Has("cluster") {
		// bootstrap root compute workspace
		computeBoostraphookName := "rootComputeBoostrap"
		if err := s.AddPostStartHook(computeBoostraphookName, func(hookContext genericapiserver.PostStartHookContext) error {
			logger := logger.WithValues("postStartHook", computeBoostraphookName)
			if s.Options.Extra.ShardName == tenancyv1alpha1.RootShard {
				// the root ws is only present on the root shard
				logger.Info("waiting to bootstrap root compute workspace until root phase1 is complete")
				<-s.rootPhase1FinishedCh

				logger.Info("starting bootstrapping root compute workspace")
				if err := configrootcompute.Bootstrap(goContext(hookContext),
					s.BootstrapApiExtensionsClusterClient,
					s.BootstrapDynamicClusterClient,
					sets.NewString(s.Options.Extra.BatteriesIncluded...),
				); err != nil {
					logger.Error(err, "failed to bootstrap root compute workspace")
					return nil // don't klog.Fatal. This only happens when context is cancelled.
				}
				logger.Info("finished bootstrapping root compute workspace")
			}
			return nil
		}); err != nil {
			return err
		}

		// TODO(marun) Consider enabling each controller via a separate flag
		if err := s.installApiResourceController(ctx, controllerConfig); err != nil {
			return err
		}
		if err := s.installSyncTargetHeartbeatController(ctx, controllerConfig); err != nil {
			return err
		}
		if err := s.installSyncTargetController(ctx, controllerConfig, delegationChainHead); err != nil {
			return err
		}
		if err := s.installWorkloadsSyncTargetExportController(ctx, controllerConfig, delegationChainHead); err != nil {
			return err
		}
	}

	if s.Options.Controllers.EnableAll || enabled.Has("workspace-scheduler") {
		if err := s.installWorkspaceScheduler(ctx, controllerConfig, s.LogicalClusterAdminConfig); err != nil {
			return err
		}
		if err := s.installWorkspaceDeletionController(ctx, controllerConfig, s.LogicalClusterAdminConfig, s.CompletedConfig.ShardExternalURL); err != nil {
			return err
		}
		if err := s.installThisWorkspace(ctx, controllerConfig); err != nil {
			return err
		}
	}

	if s.Options.Controllers.EnableAll || enabled.Has("resource-scheduler") {
		if err := s.installWorkloadResourceScheduler(ctx, controllerConfig, s.DynamicDiscoverySharedInformerFactory); err != nil {
			return err
		}
	}

	if s.Options.Controllers.EnableAll || enabled.Has("apibinding") {
		if err := s.installAPIBindingController(ctx, controllerConfig, delegationChainHead, s.DynamicDiscoverySharedInformerFactory); err != nil {
			return err
		}
		if err := s.installCRDCleanupController(ctx, controllerConfig, delegationChainHead); err != nil {
			return err
		}
		if err := s.installExtraAnnotationSyncController(ctx, controllerConfig, delegationChainHead); err != nil {
			return err
		}
	}

	if s.Options.Controllers.EnableAll || enabled.Has("apiexport") {
		if err := s.installAPIExportController(ctx, controllerConfig, delegationChainHead); err != nil {
			return err
		}
	}

	if s.Options.Controllers.EnableAll || enabled.Has("apibinder") {
		if err := s.installAPIBinderController(ctx, controllerConfig, delegationChainHead); err != nil {
			return err
		}
	}

	if kcpfeatures.DefaultFeatureGate.Enabled(kcpfeatures.LocationAPI) {
		if s.Options.Controllers.EnableAll || enabled.Has("scheduling") {
			if err := s.installWorkloadNamespaceScheduler(ctx, controllerConfig, delegationChainHead); err != nil {
				return err
			}
			if err := s.installWorkloadPlacementScheduler(ctx, controllerConfig, delegationChainHead); err != nil {
				return err
			}
			if err := s.installSchedulingLocationStatusController(ctx, controllerConfig, delegationChainHead); err != nil {
				return err
			}
			if err := s.installSchedulingPlacementController(ctx, controllerConfig, delegationChainHead); err != nil {
				return err
			}
			if err := s.installWorkloadsAPIExportController(ctx, controllerConfig, delegationChainHead); err != nil {
				return err
			}
			if err := s.installWorkloadsAPIExportCreateController(ctx, controllerConfig, delegationChainHead); err != nil {
				return err
			}
		}
	}

	if s.Options.Controllers.EnableAll || enabled.Has("quota") {
		if err := s.installKubeQuotaController(ctx, controllerConfig, delegationChainHead); err != nil {
			return err
		}
	}

	if s.Options.Controllers.EnableAll || enabled.Has("garbagecollector") {
		if err := s.installGarbageCollectorController(ctx, controllerConfig, delegationChainHead); err != nil {
			return err
		}
	}

	if s.Options.Virtual.Enabled {
		virtualWorkspacesConfig := rest.CopyConfig(s.GenericConfig.LoopbackClientConfig)
		virtualWorkspacesConfig = rest.AddUserAgent(virtualWorkspacesConfig, "virtual-workspaces")
		if err := s.installVirtualWorkspaces(ctx, virtualWorkspacesConfig, delegationChainHead, s.GenericConfig.Authentication, s.GenericConfig.ExternalAddress, s.GenericConfig.AuditPolicyRuleEvaluator, s.preHandlerChainMux); err != nil {
			return err
		}
	}

	if s.Options.Cache.Enabled && len(s.Options.Cache.KubeconfigFile) == 0 {
		if err := s.installCacheServer(ctx); err != nil {
			return err
		}
	}

	if err := s.Options.AdminAuthentication.WriteKubeConfig(s.GenericConfig, s.kcpAdminToken, s.shardAdminToken, s.userToken, s.shardAdminTokenHash); err != nil {
		return err
	}

	return delegationChainHead.PrepareRun().Run(ctx.Done())
}

type handlerChainMuxes []*http.ServeMux

func (mxs *handlerChainMuxes) Handle(pattern string, handler http.Handler) {
	for _, mx := range *mxs {
		mx.Handle(pattern, handler)
	}
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
