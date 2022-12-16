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

package logicalcluster

import (
	"context"
	"fmt"
	"io"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/admission"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"

	kcpinitializers "github.com/kcp-dev/kcp/pkg/admission/initializers"
	corev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/core/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
	kcpinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	corev1alpha1listers "github.com/kcp-dev/kcp/pkg/client/listers/core/v1alpha1"
)

// Protects deletion of LogicalCluster if spec.directlyDeletable is false.

const (
	PluginName = "core.kcp.dev/LogicalCluster"
)

func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName,
		func(_ io.Reader) (admission.Interface, error) {
			return &plugin{
				Handler: admission.NewHandler(admission.Create, admission.Update, admission.Delete),
			}, nil
		})
}

type plugin struct {
	*admission.Handler

	logicalClusterLister corev1alpha1listers.LogicalClusterClusterLister
}

// Ensure that the required admission interfaces are implemented.
var _ = admission.ValidationInterface(&plugin{})
var _ = admission.MutationInterface(&plugin{})
var _ = admission.InitializationValidator(&plugin{})
var _ = kcpinitializers.WantsKcpInformers(&plugin{})

var phaseOrdinal = map[corev1alpha1.LogicalClusterPhaseType]int{
	corev1alpha1.LogicalClusterPhaseType(""):     1,
	corev1alpha1.LogicalClusterPhaseScheduling:   2,
	corev1alpha1.LogicalClusterPhaseInitializing: 3,
	corev1alpha1.LogicalClusterPhaseReady:        4,
}

