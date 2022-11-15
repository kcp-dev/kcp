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

	kcpcache "github.com/kcp-dev/apimachinery/pkg/cache"
	"github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v2"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilserrors "k8s.io/apimachinery/pkg/util/errors"
	restclient "k8s.io/client-go/rest"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	tenancyv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
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
	getShardByName := func(hash string) (*tenancyv1alpha1.ClusterWorkspaceShard, error) {
		shards, err := c.clusterWorkspaceShardIndexer.ByIndex(byBase36Sha224Name, hash)
		if err != nil {
			return nil, err
		}
		if len(shards) == 0 {
			return nil, nil
		}
		return shards[0].(*tenancyv1alpha1.ClusterWorkspaceShard), nil
	}

	// kcpLogicalClusterAdminClientFor returns a kcp client (i.e. a client that implements kcpclient.ClusterInterface) for the given shard.
	// the returned client establishes a direct connection with the shard with credentials stored in r.logicalClusterAdminConfig.
	// TODO:(p0lyn0mial): make it more efficient, maybe we need a per shard client pool or we could use an HTTPRoundTripper
	kcpDirectClientFor := func(shard *tenancyv1alpha1.ClusterWorkspaceShard) (kcpclientset.ClusterInterface, error) {
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
	kubeDirectClientFor := func(shard *tenancyv1alpha1.ClusterWorkspaceShard) (kubernetes.ClusterInterface, error) {
		shardConfig := restclient.CopyConfig(c.logicalClusterAdminConfig)
		shardConfig.Host = shard.Spec.BaseURL
		shardClient, err := kubernetes.NewForConfig(shardConfig)
		if err != nil {
			return nil, fmt.Errorf("failed to create shard %q kube client: %w", shard.Name, err)
		}
		return shardClient, nil
	}

	reconcilers := []reconciler{
		&metaDataReconciler{},
		&deletionReconciler{
			getThisWorkspace: func(ctx context.Context, cluster logicalcluster.Name) (*tenancyv1alpha1.ThisWorkspace, error) {
				return c.kcpExternalClient.Cluster(cluster).TenancyV1alpha1().ThisWorkspaces().Get(ctx, tenancyv1alpha1.ThisWorkspaceName, metav1.GetOptions{})
			},
			deleteThisWorkspace: func(ctx context.Context, cluster logicalcluster.Name) error {
				return c.kcpExternalClient.Cluster(cluster).TenancyV1alpha1().ThisWorkspaces().Delete(ctx, tenancyv1alpha1.ThisWorkspaceName, metav1.DeleteOptions{})
			},
		},
		&schedulingReconciler{
			getShard: func(name string) (*tenancyv1alpha1.ClusterWorkspaceShard, error) {
				return c.clusterWorkspaceShardLister.Cluster(tenancyv1alpha1.RootCluster).Get(name)
			},
			getShardByHash:                   getShardByName,
			listShards:                       c.clusterWorkspaceShardLister.List,
			kcpLogicalClusterAdminClientFor:  kcpDirectClientFor,
			kubeLogicalClusterAdminClientFor: kubeDirectClientFor,
		},
		&phaseReconciler{
			getThisWorkspace: func(ctx context.Context, cluster logicalcluster.Name) (*tenancyv1alpha1.ThisWorkspace, error) {
				return c.kcpExternalClient.Cluster(cluster).TenancyV1alpha1().ThisWorkspaces().Get(ctx, tenancyv1alpha1.ThisWorkspaceName, metav1.GetOptions{})
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
