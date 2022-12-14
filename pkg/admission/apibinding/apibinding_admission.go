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
	"reflect"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	kcpinitializers "github.com/kcp-dev/kcp/pkg/admission/initializers"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1/permissionclaims"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/authorization/delegated"
	kcpinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	apisv1alpha1listers "github.com/kcp-dev/kcp/pkg/client/listers/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/indexers"
)

const (
	PluginName = "apis.kcp.dev/APIBinding"
)

func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName,
		func(_ io.Reader) (admission.Interface, error) {
			p := &apiBindingAdmission{
				Handler:          admission.NewHandler(admission.Create, admission.Update),
				createAuthorizer: delegated.NewDelegatedAuthorizer,
			}
			p.getAPIExport = func(path logicalcluster.Path, name string) (*apisv1alpha1.APIExport, error) {
				objs, err := p.apiExportIndexer.ByIndex(indexers.ByLogicalClusterPathAndName, path.Join(name).String())
				if err != nil {
					return nil, fmt.Errorf("failed to look up APIExport: %w", err)
				} else if len(objs) == 0 {
					return nil, fmt.Errorf("referenced APIExport %s not found", path.Join(name).String())
				}
				return objs[0].(*apisv1alpha1.APIExport), nil
			}

			return p, nil
		})
}

