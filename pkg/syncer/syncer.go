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

	"github.com/kcp-dev/logicalcluster/v2"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/pkg/version"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	kcpinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	kcpfeatures "github.com/kcp-dev/kcp/pkg/features"
	"github.com/kcp-dev/kcp/pkg/syncer/namespace"
	"github.com/kcp-dev/kcp/pkg/syncer/resourcesync"
	"github.com/kcp-dev/kcp/pkg/syncer/spec"
	"github.com/kcp-dev/kcp/pkg/syncer/status"
	"github.com/kcp-dev/kcp/third_party/keyfunctions"
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
	UpstreamConfig      *rest.Config
	DownstreamConfig    *rest.Config
	ResourcesToSync     sets.String
	SyncTargetWorkspace logicalcluster.Name
	SyncTargetName      string
	SyncTargetUID       string
}

func StartSyncer(ctx context.Context, cfg *SyncerConfig, numSyncerThreads int, importPollInterval time.Duration) error {
	logger := klog.FromContext(ctx)
	logger.WithValues("target-workspace", cfg.SyncTargetWorkspace, "target-name", cfg.SyncTargetName)
	ctx = klog.NewContext(ctx, logger)
	logger.V(2).Info("starting syncer")

	kcpVersion := version.Get().GitVersion

	kcpClusterClient, err := kcpclient.NewClusterForConfig(rest.AddUserAgent(rest.CopyConfig(cfg.UpstreamConfig), "kcp#syncer/"+kcpVersion))
	if err != nil {
		return err
	}
	kcpClient := kcpClusterClient.Cluster(cfg.SyncTargetWorkspace)

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
		syncTarget, err = kcpClient.WorkloadV1alpha1().SyncTargets().Get(ctx, cfg.SyncTargetName, metav1.GetOptions{})
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

	kcpInformerFactory := kcpinformers.NewSharedInformerFactoryWithOptions(kcpClient, resyncPeriod)

	// Resources are accepted as a set to ensure the provision of a
	// unique set of resources, but all subsequent consumption is via
	// slice whose entries are assumed to be unique.
	resources := cfg.ResourcesToSync.List()

	// Start api import first because spec and status syncers are blocked by
	// gvr discovery finding all the configured resource types in the kcp
	// workspace.
	apiImporter, err := NewAPIImporter(cfg.UpstreamConfig, cfg.DownstreamConfig, kcpInformerFactory, resources, cfg.SyncTargetWorkspace, cfg.SyncTargetName)
	if err != nil {
		return err
	}

	upstreamConfig := rest.CopyConfig(cfg.UpstreamConfig)
	upstreamConfig.Host = syncerVirtualWorkspaceURL
	upstreamConfig.UserAgent = "kcp#spec-syncer/" + kcpVersion
	downstreamConfig := rest.CopyConfig(cfg.DownstreamConfig)
	downstreamConfig.UserAgent = "kcp#status-syncer/" + kcpVersion

	upstreamDynamicClusterClient, err := dynamic.NewClusterForConfig(upstreamConfig)
	if err != nil {
		return err
	}
	downstreamDynamicClient, err := dynamic.NewForConfig(downstreamConfig)
	if err != nil {
		return err
	}
	downstreamKubeClient, err := kubernetes.NewForConfig(downstreamConfig)
	if err != nil {
		return err
	}

	syncTargetKey := workloadv1alpha1.ToSyncTargetKey(cfg.SyncTargetWorkspace, cfg.SyncTargetName)
	upstreamInformers := dynamicinformer.NewFilteredDynamicSharedInformerFactory(upstreamDynamicClusterClient.Cluster(logicalcluster.Wildcard), resyncPeriod, metav1.NamespaceAll, func(o *metav1.ListOptions) {
		o.LabelSelector = workloadv1alpha1.ClusterResourceStateLabelPrefix + syncTargetKey + "=" + string(workloadv1alpha1.ResourceStateSync)
	})
	downstreamInformers := dynamicinformer.NewFilteredDynamicSharedInformerFactoryWithOptions(downstreamDynamicClient, metav1.NamespaceAll, func(o *metav1.ListOptions) {
		o.LabelSelector = workloadv1alpha1.InternalDownstreamClusterLabel + "=" + syncTargetKey
	}, cache.WithResyncPeriod(resyncPeriod), cache.WithKeyFunction(keyfunctions.DeletionHandlingMetaNamespaceKeyFunc))

	syncerInformers, err := resourcesync.NewController(
		upstreamDynamicClusterClient,
		downstreamDynamicClient,
		downstreamKubeClient,
		kcpClusterClient,
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
		klog.Infof("Advanced Scheduling feature is enabled for syncTarget %s", cfg.SyncTargetName)
		advancedSchedulingEnabled = true
	}

	klog.Infof("Creating spec syncer for SyncTarget %s|%s, resources %v", cfg.SyncTargetWorkspace, cfg.SyncTargetName, resources)
	upstreamURL, err := url.Parse(cfg.UpstreamConfig.Host)
	if err != nil {
		return err
	}
	specSyncer, err := spec.NewSpecSyncer(cfg.SyncTargetWorkspace, cfg.SyncTargetName, syncTargetKey, upstreamURL, advancedSchedulingEnabled,
		upstreamDynamicClusterClient, downstreamDynamicClient, upstreamInformers, downstreamInformers, syncerInformers, syncTarget.GetUID())
	if err != nil {
		return err
	}

	klog.Infof("Creating status syncer for SyncTarget %s|%s, resources %v", cfg.SyncTargetWorkspace, cfg.SyncTargetName, resources)
	statusSyncer, err := status.NewStatusSyncer(cfg.SyncTargetWorkspace, cfg.SyncTargetName, syncTargetKey, advancedSchedulingEnabled,
		upstreamDynamicClusterClient, downstreamDynamicClient, upstreamInformers, downstreamInformers, syncerInformers, syncTarget.GetUID())
	if err != nil {
		return err
	}

	downstreamNamespaceController, err := namespace.NewDownstreamController(cfg.SyncTargetWorkspace, cfg.SyncTargetName, syncTargetKey, syncTarget.GetUID(), downstreamDynamicClient, upstreamInformers, downstreamInformers)
	if err != nil {
		return err
	}

	upstreamNamespaceController, err := namespace.NewUpstreamController(cfg.SyncTargetWorkspace, cfg.SyncTargetName, syncTargetKey, syncTarget.GetUID(), downstreamDynamicClient, upstreamInformers, downstreamInformers)
	if err != nil {
		return err
	}

	upstreamInformers.Start(ctx.Done())
	downstreamInformers.Start(ctx.Done())
	kcpInformerFactory.Start(ctx.Done())

	upstreamInformers.WaitForCacheSync(ctx.Done())
	downstreamInformers.WaitForCacheSync(ctx.Done())
	kcpInformerFactory.WaitForCacheSync(ctx.Done())

	go apiImporter.Start(ctx, importPollInterval)
	go syncerInformers.Start(ctx, 1)
	go specSyncer.Start(ctx, numSyncerThreads)
	go statusSyncer.Start(ctx, numSyncerThreads)
	go downstreamNamespaceController.Start(ctx, numSyncerThreads)
	go upstreamNamespaceController.Start(ctx, numSyncerThreads)

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
			syncTarget, err = kcpClusterClient.Cluster(cfg.SyncTargetWorkspace).WorkloadV1alpha1().SyncTargets().Patch(ctx, cfg.SyncTargetName, types.JSONPatchType, patchBytes, metav1.PatchOptions{}, "status")
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
