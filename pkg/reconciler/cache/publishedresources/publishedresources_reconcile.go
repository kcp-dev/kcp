/*
Copyright 2026 The KCP Authors.

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

package publishedresources

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"

	"github.com/kcp-dev/logicalcluster/v3"

	cachev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/cache/v1alpha1"
)

type reconcileStatus int

const (
	reconcileStatusStopAndRequeue reconcileStatus = iota
	reconcileStatusContinue
)

type reconciler interface {
	reconcile(ctx context.Context, workspace *cachev1alpha1.PublishedResource) (reconcileStatus, error)
}

// reconcile reconciles the workspace objects. It is intended to be single reconciler for all the
// workspace replated operations. For now it has single reconciler that updates the status of the
// workspace based on the mount status.
func (c *Controller) reconcile(ctx context.Context, cluster logicalcluster.Name, ws *cachev1alpha1.PublishedResource) (bool, error) {
	reconcilers := []reconciler{
		&finalizer{},
		&checker{
			listSelectedResources: func(ctx context.Context, publishedResource *cachev1alpha1.PublishedResource) (*unstructured.UnstructuredList, error) {
				return c.listSelectedResources(ctx, cluster, publishedResource)
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

	return requeue, utilerrors.NewAggregate(errs)
}

func (c *Controller) listSelectedResources(ctx context.Context, cluster logicalcluster.Name, publishedResource *cachev1alpha1.PublishedResource) (*unstructured.UnstructuredList, error) {
	gvr := schema.GroupVersionResource{
		Group:    publishedResource.Spec.Group,
		Version:  publishedResource.Spec.Version,
		Resource: publishedResource.Spec.Resource,
	}

	resources, err := c.dynamicClient.Cluster(cluster.Path()).Resource(gvr).List(ctx, metav1.ListOptions{
		//LabelSelector: labels.SelectorFromSet(publishedResource.Spec.LabelSelector.MatchLabels).String(),
	})
	if err != nil {
		return nil, err
	}

	return resources, nil
}
