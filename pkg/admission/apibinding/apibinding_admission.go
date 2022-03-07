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

package apibinding

import (
	"context"
	"errors"
	"fmt"
	"io"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	kcpadmissionhelpers "github.com/kcp-dev/kcp/pkg/admission/helpers"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1/helper"
)

const (
	PluginName = "apis.kcp.dev/APIBinding"
)

func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName,
		func(_ io.Reader) (admission.Interface, error) {
			return &apiBindingAdmission{
				Handler:          admission.NewHandler(admission.Create, admission.Update),
				createAuthorizer: kcpadmissionhelpers.NewAdmissionAuthorizer,
			}, nil
		})
}

type apiBindingAdmission struct {
	*admission.Handler
	kubeClusterClient *kubernetes.Cluster

	createAuthorizer kcpadmissionhelpers.AdmissionAuthorizerFactory
}

// Ensure that the required admission interfaces are implemented.
var _ = admission.ValidationInterface(&apiBindingAdmission{})
var _ = admission.MutationInterface(&apiBindingAdmission{})
var _ = admission.InitializationValidator(&apiBindingAdmission{})

var phaseOrdinal = map[apisv1alpha1.APIBindingPhaseType]int{
	apisv1alpha1.APIBindingPhaseType(""): 1,
	apisv1alpha1.APIBindingPhaseBinding:  2,
	apisv1alpha1.APIBindingPhaseBound:    3,
	// not including Rebinding as you can move back and forth between Bound and Rebinding
}

// Validate validates the creation and updating of APIBinding resources. It also performs a SubjectAccessReview
// making sure the user is allowed to use the 'bind' verb with the referenced APIExport.
func (o *apiBindingAdmission) Validate(ctx context.Context, a admission.Attributes, _ admission.ObjectInterfaces) error {
	if a.GetResource().GroupResource() != apisv1alpha1.Resource("apibindings") {
		return nil
	}

	obj, err := kcpadmissionhelpers.NativeObject(a.GetObject())
	if err != nil {
		// nolint: nilerr
		return nil // only work on unstructured APIBindings
	}
	apiBinding, ok := obj.(*apisv1alpha1.APIBinding)
	if !ok {
		// nolint: nilerr
		return nil // only work on unstructured APIBindings
	}

	// Return early if there's nothing to validate
	if apiBinding.Spec.Reference.Workspace == nil {
		return nil
	}

	// Object validation
	var errs field.ErrorList

	switch a.GetOperation() {
	case admission.Create:
		errs = ValidateAPIBinding(apiBinding)
	case admission.Update:
		obj, err = kcpadmissionhelpers.NativeObject(a.GetOldObject())
		if err != nil {
			return fmt.Errorf("unexpected unknown old object, got %v, expected APIBinding", a.GetOldObject().GetObjectKind().GroupVersionKind().Kind)
		}
		old, ok := obj.(*apisv1alpha1.APIBinding)
		if !ok {
			return fmt.Errorf("unexpected unknown old object, got %v, expected APIBinding", obj.GetObjectKind().GroupVersionKind().Kind)
		}
		errs = ValidateAPIBindingUpdate(old, apiBinding)
	}
	if len(errs) > 0 {
		return admission.NewForbidden(a, fmt.Errorf("%v", errs))
	}

	// Determine the cluster name for the referenced export
	cluster, err := genericapirequest.ValidClusterFrom(ctx)
	if err != nil {
		// TODO(ncdc): is this user-facing error message ok?
		return admission.NewForbidden(a, fmt.Errorf("error determining logical cluster name: %w", err))
	}
	org, _, err := helper.ParseLogicalClusterName(cluster.Name)
	if err != nil {
		// TODO(ncdc): is this user-facing error message ok?
		return admission.NewForbidden(a, fmt.Errorf("error parsing logical cluster name %q: %w", apiBinding.ClusterName, err))
	}
	apiExportClusterName := helper.EncodeOrganizationAndClusterWorkspace(org, apiBinding.Spec.Reference.Workspace.WorkspaceName)

	// Access check
	if err := o.checkAPIExportAccess(ctx, a.GetUserInfo(), apiExportClusterName, apiBinding.Spec.Reference.Workspace.ExportName); err != nil {
		action := "create"
		if a.GetOperation() == admission.Update {
			action = "update"
		}
		return admission.NewForbidden(a, fmt.Errorf("unable to %s APIImport: %w", action, err))
	}

	return nil
}

