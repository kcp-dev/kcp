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

package permissionclaims

import (
	"context"
	"fmt"
	"io"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/admission"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/tools/cache"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1/permissionclaims"
	kcpinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	"github.com/kcp-dev/kcp/pkg/indexers"
)

const (
	PluginName = "apis.kcp.dev/PermissionClaims"
)

func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName, func(configFile io.Reader) (admission.Interface, error) {
		return NewMutatingPermissionClaims(), nil
	})
}

type mutatingPermissionClaims struct {
	*admission.Handler

	apiBindingsIndexer   cache.Indexer
	apiBindingsHasSynced func() bool
}

var _ admission.MutationInterface = &mutatingPermissionClaims{}
var _ admission.ValidationInterface = &mutatingPermissionClaims{}

// NewMutatingPermissionClaims creates a mutating admission plugin that is responsible for labeling objects
// according to permission claims. or every creation and update request, we will determine the bindings
// in the workspace and if the object is claimed by an accepted permission claim we will add the label,
// and remove those that are not backed by a permission claim anymore.
func NewMutatingPermissionClaims() admission.MutationInterface {
	p := &mutatingPermissionClaims{}
	p.Handler = admission.NewHandler(admission.Create)
	p.SetReadyFunc(p.apiBindingsHasSynced)
	return p
}

func (m *mutatingPermissionClaims) Admit(ctx context.Context, a admission.Attributes, o admission.ObjectInterfaces) error {
	u, ok := a.GetObject().(metav1.Object)
	if !ok {
		return fmt.Errorf("expected type %T, expected metav1.Object", a.GetObject())
	}

	lcluster, err := genericapirequest.ClusterNameFrom(ctx)
	if err != nil {
		return err
	}
	objs, err := m.apiBindingsIndexer.ByIndex(indexers.ByLogicalCluster, lcluster.String())
	if err != nil {
		return err
	}
	bindings := make([]*apisv1alpha1.APIBinding, 0, len(objs))
	for _, binding := range objs {
		bindings = append(bindings, binding.(*apisv1alpha1.APIBinding))
	}

	expectedLabels, err := ClaimLabels(a.GetResource().Group, a.GetResource().Resource, bindings)
	if err != nil {
		return err
	}

	// set new labels
	labels := u.GetLabels()
	expectedKeys := sets.NewString()
	for k, v := range expectedLabels {
		if labels == nil {
			labels = make(map[string]string)
		}
		labels[k] = v
		expectedKeys.Insert(k)
	}

	// clean up labels not backed by a claim
	for k := range labels {
		if strings.HasPrefix(k, apisv1alpha1.APIExportPermissionClaimLabelPrefix) && !expectedKeys.Has(k) {
			delete(labels, k)
		}
	}

	u.SetLabels(labels)

	return nil

}

func (m *mutatingPermissionClaims) Validate(ctx context.Context, a admission.Attributes, o admission.ObjectInterfaces) error {
	u, ok := a.GetObject().(metav1.Object)
	if !ok {
		return fmt.Errorf("expected type %T, expected metav1.Object", a.GetObject())
	}

	lcluster, err := genericapirequest.ClusterNameFrom(ctx)
	if err != nil {
		return err
	}
	objs, err := m.apiBindingsIndexer.ByIndex(indexers.ByLogicalCluster, lcluster.String())
	if err != nil {
		return err
	}
	bindings := make([]*apisv1alpha1.APIBinding, 0, len(objs))
	for _, binding := range objs {
		bindings = append(bindings, binding.(*apisv1alpha1.APIBinding))
	}

	// find those that are requested
	expectedLabels, err := ClaimLabels(a.GetResource().Group, a.GetResource().Resource, bindings)
	if err != nil {
		return err
	}

	// check existing values
	labels := u.GetLabels()
	expectedKeys := sets.NewString()
	for k, v := range expectedLabels {
		if existing, found := labels[k]; !found || existing != v {
			return admission.NewForbidden(a, fmt.Errorf("metadata.label.%s must be %q", k, v))
		}
		expectedKeys.Insert(k)
	}

	// check for labels that shouldn't exist
	for k := range labels {
		if strings.HasPrefix(k, apisv1alpha1.APIExportPermissionClaimLabelPrefix) && !expectedKeys.Has(k) {
			return admission.NewForbidden(a, fmt.Errorf("metadata.label.%s must not be set", k))
		}
	}

	return nil
}

func ClaimLabels(group, resource string, bindings []*apisv1alpha1.APIBinding) (map[string]string, error) {
	grsToBoundResource := map[apisv1alpha1.GroupResource]apisv1alpha1.BoundAPIResource{}
	for _, binding := range bindings {
		for _, resource := range binding.Status.BoundResources {
			grsToBoundResource[apisv1alpha1.GroupResource{Group: resource.Group, Resource: resource.Resource}] = resource
		}
	}

	// add labels for new claims
	labels := map[string]string{}
	for _, binding := range bindings {
		for _, pc := range binding.Status.ObservedAcceptedPermissionClaims {
			if pc.Group != group || pc.Resource != resource {
				continue
			}
			boundResource, bound := grsToBoundResource[pc.GroupResource]
			if bound && pc.IdentityHash != boundResource.Schema.IdentityHash {
				continue
			}
			if !bound && pc.IdentityHash != "" {
				continue
			}

			k, v, err := permissionclaims.ToLabelKeyAndValue(pc)
			if err != nil {
				return nil, err
			}

			labels[k] = v
		}
	}

	return labels, nil
}

// SetKcpInformers implements the WantsExternalKcpInformerFactory interface.
func (m *mutatingPermissionClaims) SetKcpInformers(f kcpinformers.SharedInformerFactory) {
	m.apiBindingsIndexer = f.Apis().V1alpha1().APIBindings().Informer().GetIndexer()
	m.apiBindingsHasSynced = f.Apis().V1alpha1().APIBindings().Informer().HasSynced
}
