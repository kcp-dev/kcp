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
	"net/url"
	"time"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpdynamicinformer "github.com/kcp-dev/client-go/dynamic/dynamicinformer"
	"github.com/kcp-dev/logicalcluster/v3"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	kubernetesinformers "k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/pkg/version"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	kcpclusterclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	kcpinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	kcpfeatures "github.com/kcp-dev/kcp/pkg/features"
	"github.com/kcp-dev/kcp/pkg/syncer/namespace"
	"github.com/kcp-dev/kcp/pkg/syncer/resourcesync"
	"github.com/kcp-dev/kcp/pkg/syncer/spec"
	"github.com/kcp-dev/kcp/pkg/syncer/status"
	. "github.com/kcp-dev/kcp/tmc/pkg/logging"
)

const (
	AdvancedSchedulingFeatureAnnotation = "featuregates.experimental.workload.kcp.dev/advancedscheduling"

	resyncPeriod = 10 * time.Hour

	// TODO(marun) Coordinate this value with the interval configured for the heartbeat controller
	heartbeatInterval = 20 * time.Second
)

// SyncerConfig defines the syncer configuration that is guaranteed to
// vary across syncer deployments. Capturing these details in a struct
// simplifies defining these details in test fixture.
type SyncerConfig struct {
	UpstreamConfig                *rest.Config
	DownstreamConfig              *rest.Config
	ResourcesToSync               sets.String
	SyncTargetWorkspace           logicalcluster.Name
	SyncTargetName                string
	SyncTargetUID                 string
	DownstreamNamespaceCleanDelay time.Duration
	DNSImage                      string
}