func (o *apiBindingAdmission) checkAPIExportAccess(ctx context.Context, user user.Info, clusterName, exportName string) error {
	authz, err := o.createAuthorizer(clusterName, o.kubeClusterClient)
	if err != nil {
		// Logging a more specific error for the operator
		klog.Errorf("error creating authorizer from delegating authorizer config: %v", err)
		// Returning a less specific error to the end user
		return errors.New("unable to authorize request")
	}

	bindAttr := authorizer.AttributesRecord{
		User:            user,
		Verb:            "bind",
		APIGroup:        apisv1alpha1.SchemeGroupVersion.Group,
		APIVersion:      apisv1alpha1.SchemeGroupVersion.Version,
		Resource:        "apiexports",
		Name:            exportName,
		ResourceRequest: true,
	}

	if decision, _, err := authz.Authorize(ctx, bindAttr); err != nil {
		return fmt.Errorf("unable to determine access to APIExport: %w", err)
	} else if decision != authorizer.DecisionAllow {
		return errors.New("missing verb='bind' permission on apiexports")
	}

	return nil
}

// Admit applies the default APIBinding initializer to an APIBinding when it is transitioning to the
// Initializing phase.
func (o *apiBindingAdmission) Admit(_ context.Context, a admission.Attributes, _ admission.ObjectInterfaces) error {
	if a.GetResource().GroupResource() != apisv1alpha1.Resource("apibindings") {
		return nil
	}

	if a.GetOperation() != admission.Update {
		return nil
	}

	u, ok := a.GetObject().(*unstructured.Unstructured)
	if !ok {
		return nil
	}
	obj, err := kcpadmissionhelpers.DecodeUnstructured(u)
	if err != nil {
		// nolint: nilerr
		return nil // only work on unstructured APIBindings
	}
	apiBinding, ok := obj.(*apisv1alpha1.APIBinding)
	if !ok {
		// nolint: nilerr
		return nil // only work on unstructured APIBindings
	}

	obj, err = kcpadmissionhelpers.NativeObject(a.GetOldObject())
	if err != nil {
		return fmt.Errorf("unexpected unknown old object, got %v, expected APIBinding", a.GetOldObject().GetObjectKind().GroupVersionKind().Kind)
	}
	old, ok := obj.(*apisv1alpha1.APIBinding)
	if !ok {
		return fmt.Errorf("unexpected unknown old object, got %v, expected APIBinding", obj.GetObjectKind().GroupVersionKind().Kind)
	}

	// we only admit at state transition to initializing
	transitioningToInitializing :=
		old.Status.Phase != apisv1alpha1.APIBindingPhaseBinding &&
			apiBinding.Status.Phase == apisv1alpha1.APIBindingPhaseBinding
	if !transitioningToInitializing {
		return nil
	}

	apiBinding.Status.Initializers = []string{apisv1alpha1.DefaultAPIBindingInitializer}

	if err := kcpadmissionhelpers.EncodeIntoUnstructured(u, apiBinding); err != nil {
		return err
	}

	return nil
}

// ValidateInitialization ensures the required injected fields are set.
func (o *apiBindingAdmission) ValidateInitialization() error {
	if o.kubeClusterClient == nil {
		return fmt.Errorf(PluginName + " plugin needs a Kubernetes ClusterInterface")
	}

	return nil
}

// SetKubeClusterClient is an admission plugin initializer function that injects a Kubernetes cluster client into
// this admission plugin.
func (o *apiBindingAdmission) SetKubeClusterClient(clusterClient *kubernetes.Cluster) {
	o.kubeClusterClient = clusterClient
}
