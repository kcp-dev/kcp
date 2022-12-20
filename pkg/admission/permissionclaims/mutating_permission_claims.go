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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/apiserver/pkg/admission"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/tools/cache"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	kcpinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	"github.com/kcp-dev/kcp/pkg/permissionclaim"
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

	apiBindingsHasSynced cache.InformerSynced

	permissionClaimLabeler *permissionclaim.Labeler
}

var _ admission.MutationInterface = &mutatingPermissionClaims{}
var _ admission.ValidationInterface = &mutatingPermissionClaims{}
var _ admission.InitializationValidator = &mutatingPermissionClaims{}

// NewMutatingPermissionClaims creates a mutating admission plugin that is responsible for labeling objects
// according to permission claims. or every creation and update request, we will determine the bindings
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

	clusterName, err := genericapirequest.ClusterNameFrom(ctx)
	if err != nil {
		return err
	}

	expectedLabels, err := m.permissionClaimLabeler.LabelsFor(ctx, clusterName, a.GetResource().GroupResource(), a.GetName())
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

	clusterName, err := genericapirequest.ClusterNameFrom(ctx)
	if err != nil {
		return err
	}

	expectedLabels, err := m.permissionClaimLabeler.LabelsFor(ctx, clusterName, a.GetResource().GroupResource(), a.GetName())
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
func (m *mutatingPermissionClaims) SetKcpInformers(f kcpinformers.SharedInformerFactory) {
	m.apiBindingsHasSynced = f.Apis().V1alpha1().APIBindings().Informer().HasSynced

	m.permissionClaimLabeler = permissionclaim.NewLabeler(
		f.Apis().V1alpha1().APIBindings(),
		f.Apis().V1alpha1().APIExports(),
	)
}

func (m *mutatingPermissionClaims) ValidateInitialization() error {
	if m.apiBindingsHasSynced == nil {
		return errors.New("missing apiBindingsHasSynced")
	}
	if m.permissionClaimLabeler == nil {
		return errors.New("missing permissionClaimLabeler")
	}
	return nil
}
