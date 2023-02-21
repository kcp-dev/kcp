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
	"errors"
	"fmt"
	"net/url"
	"time"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	kubernetesinformers "k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/pkg/version"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kcpclusterclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	kcpinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	kcpfeatures "github.com/kcp-dev/kcp/pkg/features"
	ddsif "github.com/kcp-dev/kcp/pkg/informer"
	"github.com/kcp-dev/kcp/pkg/syncer/controllermanager"
	"github.com/kcp-dev/kcp/pkg/syncer/endpoints"
	"github.com/kcp-dev/kcp/pkg/syncer/indexers"
	"github.com/kcp-dev/kcp/pkg/syncer/namespace"
	"github.com/kcp-dev/kcp/pkg/syncer/resourcesync"
	"github.com/kcp-dev/kcp/pkg/syncer/shared"
	"github.com/kcp-dev/kcp/pkg/syncer/spec"
	"github.com/kcp-dev/kcp/pkg/syncer/spec/mutators"
	"github.com/kcp-dev/kcp/pkg/syncer/status"
	"github.com/kcp-dev/kcp/pkg/syncer/upsync"
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
	ResourcesToSync               sets.String
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

	// TODO(david): Implement real support for several virtual workspace URLs that can change over time.
	// TODO(david): For now we retrieve the syncerVirtualWorkpaceURL at start, since we temporarily stick to a single URL (sharding not supported).
	// TODO(david): But the complete implementation should setup a SyncTarget informer, create spec and status syncer for every URLs found in the
	// TODO(david): Status.SyncerVirtualWorkspaceURLs slice, and update them each time this list changes.
	var syncerVirtualWorkspaceURL string
	var upsyncerVirtualWorkspaceURL string
	// TODO(david): we need to provide user-facing details if this polling goes on forever. Blocking here is a bad UX.
	// TODO(david): Also, any regressions in our code will make any e2e test that starts a syncer (at least in-process)
	// TODO(david): block until it hits the 10 minute overall test timeout.
	logger.Info("attempting to retrieve the Syncer virtual workspace URL")
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

		if len(syncTarget.Status.VirtualWorkspaces) == 0 {
			return false, nil
		}

		if len(syncTarget.Status.VirtualWorkspaces) > 1 {
			logger.Error(fmt.Errorf("SyncTarget should not have several Syncer virtual workspace URLs: not supported for now, ignoring additional URLs"), "error processing SyncTarget")
		}
		syncerVirtualWorkspaceURL = syncTarget.Status.VirtualWorkspaces[0].SyncerURL
		upsyncerVirtualWorkspaceURL = syncTarget.Status.VirtualWorkspaces[0].UpsyncerURL
		return true, nil
	})
	if err != nil {
		return err
	}

	upstreamConfig := rest.CopyConfig(cfg.UpstreamConfig)
	upstreamConfig.Host = syncerVirtualWorkspaceURL
	rest.AddUserAgent(upstreamConfig, "kcp#syncing/"+kcpVersion)
	upstreamSyncerClusterClient, err := kcpdynamic.NewForConfig(upstreamConfig)
	if err != nil {
		return err
	}

	upstreamUpsyncConfig := rest.CopyConfig(cfg.UpstreamConfig)
	upstreamUpsyncConfig.Host = upsyncerVirtualWorkspaceURL
	rest.AddUserAgent(upstreamUpsyncConfig, "kcp#upsyncing/"+kcpVersion)
	upstreamUpsyncerClusterClient, err := kcpdynamic.NewForConfig(upstreamUpsyncConfig)
	if err != nil {
		return err
	}

	// Resources are accepted as a set to ensure the provision of a
	// unique set of resources, but all subsequent consumption is via
	// slice whose entries are assumed to be unique.
	resources := cfg.ResourcesToSync.List()

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

	// syncerNamespaceInformerFactory to watch some DNS-related resources in the dns namespace
	syncerNamespaceInformerFactory := kubernetesinformers.NewSharedInformerFactoryWithOptions(downstreamKubeClient, resyncPeriod, kubernetesinformers.WithNamespace(syncerNamespace))

	downstreamSyncerDiscoveryClient := discovery.NewDiscoveryClient(downstreamKubeClient.RESTClient())
	syncTargetGVRSource, err := resourcesync.NewSyncTargetGVRSource(
		logger,
		downstreamSyncerDiscoveryClient,
		upstreamSyncerClusterClient,
		downstreamDynamicClient,
		downstreamKubeClient,
		kcpSyncTargetClient.WorkloadV1alpha1().SyncTargets(),
		kcpSyncTargetInformerFactory.Workload().V1alpha1().SyncTargets(),
		cfg.SyncTargetName,
		logicalcluster.From(syncTarget),
		syncTarget.GetUID(),
	)
	if err != nil {
		return err
	}

	ddsifForUpstreamSyncer, err := ddsif.NewDiscoveringDynamicSharedInformerFactory(upstreamSyncerClusterClient, nil, nil,
		&filteredGVRSource{
			GVRSource: syncTargetGVRSource,
			keepGVR: func(gvr schema.GroupVersionResource) bool {
				// Don't expose pods or endpoints via the syncer vw
				if gvr.Group == corev1.GroupName && (gvr.Resource == "pods" || gvr.Resource == "endpoints") {
					return false
				}
				return true
			},
		},
		cache.Indexers{})
	if err != nil {
		return err
	}

	ddsifForUpstreamUpsyncer, err := ddsif.NewDiscoveringDynamicSharedInformerFactory(upstreamUpsyncerClusterClient, nil, nil,
		&filteredGVRSource{
			GVRSource: syncTargetGVRSource,
			keepGVR: func(gvr schema.GroupVersionResource) bool {
				return gvr.Group == corev1.GroupName && (gvr.Resource == "persistentvolumes" ||
					gvr.Resource == "pods" ||
					gvr.Resource == "endpoints")
			},
		},
		cache.Indexers{})
	if err != nil {
		return err
	}

	ddsifForDownstream, err := ddsif.NewScopedDiscoveringDynamicSharedInformerFactory(downstreamDynamicClient, nil,
		func(o *metav1.ListOptions) {
			o.LabelSelector = workloadv1alpha1.InternalDownstreamClusterLabel + "=" + syncTargetKey
		},
		syncTargetGVRSource,
		cache.Indexers{
			indexers.ByNamespaceLocatorIndexName: indexers.IndexByNamespaceLocator,
		},
	)
	if err != nil {
		return err
	}

	// Check whether we're in the Advanced Scheduling feature-gated mode.
	advancedSchedulingEnabled := false
	if syncTarget.GetAnnotations()[AdvancedSchedulingFeatureAnnotation] == "true" {
		logger.Info("Advanced Scheduling feature is enabled")
		advancedSchedulingEnabled = true
	}

	logger.Info("Creating spec syncer")
	upstreamURL, err := url.Parse(cfg.UpstreamConfig.Host)
	if err != nil {
		return err
	}

	downstreamNamespaceController, err := namespace.NewDownstreamController(logger, logicalcluster.From(syncTarget), cfg.SyncTargetName, syncTargetKey, syncTarget.GetUID(), downstreamConfig, downstreamDynamicClient, ddsifForUpstreamSyncer, ddsifForDownstream, syncerNamespace, cfg.DownstreamNamespaceCleanDelay)
	if err != nil {
		return err
	}

	secretMutator := mutators.NewSecretMutator()
	secretsGVR := corev1.SchemeGroupVersion.WithResource("secrets")
	podspecableMutator := mutators.NewPodspecableMutator(upstreamURL, func(clusterName logicalcluster.Name, namespace string) ([]runtime.Object, error) {
		informers, notSynced := ddsifForUpstreamSyncer.Informers()
		informer, ok := informers[secretsGVR]
		if !ok {
			if shared.ContainsGVR(notSynced, secretsGVR) {
				return nil, fmt.Errorf("informer for gvr %v not synced in the upstream informer factory", secretsGVR)
			}
			return nil, fmt.Errorf("gvr %v should be known in the downstream upstream factory", secretsGVR)
		}
		if err != nil {
			return nil, errors.New("informer should be up and synced for namespaces in the upstream syncer informer factory")
		}
		return informer.Lister().ByCluster(clusterName).ByNamespace(namespace).List(labels.Everything())
	}, syncerNamespaceInformerFactory.Core().V1().Services().Lister(), logicalcluster.From(syncTarget), types.UID(cfg.SyncTargetUID), cfg.SyncTargetName, syncerNamespace, kcpfeatures.DefaultFeatureGate.Enabled(kcpfeatures.SyncerTunnel))

	specSyncer, err := spec.NewSpecSyncer(logger, logicalcluster.From(syncTarget), cfg.SyncTargetName, syncTargetKey, upstreamURL, advancedSchedulingEnabled,
		upstreamSyncerClusterClient, downstreamDynamicClient, downstreamKubeClient, ddsifForUpstreamSyncer, ddsifForDownstream, downstreamNamespaceController, syncTarget.GetUID(),
		syncerNamespace, syncerNamespaceInformerFactory, cfg.DNSImage, secretMutator, podspecableMutator)
	if err != nil {
		return err
	}

	logger.Info("Creating status syncer")
	statusSyncer, err := status.NewStatusSyncer(logger, logicalcluster.From(syncTarget), cfg.SyncTargetName, syncTargetKey, advancedSchedulingEnabled,
		upstreamSyncerClusterClient, downstreamDynamicClient, ddsifForUpstreamSyncer, ddsifForDownstream, syncTarget.GetUID())
	if err != nil {
		return err
	}

	logger.Info("Creating resource upsyncer")
	upSyncer, err := upsync.NewUpSyncer(logger, logicalcluster.From(syncTarget), cfg.SyncTargetName, syncTargetKey, upstreamUpsyncerClusterClient, downstreamDynamicClient, ddsifForUpstreamUpsyncer, ddsifForDownstream, syncTarget.GetUID())
	if err != nil {
		return err
	}

	// Start and sync informer factories
	var cacheSyncsForAlwaysRequiredGVRs []cache.InformerSynced
	for _, alwaysRequired := range []string{"secrets", "namespaces"} {
		gvr := corev1.SchemeGroupVersion.WithResource(alwaysRequired)
		if informer, err := ddsifForUpstreamSyncer.ForResource(gvr); err != nil {
			return err
		} else {
			cacheSyncsForAlwaysRequiredGVRs = append(cacheSyncsForAlwaysRequiredGVRs, informer.Informer().HasSynced)
		}
		if informer, err := ddsifForDownstream.ForResource(gvr); err != nil {
			return err
		} else {
			cacheSyncsForAlwaysRequiredGVRs = append(cacheSyncsForAlwaysRequiredGVRs, informer.Informer().HasSynced)
		}
	}
	ddsifForUpstreamSyncer.Start(ctx.Done())
	ddsifForUpstreamUpsyncer.Start(ctx.Done())
	ddsifForDownstream.Start(ctx.Done())

	kcpSyncTargetInformerFactory.Start(ctx.Done())
	syncerNamespaceInformerFactory.Start(ctx.Done())

	kcpSyncTargetInformerFactory.WaitForCacheSync(ctx.Done())
	syncerNamespaceInformerFactory.WaitForCacheSync(ctx.Done())
	cache.WaitForCacheSync(ctx.Done(), cacheSyncsForAlwaysRequiredGVRs...)

	go ddsifForUpstreamSyncer.StartWorker(ctx)
	go ddsifForUpstreamUpsyncer.StartWorker(ctx)
	go ddsifForDownstream.StartWorker(ctx)

	// Start static controllers
	go apiImporter.Start(klog.NewContext(ctx, logger.WithValues("resources", resources)), importPollInterval)
	go syncTargetGVRSource.Start(ctx, 1)
	go specSyncer.Start(ctx, numSyncerThreads)
	go statusSyncer.Start(ctx, numSyncerThreads)
	go upSyncer.Start(ctx, numSyncerThreads)
	go downstreamNamespaceController.Start(ctx, numSyncerThreads)

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
	go downstreamSyncerControllerManager.Start(ctx)

	// Start tunneler for POD access
	if kcpfeatures.DefaultFeatureGate.Enabled(kcpfeatures.SyncerTunnel) {
		go startSyncerTunnel(ctx, upstreamConfig, downstreamConfig, logicalcluster.From(syncTarget), cfg.SyncTargetName)
	}

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

type filteredGVRSource struct {
	ddsif.GVRSource
	keepGVR func(gvr schema.GroupVersionResource) bool
}

func (s *filteredGVRSource) GVRs() map[schema.GroupVersionResource]ddsif.GVRPartialMetadata {
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