func StartSyncer(ctx context.Context, cfg *SyncerConfig, numSyncerThreads int, importPollInterval time.Duration, syncerNamespace string) error {
	logger := klog.FromContext(ctx)
	logger = logger.WithValues(SyncTargetWorkspace, cfg.SyncTargetWorkspace, SyncTargetName, cfg.SyncTargetName)
	logger.V(2).Info("starting syncer")

	kcpVersion := version.Get().GitVersion

	bootstrapConfig := rest.CopyConfig(cfg.UpstreamConfig)
	rest.AddUserAgent(bootstrapConfig, "kcp#syncer/"+kcpVersion)
	kcpBootstrapClusterClient, err := kcpclusterclientset.NewForConfig(bootstrapConfig)
	if err != nil {
		return err
	}
	kcpBootstrapClient := kcpBootstrapClusterClient.Cluster(cfg.SyncTargetWorkspace)

	// kcpInformerFactory to watch a certain syncTarget
	kcpInformerFactory := kcpinformers.NewSharedScopedInformerFactoryWithOptions(kcpBootstrapClient, resyncPeriod, kcpinformers.WithTweakListOptions(
		func(listOptions *metav1.ListOptions) {
			listOptions.FieldSelector = fields.OneTermEqualSelector("metadata.name", cfg.SyncTargetName).String()
		},
	))

	// TODO(david): Implement real support for several virtual workspace URLs that can change over time.
	// TODO(david): For now we retrieve the syncerVirtualWorkpaceURL at start, since we temporarily stick to a single URL (sharding not supported).
	// TODO(david): But the complete implementation should setup a SyncTarget informer, create spec and status syncer for every URLs found in the
	// TODO(david): Status.SyncerVirtualWorkspaceURLs slice, and update them each time this list changes.
	var syncerVirtualWorkspaceURL string
	// TODO(david): we need to provide user-facing details if this polling goes on forever. Blocking here is a bad UX.
	// TODO(david): Also, any regressions in our code will make any e2e test that starts a syncer (at least in-process)
	// TODO(david): block until it hits the 10 minute overall test timeout.
	logger.Info("attempting to retrieve the Syncer virtual workspace URL")
	var syncTarget *workloadv1alpha1.SyncTarget
	err = wait.PollImmediateInfinite(5*time.Second, func() (bool, error) {
		var err error
		syncTarget, err = kcpBootstrapClient.WorkloadV1alpha1().SyncTargets().Get(ctx, cfg.SyncTargetName, metav1.GetOptions{})
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
		syncerVirtualWorkspaceURL = syncTarget.Status.VirtualWorkspaces[0].URL
		return true, nil
	})
	if err != nil {
		return err
	}

	upstreamConfig := rest.CopyConfig(cfg.UpstreamConfig)
	upstreamConfig.Host = syncerVirtualWorkspaceURL
	rest.AddUserAgent(upstreamConfig, "kcp#spec-syncer/"+kcpVersion)

	upstreamDynamicClusterClient, err := kcpdynamic.NewForConfig(upstreamConfig)
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
	kcpImporterInformerFactory := kcpinformers.NewSharedScopedInformerFactoryWithOptions(kcpBootstrapClient, resyncPeriod)
	apiImporter, err := NewAPIImporter(
		cfg.UpstreamConfig, cfg.DownstreamConfig,
		kcpInformerFactory.Workload().V1alpha1().SyncTargets(),
		kcpImporterInformerFactory.Apiresource().V1alpha1().APIResourceImports(),
		resources,
		cfg.SyncTargetWorkspace, cfg.SyncTargetName, syncTarget.GetUID())
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

	syncTargetKey := workloadv1alpha1.ToSyncTargetKey(cfg.SyncTargetWorkspace, cfg.SyncTargetName)
	logger = logger.WithValues(SyncTargetKey, syncTargetKey)
	ctx = klog.NewContext(ctx, logger)

	upstreamInformers := kcpdynamicinformer.NewFilteredDynamicSharedInformerFactory(upstreamDynamicClusterClient, resyncPeriod, func(o *metav1.ListOptions) {
		o.LabelSelector = workloadv1alpha1.ClusterResourceStateLabelPrefix + syncTargetKey + "=" + string(workloadv1alpha1.ResourceStateSync)
	})
	downstreamInformers := dynamicinformer.NewFilteredDynamicSharedInformerFactory(downstreamDynamicClient, resyncPeriod, metav1.NamespaceAll, func(o *metav1.ListOptions) {
		o.LabelSelector = workloadv1alpha1.InternalDownstreamClusterLabel + "=" + syncTargetKey
	})

	// downstreamInformerFactory to watch some DNS-related resources in the dns namespace
	downstreamInformerFactory := kubernetesinformers.NewSharedInformerFactoryWithOptions(downstreamKubeClient, resyncPeriod, kubernetesinformers.WithNamespace(syncerNamespace))
	serviceAccountLister := downstreamInformerFactory.Core().V1().ServiceAccounts().Lister()
	roleLister := downstreamInformerFactory.Rbac().V1().Roles().Lister()
	roleBindingLister := downstreamInformerFactory.Rbac().V1().RoleBindings().Lister()
	deploymentLister := downstreamInformerFactory.Apps().V1().Deployments().Lister()
	serviceLister := downstreamInformerFactory.Core().V1().Services().Lister()
	endpointLister := downstreamInformerFactory.Core().V1().Endpoints().Lister()

	syncerInformers, err := resourcesync.NewController(
		logger,
		upstreamDynamicClusterClient,
		downstreamDynamicClient,
		downstreamKubeClient,
		kcpBootstrapClient,
		kcpInformerFactory.Workload().V1alpha1().SyncTargets(),
		cfg.SyncTargetName,
		cfg.SyncTargetWorkspace,
		syncTarget.GetUID(),
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

	downstreamNamespaceController, err := namespace.NewDownstreamController(logger, cfg.SyncTargetWorkspace, cfg.SyncTargetName, syncTargetKey, syncTarget.GetUID(), syncerInformers, downstreamConfig, downstreamDynamicClient, upstreamInformers, downstreamInformers, syncerNamespace, cfg.DownstreamNamespaceCleanDelay)
	if err != nil {
		return err
	}

	specSyncer, err := spec.NewSpecSyncer(logger, cfg.SyncTargetWorkspace, cfg.SyncTargetName, syncTargetKey, upstreamURL, advancedSchedulingEnabled,
		upstreamDynamicClusterClient, downstreamDynamicClient, downstreamKubeClient, upstreamInformers, downstreamInformers, downstreamNamespaceController, syncerInformers, syncTarget.GetUID(),
		serviceAccountLister, roleLister, roleBindingLister, deploymentLister, serviceLister, endpointLister, syncerNamespace, cfg.DNSImage)
	if err != nil {
		return err
	}

	logger.Info("Creating status syncer")
	statusSyncer, err := status.NewStatusSyncer(logger, cfg.SyncTargetWorkspace, cfg.SyncTargetName, syncTargetKey, advancedSchedulingEnabled,
		upstreamDynamicClusterClient, downstreamDynamicClient, downstreamInformers, syncerInformers, syncTarget.GetUID())
	if err != nil {
		return err
	}

	upstreamInformers.Start(ctx.Done())
	downstreamInformers.Start(ctx.Done())
	kcpInformerFactory.Start(ctx.Done())
	downstreamInformerFactory.Start(ctx.Done())

	upstreamInformers.WaitForCacheSync(ctx.Done())
	downstreamInformers.WaitForCacheSync(ctx.Done())
	kcpInformerFactory.WaitForCacheSync(ctx.Done())
	downstreamInformerFactory.WaitForCacheSync(ctx.Done())

	go apiImporter.Start(klog.NewContext(ctx, logger.WithValues("resources", resources)), importPollInterval)
	go syncerInformers.Start(ctx, 1)
	go specSyncer.Start(ctx, numSyncerThreads)
	go statusSyncer.Start(ctx, numSyncerThreads)
	go downstreamNamespaceController.Start(ctx, numSyncerThreads)

	if kcpfeatures.DefaultFeatureGate.Enabled(kcpfeatures.SyncerTunnel) {
		go startSyncerTunnel(ctx, upstreamConfig, downstreamConfig, cfg.SyncTargetWorkspace, cfg.SyncTargetName)
	}

	// Attempt to heartbeat every interval
	go wait.UntilWithContext(ctx, func(ctx context.Context) {
		var heartbeatTime time.Time

		// TODO(marun) Figure out a strategy for backoff to avoid a thundering herd problem with lots of syncers
		// Attempt to heartbeat every second until successful. Errors are logged instead of being returned so the
		// poll error can be safely ignored.
		_ = wait.PollImmediateInfiniteWithContext(ctx, 1*time.Second, func(ctx context.Context) (bool, error) {
			patchBytes := []byte(fmt.Sprintf(`[{"op":"test","path":"/metadata/uid","value":%q},{"op":"replace","path":"/status/lastSyncerHeartbeatTime","value":%q}]`, cfg.SyncTargetUID, time.Now().Format(time.RFC3339)))
			syncTarget, err = kcpBootstrapClient.WorkloadV1alpha1().SyncTargets().Patch(ctx, cfg.SyncTargetName, types.JSONPatchType, patchBytes, metav1.PatchOptions{}, "status")
			if err != nil {
				logger.Error(err, "failed to set status.lastSyncerHeartbeatTime")
				return false, nil //nolint:nilerr
			}

			heartbeatTime = syncTarget.Status.LastSyncerHeartbeatTime.Time
			return true, nil
		})
		logger.V(5).Info("Heartbeat set", "heartbeatTime", heartbeatTime)
	}, heartbeatInterval)

	return nil
}