type apiBindingAdmission struct {
	*admission.Handler

	getAPIExport func(path logicalcluster.Path, name string) (*apisv1alpha1.APIExport, error)

	apiExportLister  apisv1alpha1listers.APIExportClusterLister
	apiExportIndexer cache.Indexer

	deepSARClient    kcpkubernetesclientset.ClusterInterface
	createAuthorizer delegated.DelegatedAuthorizerFactory
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
	clusterName, err := genericapirequest.ClusterNameFrom(ctx)
	if err != nil {
		return apierrors.NewInternalError(err)
	}

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

	if apiBinding.Spec.Reference.Export == nil {
		return nil
	}

	var oldAPIBinding *apisv1alpha1.APIBinding
	if a.GetOperation() == admission.Update {
		u, ok := a.GetOldObject().(*unstructured.Unstructured)
		if !ok {
			return fmt.Errorf("unexpected type %T", a.GetObject())
		}

		oldAPIBinding = &apisv1alpha1.APIBinding{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, oldAPIBinding); err != nil {
			return fmt.Errorf("failed to convert unstructured to APIBinding: %w", err)
		}
	}

	switch {
	case a.GetOperation() == admission.Create,
		a.GetOperation() == admission.Update && !reflect.DeepEqual(apiBinding.Spec.Reference, oldAPIBinding.Spec.Reference),
		a.GetOperation() == admission.Update && apiBinding.Labels[apisv1alpha1.InternalAPIBindingExportLabelKey] != oldAPIBinding.Labels[apisv1alpha1.InternalAPIBindingExportLabelKey]:

		// unified forbidden error that does not leak workspace existence
		action := "create"
		if a.GetOperation() == admission.Update {
			action = "update"
		}
		forbidden := admission.NewForbidden(a, fmt.Errorf("unable to %s APIBinding: no permission to bind to export %s", action,
			logicalcluster.NewPath(apiBinding.Spec.Reference.Export.Path).Join(apiBinding.Spec.Reference.Export.Name).String()))

		// get cluster name of export
		var exportClusterName logicalcluster.Name
		if apiBinding.Spec.Reference.Export.Path == "" {
			exportClusterName = clusterName
		} else if apiBinding.Spec.Reference.Export.Path == tenancyv1alpha1.RootCluster.String() {
			// special case to allow bootstrapping
			exportClusterName = tenancyv1alpha1.RootCluster
		} else {
			path := logicalcluster.NewPath(apiBinding.Spec.Reference.Export.Path)
			export, err := o.getAPIExport(path, apiBinding.Spec.Reference.Export.Name)
			if err != nil {
				return forbidden
			}
			exportClusterName = logicalcluster.From(export)
		}

		// set labels
		if apiBinding.Labels == nil {
			apiBinding.Labels = make(map[string]string)
		}
		apiBinding.Labels[apisv1alpha1.InternalAPIBindingExportLabelKey] = permissionclaims.ToAPIBindingExportLabelValue(
			exportClusterName,
			apiBinding.Spec.Reference.Export.Name,
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
	clusterName, err := genericapirequest.ClusterNameFrom(ctx)
	if err != nil {
		return apierrors.NewInternalError(err)
	}

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

	// Object validation
	var errs field.ErrorList
	var oldAPIBinding *apisv1alpha1.APIBinding
	switch a.GetOperation() {
	case admission.Create:
		errs = ValidateAPIBinding(apiBinding)
	case admission.Update:
		u, ok = a.GetOldObject().(*unstructured.Unstructured)
		if !ok {
			return fmt.Errorf("unexpected type %T", a.GetOldObject())
		}
		oldAPIBinding = &apisv1alpha1.APIBinding{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, oldAPIBinding); err != nil {
			return fmt.Errorf("failed to convert unstructured to APIBinding: %w", err)
		}

		errs = ValidateAPIBindingUpdate(oldAPIBinding, apiBinding)
	}
	if len(errs) > 0 {
		return admission.NewForbidden(a, fmt.Errorf("%v", errs))
	}

	switch {
	case a.GetOperation() == admission.Create,
		a.GetOperation() == admission.Update && !reflect.DeepEqual(apiBinding.Spec.Reference, oldAPIBinding.Spec.Reference),
		a.GetOperation() == admission.Update && apiBinding.Labels[apisv1alpha1.InternalAPIBindingExportLabelKey] != oldAPIBinding.Labels[apisv1alpha1.InternalAPIBindingExportLabelKey]:

		// unified forbidden error that does not leak workspace existence
		action := "create"
		if a.GetOperation() == admission.Update {
			action = "update"
		}
		forbidden := admission.NewForbidden(a, fmt.Errorf("unable to %s APIBinding: no permission to bind to export %s", action,
			logicalcluster.NewPath(apiBinding.Spec.Reference.Export.Path).Join(apiBinding.Spec.Reference.Export.Name).String()))

		// get cluster name of export
		var exportClusterName logicalcluster.Name
		if apiBinding.Spec.Reference.Export.Path == "" {
			exportClusterName = clusterName
		} else if apiBinding.Spec.Reference.Export.Path == tenancyv1alpha1.RootCluster.String() {
			// special case to allow bootstrapping
			exportClusterName = tenancyv1alpha1.RootCluster
		} else {
			path := logicalcluster.NewPath(apiBinding.Spec.Reference.Export.Path)
			export, err := o.getAPIExport(path, apiBinding.Spec.Reference.Export.Name)
			if err != nil {
				return forbidden
			}
			exportClusterName = logicalcluster.From(export)
		}

		// Access check
		if err := o.checkAPIExportAccess(ctx, a.GetUserInfo(), exportClusterName, apiBinding.Spec.Reference.Export.Name); err != nil {
			return forbidden
		}

		// Verify the labels
		value := apiBinding.Labels[apisv1alpha1.InternalAPIBindingExportLabelKey]
		if expected := permissionclaims.ToAPIBindingExportLabelValue(
			exportClusterName,
			apiBinding.Spec.Reference.Export.Name,
		); value != expected {
			return admission.NewForbidden(a, field.Invalid(field.NewPath("metadata").Child("labels").Key(apisv1alpha1.InternalAPIBindingExportLabelKey), value, fmt.Sprintf("must be set to %q", expected)))
		}
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
		return fmt.Errorf("no permission to bind to export %q", apiExportName)
	}

	return nil
}

// ValidateInitialization ensures the required injected fields are set.
func (o *apiBindingAdmission) ValidateInitialization() error {
	if o.deepSARClient == nil {
		return fmt.Errorf(PluginName + " plugin needs a Kubernetes ClusterInterface")
	}
	if o.apiExportLister == nil {
		return fmt.Errorf(PluginName + " plugin needs an APIExport lister")
	}
	return nil
}

// SetDeepSARClient is an admission plugin initializer function that injects a client capable of deep SAR requests into
// this admission plugin.
func (o *apiBindingAdmission) SetDeepSARClient(client kcpkubernetesclientset.ClusterInterface) {
	o.deepSARClient = client
}

func (o *apiBindingAdmission) SetKcpInformers(informers kcpinformers.SharedInformerFactory) {
	apiExportsReady := informers.Apis().V1alpha1().APIExports().Informer().HasSynced
	o.SetReadyFunc(func() bool {
		return apiExportsReady()
	})
	o.apiExportLister = informers.Apis().V1alpha1().APIExports().Lister()
	o.apiExportIndexer = informers.Apis().V1alpha1().APIExports().Informer().GetIndexer()

	indexers.AddIfNotPresentOrDie(informers.Tenancy().V1alpha1().ClusterWorkspaceTypes().Informer().GetIndexer(), cache.Indexers{
		indexers.ByLogicalClusterPathAndName: indexers.IndexByLogicalClusterPathAndName,
	})
}
