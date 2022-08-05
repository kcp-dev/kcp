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

package clusterworkspace

import (
	"context"

	"github.com/kcp-dev/logicalcluster/v2"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilserrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/tools/clusters"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
)

type reconcileStatus int

const (
	reconcileStatusStopAndRequeue reconcileStatus = iota
	reconcileStatusContinue
)

type reconciler interface {
	reconcile(ctx context.Context, workspace *tenancyv1alpha1.ClusterWorkspace) (reconcileStatus, error)
}

func (c *Controller) reconcile(ctx context.Context, ws *tenancyv1alpha1.ClusterWorkspace) (bool, error) {
	reconcilers := []reconciler{
		&metaDataReconciler{},
		&schedulingReconciler{
			getShard: func(name string) (*tenancyv1alpha1.ClusterWorkspaceShard, error) {
				return c.clusterWorkspaceShardLister.Get(clusters.ToClusterAwareKey(tenancyv1alpha1.RootCluster, name))
			},
			listShards: c.clusterWorkspaceShardLister.List,
		},
		&phaseReconciler{
			getShardWithQuorum: func(ctx context.Context, name string, options metav1.GetOptions) (*tenancyv1alpha1.ClusterWorkspaceShard, error) {
				rootCtx := logicalcluster.WithCluster(ctx, tenancyv1alpha1.RootCluster)
				return c.kcpClusterClient.TenancyV1alpha1().ClusterWorkspaceShards().Get(rootCtx, name, options)
			},
			getAPIBindings: func(clusterName logicalcluster.Name) ([]*apisv1alpha1.APIBinding, error) {
				objs, err := c.apiBindingIndexer.ByIndex(byWorkspace, clusterName.String())
				if err != nil {
					return nil, err
				}
				bindings := make([]*apisv1alpha1.APIBinding, len(objs))
				for i, obj := range objs {
					bindings[i] = obj.(*apisv1alpha1.APIBinding)
				}
				return bindings, nil
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
