/*
Copyright 2022 The kcp Authors.

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

package apiresourceschema

import (
	"context"
	"fmt"
	"io"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/apiserver/pkg/admission"
	kuser "k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/endpoints/request"

	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	kcpinformers "github.com/kcp-dev/sdk/client/informers/externalversions"
	apisv1alpha2listers "github.com/kcp-dev/sdk/client/listers/apis/v1alpha2"

	"github.com/kcp-dev/kcp/pkg/admission/initializers"
	"github.com/kcp-dev/kcp/pkg/reconciler/apis/apibinding"
)

const (
	PluginName = "tenancy.kcp.io/APIResourceSchema"
)

func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName,
		func(_ io.Reader) (admission.Interface, error) {
			return &apiResourceSchemaValidation{
				Handler: admission.NewHandler(admission.Create, admission.Update),
			}, nil
		})
}

type apiResourceSchemaValidation struct {
	*admission.Handler

	hasSynced       func() bool
	apiExportLister apisv1alpha2listers.APIExportClusterLister
	historyLister   apisv1alpha2listers.APIExportHistoryClusterLister
}

// Ensure that the required admission interfaces are implemented.
var _ = admission.ValidationInterface(&apiResourceSchemaValidation{})
var _ = admission.InitializationValidator(&apiResourceSchemaValidation{})
var _ = initializers.WantsKcpInformers(&apiResourceSchemaValidation{})

// SetKcpInformers implements initializers.WantsKcpInformers.
func (o *apiResourceSchemaValidation) SetKcpInformers(local, _ kcpinformers.SharedInformerFactory) {
	apiExports := local.Apis().V1alpha2().APIExports()
	histories := local.Apis().V1alpha2().APIExportHistories()

	o.hasSynced = func() bool {
		return apiExports.Informer().HasSynced() && histories.Informer().HasSynced()
	}
	o.apiExportLister = apiExports.Lister()
	o.historyLister = histories.Lister()
}

// ValidateInitialization implements admission.InitializationValidator.
func (o *apiResourceSchemaValidation) ValidateInitialization() error {
	if o.apiExportLister == nil {
		return fmt.Errorf(PluginName + " plugin needs an APIExport lister")
	}
	if o.historyLister == nil {
		return fmt.Errorf(PluginName + " plugin needs an APIExportHistory lister")
	}
	if o.hasSynced == nil {
		return fmt.Errorf(PluginName + " plugin needs an informer sync check")
	}
	return nil
}

// Validate does validation of a APIResourceSchema for create and update.
func (o *apiResourceSchemaValidation) Validate(ctx context.Context, a admission.Attributes, _ admission.ObjectInterfaces) (err error) {
	if a.GetResource().GroupResource() != apisv1alpha1.Resource("apiresourceschemas") {
		return nil
	}

	u, ok := a.GetObject().(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("unexpected type %T", a.GetObject())
	}
	schema := &apisv1alpha1.APIResourceSchema{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, schema); err != nil {
		return fmt.Errorf("failed to convert unstructured to APIResourceSchema: %w", err)
	}

	// first all steps where we need no lister
	var old *apisv1alpha1.APIResourceSchema
	switch a.GetOperation() {
	case admission.Create:
		if errs := ValidateAPIResourceSchema(ctx, schema); len(errs) > 0 {
			return admission.NewForbidden(a, fmt.Errorf("%v", errs))
		}

		if err := o.validateHistory(ctx, a, schema); err != nil {
			return err
		}

	case admission.Update:
		u, ok = a.GetOldObject().(*unstructured.Unstructured)
		if !ok {
			return fmt.Errorf("unexpected type %T", a.GetOldObject())
		}
		old = &apisv1alpha1.APIResourceSchema{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, old); err != nil {
			return fmt.Errorf("failed to convert unstructured to APIResourceSchema: %w", err)
		}

		if errs := ValidateAPIResourceSchemaUpdate(ctx, schema, old); len(errs) > 0 {
			return admission.NewForbidden(a, fmt.Errorf("%v", errs))
		}
	}

	return nil
}

// validateHistory rejects a schema that changes the scope of a group resource an
// APIExport referencing it has served before.
func (o *apiResourceSchemaValidation) validateHistory(ctx context.Context, a admission.Attributes, schema *apisv1alpha1.APIResourceSchema) error {
	// Do not wait for the informers here: the system APIResourceSchemas are written
	// while bootstrapping, before the kcp informers this plugin needs are started.
	// Only those privileged writes are exempt, everybody else fails closed.
	if o.hasSynced == nil {
		return nil
	}
	if !o.hasSynced() {
		if isSystemPrivileged(a) {
			return nil
		}
		return admission.NewForbidden(a, fmt.Errorf("not yet ready to handle request"))
	}

	clusterName, err := request.ClusterNameFrom(ctx)
	if err != nil {
		return fmt.Errorf("failed to retrieve cluster from context: %w", err)
	}

	apiExports, err := o.apiExportLister.Cluster(clusterName).List(labels.Everything())
	if err != nil {
		return fmt.Errorf("failed to list APIExports: %w", err)
	}

	for _, apiExport := range apiExports {
		if !referencesSchema(apiExport, schema.Name) {
			continue
		}

		history, err := o.historyLister.Cluster(apibinding.SystemBoundCRDsClusterName).Get(string(apiExport.UID))
		if apierrors.IsNotFound(err) {
			continue
		} else if err != nil {
			return fmt.Errorf("failed to get scope history for APIExport %s: %w", apiExport.Name, err)
		}

		for _, resource := range history.Status.Resources {
			if resource.Group != schema.Spec.Group || resource.Resource != schema.Spec.Names.Plural {
				continue
			}
			if resource.Scope == schema.Spec.Scope {
				continue
			}

			return admission.NewForbidden(a, field.Invalid(
				field.NewPath("spec").Child("scope"),
				schema.Spec.Scope,
				fmt.Sprintf("%s.%s has been served with scope %q by APIExport %s, it cannot be served with scope %q; "+
					"use a different group resource or a new APIExport instead",
					schema.Spec.Names.Plural, schema.Spec.Group, resource.Scope, apiExport.Name, schema.Spec.Scope)))
		}
	}

	return nil
}

func referencesSchema(apiExport *apisv1alpha2.APIExport, name string) bool {
	for _, resource := range apiExport.Spec.Resources {
		if resource.Schema == name {
			return true
		}
	}
	return false
}

func isSystemPrivileged(a admission.Attributes) bool {
	u := a.GetUserInfo()
	if u == nil {
		return false
	}
	return sets.New(u.GetGroups()...).Has(kuser.SystemPrivilegedGroup)
}
