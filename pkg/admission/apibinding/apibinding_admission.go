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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1/helper"
	"github.com/kcp-dev/kcp/pkg/authorization/delegated"
)

const (
	PluginName = "apis.kcp.dev/APIBinding"
)

func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName,
		func(_ io.Reader) (admission.Interface, error) {
			return &apiBindingAdmission{
				Handler:          admission.NewHandler(admission.Create, admission.Update),
				createAuthorizer: delegated.NewDelegatedAuthorizer,
			}, nil
		})
}

type apiBindingAdmission struct {
	*admission.Handler
	kubeClusterClient *kubernetes.Cluster

	createAuthorizer delegated.DelegatedAuthorizerFactory
}

// Ensure that the required admission interfaces are implemented.
var _ = admission.ValidationInterface(&apiBindingAdmission{})
var _ = admission.MutationInterface(&apiBindingAdmission{})
var _ = admission.InitializationValidator(&apiBindingAdmission{})

// Validate validates the creation and updating of APIBinding resources. It also performs a SubjectAccessReview
// making sure the user is allowed to use the 'bind' verb with the referenced APIExport.
func (o *apiBindingAdmission) Validate(ctx context.Context, a admission.Attributes, _ admission.ObjectInterfaces) error {
	if a.GetResource().GroupResource() != apisv1alpha1.Resource("apibindings") {
		return nil
	}

	u, ok := a.GetObject().(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("unexpected type %T", a.GetObject())
	}

	apiBinding := &apisv1alpha1.APIBinding{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, apiBinding); err != nil {
		return fmt.Errorf("failed to convert unstructured to APIBinding: %w", err)
	}

	// Return early if there's nothing to validate (but this should never happen because it's required via OpenAPI).
	if apiBinding.Spec.Reference.Workspace == nil {
		return nil
	}

	// Object validation
	var errs field.ErrorList

	switch a.GetOperation() {
	case admission.Create:
		errs = ValidateAPIBinding(apiBinding)
	case admission.Update:
		u, ok = a.GetOldObject().(*unstructured.Unstructured)
		if !ok {
			return fmt.Errorf("unexpected type %T", a.GetOldObject())
		}
		old := &apisv1alpha1.APIBinding{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, old); err != nil {
			return fmt.Errorf("failed to convert unstructured to APIBinding: %w", err)
		}

		errs = ValidateAPIBindingUpdate(old, apiBinding)
	}
	if len(errs) > 0 {
		return admission.NewForbidden(a, fmt.Errorf("%v", errs))
	}

	// Determine the cluster name for the referenced export
	cluster, err := genericapirequest.ValidClusterFrom(ctx)
	if err != nil {
		return admission.NewForbidden(a, fmt.Errorf("error determining workspace: %w", err))
	}
	org, _, err := helper.ParseLogicalClusterName(cluster.Name)
	if err != nil {
		return admission.NewForbidden(a, fmt.Errorf("%q is not a valid workspace name: %w", cluster.Name, err))
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

func (o *apiBindingAdmission) checkAPIExportAccess(ctx context.Context, user user.Info, apiExportClusterName, apiExportName string) error {
	authz, err := o.createAuthorizer(apiExportClusterName, o.kubeClusterClient)
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
		Name:            apiExportName,
		ResourceRequest: true,
	}

	if decision, _, err := authz.Authorize(ctx, bindAttr); err != nil {
		return fmt.Errorf("unable to determine access to apiexports: %w", err)
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
		return fmt.Errorf("unexpected type %T", a.GetObject())
	}

	apiBinding := &apisv1alpha1.APIBinding{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, apiBinding); err != nil {
		return fmt.Errorf("failed to convert unstructured to APIBinding: %w", err)
	}

	u, ok = a.GetOldObject().(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("unexpected type %T", a.GetOldObject())
	}
	old := &apisv1alpha1.APIBinding{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, old); err != nil {
		return fmt.Errorf("failed to convert unstructured to APIBinding: %w", err)
	}

	// we only admit at state transition to initializing
	transitioningToInitializing :=
		old.Status.Phase != apisv1alpha1.APIBindingPhaseBinding &&
			apiBinding.Status.Phase == apisv1alpha1.APIBindingPhaseBinding
	if !transitioningToInitializing {
		return nil
	}

	apiBinding.Status.Initializers = []string{apisv1alpha1.DefaultAPIBindingInitializer}

	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(apiBinding)
	if err != nil {
		return err
	}
	u.Object = raw

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