// Admit adds type initializer to status on transition to initializing phase.
func (o *plugin) Admit(ctx context.Context, a admission.Attributes, _ admission.ObjectInterfaces) (err error) {
	if a.GetResource().GroupResource() != corev1alpha1.Resource("logicalclusters") {
		return nil
	}

	switch a.GetOperation() {
	case admission.Update:
		if a.GetObject().GetObjectKind().GroupVersionKind() != corev1alpha1.SchemeGroupVersion.WithKind("LogicalCluster") {
			return nil
		}
		u, ok := a.GetObject().(*unstructured.Unstructured)
		if !ok {
			return fmt.Errorf("unexpected type %T", a.GetObject())
		}
		this := &corev1alpha1.LogicalCluster{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, this); err != nil {
			return fmt.Errorf("failed to convert unstructured to LogicalCluster: %w", err)
		}

		if a.GetOldObject().GetObjectKind().GroupVersionKind() != corev1alpha1.SchemeGroupVersion.WithKind("LogicalCluster") {
			return nil
		}
		oldU, ok := a.GetOldObject().(*unstructured.Unstructured)
		if !ok {
			return fmt.Errorf("unexpected type %T", a.GetOldObject())
		}
		old := &corev1alpha1.LogicalCluster{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(oldU.Object, old); err != nil {
			return fmt.Errorf("failed to convert unstructured to LogicalCluster: %w", err)
		}

		// we only admit at state transition to initializing
		transitioningToInitializing := old.Status.Phase != corev1alpha1.LogicalClusterPhaseInitializing && this.Status.Phase == corev1alpha1.LogicalClusterPhaseInitializing
		if !transitioningToInitializing {
			return nil
		}

		this.Status.Initializers = this.Spec.Initializers

		return updateUnstructured(u, this)
	}

	return nil
}

func (o *plugin) Validate(ctx context.Context, a admission.Attributes, _ admission.ObjectInterfaces) (err error) {
	clusterName, err := genericapirequest.ClusterNameFrom(ctx)
	if err != nil {
		return apierrors.NewInternalError(err)
	}

	if a.GetResource().GroupResource() != corev1alpha1.Resource("logicalclusters") {
		return nil
	}

	groups := sets.NewString(a.GetUserInfo().GetGroups()...)
	if groups.Has("system:masters") || groups.Has(bootstrap.SystemLogicalClusterAdmin) || groups.Has(bootstrap.SystemKcpWorkspaceBootstrapper) {
		return nil
	}

	switch a.GetOperation() {
	case admission.Update:
		u, ok := a.GetObject().(*unstructured.Unstructured)
		if !ok {
			return fmt.Errorf("unexpected type %T", a.GetObject())
		}
		this := &corev1alpha1.LogicalCluster{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, this); err != nil {
			return fmt.Errorf("failed to convert unstructured to LogicalCluster: %w", err)
		}

		u, ok = a.GetOldObject().(*unstructured.Unstructured)
		if !ok {
			return fmt.Errorf("unexpected type %T", a.GetObject())
		}
		old := &corev1alpha1.LogicalCluster{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, old); err != nil {
			return fmt.Errorf("failed to convert unstructured to LogicalCluster: %w", err)
		}

		oldSpec := toSet(old.Spec.Initializers)
		newSpec := toSet(this.Spec.Initializers)
		oldStatus := toSet(old.Status.Initializers)
		newStatus := toSet(this.Status.Initializers)

		if !oldSpec.Equal(newSpec) {
			return admission.NewForbidden(a, fmt.Errorf("spec.initializers is immutable"))
		}

		transitioningToInitializing := old.Status.Phase != corev1alpha1.LogicalClusterPhaseInitializing && this.Status.Phase == corev1alpha1.LogicalClusterPhaseInitializing
		if transitioningToInitializing && !newSpec.Equal(newStatus) {
			return admission.NewForbidden(a, fmt.Errorf("status.initializers do not equal spec.initializers"))
		}

		if !transitioningToInitializing && this.Status.Phase == corev1alpha1.LogicalClusterPhaseInitializing && !oldStatus.IsSuperset(newStatus) {
			return admission.NewForbidden(a, fmt.Errorf("status.initializers must not grow"))
		}

		if this.Status.Phase != corev1alpha1.LogicalClusterPhaseInitializing && !oldStatus.Equal(newStatus) {
			return admission.NewForbidden(a, fmt.Errorf("status.initializers is immutable after initilization"))
		}

		if old.Status.Phase == corev1alpha1.LogicalClusterPhaseInitializing && this.Status.Phase != corev1alpha1.LogicalClusterPhaseInitializing {
			if len(this.Status.Initializers) > 0 {
				return admission.NewForbidden(a, fmt.Errorf("status.initializers is not empty"))
			}
		}

		if phaseOrdinal[old.Status.Phase] > phaseOrdinal[this.Status.Phase] {
			return admission.NewForbidden(a, fmt.Errorf("cannot transition from %q to %q", old.Status.Phase, this.Status.Phase))
		}

		return nil

	case admission.Delete:
		this, err := o.logicalClusterLister.Cluster(clusterName).Get(corev1alpha1.LogicalClusterName)
		if err != nil {
			return fmt.Errorf("LogicalCluster cannot be deleted")
		}
		groups := sets.NewString(a.GetUserInfo().GetGroups()...)
		if !this.Spec.DirectlyDeletable && !groups.Has("system:masters") && !groups.Has(bootstrap.SystemLogicalClusterAdmin) {
			return admission.NewForbidden(a, fmt.Errorf("LogicalCluster cannot be deleted"))
		}

	case admission.Create:
		return admission.NewForbidden(a, fmt.Errorf("LogicalCluster cannot be created"))
	}

	return nil
}

func (o *plugin) ValidateInitialization() error {
	if o.logicalClusterLister == nil {
		return fmt.Errorf(PluginName + " plugin needs an LogicalCluster lister")
	}
	return nil
}

func (o *plugin) SetKcpInformers(informers kcpinformers.SharedInformerFactory) {
	logicalClustersReady := informers.Core().V1alpha1().LogicalClusters().Informer().HasSynced
	o.SetReadyFunc(func() bool {
		return logicalClustersReady()
	})
	o.logicalClusterLister = informers.Core().V1alpha1().LogicalClusters().Lister()
}

func toSet(initializers []corev1alpha1.LogicalClusterInitializer) sets.String {
	ret := sets.NewString()
	for _, initializer := range initializers {
		ret.Insert(string(initializer))
	}
	return ret
}

// updateUnstructured updates the given unstructured object to match the given cluster workspace.
func updateUnstructured(u *unstructured.Unstructured, cw *corev1alpha1.LogicalCluster) error {
	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(cw)
	if err != nil {
		return err
	}
	u.Object = raw
	return nil
}
