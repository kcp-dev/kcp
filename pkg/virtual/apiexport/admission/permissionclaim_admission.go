/*
Copyright 2023 The KCP Authors.

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

package admission

import (
	"context"
	"errors"
	"fmt"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	kcpinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	"github.com/kcp-dev/kcp/pkg/permissionclaim"
	virtualcontext "github.com/kcp-dev/kcp/pkg/virtual/framework/context"
	"io"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/apiserver/pkg/admission"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/tools/cache"
	"strings"
)

const (
	PluginName = "apis.view.kcp.io/PermissionClaims"
)

func Register(plugins *admission.Plugins) {
	fmt.Println(">>>> Registered new plugin")
	plugins.Register(PluginName, func(configFile io.Reader) (admission.Interface, error) {
		return NewAdmitPermissionClaims(), nil
	})
}

type admitPermissionClaims struct {
	*admission.Handler

	apiBindingsHasSynced cache.InformerSynced

	permissionClaimLabeler *permissionclaim.Labeler
}

var _ admission.ValidationInterface = &admitPermissionClaims{}
var _ admission.InitializationValidator = &admitPermissionClaims{}

// NewAdmitPermissionClaims creates a mutating admission plugin that is responsible for labeling objects
// according to permission claims. or every creation and update request, we will determine the bindings
// in the workspace and if the object is claimed by an accepted permission claim we will add the label,
// and remove those that are not backed by a permission claim anymore.
func NewAdmitPermissionClaims() *admitPermissionClaims {
	p := &admitPermissionClaims{
		Handler: admission.NewHandler(admission.Create, admission.Update, admission.Delete, admission.Connect),
	}

	p.SetReadyFunc(
		func() bool {
			return p.apiBindingsHasSynced()
		},
	)

	return p
}

func (m *admitPermissionClaims) Validate(ctx context.Context, a admission.Attributes, o admission.ObjectInterfaces) error {
	// The request is not for a virtual workspace, so we can safely exit
	if _, hasVirtualWorkspaceName := virtualcontext.VirtualWorkspaceNameFrom(ctx); !hasVirtualWorkspaceName {
		return nil
	}
	fmt.Println(">>>>>>> In custom Validate")
	u, ok := a.GetObject().(metav1.Object)
	if !ok {
		return fmt.Errorf("expected type %T, expected metav1.Object", a.GetObject())
	}

	clusterName, err := genericapirequest.ClusterNameFrom(ctx)
	if err != nil {
		return err
	}
	if a.GetName() == "not-unique" {
		fmt.Println("Hi")
	}
	// TODO(nrb): use a new method wrapping Selects
	expectedLabels, err := m.permissionClaimLabeler.LabelsFor(ctx, clusterName, a.GetResource().GroupResource(), a.GetName(), a.GetNamespace())
	if err != nil {
		return err
	}

	// Reject the object because it was not selected, and there is no error.
	//if !admit {
	//	return admission.NewForbidden(a, fmt.Errorf("does not match resourceselector"))
	//}

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
func (m *admitPermissionClaims) SetKcpInformers(local, global kcpinformers.SharedInformerFactory) {
	fmt.Println(">>>> In SetKcpInformers")
	m.apiBindingsHasSynced = local.Apis().V1alpha1().APIBindings().Informer().HasSynced

	m.permissionClaimLabeler = permissionclaim.NewLabeler(
		local.Apis().V1alpha1().APIBindings(),
		local.Apis().V1alpha1().APIExports(),
		global.Apis().V1alpha1().APIExports(),
	)
}

func (m *admitPermissionClaims) ValidateInitialization() error {
	fmt.Println(">>>> In ValidateInitialization")
	if m.apiBindingsHasSynced == nil {
		return errors.New("missing apiBindingsHasSynced")
	}
	if m.permissionClaimLabeler == nil {
		return errors.New("missing permissionClaimLabeler")
	}
	return nil
}
