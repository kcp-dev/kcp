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
	"errors"
	"fmt"
	"io"
	"strings"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/apiserver/pkg/admission"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/tools/cache"

	"github.com/kcp-dev/kcp/pkg/permissionclaim"
	"github.com/kcp-dev/kcp/pkg/reconciler/dynamicrestmapper"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	kcpinformers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions"
)

const (
	PluginName = "apis.kcp.io/PermissionClaims"
)

func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName, func(configFile io.Reader) (admission.Interface, error) {
		return NewMutatingPermissionClaims(), nil
	})
}

type mutatingPermissionClaims struct {
	*admission.Handler

	apiBindingsHasSynced cache.InformerSynced

	// these are kept temporarily until all of them are set, then the claimLabeler is created
	local, global    kcpinformers.SharedInformerFactory
	dynRESTMapper    *dynamicrestmapper.DynamicRESTMapper
	dynClusterClient kcpdynamic.ClusterInterface

	permissionClaimLabeler *permissionclaim.Labeler
}

var _ admission.MutationInterface = &mutatingPermissionClaims{}
var _ admission.ValidationInterface = &mutatingPermissionClaims{}
var _ admission.InitializationValidator = &mutatingPermissionClaims{}

// NewMutatingPermissionClaims creates a mutating admission plugin that is responsible for labeling objects
// according to permission claims. For every creation and update request, we will determine the bindings
// in the workspace and if the object is claimed by an accepted permission claim we will add the label,
// and remove those that are not backed by a permission claim anymore.
func NewMutatingPermissionClaims() admission.MutationInterface {
	p := &mutatingPermissionClaims{
		Handler: admission.NewHandler(admission.Create, admission.Update),
	}

	p.SetReadyFunc(
		func() bool {
			return p.apiBindingsHasSynced()
		},
	)

	return p
}

func (m *mutatingPermissionClaims) Admit(ctx context.Context, a admission.Attributes, o admission.ObjectInterfaces) error {
	if a.GetSubresource() != "" {
		return nil
	}

	u, ok := a.GetObject().(metav1.Object)
	if !ok {
		return fmt.Errorf("got type %T, expected metav1.Object", a.GetObject())
	}

	uObject, err := toUnstructured(u)
	if err != nil {
		return err
	}

	clusterName, err := genericapirequest.ClusterNameFrom(ctx)
	if err != nil {
		return err
	}

	labeler := m.getLabeler()
	if labeler == nil {
		return errors.New("no DDSIF provided yet")
	}

	expectedLabels, err := labeler.LabelsFor(ctx, clusterName, a.GetResource().GroupResource(), uObject)
	if err != nil {
		return err
	}

	labels := u.GetLabels()
	for k := range labels {
		if !strings.HasPrefix(k, apisv1alpha1.APIExportPermissionClaimLabelPrefix) {
			continue
		}
		if _, expected := expectedLabels[k]; !expected {
			delete(labels, k)
		}
	}
	for k, v := range expectedLabels {
		if labels == nil {
			labels = make(map[string]string)
		}
		labels[k] = v
	}
	u.SetLabels(labels)

	return nil
}

func (m *mutatingPermissionClaims) Validate(ctx context.Context, a admission.Attributes, o admission.ObjectInterfaces) error {
	if a.GetSubresource() != "" {
		return nil
	}

	u, ok := a.GetObject().(metav1.Object)
	if !ok {
		return fmt.Errorf("expected type %T, expected metav1.Object", a.GetObject())
	}

	uObject, err := toUnstructured(u)
	if err != nil {
		return err
	}

	clusterName, err := genericapirequest.ClusterNameFrom(ctx)
	if err != nil {
		return err
	}

	labeler := m.getLabeler()
	if labeler == nil {
		return errors.New("no DDSIF provided yet")
	}

	expectedLabels, err := labeler.LabelsFor(ctx, clusterName, a.GetResource().GroupResource(), uObject)
	if err != nil {
		return err
	}

	var errs field.ErrorList

	// check existing values
	labels := u.GetLabels()
	for k, v := range expectedLabels {
		if labels[k] != v {
			errs = append(errs, field.Invalid(field.NewPath("metadata", "labels").Key(k), labels[k], fmt.Sprintf("must be %q", v)))
		}
	}

	// check for labels that shouldn't exist
	for k := range labels {
		if !strings.HasPrefix(k, apisv1alpha1.APIExportPermissionClaimLabelPrefix) {
			continue
		}
		if _, ok := expectedLabels[k]; !ok {
			errs = append(errs, field.Forbidden(field.NewPath("metadata", "labels").Key(k), "must not be set"))
		}
	}

	if len(errs) > 0 {
		return admission.NewForbidden(a, errs.ToAggregate())
	}

	return nil
}

// SetKcpInformers implements the WantsExternalKcpInformerFactory interface.
func (m *mutatingPermissionClaims) SetKcpInformers(local, global kcpinformers.SharedInformerFactory) {
	m.apiBindingsHasSynced = local.Apis().V1alpha2().APIBindings().Informer().HasSynced
	m.local = local
	m.global = global
}

// SetDynamicClusterClient implements the WantsDynamicClusterClient interface.
func (m *mutatingPermissionClaims) SetDynamicClusterClient(clusterInterface kcpdynamic.ClusterInterface) {
	m.dynClusterClient = clusterInterface
}

// SetDynamicRESTMapper implements the WantsDynamicRESTMapper interface.
func (m *mutatingPermissionClaims) SetDynamicRESTMapper(mapper *dynamicrestmapper.DynamicRESTMapper) {
	m.dynRESTMapper = mapper
}

func (m *mutatingPermissionClaims) ValidateInitialization() error {
	if m.apiBindingsHasSynced == nil {
		return errors.New("missing apiBindingsHasSynced")
	}
	if m.local == nil {
		return errors.New("missing local informer factory")
	}
	if m.global == nil {
		return errors.New("missing global informer factory")
	}
	if m.dynClusterClient == nil {
		return errors.New("missing dynamic cluster client")
	}
	if m.dynRESTMapper == nil {
		return errors.New("missing dynamic REST mapper")
	}

	return nil
}

func (m *mutatingPermissionClaims) getLabeler() *permissionclaim.Labeler {
	if m.permissionClaimLabeler != nil {
		return m.permissionClaimLabeler
	}

	// wait until all initializers are done
	if m.ValidateInitialization() != nil {
		return nil
	}

	m.permissionClaimLabeler = permissionclaim.NewLabeler(
		m.local.Apis().V1alpha2().APIBindings(),
		m.local.Apis().V1alpha2().APIExports(),
		m.global.Apis().V1alpha2().APIExports(),
		m.dynRESTMapper,
		m.dynClusterClient,
	)

	return m.permissionClaimLabeler
}

func toUnstructured(obj metav1.Object) (*unstructured.Unstructured, error) {
	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil, err
	}

	return &unstructured.Unstructured{Object: raw}, nil
}
