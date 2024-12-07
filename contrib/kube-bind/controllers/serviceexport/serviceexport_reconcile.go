/*
Copyright 2022 The Kube Bind Authors.

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

package serviceexport

import (
	"context"

	kubebindv1alpha1 "github.com/kube-bind/kube-bind/pkg/apis/kubebind/v1alpha1"
	kubebindhelpers "github.com/kube-bind/kube-bind/pkg/apis/kubebind/v1alpha1/helpers"
	conditionsapi "github.com/kube-bind/kube-bind/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kube-bind/kube-bind/pkg/apis/third_party/conditions/util/conditions"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/klog/v2"
)

type reconciler struct {
	getCRD              func(name string) (*apiextensionsv1.CustomResourceDefinition, error)
	deleteServiceExport func(ctx context.Context, namespace, name string) error

	requeue func(export *kubebindv1alpha1.APIServiceExport)
}

func (r *reconciler) reconcile(ctx context.Context, export *kubebindv1alpha1.APIServiceExport) error {
	var errs []error

	if specChanged, err := r.ensureSchema(ctx, export); err != nil {
		errs = append(errs, err)
	} else if specChanged {
		r.requeue(export)
		return nil
	}

	return utilerrors.NewAggregate(errs)
}

func (r *reconciler) ensureSchema(ctx context.Context, export *kubebindv1alpha1.APIServiceExport) (specChanged bool, err error) {
	logger := klog.FromContext(ctx)

	crd, err := r.getCRD(export.Name)
	if err != nil && !errors.IsNotFound(err) {
		return false, err
	}

	if crd == nil {
		// CRD missing => delete SER too
		logger.V(1).Info("Deleting APIServiceExport because CRD is missing")
		return false, r.deleteServiceExport(ctx, export.Namespace, export.Name)
	}

	expected, err := kubebindhelpers.CRDToServiceExport(crd)
	if err != nil {
		conditions.MarkFalse(
			export,
			kubebindv1alpha1.APIServiceExportConditionProviderInSync,
			"CustomResourceDefinitionUpdateFailed",
			conditionsapi.ConditionSeverityError,
			"CustomResourceDefinition %s cannot be converted into a APIServiceExport: %s",
			export.Name, err,
		)
		return false, nil //nothing we can do
	}

	if hash := kubebindhelpers.APIServiceExportCRDSpecHash(expected); export.Annotations[kubebindv1alpha1.SourceSpecHashAnnotationKey] != hash {
		// both exist, update APIServiceExport
		logger.V(1).Info("Updating APIServiceExport")
		export.Spec.APIServiceExportCRDSpec = *expected
		if export.Annotations == nil {
			export.Annotations = map[string]string{}
		}
		export.Annotations[kubebindv1alpha1.SourceSpecHashAnnotationKey] = hash
		return true, nil
	}

	conditions.MarkTrue(export, kubebindv1alpha1.APIServiceExportConditionProviderInSync)

	return false, nil
}
