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
	"k8s.io/apimachinery/pkg/labels"
	utilserrors "k8s.io/apimachinery/pkg/util/errors"

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
				return c.clusterWorkspaceShardLister.Cluster(tenancyv1alpha1.RootCluster).Get(name)
			},
			listShards:                c.clusterWorkspaceShardLister.List,
			logicalClusterAdminConfig: c.logicalClusterAdminConfig,
		},
		&phaseReconciler{
			getShardWithQuorum: func(ctx context.Context, name string, options metav1.GetOptions) (*tenancyv1alpha1.ClusterWorkspaceShard, error) {
				return c.kcpClusterClient.Cluster(tenancyv1alpha1.RootCluster).TenancyV1alpha1().ClusterWorkspaceShards().Get(ctx, name, options)
			},
			getAPIBindings: func(clusterName logicalcluster.Name) ([]*apisv1alpha1.APIBinding, error) {
				return c.apiBindingLister.Cluster(clusterName).List(labels.Everything())
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
