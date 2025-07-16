/*
Copyright 2025 The KCP Authors.

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

package cachedresourceendpointslice

import (
	"context"
	"net/url"
	"path"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/logicalcluster/v3"

	virtualworkspacesoptions "github.com/kcp-dev/kcp/cmd/virtual-workspaces/options"
	"github.com/kcp-dev/kcp/pkg/logging"
	apisv1alpha2 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha2"
	cachev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/cache/v1alpha1"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
)

type reconcileStatus int

const (
	reconcileStatusContinue reconcileStatus = iota
	reconcileStatusStopAndRequeue
	reconcileStatusStop
)

type reconciler interface {
	reconcile(ctx context.Context, endpoints *cachev1alpha1.CachedResourceEndpointSlice) (reconcileStatus, error)
}

func (c *controller) reconcile(ctx context.Context, endpoints *cachev1alpha1.CachedResourceEndpointSlice) (bool, error) {
	reconcilers := []reconciler{
		&endpointsReconciler{
			getLogicalCluster: c.getLogicalCluster,
			getAPIBinding:     c.getAPIBinding,
			getCachedResource: c.getCachedResource,
			getMyShard:        c.getMyShard,
		},
	}

	var errs []error

	requeue := false
	for _, r := range reconcilers {
		var err error
		var status reconcileStatus
		status, err = r.reconcile(ctx, endpoints)
		if err != nil {
			errs = append(errs, err)
		}
		if status == reconcileStatusStopAndRequeue {
			requeue = true
			break
		}
		if status == reconcileStatusStop {
			break
		}
	}

	return requeue, utilerrors.NewAggregate(errs)
}

type endpointsReconciler struct {
	getLogicalCluster func(clusterName logicalcluster.Name) (*corev1alpha1.LogicalCluster, error)
	getAPIBinding     func(clusterName logicalcluster.Name, bindingName string) (*apisv1alpha2.APIBinding, error)
	getCachedResource func(clusterName logicalcluster.Name, name string) (*cachev1alpha1.CachedResource, error)
	getMyShard        func() (*corev1alpha1.Shard, error)
}

func (r *endpointsReconciler) reconcile(ctx context.Context, endpoints *cachev1alpha1.CachedResourceEndpointSlice) (reconcileStatus, error) {
	logger := klog.FromContext(ctx)

	shard, err := r.getMyShard()
	if err != nil {
		return reconcileStatusStopAndRequeue, err
	}

	addr, err := url.Parse(shard.Spec.VirtualWorkspaceURL)
	if err != nil {
		// Should never happen
		logger = logging.WithObject(logger, shard)
		logger.Error(
			err, "error parsing shard.spec.virtualWorkspaceURL",
			"VirtualWorkspaceURL", shard.Spec.VirtualWorkspaceURL,
		)
		return reconcileStatusStop, nil
	}

	// TODO(gmna0): this needs handling for per-shard URLs. To be completed
	// once we do CachedResource aggregation with APIExports.

	// Formats the Replication VW URL like so:
	//   /services/replication/<CachedResource cluster>/<CachedResource name>
	addr.Path = path.Join(
		addr.Path,
		virtualworkspacesoptions.DefaultRootPathPrefix,
		"replication",
		logicalcluster.From(endpoints).String(),
		endpoints.Spec.CachedResource.Name,
	)

	addrUrl := addr.String()

	_, err = r.getCachedResource(logicalcluster.From(endpoints), endpoints.Spec.CachedResource.Name)
	if err == nil {
		endpoints.Status.CachedResourceEndpoints = addURLIfNotPresent(endpoints.Status.CachedResourceEndpoints, addrUrl)
		return reconcileStatusContinue, nil
	}
	if apierrors.IsNotFound(err) {
		endpoints.Status.CachedResourceEndpoints = removeURLIfPresent(endpoints.Status.CachedResourceEndpoints, addrUrl)
		return reconcileStatusContinue, nil
	}

	return reconcileStatusStopAndRequeue, err
}

func addURLIfNotPresent(endpoints []cachev1alpha1.CachedResourceEndpoint, urlToAdd string) []cachev1alpha1.CachedResourceEndpoint {
	for _, endpoint := range endpoints {
		if endpoint.URL == urlToAdd {
			// Already in endpoints slice, nothing to do.
			return endpoints
		}
	}
	return append(endpoints, cachev1alpha1.CachedResourceEndpoint{
		URL: urlToAdd,
	})
}

func removeURLIfPresent(endpoints []cachev1alpha1.CachedResourceEndpoint, urlToRemove string) []cachev1alpha1.CachedResourceEndpoint {
	for i, endpoint := range endpoints {
		if endpoint.URL == urlToRemove {
			return append(endpoints[:i], endpoints[i+1:]...)
		}
	}
	return endpoints
}
