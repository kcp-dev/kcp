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

package placement

import (
	"context"

	"github.com/kcp-dev/logicalcluster/v3"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	utilserrors "k8s.io/apimachinery/pkg/util/errors"

	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/indexers"
)

type reconcileStatus int

const (
	reconcileStatusStop reconcileStatus = iota
	reconcileStatusContinue
)

type reconciler interface {
	reconcile(ctx context.Context, placement *schedulingv1alpha1.Placement) (reconcileStatus, *schedulingv1alpha1.Placement, error)
}

func (c *controller) reconcile(ctx context.Context, placement *schedulingv1alpha1.Placement) error {
	reconcilers := []reconciler{
		&placementReconciler{
			listLocationsByPath: c.listLocationsByPath,
		},
		&placementNamespaceReconciler{
			listNamespacesWithAnnotation: c.listNamespacesWithAnnotation,
		},
	}

	var errs []error

	for _, r := range reconcilers {
		var err error
		var status reconcileStatus
		status, placement, err = r.reconcile(ctx, placement)
		if err != nil {
			errs = append(errs, err)
		}
		if status == reconcileStatusStop {
			break
		}
	}

	return utilserrors.NewAggregate(errs)
}

func (c *controller) listLocationsByPath(path logicalcluster.Path) ([]*schedulingv1alpha1.Location, error) {
	objs, err := c.locationIndexer.ByIndex(indexers.ByLogicalClusterPath, path.String())
	if err != nil {
		return nil, err
	}
	ret := make([]*schedulingv1alpha1.Location, 0, len(objs))
	for _, obj := range objs {
		ret = append(ret, obj.(*schedulingv1alpha1.Location))
	}
	return ret, nil
}

func (c *controller) listNamespacesWithAnnotation(clusterName logicalcluster.Name) ([]*corev1.Namespace, error) {
	items, err := c.namespaceLister.Cluster(clusterName).List(labels.Everything())
	if err != nil {
		return nil, err
	}
	ret := make([]*corev1.Namespace, 0, len(items))
	for _, ns := range items {
		_, foundPlacement := ns.Annotations[schedulingv1alpha1.PlacementAnnotationKey]
		if foundPlacement {
			ret = append(ret, ns)
		}
	}
	return ret, nil
}
