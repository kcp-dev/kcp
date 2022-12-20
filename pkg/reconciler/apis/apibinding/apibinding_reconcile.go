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
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apiextensions-apiserver/pkg/apihelpers"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilserrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	"github.com/kcp-dev/kcp/pkg/logging"
)

type reconcileStatus int

const (
	reconcileStatusStopAndRequeue reconcileStatus = iota
	reconcileStatusContinue
)

type reconciler interface {
	reconcile(ctx context.Context, this *apisv1alpha1.APIBinding) (reconcileStatus, error)
}

func (c *controller) reconcile(ctx context.Context, apiBinding *apisv1alpha1.APIBinding) (bool, error) {
	reconcilers := []reconciler{
		&phaseReconciler{
			newReconciler:     &newReconciler{controller: c},
			bindingReconciler: &bindingReconciler{controller: c},
		},
		&summaryReconciler{controller: c},
	}

	var errs []error

	requeue := false
	for _, r := range reconcilers {
		var err error
		var status reconcileStatus
		status, err = r.reconcile(ctx, apiBinding)
		if err != nil {
			errs = append(errs, err)
		}
		if status == reconcileStatusStopAndRequeue {
			requeue = true
			break
		}
	}

	return requeue, utilserrors.NewAggregate(errs)
}

type summaryReconciler struct {
	*controller
}

func (r *summaryReconciler) reconcile(ctx context.Context, apiBinding *apisv1alpha1.APIBinding) (reconcileStatus, error) {
	// The only condition that reflects if the APIBinding is Ready is InitialBindingCompleted. Other conditions
	// (e.g. APIExportValid) may revert to false after the initial binding has completed, but those must not affect
	// the readiness.
	conditions.SetSummary(
		apiBinding,
		conditions.WithConditions(
			apisv1alpha1.InitialBindingCompleted,
		),
	)

	return reconcileStatusContinue, nil
}

type phaseReconciler struct {
	newReconciler     reconciler
	bindingReconciler reconciler
}

func (r *phaseReconciler) reconcile(ctx context.Context, apiBinding *apisv1alpha1.APIBinding) (reconcileStatus, error) {
	switch apiBinding.Status.Phase {
	case "":
		return r.newReconciler.reconcile(ctx, apiBinding)
	case apisv1alpha1.APIBindingPhaseBinding, apisv1alpha1.APIBindingPhaseBound:
		return r.bindingReconciler.reconcile(ctx, apiBinding)
	}

	// should never happen
	return reconcileStatusContinue, nil
}

type newReconciler struct {
	*controller
}

func (r *newReconciler) reconcile(ctx context.Context, apiBinding *apisv1alpha1.APIBinding) (reconcileStatus, error) {
	apiBinding.Status.Phase = apisv1alpha1.APIBindingPhaseBinding

	conditions.MarkFalse(
		apiBinding,
		apisv1alpha1.InitialBindingCompleted,
		apisv1alpha1.WaitingForEstablishedReason,
		conditionsv1alpha1.ConditionSeverityInfo,
		"Waiting for API(s) to be established",
	)

	return reconcileStatusContinue, nil
}

type bindingReconciler struct {
	*controller
}

