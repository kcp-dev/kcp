/*
Copyright 2024 The KCP Authors.

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

package vcluster

import (
	"context"
	"net/url"

	"github.com/kcp-dev/logicalcluster/v3"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilserrors "k8s.io/apimachinery/pkg/util/errors"

	targetsv1alpha1 "github.com/kcp-dev/kcp/contrib/mounts-vw/apis/targets/v1alpha1"
	"github.com/kcp-dev/kcp/contrib/mounts-vw/state"
)

type reconcileStatus int

const (
	reconcileStatusStopAndRequeue reconcileStatus = iota
	reconcileStatusContinue
)

type reconciler interface {
	reconcile(ctx context.Context, target *targetsv1alpha1.TargetVCluster) (reconcileStatus, error)
}

// reconcile reconciles the workspace objects. It is intended to be single reconciler for all the
// workspace replated operations. For now it has single reconciler that updates the status of the
// workspace based on the mount status.
func (c *Controller) reconcile(ctx context.Context, target *targetsv1alpha1.TargetVCluster) (bool, error) {

	u, err := url.Parse(c.virtualWorkspaceURL)
	if err != nil {
		return false, err
	}

	reconcilers := []reconciler{
		&targetSecretReconciler{
			getSecret: func(ctx context.Context, cluster logicalcluster.Path, namespaces, name string) (*corev1.Secret, error) {
				return c.kubeClusterClient.CoreV1().Cluster(cluster).Secrets(namespaces).Get(ctx, name, metav1.GetOptions{})
			},
			setState: func(key string, value state.Value) {
				c.store.Set(state.KindVClusters, key, value)
			},
			deleteState: func(key string) {
				c.store.Delete(state.KindVClusters, key)
			},
			getVirtualWorkspaceURL: func() *url.URL {
				return u
			},
		},
	}

	var errs []error

	requeue := false
	for _, r := range reconcilers {
		var err error
		var status reconcileStatus
		status, err = r.reconcile(ctx, target)
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
