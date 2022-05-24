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
	"strings"
	"time"

	"github.com/kcp-dev/logicalcluster"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/pkg/version"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	workloadcliplugin "github.com/kcp-dev/kcp/pkg/cliplugins/workload/plugin"
	"github.com/kcp-dev/kcp/pkg/syncer/spec"
	"github.com/kcp-dev/kcp/pkg/syncer/status"
	"github.com/kcp-dev/kcp/third_party/keyfunctions"
)

const (
	advancedSchedulingFeatureAnnotation = "featuregates.experimental.workloads.kcp.dev/advancedscheduling"

	resyncPeriod = 10 * time.Hour

	// TODO(marun) Coordinate this value with the interval configured for the heartbeat controller
	heartbeatInterval = 20 * time.Second

	// TODO(marun) Ensure backoff rather than using a constant to avoid thundering herds
	gvrQueryInterval = 1 * time.Second
)

// SyncerConfig defines the syncer configuration that is guaranteed to
// vary across syncer deployments. Capturing these details in a struct
// simplifies defining these details in test fixture.
type SyncerConfig struct {
	UpstreamConfig      *rest.Config
	DownstreamConfig    *rest.Config
	ResourcesToSync     sets.String
	KCPClusterName      logicalcluster.Name
	WorkloadClusterName string
}

func (sc *SyncerConfig) ID() string {
	return workloadcliplugin.GetSyncerID(sc.KCPClusterName.String(), sc.WorkloadClusterName)
}

