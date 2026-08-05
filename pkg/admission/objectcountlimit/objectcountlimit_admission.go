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

package objectcountlimit

import (
	"context"
	"fmt"
	"io"

	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apiserver/pkg/admission"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/klog/v2"

	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	kcpinformers "github.com/kcp-dev/sdk/client/informers/externalversions"
	corev1alpha1listers "github.com/kcp-dev/sdk/client/listers/core/v1alpha1"

	"github.com/kcp-dev/kcp/pkg/admission/initializers"
	"github.com/kcp-dev/kcp/pkg/objectcount"
)

// PluginName is the name of this admission plugin.
const PluginName = "core.kcp.io/LogicalClusterObjectCountLimit"

// Register registers this admission plugin.
func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName,
		func(_ io.Reader) (admission.Interface, error) {
			return &objectCountLimit{
				Handler: admission.NewHandler(admission.Create, admission.Delete),
			}, nil
		},
	)
}

// objectCountLimit is a validating admission plugin enforcing a hard limit on
// the total number of objects in a logical cluster. The current count is
// tracked by the objectcount.Registry: an authoritative base from a periodic
// etcd scan plus a delta maintained here (+1 per admitted create, -1 per
// delete). The limit is resolved from the core.kcp.io/max-total-objects
// annotation on the LogicalCluster, falling back to the shard-wide default.
type objectCountLimit struct {
	*admission.Handler

	registry             *objectcount.Registry
	logicalClusterLister corev1alpha1listers.LogicalClusterClusterLister
}

var _ admission.ValidationInterface = &objectCountLimit{}
var _ = initializers.WantsKcpInformers(&objectCountLimit{})
var _ = initializers.WantsObjectCountRegistry(&objectCountLimit{})

// ValidateInitialization validates all the expected fields are set.
func (o *objectCountLimit) ValidateInitialization() error {
	if o.registry == nil {
		return fmt.Errorf("missing objectCountRegistry")
	}
	if o.logicalClusterLister == nil {
		return fmt.Errorf("missing logicalClusterLister")
	}
	return nil
}

// Validate enforces the total object count limit of the logical cluster of
// the request. Deletes are always allowed and decrement the tracked count.
func (o *objectCountLimit) Validate(ctx context.Context, a admission.Attributes, _ admission.ObjectInterfaces) error {
	if !o.registry.EnforcementActive() {
		return nil
	}

	// Only persisted objects count; subresource requests never create objects.
	if a.GetSubresource() != "" {
		return nil
	}

	// Skip workspace bootstrapping resources (like the kubequota plugin does)
	// and events, which are short-lived and needed to debug an exhausted
	// logical cluster.
	switch a.GetResource().GroupResource() {
	case corev1alpha1.SchemeGroupVersion.WithResource("logicalclusters").GroupResource():
		return nil
	case rbacv1.SchemeGroupVersion.WithResource("clusterrolebindings").GroupResource():
		if a.GetName() == "workspace-admin" {
			return nil
		}
	case corev1.SchemeGroupVersion.WithResource("events").GroupResource(),
		eventsv1.SchemeGroupVersion.WithResource("events").GroupResource():
		return nil
	}

	cluster, err := genericapirequest.ValidClusterFrom(ctx)
	if err != nil {
		return err
	}

	if a.GetOperation() == admission.Delete {
		o.registry.Dec(cluster.Name)
		return nil
	}

	// The scan corrects overcounting from writes that fail after admission,
	// so count the create optimistically in all allowed paths below.
	logicalCluster, err := o.logicalClusterLister.Cluster(cluster.Name).Get(corev1alpha1.LogicalClusterName)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			// Fail open: enforcing quota is less important than availability.
			klog.FromContext(ctx).Error(err, "failed to get LogicalCluster, skipping object count limit", "cluster", cluster.Name)
		}
		o.registry.Inc(cluster.Name)
		return nil
	}

	// Don't enforce before the logical cluster is fully bootstrapped.
	if logicalCluster.Status.Phase != corev1alpha1.LogicalClusterPhaseReady {
		o.registry.Inc(cluster.Name)
		return nil
	}

	limit := o.registry.LimitFor(cluster.Name, logicalCluster.Annotations)
	if limit <= 0 {
		o.registry.Inc(cluster.Name)
		return nil
	}

	if count := o.registry.Count(cluster.Name); count >= limit {
		return admission.NewForbidden(a, fmt.Errorf(
			"logical cluster %q has reached its total object count limit (%d/%d objects); delete objects to free up capacity",
			cluster.Name, count, limit))
	}

	o.registry.Inc(cluster.Name)
	return nil
}

func (o *objectCountLimit) SetKcpInformers(local, _ kcpinformers.SharedInformerFactory) {
	logicalClusterInformer := local.Core().V1alpha1().LogicalClusters()
	o.logicalClusterLister = logicalClusterInformer.Lister()
	o.SetReadyFunc(logicalClusterInformer.Informer().HasSynced)
}

func (o *objectCountLimit) SetObjectCountRegistry(registry *objectcount.Registry) {
	o.registry = registry
}
