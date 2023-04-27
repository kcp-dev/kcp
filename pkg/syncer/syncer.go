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

package syncer

import (
	"context"
	"fmt"
	"time"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	kubernetesinformers "k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/pkg/version"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	kcpfeatures "github.com/kcp-dev/kcp/pkg/features"
	ddsif "github.com/kcp-dev/kcp/pkg/informer"
	"github.com/kcp-dev/kcp/pkg/syncer/controllermanager"
	"github.com/kcp-dev/kcp/pkg/syncer/endpoints"
	"github.com/kcp-dev/kcp/pkg/syncer/indexers"
	"github.com/kcp-dev/kcp/pkg/syncer/namespace"
	"github.com/kcp-dev/kcp/pkg/syncer/shared"
	"github.com/kcp-dev/kcp/pkg/syncer/spec"
	"github.com/kcp-dev/kcp/pkg/syncer/spec/dns"
	"github.com/kcp-dev/kcp/pkg/syncer/spec/mutators"
	"github.com/kcp-dev/kcp/pkg/syncer/status"
	"github.com/kcp-dev/kcp/pkg/syncer/synctarget"
	"github.com/kcp-dev/kcp/pkg/syncer/upsync"
	kcpcorev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/workload/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned"
	kcpclusterclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	kcpinformers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions"
	. "github.com/kcp-dev/kcp/tmc/pkg/logging"
)

const (
	AdvancedSchedulingFeatureAnnotation = "featuregates.experimental.workload.kcp.io/advancedscheduling"

	resyncPeriod = 10 * time.Hour

	// TODO(marun) Coordinate this value with the interval configured for the heartbeat controller.
	heartbeatInterval = 20 * time.Second
)

// SyncerConfig defines the syncer configuration that is guaranteed to
// vary across syncer deployments. Capturing these details in a struct
// simplifies defining these details in test fixture.
type SyncerConfig struct {
	UpstreamConfig                *rest.Config
	DownstreamConfig              *rest.Config
	ResourcesToSync               sets.Set[string]
	SyncTargetPath                logicalcluster.Path
	SyncTargetName                string
	SyncTargetUID                 string
	DownstreamNamespaceCleanDelay time.Duration
	DNSImage                      string
}

