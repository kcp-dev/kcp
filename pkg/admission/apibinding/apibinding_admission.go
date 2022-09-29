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

	"github.com/kcp-dev/logicalcluster/v2"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	kubernetesclient "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/klog/v2"

	kcpinitializers "github.com/kcp-dev/kcp/pkg/admission/initializers"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1/permissionclaims"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/authorization/delegated"
	kcpinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
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
	deepSARClient kubernetesclient.ClusterInterface

	createAuthorizer delegated.DelegatedAuthorizerFactory
	getWorkspace     func(name string) (*v1alpha1.ClusterWorkspace, error)
}

// Ensure that the required admission interfaces are implemented.
var (
	_ = admission.ValidationInterface(&apiBindingAdmission{})
	_ = admission.MutationInterface(&apiBindingAdmission{})
	_ = admission.InitializationValidator(&apiBindingAdmission{})
	_ = kcpinitializers.WantsDeepSARClient(&apiBindingAdmission{})
	_ = kcpinitializers.WantsKcpInformers(&apiBindingAdmission{})
)

func (o *apiBindingAdmission) Admit(ctx context.Context, a admission.Attributes, _ admission.ObjectInterfaces) error {
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

	if apiBinding.Spec.Reference.Workspace == nil {
		return nil
	}

	// do defaulting
	cluster, err := genericapirequest.ValidClusterFrom(ctx)
	if err != nil {
		return admission.NewForbidden(a, fmt.Errorf("error determining workspace: %w", err))
	}
	if apiBinding.Spec.Reference.Workspace.Path == "" {
		apiBinding.Spec.Reference.Workspace.Path = cluster.Name.String()
	}

	// set labels
	if apiBinding.Spec.Reference.Workspace == nil {
		delete(apiBinding.Labels, apisv1alpha1.InternalAPIBindingExportLabelKey)
	} else {
		if apiBinding.Labels == nil {
			apiBinding.Labels = make(map[string]string)
		}
		apiBinding.Labels[apisv1alpha1.InternalAPIBindingExportLabelKey] = permissionclaims.ToAPIBindingExportLabelValue(
			logicalcluster.New(apiBinding.Spec.Reference.Workspace.Path),
			apiBinding.Spec.Reference.Workspace.ExportName,
		)
	}

	// write back
	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(apiBinding)
	if err != nil {
		return err
	}
	u.Object = raw

	return nil
}

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
		return admission.NewForbidden(a, fmt.Errorf(".spec.reference.workspace is required"))
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

	// Verify the workspace reference.
	if apiBinding.Spec.Reference.Workspace.Path == "" {
		return admission.NewForbidden(a, fmt.Errorf("workspace reference is missing")) // this should not happen due to validation
	}

	clusterName := logicalcluster.New(apiBinding.Spec.Reference.Workspace.Path)
	org, hasParent := clusterName.Parent()
	if hasParent {
		// Checking workspace references only make sense for non-root workspaces.
		// The root workspace (which has no parent) always exists and wouldn't be found by the lister.
		_, err := o.getWorkspace(clusters.ToClusterAwareKey(org, clusterName.Base()))
		if apierrors.IsNotFound(err) {
			return admission.NewForbidden(a, err)
		}
	}

	// Verify the labels
	value, found := apiBinding.Labels[apisv1alpha1.InternalAPIBindingExportLabelKey]
	if apiBinding.Spec.Reference.Workspace == nil && found {
		return admission.NewForbidden(a, field.Invalid(field.NewPath("metadata").Child("labels").Key(apisv1alpha1.InternalAPIBindingExportLabelKey), value, "must not be set"))
	} else if expected := permissionclaims.ToAPIBindingExportLabelValue(
		logicalcluster.New(apiBinding.Spec.Reference.Workspace.Path),
		apiBinding.Spec.Reference.Workspace.ExportName,
	); value != expected {
		return admission.NewForbidden(a, field.Invalid(field.NewPath("metadata").Child("labels").Key(apisv1alpha1.InternalAPIBindingExportLabelKey), value, fmt.Sprintf("must be set to %q", expected)))
	}

	// Access check
	if err := o.checkAPIExportAccess(ctx, a.GetUserInfo(), logicalcluster.New(apiBinding.Spec.Reference.Workspace.Path), apiBinding.Spec.Reference.Workspace.ExportName); err != nil {
		action := "create"
		if a.GetOperation() == admission.Update {
			action = "update"
		}
		return admission.NewForbidden(a, fmt.Errorf("unable to %s APIImport: %w", action, err))
	}

	return nil
}

func (o *apiBindingAdmission) checkAPIExportAccess(ctx context.Context, user user.Info, apiExportClusterName logicalcluster.Name, apiExportName string) error {
	logger := klog.FromContext(ctx)
	authz, err := o.createAuthorizer(apiExportClusterName, o.deepSARClient)
	if err != nil {
		// Logging a more specific error for the operator
		logger.Error(err, "error creating authorizer from delegating authorizer config")
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

// ValidateInitialization ensures the required injected fields are set.
func (o *apiBindingAdmission) ValidateInitialization() error {
	if o.deepSARClient == nil {
		return fmt.Errorf(PluginName + " plugin needs a Kubernetes ClusterInterface")
	}

	if o.getWorkspace == nil {
		return fmt.Errorf(PluginName + " missing workspaceLister")
	}

	return nil
}

// SetDeepSARClient is an admission plugin initializer function that injects a client capable of deep SAR requests into
// this admission plugin.
func (o *apiBindingAdmission) SetDeepSARClient(client kubernetesclient.ClusterInterface) {
	o.deepSARClient = client
}

func (o *apiBindingAdmission) SetKcpInformers(informers kcpinformers.SharedInformerFactory) {
	o.SetReadyFunc(informers.Tenancy().V1alpha1().ClusterWorkspaces().Informer().HasSynced)
	o.getWorkspace = func(name string) (*v1alpha1.ClusterWorkspace, error) {
		return informers.Tenancy().V1alpha1().ClusterWorkspaces().Lister().Get(name)
	}
}