func (r *bindingReconciler) reconcile(ctx context.Context, apiBinding *apisv1alpha1.APIBinding) (reconcileStatus, error) {
	logger := klog.FromContext(ctx)

	// Check for valid reference
	workspaceRef := apiBinding.Spec.Reference.Export
	if workspaceRef == nil {
		// this should not happen because of OpenAPI
		conditions.MarkFalse(
			apiBinding,
			apisv1alpha1.APIExportValid,
			apisv1alpha1.APIExportInvalidReferenceReason,
			conditionsv1alpha1.ConditionSeverityError,
			"Missing APIExport reference",
		)
		return reconcileStatusContinue, nil
	}

	// Get APIExport
	apiExportPath := logicalcluster.NewPath(apiBinding.Spec.Reference.Export.Path)
	if apiExportPath.Empty() {
		apiExportPath = logicalcluster.From(apiBinding).Path()
	}
	apiExport, err := r.controller.getAPIExport(apiExportPath, workspaceRef.Name)
	if apierrors.IsNotFound(err) {
		conditions.MarkFalse(
			apiBinding,
			apisv1alpha1.APIExportValid,
			apisv1alpha1.APIExportNotFoundReason,
			conditionsv1alpha1.ConditionSeverityError,
			"APIExport %s|%s not found",
			apiExportPath,
			workspaceRef.Name,
		)
		return reconcileStatusContinue, nil
	}
	if err != nil {
		conditions.MarkFalse(
			apiBinding,
			apisv1alpha1.APIExportValid,
			apisv1alpha1.InternalErrorReason,
			conditionsv1alpha1.ConditionSeverityError,
			"Error getting APIExport %s|%s: %v",
			apiExportPath,
			workspaceRef.Name,
			err,
		)
		return reconcileStatusContinue, err
	}

	logger = logging.WithObject(logger, apiExport)

	// Record the export's permission claims
	apiBinding.Status.ExportPermissionClaims = apiExport.Spec.PermissionClaims

	// Make sure the APIExport has an identity
	if apiExport.Status.IdentityHash == "" {
		conditions.MarkFalse(
			apiBinding,
			apisv1alpha1.APIExportValid,
			"MissingIdentityHash",
			conditionsv1alpha1.ConditionSeverityWarning,
			"APIExport %s|%s is missing status.identityHash",
			apiExportPath,
			workspaceRef.Name,
		)
		return reconcileStatusContinue, nil
	}

	var needToWaitForRequeueWhenEstablished []string

	// Process all APIResourceSchemas
	for _, schemaName := range apiExport.Spec.LatestResourceSchemas {
		bindingClusterName := logicalcluster.From(apiBinding)

		// Get the schema
		schema, err := r.getAPIResourceSchema(logicalcluster.From(apiExport), schemaName)
		if err != nil {
			logger.Error(err, "error binding")

			conditions.MarkFalse(
				apiBinding,
				apisv1alpha1.APIExportValid,
				apisv1alpha1.InternalErrorReason,
				conditionsv1alpha1.ConditionSeverityError,
				"Invalid APIExport. Please contact the APIExport owner to resolve",
			)

			if apierrors.IsNotFound(err) {
				return reconcileStatusContinue, nil
			}

			return reconcileStatusContinue, err
		}

		logger = logging.WithObject(logger, schema)

		// Check for conflicts
		checker := &conflictChecker{
			listAPIBindings:      r.listAPIBindings,
			getAPIExport:         r.getAPIExport,
			getAPIResourceSchema: r.getAPIResourceSchema,
			getCRD:               r.getCRD,
			listCRDs:             r.listCRDs,
		}

		if err := checker.checkForConflicts(schema, apiBinding); err != nil {
			conditions.MarkFalse(
				apiBinding,
				apisv1alpha1.BindingUpToDate,
				apisv1alpha1.NamingConflictsReason,
				conditionsv1alpha1.ConditionSeverityError,
				"Unable to bind APIs: %v",
				err,
			)

			// Only change InitialBindingCompleted if it's false
			if conditions.IsFalse(apiBinding, apisv1alpha1.InitialBindingCompleted) {
				conditions.MarkFalse(
					apiBinding,
					apisv1alpha1.InitialBindingCompleted,
					apisv1alpha1.NamingConflictsReason,
					conditionsv1alpha1.ConditionSeverityError,
					"Unable to bind APIs: %v",
					err,
				)
			}
			return reconcileStatusContinue, nil
		}

		// Try to get the bound CRD
		existingCRD, err := r.getCRD(SystemBoundCRDSClusterName, boundCRDName(schema))
		if err != nil && !apierrors.IsNotFound(err) {
			conditions.MarkFalse(
				apiBinding,
				apisv1alpha1.APIExportValid,
				apisv1alpha1.InternalErrorReason,
				conditionsv1alpha1.ConditionSeverityError,
				"Invalid APIExport. Please contact the APIExport owner to resolve",
			)

			return reconcileStatusContinue, fmt.Errorf(
				"error getting CRD %s|%s for APIBinding %s|%s, APIExport %s|%s, APIResourceSchema %s|%s: %w",
				SystemBoundCRDSClusterName, boundCRDName(schema),
				bindingClusterName, apiBinding.Name,
				apiExportPath, apiExport.Name,
				apiExportPath, schemaName,
				err,
			)
		}

		if err == nil {
			// Bound CRD already exists
			if !apihelpers.IsCRDConditionTrue(existingCRD, apiextensionsv1.Established) {
				logger.V(4).Info("CRD is not established", "conditions", fmt.Sprintf("%#v", existingCRD.Status.Conditions))
				needToWaitForRequeueWhenEstablished = append(needToWaitForRequeueWhenEstablished, schemaName)
				continue
			} else if apihelpers.IsCRDConditionTrue(existingCRD, apiextensionsv1.Terminating) {
				logger.V(4).Info("CRD is terminating")
				needToWaitForRequeueWhenEstablished = append(needToWaitForRequeueWhenEstablished, schemaName)
				continue
			}
		} else {
			// Need to create bound CRD
			crd, err := generateCRD(schema)
			if err != nil {
				logger.Error(err, "error generating CRD")

				conditions.MarkFalse(
					apiBinding,
					apisv1alpha1.APIExportValid,
					apisv1alpha1.InternalErrorReason,
					conditionsv1alpha1.ConditionSeverityError,
					"Invalid APIExport. Please contact the APIExport owner to resolve",
				)

				return reconcileStatusContinue, nil
			}
			logger = logging.WithObject(logger, crd).WithValues(
				"groupResource", fmt.Sprintf("%s.%s", crd.Spec.Names.Plural, crd.Spec.Group),
			)

			// The crd was deleted and needs to be recreated. `existingCRD` might be non-nil if
			// the lister is behind, so explicitly set to nil to ensure recreation.
			if r.deletedCRDTracker.Has(crd.Name) {
				logger.V(4).Info("bound CRD was deleted - need to recreate")
				existingCRD = nil
			}

			// Create bound CRD
			logger.V(2).Info("creating CRD")
			if _, err := r.createCRD(ctx, SystemBoundCRDSClusterName.Path(), crd); err != nil {
				schemaClusterName := logicalcluster.From(schema)
				if apierrors.IsInvalid(err) {
					status := apierrors.APIStatus(nil)
					// The error is guaranteed to implement APIStatus here
					errors.As(err, &status)
					conditions.MarkFalse(
						apiBinding,
						apisv1alpha1.BindingUpToDate,
						apisv1alpha1.APIResourceSchemaInvalidReason,
						conditionsv1alpha1.ConditionSeverityError,
						fmt.Sprintf("APIResourceSchema %s|%s is invalid: %v\"", schemaClusterName, schemaName, status.Status().Details.Causes),
					)
					// Only change InitialBindingCompleted if it's false
					if conditions.IsFalse(apiBinding, apisv1alpha1.InitialBindingCompleted) {
						conditions.MarkFalse(
							apiBinding,
							apisv1alpha1.InitialBindingCompleted,
							apisv1alpha1.APIResourceSchemaInvalidReason,
							conditionsv1alpha1.ConditionSeverityError,
							fmt.Sprintf("APIResourceSchema %s|%s is invalid: %v\"", schemaClusterName, schemaName, status.Status().Details.Causes),
						)
					}

					logger.Error(err, "error creating CRD")

					return reconcileStatusContinue, nil
				}

				conditions.MarkFalse(
					apiBinding,
					apisv1alpha1.BindingUpToDate,
					apisv1alpha1.InternalErrorReason,
					conditionsv1alpha1.ConditionSeverityError,
					"An internal error prevented the APIBinding process from completing. Please contact your system administrator for assistance",
				)
				// Only change InitialBindingCompleted if it's false
				if conditions.IsFalse(apiBinding, apisv1alpha1.InitialBindingCompleted) {
					conditions.MarkFalse(
						apiBinding,
						apisv1alpha1.InitialBindingCompleted,
						apisv1alpha1.InternalErrorReason,
						conditionsv1alpha1.ConditionSeverityError,
						"An internal error prevented the APIBinding process from completing. Please contact your system administrator for assistance",
					)
				}

				return reconcileStatusContinue, err
			}

			r.deletedCRDTracker.Remove(crd.Name)

			needToWaitForRequeueWhenEstablished = append(needToWaitForRequeueWhenEstablished, schemaName)
			continue
		}

		// Merge any current storage versions with new ones
		storageVersions := sets.NewString()
		if existingCRD != nil {
			storageVersions.Insert(existingCRD.Status.StoredVersions...)
		}

		for _, b := range apiBinding.Status.BoundResources {
			if b.Group == schema.Spec.Group && b.Resource == schema.Spec.Names.Plural {
				storageVersions.Insert(b.StorageVersions...)
				break
			}
		}

		sortedStorageVersions := storageVersions.List()
		sort.Strings(sortedStorageVersions)

		// Upsert the BoundAPIResource for this APIResourceSchema
		newBoundResource := apisv1alpha1.BoundAPIResource{
			Group:    schema.Spec.Group,
			Resource: schema.Spec.Names.Plural,
			Schema: apisv1alpha1.BoundAPIResourceSchema{
				Name:         schema.Name,
				UID:          string(schema.UID),
				IdentityHash: apiExport.Status.IdentityHash,
			},
			StorageVersions: sortedStorageVersions,
		}

		found := false
		for i, r := range apiBinding.Status.BoundResources {
			if r.Group == schema.Spec.Group && r.Resource == schema.Spec.Names.Plural {
				apiBinding.Status.BoundResources[i] = newBoundResource
				found = true
				break
			}
		}
		if !found {
			apiBinding.Status.BoundResources = append(apiBinding.Status.BoundResources, newBoundResource)
		}
	}

	conditions.MarkTrue(apiBinding, apisv1alpha1.APIExportValid)

	if len(needToWaitForRequeueWhenEstablished) > 0 {
		sort.Strings(needToWaitForRequeueWhenEstablished)

		conditions.MarkFalse(
			apiBinding,
			apisv1alpha1.BindingUpToDate,
			apisv1alpha1.WaitingForEstablishedReason,
			conditionsv1alpha1.ConditionSeverityInfo,
			"Waiting for API(s) to be established: %s", strings.Join(needToWaitForRequeueWhenEstablished, ", "),
		)

		// Only change InitialBindingCompleted if it's false
		if conditions.IsFalse(apiBinding, apisv1alpha1.InitialBindingCompleted) {
			conditions.MarkFalse(
				apiBinding,
				apisv1alpha1.InitialBindingCompleted,
				apisv1alpha1.WaitingForEstablishedReason,
				conditionsv1alpha1.ConditionSeverityInfo,
				"Waiting for API(s) to be established: %s", strings.Join(needToWaitForRequeueWhenEstablished, ", "),
			)
		}
	} else {
		conditions.MarkTrue(apiBinding, apisv1alpha1.InitialBindingCompleted)
		conditions.MarkTrue(apiBinding, apisv1alpha1.BindingUpToDate)
		apiBinding.Status.Phase = apisv1alpha1.APIBindingPhaseBound
	}

	return reconcileStatusContinue, nil
}