func StartSyncer(ctx context.Context, cfg *SyncerConfig, numSyncerThreads int, importPollInterval time.Duration, syncerNamespace string) error {
	logger := klog.FromContext(ctx)
	logger = logger.WithValues(SyncTargetWorkspace, cfg.SyncTargetPath, SyncTargetName, cfg.SyncTargetName)

	logger.V(2).Info("starting syncer")

	kcpVersion := version.Get().GitVersion

	bootstrapConfig := rest.CopyConfig(cfg.UpstreamConfig)
	rest.AddUserAgent(bootstrapConfig, "kcp#syncer/"+kcpVersion)
	kcpBootstrapClusterClient, err := kcpclusterclientset.NewForConfig(bootstrapConfig)
	if err != nil {
		return err
	}
	kcpSyncTargetClient := kcpBootstrapClusterClient.Cluster(cfg.SyncTargetPath)

	// kcpSyncTargetInformerFactory to watch a certain syncTarget
	kcpSyncTargetInformerFactory := kcpinformers.NewSharedScopedInformerFactoryWithOptions(kcpSyncTargetClient, resyncPeriod, kcpinformers.WithTweakListOptions(
		func(listOptions *metav1.ListOptions) {
			listOptions.FieldSelector = fields.OneTermEqualSelector("metadata.name", cfg.SyncTargetName).String()
		},
	))

	// TODO(david): we need to provide user-facing details if this polling goes on forever. Blocking here is a bad UX.
	// TODO(david): Also, any regressions in our code will make any e2e test that starts a syncer (at least in-process)
	// TODO(david): block until it hits the 10 minute overall test timeout.
	logger.Info("attempting to retrieve the Syncer SyncTarget resource")
	var syncTarget *workloadv1alpha1.SyncTarget
	err = wait.PollImmediateInfinite(5*time.Second, func() (bool, error) {
		var err error
		syncTarget, err = kcpSyncTargetClient.WorkloadV1alpha1().SyncTargets().Get(ctx, cfg.SyncTargetName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		// If the SyncTargetUID flag is set, we compare the provided value with the kcp synctarget uid, if the values don't match
		// the syncer will refuse to work.
		if cfg.SyncTargetUID != "" && cfg.SyncTargetUID != string(syncTarget.UID) {
			return false, fmt.Errorf("unexpected SyncTarget UID %s, expected %s, refusing to sync", syncTarget.UID, cfg.SyncTargetUID)
		}
		return true, nil
	})
	if err != nil {
		return err
	}

	// Resources are accepted as a set to ensure the provision of a
	// unique set of resources, but all subsequent consumption is via
	// slice whose entries are assumed to be unique.
	resources := sets.List[string](cfg.ResourcesToSync)

	// Start api import first because spec and status syncers are blocked by
	// gvr discovery finding all the configured resource types in the kcp
	// workspace.

	// kcpImporterInformerFactory only used for apiimport to watch APIResourceImport
	// TODO(qiujian16) make starting apiimporter optional after we check compatibility of supported APIExports
	// of synctarget in syncer rather than in server.
	kcpImporterInformerFactory := kcpinformers.NewSharedScopedInformerFactoryWithOptions(kcpSyncTargetClient, resyncPeriod)
	apiImporter, err := NewAPIImporter(
		cfg.UpstreamConfig, cfg.DownstreamConfig,
		kcpSyncTargetInformerFactory.Workload().V1alpha1().SyncTargets(),
		kcpImporterInformerFactory.Apiresource().V1alpha1().APIResourceImports(),
		resources,
		cfg.SyncTargetPath, cfg.SyncTargetName, syncTarget.GetUID())
	if err != nil {
		return err
	}
	kcpImporterInformerFactory.Start(ctx.Done())

	downstreamConfig := rest.CopyConfig(cfg.DownstreamConfig)
	rest.AddUserAgent(downstreamConfig, "kcp#status-syncer/"+kcpVersion)
	downstreamDynamicClient, err := dynamic.NewForConfig(downstreamConfig)
	if err != nil {
		return err
	}
	downstreamKubeClient, err := kubernetes.NewForConfig(downstreamConfig)
	if err != nil {
		return err
	}

	syncTargetKey := workloadv1alpha1.ToSyncTargetKey(logicalcluster.From(syncTarget), cfg.SyncTargetName)
	logger = logger.WithValues(SyncTargetKey, syncTargetKey)
	ctx = klog.NewContext(ctx, logger)

	syncTargetGVRSource := synctarget.NewSyncTargetGVRSource(
		kcpSyncTargetInformerFactory.Workload().V1alpha1().SyncTargets(),
		downstreamKubeClient,
	)

	// Check whether we're in the Advanced Scheduling feature-gated mode.
	advancedSchedulingEnabled := false
	if syncTarget.GetAnnotations()[AdvancedSchedulingFeatureAnnotation] == "true" {
		logger.Info("Advanced Scheduling feature is enabled")
		advancedSchedulingEnabled = true
	}

	ddsifForDownstream, err := ddsif.NewScopedDiscoveringDynamicSharedInformerFactory(downstreamDynamicClient, nil,
		func(o *metav1.ListOptions) {
			o.LabelSelector = workloadv1alpha1.InternalDownstreamClusterLabel + "=" + syncTargetKey
		},
		&filteringGVRSource{
			syncTargetGVRSource,
			func(gvr schema.GroupVersionResource) bool {
				return gvr.Group != kcpcorev1alpha1.SchemeGroupVersion.Group
			},
		},
		cache.Indexers{
			indexers.ByNamespaceLocatorIndexName: indexers.IndexByNamespaceLocator,
		},
	)
	if err != nil {
		return err
	}

	// syncerNamespaceInformerFactory to watch some DNS-related resources in the dns namespace
	syncerNamespaceInformerFactory := kubernetesinformers.NewSharedInformerFactoryWithOptions(downstreamKubeClient, resyncPeriod, kubernetesinformers.WithNamespace(syncerNamespace))
	dnsProcessor := dns.NewDNSProcessor(downstreamKubeClient,
		syncerNamespaceInformerFactory,
		cfg.SyncTargetName, syncTarget.GetUID(), syncerNamespace, cfg.DNSImage)

	alwaysRequiredGVRs := []schema.GroupVersionResource{
		corev1.SchemeGroupVersion.WithResource("secrets"),
		corev1.SchemeGroupVersion.WithResource("namespaces"),
	}

	namespaceCleaner := &delegatingCleaner{}
	shardManager := synctarget.NewShardManager(
		func(ctx context.Context, shardURLs workloadv1alpha1.VirtualWorkspace) (*synctarget.ShardAccess, func() error, error) {
			upstreamConfig := rest.CopyConfig(cfg.UpstreamConfig)
			upstreamConfig.Host = shardURLs.SyncerURL
			rest.AddUserAgent(upstreamConfig, "kcp#syncing/"+kcpVersion)
			upstreamSyncerClusterClient, err := kcpdynamic.NewForConfig(upstreamConfig)
			if err != nil {
				return nil, nil, err
			}

			upstreamUpsyncConfig := rest.CopyConfig(cfg.UpstreamConfig)
			upstreamUpsyncConfig.Host = shardURLs.UpsyncerURL
			rest.AddUserAgent(upstreamUpsyncConfig, "kcp#upsyncing/"+kcpVersion)
			upstreamUpsyncerClusterClient, err := kcpdynamic.NewForConfig(upstreamUpsyncConfig)
			if err != nil {
				return nil, nil, err
			}

			ddsifForUpstreamSyncer, err := ddsif.NewDiscoveringDynamicSharedInformerFactory(upstreamSyncerClusterClient, nil, nil,
				&filteringGVRSource{
					syncTargetGVRSource,
					func(gvr schema.GroupVersionResource) bool {
						// Don't expose pods or endpoints via the syncer vw
						if gvr.Group == corev1.GroupName && (gvr.Resource == "pods") {
							return false
						}
						return true
					},
				}, cache.Indexers{})
			if err != nil {
				return nil, nil, err
			}

			ddsifForUpstreamUpsyncer, err := ddsif.NewDiscoveringDynamicSharedInformerFactory(upstreamUpsyncerClusterClient, nil, nil,
				&filteringGVRSource{
					syncTargetGVRSource,
					func(gvr schema.GroupVersionResource) bool {
						return gvr.Group == corev1.GroupName && (gvr.Resource == "persistentvolumes" ||
							gvr.Resource == "pods" ||
							gvr.Resource == "endpoints")
					},
				},
				cache.Indexers{})
			if err != nil {
				return nil, nil, err
			}

			logicalClusterIndex := synctarget.NewLogicalClusterIndex(ddsifForUpstreamSyncer, ddsifForUpstreamUpsyncer)

			secretMutator := mutators.NewSecretMutator()
			podspecableMutator := mutators.NewPodspecableMutator(
				func(clusterName logicalcluster.Name) (*ddsif.DiscoveringDynamicSharedInformerFactory, error) {
					return ddsifForUpstreamSyncer, nil
				}, syncerNamespaceInformerFactory.Core().V1().Services().Lister(), logicalcluster.From(syncTarget), cfg.SyncTargetName, types.UID(cfg.SyncTargetUID), syncerNamespace, kcpfeatures.DefaultFeatureGate.Enabled(kcpfeatures.SyncerTunnel))

			logger.Info("Creating spec syncer")
			specSyncer, err := spec.NewSpecSyncer(logger, logicalcluster.From(syncTarget), cfg.SyncTargetName, syncTargetKey, advancedSchedulingEnabled,
				upstreamSyncerClusterClient, downstreamDynamicClient, downstreamKubeClient, ddsifForUpstreamSyncer, ddsifForDownstream,
				namespaceCleaner, syncTarget.GetUID(),
				syncerNamespace, dnsProcessor, cfg.DNSImage, secretMutator, podspecableMutator)
			if err != nil {
				return nil, nil, err
			}

			upsyncerCleaner, err := upsync.NewUpSyncerCleanupController(logger, logicalcluster.From(syncTarget), cfg.SyncTargetName, types.UID(cfg.SyncTargetUID), syncTargetKey,
				upstreamUpsyncerClusterClient, ddsifForUpstreamUpsyncer,
				ddsifForDownstream)
			if err != nil {
				return nil, nil, err
			}

			var cacheSyncsForAlwaysRequiredGVRs []cache.InformerSynced
			for _, alwaysRequiredGVR := range alwaysRequiredGVRs {
				if informer, err := ddsifForUpstreamSyncer.ForResource(alwaysRequiredGVR); err != nil {
					return nil, nil, err
				} else {
					cacheSyncsForAlwaysRequiredGVRs = append(cacheSyncsForAlwaysRequiredGVRs, informer.Informer().HasSynced)
				}
			}

			start := func() error {
				// Start and sync informer factories

				ddsifForUpstreamSyncer.Start(ctx.Done())
				ddsifForUpstreamUpsyncer.Start(ctx.Done())

				if ok := cache.WaitForCacheSync(ctx.Done(), cacheSyncsForAlwaysRequiredGVRs...); !ok {
					return fmt.Errorf("unable to sync watch caches for virtual workspace %q", shardURLs.SyncerURL)
				}

				go ddsifForUpstreamSyncer.StartWorker(ctx)
				go ddsifForUpstreamUpsyncer.StartWorker(ctx)

				go specSyncer.Start(ctx, numSyncerThreads)
				go upsyncerCleaner.Start(ctx, numSyncerThreads)

				// Create and start GVR-specific controllers through controller managers
				upstreamSyncerControllerManager := controllermanager.NewControllerManager(ctx,
					"upstream-syncer",
					controllermanager.InformerSource{
						Subscribe: ddsifForUpstreamSyncer.Subscribe,
						Informers: func() (informers map[schema.GroupVersionResource]cache.SharedIndexInformer, notSynced []schema.GroupVersionResource) {
							genericInformers, notSynced := ddsifForUpstreamSyncer.Informers()
							informers = make(map[schema.GroupVersionResource]cache.SharedIndexInformer, len(genericInformers))
							for gvr, inf := range genericInformers {
								informers[gvr] = inf.Informer()
							}
							return informers, notSynced
						},
					},
					map[string]controllermanager.ManagedController{},
				)
				go upstreamSyncerControllerManager.Start(ctx)

				upstreamUpsyncerControllerManager := controllermanager.NewControllerManager(ctx,
					"upstream-upsyncer",
					controllermanager.InformerSource{
						Subscribe: ddsifForUpstreamUpsyncer.Subscribe,
						Informers: func() (informers map[schema.GroupVersionResource]cache.SharedIndexInformer, notSynced []schema.GroupVersionResource) {
							genericInformers, notSynced := ddsifForUpstreamUpsyncer.Informers()
							informers = make(map[schema.GroupVersionResource]cache.SharedIndexInformer, len(genericInformers))
							for gvr, inf := range genericInformers {
								informers[gvr] = inf.Informer()
							}
							return informers, notSynced
						},
					},
					map[string]controllermanager.ManagedController{},
				)
				go upstreamUpsyncerControllerManager.Start(ctx)

				return nil
			}

			return &synctarget.ShardAccess{
				SyncerClient:   upstreamSyncerClusterClient,
				SyncerDDSIF:    ddsifForUpstreamSyncer,
				UpsyncerClient: upstreamUpsyncerClusterClient,
				UpsyncerDDSIF:  ddsifForUpstreamUpsyncer,

				LogicalClusterIndex: logicalClusterIndex,
			}, start, nil
		},
	)

	syncTargetController, err := synctarget.NewSyncTargetController(
		logger,
		kcpSyncTargetClient.WorkloadV1alpha1().SyncTargets(),
		kcpSyncTargetInformerFactory.Workload().V1alpha1().SyncTargets(),
		cfg.SyncTargetName,
		logicalcluster.From(syncTarget),
		syncTarget.GetUID(),
		syncTargetGVRSource,
		shardManager,
		func(ctx context.Context, shardURL workloadv1alpha1.TunnelWorkspace) {
			// Start tunneler for POD access
			if kcpfeatures.DefaultFeatureGate.Enabled(kcpfeatures.SyncerTunnel) {
				upstreamTunnelConfig := rest.CopyConfig(cfg.UpstreamConfig)
				rest.AddUserAgent(upstreamTunnelConfig, "kcp#tunneler/"+kcpVersion)
				upstreamTunnelConfig.Host = shardURL.URL

				StartSyncerTunnel(ctx, upstreamTunnelConfig, downstreamConfig, logicalcluster.From(syncTarget), cfg.SyncTargetName, cfg.SyncTargetUID, func(gvr schema.GroupVersionResource) (cache.GenericLister, error) {
					informers, _ := ddsifForDownstream.Informers()
					informer, ok := informers[gvr]
					if !ok {
						return nil, fmt.Errorf("failed to get informer for gvr: %s", gvr)
					}
					return informer.Lister(), nil
				})
			}
		},
	)
	if err != nil {
		return err
	}

	downstreamNamespaceController, err := namespace.NewDownstreamController(logger, logicalcluster.From(syncTarget), cfg.SyncTargetName, syncTargetKey, syncTarget.GetUID(), downstreamConfig, downstreamDynamicClient, ddsifForDownstream, shardManager.ShardAccessForCluster, syncerNamespace, cfg.DownstreamNamespaceCleanDelay)
	if err != nil {
		return err
	}
	namespaceCleaner.delegate = downstreamNamespaceController

	secretMutator := mutators.NewSecretMutator()
	podspecableMutator := mutators.NewPodspecableMutator(
		func(clusterName logicalcluster.Name) (*ddsif.DiscoveringDynamicSharedInformerFactory, error) {
			shardAccess, ok, err := shardManager.ShardAccessForCluster(clusterName)
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, fmt.Errorf("shard-related clients not found for cluster %q", clusterName)
			}
			return shardAccess.SyncerDDSIF, nil
		}, syncerNamespaceInformerFactory.Core().V1().Services().Lister(), logicalcluster.From(syncTarget), cfg.SyncTargetName, types.UID(cfg.SyncTargetUID), syncerNamespace, kcpfeatures.DefaultFeatureGate.Enabled(kcpfeatures.SyncerTunnel))

	logger.Info("Creating spec syncer")
	specSyncerForDownstream, err := spec.NewSpecSyncerForDownstream(logger, logicalcluster.From(syncTarget), cfg.SyncTargetName, syncTargetKey, advancedSchedulingEnabled,
		shardManager.ShardAccessForCluster, downstreamDynamicClient, downstreamKubeClient, ddsifForDownstream,
		namespaceCleaner, syncTarget.GetUID(),
		syncerNamespace, dnsProcessor, cfg.DNSImage, secretMutator, podspecableMutator)
	if err != nil {
		return err
	}

	logger.Info("Creating status syncer")
	statusSyncer, err := status.NewStatusSyncer(logger, logicalcluster.From(syncTarget), cfg.SyncTargetName, syncTargetKey, advancedSchedulingEnabled,
		shardManager.ShardAccessForCluster, downstreamDynamicClient, ddsifForDownstream, syncTarget.GetUID())
	if err != nil {
		return err
	}

	logger.Info("Creating resource upsyncer")
	upSyncer, err := upsync.NewUpSyncer(logger, logicalcluster.From(syncTarget), cfg.SyncTargetName, syncTargetKey, shardManager.ShardAccessForCluster, downstreamDynamicClient, ddsifForDownstream, syncTarget.GetUID())
	if err != nil {
		return err
	}

	// Start and sync informer factories
	var cacheSyncsForAlwaysRequiredGVRs []cache.InformerSynced
	for _, alwaysRequiredGVR := range alwaysRequiredGVRs {
		if informer, err := ddsifForDownstream.ForResource(alwaysRequiredGVR); err != nil {
			return err
		} else {
			cacheSyncsForAlwaysRequiredGVRs = append(cacheSyncsForAlwaysRequiredGVRs, informer.Informer().HasSynced)
		}
	}

	ddsifForDownstream.Start(ctx.Done())
	kcpSyncTargetInformerFactory.Start(ctx.Done())
	syncerNamespaceInformerFactory.Start(ctx.Done())

	kcpSyncTargetInformerFactory.WaitForCacheSync(ctx.Done())
	syncerNamespaceInformerFactory.WaitForCacheSync(ctx.Done())
	cache.WaitForCacheSync(ctx.Done(), cacheSyncsForAlwaysRequiredGVRs...)

	go ddsifForDownstream.StartWorker(ctx)

	// Start static controllers
	go apiImporter.Start(klog.NewContext(ctx, logger.WithValues("resources", resources)), importPollInterval)
	go specSyncerForDownstream.Start(ctx, numSyncerThreads)
	go statusSyncer.Start(ctx, numSyncerThreads)
	go upSyncer.Start(ctx, numSyncerThreads)
	go downstreamNamespaceController.Start(ctx, numSyncerThreads)

	downstreamSyncerControllerManager := controllermanager.NewControllerManager(ctx,
		"downstream-syncer",
		controllermanager.InformerSource{
			Subscribe: ddsifForDownstream.Subscribe,
			Informers: func() (informers map[schema.GroupVersionResource]cache.SharedIndexInformer, notSynced []schema.GroupVersionResource) {
				genericInformers, notSynced := ddsifForDownstream.Informers()
				informers = make(map[schema.GroupVersionResource]cache.SharedIndexInformer, len(genericInformers))
				for gvr, inf := range genericInformers {
					informers[gvr] = inf.Informer()
				}
				return informers, notSynced
			},
		},
		map[string]controllermanager.ManagedController{
			endpoints.ControllerName: {
				RequiredGVRs: []schema.GroupVersionResource{
					corev1.SchemeGroupVersion.WithResource("services"),
					corev1.SchemeGroupVersion.WithResource("endpoints"),
				},
				Create: func(ctx context.Context) (controllermanager.StartControllerFunc, error) {
					endpointController, err := endpoints.NewEndpointController(downstreamDynamicClient, ddsifForDownstream, logicalcluster.From(syncTarget), cfg.SyncTargetName, types.UID(cfg.SyncTargetUID))
					if err != nil {
						return nil, err
					}
					return func(ctx context.Context) {
						endpointController.Start(ctx, 2)
					}, nil
				},
			},
		},
	)

	go syncTargetController.Start(ctx)
	go downstreamSyncerControllerManager.Start(ctx)

	StartHeartbeat(ctx, kcpSyncTargetClient, cfg.SyncTargetName, cfg.SyncTargetUID)

	return nil
}

