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

package apiexportendpointslice

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
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	apibindingadmission "github.com/kcp-dev/kcp/pkg/admission/apibinding"
	kcpinitializers "github.com/kcp-dev/kcp/pkg/admission/initializers"
	"github.com/kcp-dev/kcp/pkg/authorization/delegated"
	"github.com/kcp-dev/kcp/pkg/indexers"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	kcpinformers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions"
)

const (
	PluginName = "apis.kcp.io/APIExportEndpointSlice"
)

func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName,
		func(_ io.Reader) (admission.Interface, error) {
			p := &apiExportEndpointSliceAdmission{
				Handler:          admission.NewHandler(admission.Create, admission.Update),
				createAuthorizer: delegated.NewDelegatedAuthorizer,
			}
			p.getAPIExport = func(path logicalcluster.Path, name string) (*apisv1alpha1.APIExport, error) {
				return indexers.ByPathAndNameWithFallback[*apisv1alpha1.APIExport](apisv1alpha1.Resource("apiexports"), p.localApiExportIndexer, p.globalApiExportIndexer, path, name)
			}

			return p, nil
		})
}

type apiExportEndpointSliceAdmission struct {
	*admission.Handler

	getAPIExport func(path logicalcluster.Path, name string) (*apisv1alpha1.APIExport, error)

	localApiExportIndexer  cache.Indexer
	globalApiExportIndexer cache.Indexer

	deepSARClient    kcpkubernetesclientset.ClusterInterface
	createAuthorizer delegated.DelegatedAuthorizerFactory
}

// Ensure that the required admission interfaces are implemented.
var (
	_ = admission.ValidationInterface(&apiExportEndpointSliceAdmission{})
	_ = admission.InitializationValidator(&apiExportEndpointSliceAdmission{})
	_ = kcpinitializers.WantsDeepSARClient(&apiExportEndpointSliceAdmission{})
	_ = kcpinitializers.WantsKcpInformers(&apiExportEndpointSliceAdmission{})
)

// Validate validates the creation of APIExportEndpointSlice resources. It also performs a SubjectAccessReview
// making sure the user is allowed to use the 'bind' verb with the referenced APIExport.
func (o *apiExportEndpointSliceAdmission) Validate(ctx context.Context, a admission.Attributes, _ admission.ObjectInterfaces) error {
	clusterName, err := genericapirequest.ClusterNameFrom(ctx)
	if err != nil {
		return apierrors.NewInternalError(err)
	}

	if a.GetResource().GroupResource() != apisv1alpha1.Resource("apiexportendpointslices") {
		return nil
	}

	u, ok := a.GetObject().(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("unexpected type %T", a.GetObject())
	}

	slice := &apisv1alpha1.APIExportEndpointSlice{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, slice); err != nil {
		return fmt.Errorf("failed to convert unstructured to APIExportEndpointSlice: %w", err)
	}

	// Object validation
	var errs field.ErrorList
	var oldSlice *apisv1alpha1.APIExportEndpointSlice
	switch a.GetOperation() {
	case admission.Create:
		errs = ValidateAPIExportEndpointSlice(slice)
		if len(errs) > 0 {
			return admission.NewForbidden(a, fmt.Errorf("%v", errs))
		}
	case admission.Update:
		u, ok = a.GetOldObject().(*unstructured.Unstructured)
		if !ok {
			return fmt.Errorf("unexpected type %T", a.GetOldObject())
		}
		oldSlice = &apisv1alpha1.APIExportEndpointSlice{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, oldSlice); err != nil {
			return fmt.Errorf("failed to convert unstructured to APIExportEndpointSlice: %w", err)
		}
	}

	switch {
	case a.GetOperation() == admission.Create,
		// this may be redundant as APIExportEndpointSlice.Spec.APIExport is immutable
		a.GetOperation() == admission.Update && !reflect.DeepEqual(slice.Spec.APIExport, oldSlice.Spec.APIExport):
		// unified forbidden error that does not leak workspace existence
		// only considering "create" as the APIExport reference is immutable
		forbidden := admission.NewForbidden(a, fmt.Errorf("unable to create APIExportEndpointSlice: no permission to bind to export %s",
			logicalcluster.NewPath(slice.Spec.APIExport.Path).Join(slice.Spec.APIExport.Name).String()))

		// get cluster name of export
		var exportClusterName logicalcluster.Name
		if slice.Spec.APIExport.Path == "" {
			exportClusterName = clusterName
		} else {
			path := logicalcluster.NewPath(slice.Spec.APIExport.Path)
			export, err := o.getAPIExport(path, slice.Spec.APIExport.Name)
			if err != nil {
				return forbidden
			}
			exportClusterName = logicalcluster.From(export)
		}

		// Access check
		if err := o.checkAPIExportAccess(ctx, a.GetUserInfo(), exportClusterName, slice.Spec.APIExport.Name); err != nil {
			return forbidden
		}
	}

	return nil
}

func (o *apiExportEndpointSliceAdmission) checkAPIExportAccess(ctx context.Context, user user.Info, apiExportClusterName logicalcluster.Name, apiExportName string) error {
	logger := klog.FromContext(ctx)
	authz, err := o.createAuthorizer(apiExportClusterName, o.deepSARClient, delegated.Options{})
	if err != nil {
		// Logging a more specific error for the operator
		logger.Error(err, "error creating authorizer from delegating authorizer config")
		// Returning a less specific error to the end user
		return errors.New("unable to authorize request")
	}

	return apibindingadmission.CheckAPIExportAccess(ctx, user, apiExportName, authz)
}

// ValidateInitialization ensures the required injected fields are set.
func (o *apiExportEndpointSliceAdmission) ValidateInitialization() error {
	if o.deepSARClient == nil {
		return fmt.Errorf(PluginName + " plugin needs a deepSARClient")
	}
	return nil
}

// SetDeepSARClient is an admission plugin initializer function that injects a client capable of deep SAR requests into
// this admission plugin.
func (o *apiExportEndpointSliceAdmission) SetDeepSARClient(client kcpkubernetesclientset.ClusterInterface) {
	o.deepSARClient = client
}

func (o *apiExportEndpointSliceAdmission) SetKcpInformers(local, global kcpinformers.SharedInformerFactory) {
	localApiExportsReady := local.Apis().V1alpha1().APIExports().Informer().HasSynced
	globalApiExportsReady := global.Apis().V1alpha1().APIExports().Informer().HasSynced
	o.SetReadyFunc(func() bool {
		return localApiExportsReady() && globalApiExportsReady()
	})
	o.localApiExportIndexer = local.Apis().V1alpha1().APIExports().Informer().GetIndexer()
	o.globalApiExportIndexer = global.Apis().V1alpha1().APIExports().Informer().GetIndexer()
}
