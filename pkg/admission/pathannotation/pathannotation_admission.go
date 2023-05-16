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

package pathannotation

import (
	"context"
	"fmt"
	"io"

	"github.com/kcp-dev/logicalcluster/v3"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/admission"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"

	kcpinitializers "github.com/kcp-dev/kcp/pkg/admission/initializers"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
	kcpinformers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions"
	corev1alpha1listers "github.com/kcp-dev/kcp/sdk/client/listers/core/v1alpha1"
)

const (
	PluginName = "kcp.io/PathAnnotation"
)

func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName,
		func(_ io.Reader) (admission.Interface, error) {
			return &pathAnnotationPlugin{
				Handler: admission.NewHandler(admission.Create, admission.Update),
			}, nil
		})
}

// Validate checks the value of the logical cluster path annotation to match the
// canonical path in the context.

// Admit sets the value of the logical cluster path annotation for some resources
// to match the canonical path in the context.

type pathAnnotationPlugin struct {
	*admission.Handler

	logicalClusterLister corev1alpha1listers.LogicalClusterClusterLister

	// getLogicalCluster is a convenience function for easier unit testing,
	// it reads a LogicalCluster resource with the given name and from the given cluster.
	getLogicalCluster func(clusterName logicalcluster.Name, name string) (*corev1alpha1.LogicalCluster, error)
}

var pathAnnotationResources = sets.New[string](
	apisv1alpha1.Resource("apiexports").String(),
	tenancyv1alpha1.Resource("workspacetypes").String(),
)

// Ensure that the required admission interfaces are implemented.
var _ = admission.ValidationInterface(&pathAnnotationPlugin{})
var _ = admission.MutationInterface(&pathAnnotationPlugin{})
var _ = admission.InitializationValidator(&pathAnnotationPlugin{})
var _ = kcpinitializers.WantsKcpInformers(&pathAnnotationPlugin{})

func (p *pathAnnotationPlugin) Admit(ctx context.Context, a admission.Attributes, _ admission.ObjectInterfaces) error {
	clusterName, err := genericapirequest.ClusterNameFrom(ctx)
	if err != nil {
		return apierrors.NewInternalError(err)
	}

	if a.GetOperation() != admission.Create && a.GetOperation() != admission.Update {
		return nil
	}

	if a.GetResource().GroupResource() == corev1alpha1.Resource("logicalclusters") {
		return nil
	}

	u, ok := a.GetObject().(metav1.Object)
	if !ok {
		return fmt.Errorf("unexpected type %T", a.GetObject())
	}

	annotations := u.GetAnnotations()
	value, found := annotations[core.LogicalClusterPathAnnotationKey]
	if !found && !pathAnnotationResources.Has(a.GetResource().GroupResource().String()) {
		return nil
	}

	logicalCluster, err := p.getLogicalCluster(clusterName, corev1alpha1.LogicalClusterName)
	if err != nil {
		return admission.NewForbidden(a, fmt.Errorf("cannot get this workspace: %w", err))
	}
	thisPath := logicalCluster.Annotations[core.LogicalClusterPathAnnotationKey]
	if thisPath == "" {
		thisPath = logicalcluster.From(logicalCluster).Path().String()
	}

	if thisPath != "" && value != thisPath {
		if annotations == nil {
			annotations = map[string]string{}
		}
		annotations[core.LogicalClusterPathAnnotationKey] = thisPath
		u.SetAnnotations(annotations)
	}

	return nil
}

func (p *pathAnnotationPlugin) Validate(ctx context.Context, a admission.Attributes, _ admission.ObjectInterfaces) error {
	clusterName, err := genericapirequest.ClusterNameFrom(ctx)
	if err != nil {
		return apierrors.NewInternalError(err)
	}

	if a.GetOperation() != admission.Create && a.GetOperation() != admission.Update {
		return nil
	}

	if a.GetResource().GroupResource() == corev1alpha1.Resource("logicalclusters") {
		return nil
	}

	u, ok := a.GetObject().(metav1.Object)
	if !ok {
		return fmt.Errorf("unexpected type %T", a.GetObject())
	}

	value, found := u.GetAnnotations()[core.LogicalClusterPathAnnotationKey]
	if pathAnnotationResources.Has(a.GetResource().GroupResource().String()) || found {
		logicalCluster, err := p.getLogicalCluster(clusterName, corev1alpha1.LogicalClusterName)
		if err != nil {
			return admission.NewForbidden(a, fmt.Errorf("cannot get this workspace: %w", err))
		}
		thisPath := logicalCluster.Annotations[core.LogicalClusterPathAnnotationKey]
		if thisPath == "" {
			thisPath = logicalcluster.From(logicalCluster).Path().String()
		}

		if value != thisPath {
			return admission.NewForbidden(a, fmt.Errorf("annotation %q must match canonical path %q", core.LogicalClusterPathAnnotationKey, thisPath))
		}
	}

	return nil
}

func (p *pathAnnotationPlugin) ValidateInitialization() error {
	if p.logicalClusterLister == nil {
		return fmt.Errorf(PluginName + " plugin needs an LogicalCluster lister")
	}
	return nil
}

func (p *pathAnnotationPlugin) SetKcpInformers(local, global kcpinformers.SharedInformerFactory) {
	logicalClusterReady := local.Core().V1alpha1().LogicalClusters().Informer().HasSynced
	p.SetReadyFunc(func() bool {
		return logicalClusterReady()
	})
	p.logicalClusterLister = local.Core().V1alpha1().LogicalClusters().Lister()
	p.getLogicalCluster = func(clusterName logicalcluster.Name, name string) (*corev1alpha1.LogicalCluster, error) {
		return p.logicalClusterLister.Cluster(clusterName).Get(name)
	}
}
