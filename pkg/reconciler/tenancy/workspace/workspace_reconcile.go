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

package workspace

import (
	"context"
	"fmt"
	"time"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilserrors "k8s.io/apimachinery/pkg/util/errors"
	restclient "k8s.io/client-go/rest"

	"github.com/kcp-dev/kcp/pkg/admission/workspacetypeexists"
	"github.com/kcp-dev/kcp/pkg/apis/core"
	corev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	tenancyv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/pkg/indexers"
)

type reconcileStatus int

const (
	reconcileStatusStopAndRequeue reconcileStatus = iota
	reconcileStatusContinue
)

type reconciler interface {
	reconcile(ctx context.Context, workspace *tenancyv1beta1.Workspace) (reconcileStatus, error)
}

func (c *Controller) reconcile(ctx context.Context, ws *tenancyv1beta1.Workspace) (bool, error) {
	getShardByName := func(hash string) (*corev1alpha1.Shard, error) {
		shards, err := c.globalShardIndexer.ByIndex(byBase36Sha224Name, hash)
		if err != nil {
			return nil, err
		}
		if len(shards) == 0 {
			return nil, apierrors.NewNotFound(corev1alpha1.Resource("shard"), hash)
		}
		return shards[0].(*corev1alpha1.Shard), nil
	}

	// kcpLogicalClusterAdminClientFor returns a kcp client (i.e. a client that implements kcpclient.ClusterInterface) for the given shard.
	// the returned client establishes a direct connection with the shard with credentials stored in r.logicalClusterAdminConfig.
	// TODO:(p0lyn0mial): make it more efficient, maybe we need a per shard client pool or we could use an HTTPRoundTripper
	kcpDirectClientFor := func(shard *corev1alpha1.Shard) (kcpclientset.ClusterInterface, error) {
		if shard.Name == c.shardName {
			return c.kcpClusterClient, nil
		}
		shardConfig := restclient.CopyConfig(c.logicalClusterAdminConfig)
		shardConfig.Host = shard.Spec.BaseURL
		shardClient, err := kcpclientset.NewForConfig(shardConfig)
		if err != nil {
			return nil, fmt.Errorf("failed to create shard %q kube client: %w", shard.Name, err)
		}
		return shardClient, nil
	}

	// kubeLogicalClusterAdminClientFor returns a kube client (i.e. a client that implements kubernetes.ClusterInterface) for the given shard.
	// the returned client establishes a direct connection with the shard with credentials stored in r.logicalClusterAdminConfig.
	// TODO:(p0lyn0mial): make it more efficient, maybe we need a per shard client pool or we could use an HTTPRoundTripper
	kubeDirectClientFor := func(shard *corev1alpha1.Shard) (kubernetes.ClusterInterface, error) {
		if shard.Name == c.shardName {
			return c.kubeClusterClient, nil
		}
		shardConfig := restclient.CopyConfig(c.logicalClusterAdminConfig)
		shardConfig.Host = shard.Spec.BaseURL
		shardClient, err := kubernetes.NewForConfig(shardConfig)
		if err != nil {
			return nil, fmt.Errorf("failed to create shard %q kube client: %w", shard.Name, err)
		}
		return shardClient, nil
	}

	getType := func(path logicalcluster.Path, name string) (*tenancyv1alpha1.WorkspaceType, error) {
		return indexers.ByPathAndName[*tenancyv1alpha1.WorkspaceType](tenancyv1alpha1.Resource("workspacetypes"), c.globalWorkspaceTypeIndexer, path, name)
	}

	reconcilers := []reconciler{
		&metaDataReconciler{},
		&deletionReconciler{
			getLogicalCluster: func(ctx context.Context, cluster logicalcluster.Path) (*corev1alpha1.LogicalCluster, error) {
				return c.kcpExternalClient.Cluster(cluster).CoreV1alpha1().LogicalClusters().Get(ctx, corev1alpha1.LogicalClusterName, metav1.GetOptions{})
			},
			deleteLogicalCluster: func(ctx context.Context, cluster logicalcluster.Path) error {
				return c.kcpExternalClient.Cluster(cluster).CoreV1alpha1().LogicalClusters().Delete(ctx, corev1alpha1.LogicalClusterName, metav1.DeleteOptions{})
			},
		},
		&schedulingReconciler{
			generateClusterName: randomClusterName,
			getShard: func(name string) (*corev1alpha1.Shard, error) {
				return c.globalShardLister.Cluster(core.RootCluster).Get(name)
			},
			getShardByHash:   getShardByName,
			listShards:       c.globalShardLister.List,
			getWorkspaceType: getType,
			getLogicalCluster: func(clusterName logicalcluster.Name) (*corev1alpha1.LogicalCluster, error) {
				return c.logicalClusterLister.Cluster(clusterName).Get(corev1alpha1.LogicalClusterName)
			},
			transitiveTypeResolver:           workspacetypeexists.NewTransitiveTypeResolver(getType),
			kcpLogicalClusterAdminClientFor:  kcpDirectClientFor,
			kubeLogicalClusterAdminClientFor: kubeDirectClientFor,
		},
		&phaseReconciler{
			getLogicalCluster: func(ctx context.Context, cluster logicalcluster.Path) (*corev1alpha1.LogicalCluster, error) {
				return c.kcpExternalClient.Cluster(cluster).CoreV1alpha1().LogicalClusters().Get(ctx, corev1alpha1.LogicalClusterName, metav1.GetOptions{})
			},
			requeueAfter: func(workspace *tenancyv1beta1.Workspace, after time.Duration) {
				c.queue.AddAfter(kcpcache.ToClusterAwareKey(logicalcluster.From(workspace).String(), "", workspace.Name), after)
			},
		},
	}

	var errs []error

	requeue := false
	for _, r := range reconcilers {
		var err error
		var status reconcileStatus
		status, err = r.reconcile(ctx, ws)
		if err != nil {
			errs = append(errs, err)
		}
		if status == reconcileStatusStopAndRequeue {
			requeue = true
			break
		}
	}

	return requeue, utilserrors.NewAggregate(errs)
}