func StartHeartbeat(ctx context.Context, kcpSyncTargetClient kcpclientset.Interface, syncTargetName, syncTargetUID string) {
	logger := klog.FromContext(ctx)

	// Attempt to heartbeat every interval
	go wait.UntilWithContext(ctx, func(ctx context.Context) {
		var heartbeatTime time.Time

		// TODO(marun) Figure out a strategy for backoff to avoid a thundering herd problem with lots of syncers
		// Attempt to heartbeat every second until successful. Errors are logged instead of being returned so the
		// poll error can be safely ignored.
		_ = wait.PollImmediateInfiniteWithContext(ctx, 1*time.Second, func(ctx context.Context) (bool, error) {
			patchBytes := []byte(fmt.Sprintf(`[{"op":"test","path":"/metadata/uid","value":%q},{"op":"replace","path":"/status/lastSyncerHeartbeatTime","value":%q}]`, syncTargetUID, time.Now().Format(time.RFC3339)))
			syncTarget, err := kcpSyncTargetClient.WorkloadV1alpha1().SyncTargets().Patch(ctx, syncTargetName, types.JSONPatchType, patchBytes, metav1.PatchOptions{}, "status")
			if err != nil {
				logger.Error(err, "failed to set status.lastSyncerHeartbeatTime")
				return false, nil
			}

			heartbeatTime = syncTarget.Status.LastSyncerHeartbeatTime.Time
			return true, nil
		})
		logger.V(5).Info("Heartbeat set", "heartbeatTime", heartbeatTime)
	}, heartbeatInterval)
}

type filteringGVRSource struct {
	ddsif.GVRSource
	keepGVR func(gvr schema.GroupVersionResource) bool
}

func (s *filteringGVRSource) GVRs() map[schema.GroupVersionResource]ddsif.GVRPartialMetadata {
	gvrs := s.GVRSource.GVRs()
	filteredGVRs := make(map[schema.GroupVersionResource]ddsif.GVRPartialMetadata, len(gvrs))
	for gvr, metadata := range gvrs {
		if !s.keepGVR(gvr) {
			continue
		}
		filteredGVRs[gvr] = metadata
	}
	return filteredGVRs
}

type delegatingCleaner struct {
	delegate shared.Cleaner
}

func (s *delegatingCleaner) PlanCleaning(key string) {
	if s.delegate == nil {
		return
	}
	s.delegate.PlanCleaning(key)
}

func (s *delegatingCleaner) CancelCleaning(key string) {
	if s.delegate == nil {
		return
	}
	s.delegate.CancelCleaning(key)
}
