/*
Copyright 2026 The kcp Authors.

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
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
)

const (
	// watchCacheKickerInterval is just above the upstream apiserver's default
	// eventFreshDuration (75 s). Ticking at this cadence ensures each kicked
	// event finds the recent-quarter boundary in any inflated watchCache ring
	// older than the fresh window — letting upstream's resizeCacheLocked
	// shrink branch fire.
	watchCacheKickerInterval = 80 * time.Second

	// watchCacheKickerAnnotation carries a per-tick timestamp on each kicked
	// object so each patch produces a distinct etcd revision (and therefore
	// a watch event flowing through the cacher).
	watchCacheKickerAnnotation = "kcp.io/watchcache-kicker-tick"

	// watchCacheKickerFieldManager makes audit-log filtering trivial.
	watchCacheKickerFieldManager = "kcp-watchcache-kicker"
)

// installWatchCacheKicker periodically generates one watch event per target
// resource by patching a per-resource "kick object" in the root cluster.
// Works around the upstream watchCache bug where resizeCacheLocked is only
// called from updateCache(event) and gated on isCacheFullLocked(); without
// the kick, rings grow during workload bursts and never shrink during idle.
// See https://github.com/kubernetes/kubernetes/issues/139250.
//
// Cost: ~7 metadata patches every 80 s per shard. Each patch sets the same
// annotation key (only the timestamp value changes), so per-object growth
// is bounded. Field manager "kcp-watchcache-kicker" is used so the patches
// can be filtered in audit logs.
//
// If a target object is missing (e.g. no APIBindings in the root cluster),
// that resource is skipped silently — it is not an error.
func installWatchCacheKicker(
	ctx context.Context,
	kubeClient kcpkubernetesclientset.ClusterInterface,
	kcpClient kcpclientset.ClusterInterface,
) {
	logger := klog.FromContext(ctx).WithName("watchcache-kicker")
	logger.V(2).Info("starting", "interval", watchCacheKickerInterval)
	go wait.UntilWithContext(ctx, func(ctx context.Context) {
		kickAll(ctx, logger, kubeClient, kcpClient)
	}, watchCacheKickerInterval)
}

// kickAll performs one round of patches across the targeted resources.
// Per-target errors are logged at V(2) and otherwise ignored — a single
// missed kick just delays the shrink cascade by one tick.
func kickAll(
	ctx context.Context,
	logger klog.Logger,
	kubeClient kcpkubernetesclientset.ClusterInterface,
	kcpClient kcpclientset.ClusterInterface,
) {
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	patch := fmt.Appendf(nil, `{"metadata":{"annotations":{%q:%q}}}`, watchCacheKickerAnnotation, ts)
	opts := metav1.PatchOptions{FieldManager: watchCacheKickerFieldManager}
	root := core.RootCluster.Path()

	type op struct {
		resource string
		fn       func() error
	}
	ops := []op{
		{
			resource: "logicalclusters.core.kcp.io",
			fn: func() error {
				_, err := kcpClient.Cluster(root).CoreV1alpha1().LogicalClusters().
					Patch(ctx, corev1alpha1.LogicalClusterName, types.MergePatchType, patch, opts)
				return err
			},
		},
		{
			resource: "clusterrolebindings.rbac.authorization.k8s.io",
			fn: func() error {
				_, err := kubeClient.Cluster(root).RbacV1().ClusterRoleBindings().
					Patch(ctx, "system:masters", types.MergePatchType, patch, opts)
				return err
			},
		},
		{
			resource: "namespaces",
			fn: func() error {
				_, err := kubeClient.Cluster(root).CoreV1().Namespaces().
					Patch(ctx, "kcp-system", types.MergePatchType, patch, opts)
				return err
			},
		},
		{
			resource: "serviceaccounts",
			fn: func() error {
				_, err := kubeClient.Cluster(root).CoreV1().ServiceAccounts("kcp-system").
					Patch(ctx, "default", types.MergePatchType, patch, opts)
				return err
			},
		},
		{
			resource: "configmaps",
			fn: func() error {
				_, err := kubeClient.Cluster(root).CoreV1().ConfigMaps("kcp-system").
					Patch(ctx, "kube-root-ca.crt", types.MergePatchType, patch, opts)
				return err
			},
		},
		// apibindings / workspaces: kick the first one we find, if any.
		// These resources may legitimately be empty in the root cluster
		// (esp. in a freshly bootstrapped install), so we list first and
		// skip silently when empty.
		{
			resource: "apibindings.apis.kcp.io",
			fn: func() error {
				list, err := kcpClient.Cluster(root).ApisV1alpha2().APIBindings().List(ctx, metav1.ListOptions{Limit: 1})
				if err != nil || len(list.Items) == 0 {
					return err
				}
				_, err = kcpClient.Cluster(root).ApisV1alpha2().APIBindings().
					Patch(ctx, list.Items[0].Name, types.MergePatchType, patch, opts)
				return err
			},
		},
		{
			resource: "workspaces.tenancy.kcp.io",
			fn: func() error {
				list, err := kcpClient.Cluster(root).TenancyV1alpha1().Workspaces().List(ctx, metav1.ListOptions{Limit: 1})
				if err != nil || len(list.Items) == 0 {
					return err
				}
				_, err = kcpClient.Cluster(root).TenancyV1alpha1().Workspaces().
					Patch(ctx, list.Items[0].Name, types.MergePatchType, patch, opts)
				return err
			},
		},
	}

	for _, o := range ops {
		switch err := o.fn(); {
		case err == nil:
			logger.V(4).Info("kicked", "resource", o.resource)
		case apierrors.IsNotFound(err):
			logger.V(4).Info("kick target missing — skipping", "resource", o.resource)
		default:
			logger.V(2).Info("kick failed", "resource", o.resource, "err", err.Error())
		}
	}
}
