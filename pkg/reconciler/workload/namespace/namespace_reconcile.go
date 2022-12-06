/*
Copyright 2022 The KCP Authors.

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

package namespace

import (
	"context"
	"time"

	"github.com/kcp-dev/logicalcluster/v3"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	utilserrors "k8s.io/apimachinery/pkg/util/errors"

	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
)

type reconcileStatus int

const (
	reconcileStatusStop reconcileStatus = iota
	reconcileStatusContinue
)

type reconciler interface {
	reconcile(ctx context.Context, ns *corev1.Namespace) (reconcileStatus, *corev1.Namespace, error)
}

func (c *controller) reconcile(ctx context.Context, ns *corev1.Namespace) error {
	reconcilers := []reconciler{
		&bindNamespaceReconciler{
			listPlacement:  c.listPlacement,
			patchNamespace: c.patchNamespace,
		},
		&placementSchedulingReconciler{
			listPlacement:  c.listPlacement,
			enqueueAfter:   c.enqueueAfter,
			patchNamespace: c.patchNamespace,
			now:            time.Now,
		},
		&statusConditionReconciler{
			patchNamespace: c.patchNamespace,
		},
	}

	var errs []error

	for _, r := range reconcilers {
		var err error
		var status reconcileStatus
		status, ns, err = r.reconcile(ctx, ns)
		if err != nil {
			errs = append(errs, err)
		}
		if status == reconcileStatusStop {
			break
		}
	}

	return utilserrors.NewAggregate(errs)
}

func (c *controller) listPlacement(clusterName logicalcluster.Path) ([]*schedulingv1alpha1.Placement, error) {
	return c.placementLister.Cluster(clusterName).List(labels.Everything())
}