func boundCRDName(schema *apisv1alpha1.APIResourceSchema) string {
	return string(schema.UID)
}

func generateCRD(schema *apisv1alpha1.APIResourceSchema) (*apiextensionsv1.CustomResourceDefinition, error) {
	crd := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: boundCRDName(schema),
			Annotations: map[string]string{
				logicalcluster.AnnotationKey:            SystemBoundCRDSClusterName.String(),
				apisv1alpha1.AnnotationBoundCRDKey:      "",
				apisv1alpha1.AnnotationSchemaClusterKey: logicalcluster.From(schema).String(),
				apisv1alpha1.AnnotationSchemaNameKey:    schema.Name,
			},
		},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: schema.Spec.Group,
			Names: schema.Spec.Names,
			Scope: schema.Spec.Scope,
		},
	}

	// Propagate the protected API approval annotation, `api-approved.kubernetes.io`, if any.
	// API groups that match `*.k8s.io` or `*.kubernetes.io` are owned by the Kubernetes community,
	// and protected by API review. The API server rejects the creation of a CRD whose group is
	// protected, unless the approval annotation is present.
	// See https://github.com/kubernetes/enhancements/pull/1111 for more details.
	if value, found := schema.Annotations[apiextensionsv1.KubeAPIApprovedAnnotation]; found {
		crd.Annotations[apiextensionsv1.KubeAPIApprovedAnnotation] = value
	}

	for _, version := range schema.Spec.Versions {
		crdVersion := apiextensionsv1.CustomResourceDefinitionVersion{
			Name:                     version.Name,
			Served:                   version.Served,
			Storage:                  version.Storage,
			Deprecated:               version.Deprecated,
			DeprecationWarning:       version.DeprecationWarning,
			Subresources:             &version.Subresources,
			AdditionalPrinterColumns: version.AdditionalPrinterColumns,
		}

		var validation apiextensionsv1.CustomResourceValidation
		if err := json.Unmarshal(version.Schema.Raw, &validation.OpenAPIV3Schema); err != nil {
			return nil, err
		}
		crdVersion.Schema = &validation

		crd.Spec.Versions = append(crd.Spec.Versions, crdVersion)
	}

	return crd, nil
}