func StartSyncer(ctx context.Context, cfg *SyncerConfig, numSyncerThreads int, importPollInterval time.Duration) error {
	klog.Infof("Starting syncer for logical-cluster: %s, workload-cluster: %s", cfg.KCPClusterName, cfg.WorkloadClusterName)

	kcpVersion := version.Get().GitVersion

	kcpClusterClient, err := kcpclient.NewClusterForConfig(rest.AddUserAgent(rest.CopyConfig(cfg.UpstreamConfig), "kcp#syncer/"+kcpVersion))
	if err != nil {
		return err
	}

	// TODO(david): Implement real support for several virtual workspace URLs that can change over time.
	// TODO(david): For now we retrieve the syncerVirtualWorkpaceURL at start, since we temporarily stick to a single URL (sharding not supported).
	// TODO(david): But the complete implementation should setup a WorkloadCluster informer, create spec and status syncer for every URLs found in the
	// TODO(david): Status.SyncerVirtualWorkspaceURLs slice, and update them each time this list changes.
	var syncerVirtualWorkspaceURL string
	// TODO(david): we need to provide user-facing details if this polling goes on forever. Blocking here is a bad UX.
	// TODO(david): Also, any regressions in our code will make any e2e test that starts a syncer (at least in-process)
	// TODO(david): block until it hits the 10 minute overall test timeout.
	klog.Infof("Attempting to retrieve the Syncer virtual workspace URL from WorkloadCluster %s|%s", cfg.KCPClusterName, cfg.WorkloadClusterName)
	err = wait.PollImmediateInfinite(5*time.Second, func() (bool, error) {
		workloadCluster, err := kcpClusterClient.Cluster(cfg.KCPClusterName).WorkloadV1alpha1().WorkloadClusters().Get(ctx, cfg.WorkloadClusterName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		if len(workloadCluster.Status.VirtualWorkspaces) == 0 {
			return false, nil
		}

		if len(workloadCluster.Status.VirtualWorkspaces) > 1 {
			klog.Errorf("WorkloadCluster %s|%s should not have several Syncer virtual workspace URLs: not supported for now, ignoring additional URLs", cfg.KCPClusterName, cfg.WorkloadClusterName)
		}
		syncerVirtualWorkspaceURL = workloadCluster.Status.VirtualWorkspaces[0].URL
		return true, nil
	})
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
	apiImporter, err := NewAPIImporter(cfg.UpstreamConfig, cfg.DownstreamConfig, resources, cfg.KCPClusterName, cfg.WorkloadClusterName)
	if err != nil {
		return err
	}
	go apiImporter.Start(ctx, importPollInterval)

	upstreamConfig := rest.CopyConfig(cfg.UpstreamConfig)
	upstreamConfig.Host = syncerVirtualWorkspaceURL
	upstreamConfig.UserAgent = "kcp#spec-syncer/" + kcpVersion
	downstreamConfig := rest.CopyConfig(cfg.DownstreamConfig)
	downstreamConfig.UserAgent = "kcp#status-syncer/" + kcpVersion

	upstreamDynamicClient, err := dynamic.NewClusterForConfig(upstreamConfig)
	if err != nil {
		return err
	}
	downstreamDynamicClient, err := dynamic.NewForConfig(downstreamConfig)
	if err != nil {
		return err
	}
	upstreamDiscoveryClient, err := discovery.NewDiscoveryClientForConfig(upstreamConfig)
	if err != nil {
		return err
	}

	upstreamInformers := dynamicinformer.NewFilteredDynamicSharedInformerFactory(upstreamDynamicClient.Cluster(logicalcluster.Wildcard), resyncPeriod, metav1.NamespaceAll, func(o *metav1.ListOptions) {
		o.LabelSelector = workloadv1alpha1.InternalClusterResourceStateLabelPrefix + cfg.WorkloadClusterName + "=" + string(workloadv1alpha1.ResourceStateSync)
	})
	downstreamInformers := dynamicinformer.NewFilteredDynamicSharedInformerFactoryWithOptions(downstreamDynamicClient, metav1.NamespaceAll, func(o *metav1.ListOptions) {
		o.LabelSelector = workloadv1alpha1.InternalDownstreamClusterLabel + "=" + cfg.WorkloadClusterName
	}, cache.WithResyncPeriod(resyncPeriod), cache.WithKeyFunction(keyfunctions.DeletionHandlingMetaNamespaceKeyFunc))

	// TODO(ncdc): we need to provide user-facing details if this polling goes on forever. Blocking here is a bad UX.
	// TODO(ncdc): Also, any regressions in our code will make any e2e test that starts a syncer (at least in-process)
	// TODO(ncdc): block until it hits the 10 minute overall test timeout.
	//
	// Block syncer start on gvr discovery completing successfully and
	// including the resources configured for syncing. The spec and status
	// syncers depend on the types being present to start their informers.
	var gvrs []schema.GroupVersionResource
	err = wait.PollImmediateInfinite(gvrQueryInterval, func() (bool, error) {
		klog.Infof("Attempting to retrieve GVRs from upstream clusterName %s (for pcluster %s)", cfg.KCPClusterName, cfg.WorkloadClusterName)

		var err error
		// Get all types the upstream API server knows about.
		// TODO: watch this and learn about new types, or forget about old ones.
		gvrs, err = getAllGVRs(upstreamDiscoveryClient, resources...)
		// TODO(marun) Should some of these errors be fatal?
		if err != nil {
			klog.Errorf("Failed to retrieve GVRs from kcp: %v", err)
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		// Should never happen
		return err
	}

	// Check whether we're in the Advanced Scheduling feature-gated mode.
	workloadCluster, err := kcpClusterClient.Cluster(cfg.KCPClusterName).WorkloadV1alpha1().WorkloadClusters().Get(ctx, cfg.WorkloadClusterName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	advancedSchedulingEnabled := false
	if workloadCluster.GetAnnotations()[advancedSchedulingFeatureAnnotation] == "true" {
		klog.Infof("Advanced Scheduling feature is enabled for workloadCluster %s", cfg.WorkloadClusterName)
		advancedSchedulingEnabled = true
	}

	klog.Infof("Creating spec syncer for clusterName %s to pcluster %s, resources %v", cfg.KCPClusterName, cfg.WorkloadClusterName, resources)
	upstreamURL, err := url.Parse(cfg.UpstreamConfig.Host)
	if err != nil {
		return err
	}
	specSyncer, err := spec.NewSpecSyncer(gvrs, cfg.KCPClusterName, cfg.WorkloadClusterName, upstreamURL, advancedSchedulingEnabled,
		upstreamDynamicClient, downstreamDynamicClient, upstreamInformers, downstreamInformers)
	if err != nil {
		return err
	}

	klog.Infof("Creating status syncer for clusterName %s from pcluster %s, resources %v", cfg.KCPClusterName, cfg.WorkloadClusterName, resources)
	statusSyncer, err := status.NewStatusSyncer(gvrs, cfg.KCPClusterName, cfg.WorkloadClusterName, advancedSchedulingEnabled,
		upstreamDynamicClient, downstreamDynamicClient, upstreamInformers, downstreamInformers)
	if err != nil {
		return err
	}

	upstreamInformers.Start(ctx.Done())
	downstreamInformers.Start(ctx.Done())

	upstreamInformers.WaitForCacheSync(ctx.Done())
	downstreamInformers.WaitForCacheSync(ctx.Done())

	go specSyncer.Start(ctx, numSyncerThreads)
	go statusSyncer.Start(ctx, numSyncerThreads)

	// Attempt to heartbeat every interval
	go wait.UntilWithContext(ctx, func(ctx context.Context) {
		var heartbeatTime time.Time

		// TODO(marun) Figure out a strategy for backoff to avoid a thundering herd problem with lots of syncers

		// Attempt to heartbeat every second until successful. Errors are logged instead of being returned so the
		// poll error can be safely ignored.
		_ = wait.PollImmediateInfiniteWithContext(ctx, 1*time.Second, func(ctx context.Context) (bool, error) {
			patchBytes := []byte(fmt.Sprintf(`[{"op":"replace","path":"/status/lastSyncerHeartbeatTime","value":%q}]`, time.Now().Format(time.RFC3339)))
			workloadCluster, err := kcpClusterClient.Cluster(cfg.KCPClusterName).WorkloadV1alpha1().WorkloadClusters().Patch(ctx, cfg.WorkloadClusterName, types.JSONPatchType, patchBytes, metav1.PatchOptions{}, "status")
			if err != nil {
				klog.Errorf("failed to set status.lastSyncerHeartbeatTime for WorkloadCluster %s|%s: %v", cfg.KCPClusterName, cfg.WorkloadClusterName, err)
				return false, nil
			}
			heartbeatTime = workloadCluster.Status.LastSyncerHeartbeatTime.Time
			return true, nil
		})

		klog.V(5).Infof("Heartbeat set for WorkloadCluster %s|%s: %s", cfg.KCPClusterName, cfg.WorkloadClusterName, heartbeatTime)

	}, heartbeatInterval)

	return nil
}

func contains(ss []string, s string) bool {
	for _, n := range ss {
		if n == s {
			return true
		}
	}
	return false
}

func getAllGVRs(discoveryClient discovery.DiscoveryInterface, resourcesToSync ...string) ([]schema.GroupVersionResource, error) {
	toSyncSet := sets.NewString(resourcesToSync...)
	willBeSyncedSet := sets.NewString()
	rs, err := discoveryClient.ServerPreferredResources()
	if err != nil {
		if strings.Contains(err.Error(), "unable to retrieve the complete list of server APIs") {
			// This error may occur when some API resources added from CRDs are not completely ready.
			// We should just retry without a limit on the number of retries in such a case.
			//
			// In fact this might be related to a bug in the changes made on the feature-logical-cluster
			// Kubernetes branch to support legacy schema resources added as CRDs.
			// If this is confirmed, this test will be removed when the CRD bug is fixed.
			return nil, err
		} else {
			return nil, err
		}
	}
	// TODO(jmprusi): Added Configmaps and Secrets to the default syncing, but we should figure out
	//                a way to avoid doing that: https://github.com/kcp-dev/kcp/issues/727
	gvrstrs := sets.NewString("configmaps.v1.", "secrets.v1.") // A syncer should always watch secrets and configmaps.
	for _, r := range rs {
		// v1 -> v1.
		// apps/v1 -> v1.apps
		// tekton.dev/v1beta1 -> v1beta1.tekton.dev
		groupVersion, err := schema.ParseGroupVersion(r.GroupVersion)
		if err != nil {
			klog.Warningf("Unable to parse GroupVersion %s : %v", r.GroupVersion, err)
			continue
		}
		vr := groupVersion.Version + "." + groupVersion.Group
		for _, ai := range r.APIResources {
			var willBeSynced string
			groupResource := schema.GroupResource{
				Group:    groupVersion.Group,
				Resource: ai.Name,
			}

			if toSyncSet.Has(groupResource.String()) {
				willBeSynced = groupResource.String()
			} else if toSyncSet.Has(ai.Name) {
				willBeSynced = ai.Name
			} else {
				// We're not interested in this resource type
				continue
			}
			if strings.Contains(ai.Name, "/") {
				// foo/status, pods/exec, namespace/finalize, etc.
				continue
			}
			if !ai.Namespaced {
				// Ignore cluster-scoped things.
				continue
			}
			if !contains(ai.Verbs, "watch") {
				klog.Infof("resource %s %s is not watchable: %v", vr, ai.Name, ai.Verbs)
				continue
			}
			gvrstrs.Insert(fmt.Sprintf("%s.%s", ai.Name, vr))
			willBeSyncedSet.Insert(willBeSynced)
		}
	}

	notFoundResourceTypes := toSyncSet.Difference(willBeSyncedSet)
	if notFoundResourceTypes.Len() != 0 {
		// Some of the API resources expected to be there are still not published by KCP.
		// We should just retry without a limit on the number of retries in such a case,
		// until the corresponding resources are added inside KCP as CRDs and published as API resources.
		return nil, fmt.Errorf("the following resource types were requested to be synced, but were not found in the KCP logical cluster: %v", notFoundResourceTypes.List())
	}

	gvrs := make([]schema.GroupVersionResource, 0, gvrstrs.Len())
	for _, gvrstr := range gvrstrs.List() {
		gvr, _ := schema.ParseResourceArg(gvrstr)
		if gvr == nil {
			klog.Warningf("Unable to parse resource %q as <resource>.<version>.<group>", gvrstr)
			continue
		}
		gvrs = append(gvrs, *gvr)
	}
	return gvrs, nil
}
