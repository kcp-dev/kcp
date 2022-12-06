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

package crdnooverlappinggvr

import (
	"context"
	"fmt"
	"io"

	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/endpoints/request"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	kcpinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	apisv1alpha1listers "github.com/kcp-dev/kcp/pkg/client/listers/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/reconciler/apis/apibinding"
	"github.com/kcp-dev/logicalcluster/v3"
)

const (
	PluginName = "apis.kcp.dev/CRDNoOverlappingGVR"
)

func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName,
		func(_ io.Reader) (admission.Interface, error) {
			return &crdNoOverlappingGVRAdmission{
				Handler: admission.NewHandler(admission.Create),
			}, nil
		})
}

type crdNoOverlappingGVRAdmission struct {
	*admission.Handler

	apiBindingClusterLister apisv1alpha1listers.APIBindingClusterLister
}

// Ensure that the required admission interfaces are implemented.
var _ = admission.ValidationInterface(&crdNoOverlappingGVRAdmission{})
var _ = admission.InitializationValidator(&crdNoOverlappingGVRAdmission{})

func (p *crdNoOverlappingGVRAdmission) SetKcpInformers(informers kcpinformers.SharedInformerFactory) {
	p.SetReadyFunc(informers.Apis().V1alpha1().APIBindings().Informer().HasSynced)
	p.apiBindingClusterLister = informers.Apis().V1alpha1().APIBindings().Lister()
}

func (p *crdNoOverlappingGVRAdmission) ValidateInitialization() error {
	if p.apiBindingClusterLister == nil {
		return fmt.Errorf(PluginName + " plugin needs an APIBindings lister")
	}
	return nil
}

// Validate checks if the given CRD's Group and Resource don't overlap with bound CRDs
func (p *crdNoOverlappingGVRAdmission) Validate(ctx context.Context, a admission.Attributes, _ admission.ObjectInterfaces) error {
	if a.GetResource().GroupResource() != apiextensions.Resource("customresourcedefinitions") {
		return nil
	}
	if a.GetKind().GroupKind() != apiextensions.Kind("CustomResourceDefinition") {
		return nil
	}
	cluster, err := request.ClusterNameFrom(ctx)
	if err != nil {
		return fmt.Errorf("failed to retrieve cluster from context: %w", err)
	}
	clusterName := logicalcluster.Name(cluster.String()) // TODO(sttts): remove this cast once ClusterNameFrom returns a tenancy.Name
	// ignore CRDs targeting system and non-root workspaces
	if clusterName == apibinding.ShadowWorkspaceName || clusterName == "system:admin" {
		return nil
	}

	crd, ok := a.GetObject().(*apiextensions.CustomResourceDefinition)
	if !ok {
		return fmt.Errorf("unexpected type %T", a.GetObject())
	}
	apiBindingsForCurrentClusterName, err := p.listAPIBindingsFor(clusterName)
	if err != nil {
		return err
	}
	for _, apiBindingForCurrentClusterName := range apiBindingsForCurrentClusterName {
		for _, boundResource := range apiBindingForCurrentClusterName.Status.BoundResources {
			if boundResource.Group == crd.Spec.Group && boundResource.Resource == crd.Spec.Names.Plural {
				return admission.NewForbidden(a, fmt.Errorf("cannot create %q CustomResourceDefinition with %q group and %q resource because it overlaps with a bound CustomResourceDefinition for %q APIBinding in %q logical cluster",
					crd.Name, crd.Spec.Group, crd.Spec.Names.Plural, apiBindingForCurrentClusterName.Name, clusterName))
			}
		}
	}
	return nil
}

func (p *crdNoOverlappingGVRAdmission) listAPIBindingsFor(clusterName logicalcluster.Name) ([]*apisv1alpha1.APIBinding, error) {
	return p.apiBindingClusterLister.Cluster(clusterName.Path()).List(labels.Everything())
}
