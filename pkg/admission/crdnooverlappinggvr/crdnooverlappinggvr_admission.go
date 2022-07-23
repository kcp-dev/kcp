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

	"github.com/kcp-dev/logicalcluster/v2"

	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/tools/cache"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	kcpinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	"github.com/kcp-dev/kcp/pkg/reconciler/apis/apibinding"
)

const (
	PluginName  = "apis.kcp.dev/CRDNoOverlappingGVR"
	byWorkspace = PluginName + "-byWorkspace"
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

	apiBindingIndexer          cache.Indexer
	apiBindingIndexerInitError error
}

// Ensure that the required admission interfaces are implemented.
var _ = admission.ValidationInterface(&crdNoOverlappingGVRAdmission{})
var _ = admission.InitializationValidator(&crdNoOverlappingGVRAdmission{})

func (p *crdNoOverlappingGVRAdmission) SetKcpInformers(informers kcpinformers.SharedInformerFactory) {
	if err := informers.Apis().V1alpha1().APIBindings().Informer().AddIndexers(cache.Indexers{byWorkspace: indexByWorkspace}); err != nil {
		p.apiBindingIndexerInitError = err
		return
	}

	// just in case the plugin gets init multiple times in case of an error
	p.apiBindingIndexerInitError = nil
	p.SetReadyFunc(informers.Apis().V1alpha1().APIBindings().Informer().HasSynced)
	p.apiBindingIndexer = informers.Apis().V1alpha1().APIBindings().Informer().GetIndexer()
}

func (p *crdNoOverlappingGVRAdmission) ValidateInitialization() error {
	if p.apiBindingIndexerInitError != nil {
		return fmt.Errorf(PluginName+" plugin failed to initialize %q APIBindings indexer, err = %v", byWorkspace, p.apiBindingIndexerInitError)
	}
	if p.apiBindingIndexer == nil {
		return fmt.Errorf(PluginName + " plugin needs an APIBindings indexer")
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
	clusterName, err := request.ClusterNameFrom(ctx)
	if err != nil {
		return fmt.Errorf("failed to retrieve cluster from context: %w", err)
	}
	// ignore CRDs targeting system and non-root workspaces
	if clusterName == apibinding.ShadowWorkspaceName || clusterName == logicalcluster.New("system:admin") {
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
	items, err := p.apiBindingIndexer.ByIndex(byWorkspace, clusterName.String())
	if err != nil {
		return nil, err
	}
	ret := make([]*apisv1alpha1.APIBinding, 0, len(items))
	for _, item := range items {
		ret = append(ret, item.(*apisv1alpha1.APIBinding))
	}
	return ret, nil
}

func indexByWorkspace(obj interface{}) ([]string, error) {
	metaObj, ok := obj.(metav1.Object)
	if !ok {
		return []string{}, fmt.Errorf("obj is supposed to be a metav1.Object, but is %T", obj)
	}

	lcluster := logicalcluster.From(metaObj)
	return []string{lcluster.String()}, nil
}
