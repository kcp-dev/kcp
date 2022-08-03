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

	"github.com/kcp-dev/logicalcluster/v2"

	"k8s.io/apiextensions-apiserver/pkg/apihelpers"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
)

func (c *controller) reconcile(ctx context.Context, apiBinding *apisv1alpha1.APIBinding) error {
	// The only condition that reflects if the APIBinding is Ready is InitialBindingCompleted. Other conditions
	// (e.g. APIExportValid) may revert to false after the initial binding has completed, but those must not affect
	// the readiness.
	defer conditions.SetSummary(
		apiBinding,
		conditions.WithConditions(
			apisv1alpha1.InitialBindingCompleted,
		),
	)

	switch apiBinding.Status.Phase {
	case "":
		return c.reconcileNew(ctx, apiBinding)
	case apisv1alpha1.APIBindingPhaseBinding:
		return kerrors.NewAggregate([]error{c.reconcileBinding(ctx, apiBinding), c.reconcilePermissionClaims(ctx, apiBinding)})
	case apisv1alpha1.APIBindingPhaseBound:
		needsRebind, err := c.reconcileBound(ctx, apiBinding)
		if err != nil {
			// Need to run permission claim reconcile
			permissionClaimErr := c.reconcilePermissionClaims(ctx, apiBinding)
			return kerrors.NewAggregate([]error{err, permissionClaimErr})
		}
		if needsRebind {
			return kerrors.NewAggregate([]error{c.reconcileBinding(ctx, apiBinding), c.reconcilePermissionClaims(ctx, apiBinding)})
		}
		permissionClaimErr := c.reconcilePermissionClaims(ctx, apiBinding)
		return permissionClaimErr
	default:
		klog.Errorf("Invalid phase %q for APIBinding %s|%s", apiBinding.Status.Phase, logicalcluster.From(apiBinding).String(), apiBinding.Name)
		return nil
	}
}

func (c *controller) reconcileNew(ctx context.Context, apiBinding *apisv1alpha1.APIBinding) error {
	apiBinding.Status.Phase = apisv1alpha1.APIBindingPhaseBinding

	conditions.MarkFalse(
		apiBinding,
		apisv1alpha1.InitialBindingCompleted,
		apisv1alpha1.WaitingForEstablishedReason,
		conditionsv1alpha1.ConditionSeverityInfo,
		"Waiting for API(s) to be established",
	)

	return nil
}

func (c *controller) reconcileBinding(ctx context.Context, apiBinding *apisv1alpha1.APIBinding) error {
	workspaceRef := apiBinding.Spec.Reference.Workspace
	if workspaceRef == nil {
		// this should not happen because of OpenAPI
		conditions.MarkFalse(
			apiBinding,
			apisv1alpha1.APIExportValid,
			apisv1alpha1.APIExportInvalidReferenceReason,
			conditionsv1alpha1.ConditionSeverityError,
			"Missing APIExport reference",
		)
		return nil
	}

	apiExportClusterName, err := getAPIExportClusterName(apiBinding)
	if err != nil {
		// this should not happen because of OpenAPI
		conditions.MarkFalse(
			apiBinding,
			apisv1alpha1.APIExportValid,
			apisv1alpha1.APIExportInvalidReferenceReason,
			conditionsv1alpha1.ConditionSeverityError,
			err.Error(),
		)
		return nil
	}

	apiExport, err := c.getAPIExport(apiExportClusterName, workspaceRef.ExportName)
	if apierrors.IsNotFound(err) {
		conditions.MarkFalse(
			apiBinding,
			apisv1alpha1.APIExportValid,
			apisv1alpha1.APIExportNotFoundReason,
			conditionsv1alpha1.ConditionSeverityError,
			"APIExport %s|%s not found",
			apiExportClusterName,
			workspaceRef.ExportName,
		)
		return nil
	}
	if err != nil {
		conditions.MarkFalse(
			apiBinding,
			apisv1alpha1.APIExportValid,
			apisv1alpha1.InternalErrorReason,
			conditionsv1alpha1.ConditionSeverityError,
			"Error getting APIExport %s|%s: %v",
			apiExportClusterName,
			workspaceRef.ExportName,
			err,
		)
		return err
	}

	if apiExport.Status.IdentityHash == "" {
		conditions.MarkFalse(
			apiBinding,
			apisv1alpha1.APIExportValid,
			"MissingIdentityHash",
			conditionsv1alpha1.ConditionSeverityWarning,
			"APIExport %s|%s is missing status.identityHash",
			apiExportClusterName,
			workspaceRef.ExportName,
		)
		return nil
	}

	var needToWaitForRequeueWhenEstablished []string

	for _, schemaName := range apiExport.Spec.LatestResourceSchemas {
		schema, err := c.getAPIResourceSchema(apiExportClusterName, schemaName)
		bindingClusterName := logicalcluster.From(apiBinding)
		exportClustername := logicalcluster.From(apiExport)
		if err != nil {
			klog.Errorf(
				"Error binding APIBinding %s|%s, APIExport %s|%s, APIResourceSchema %s|%s: %v",
				bindingClusterName, apiBinding.Name,
				exportClustername, apiExport.Name,
				exportClustername, schemaName,
				err,
			)

			conditions.MarkFalse(
				apiBinding,
				apisv1alpha1.APIExportValid,
				apisv1alpha1.InternalErrorReason,
				conditionsv1alpha1.ConditionSeverityError,
				"Invalid APIExport. Please contact the APIExport owner to resolve",
			)

			if apierrors.IsNotFound(err) {
				return nil
			}

			return err
		}

		crd, err := generateCRD(schema)
		if err != nil {
			klog.Errorf(
				"Error generating CRD for APIBinding %s|%s, APIExport %s|%s, APIResourceSchema %s|%s: %v",
				bindingClusterName, apiBinding.Name,
				apiExportClusterName, apiExport.Name,
				apiExportClusterName, schemaName,
				err,
			)

			conditions.MarkFalse(
				apiBinding,
				apisv1alpha1.APIExportValid,
				apisv1alpha1.InternalErrorReason,
				conditionsv1alpha1.ConditionSeverityError,
				"Invalid APIExport. Please contact the APIExport owner to resolve",
			)

			return nil
		}

		// Check for conflicts
		checker := &conflictChecker{
			listAPIBindings:      c.listAPIBindings,
			getAPIExport:         c.getAPIExport,
			getAPIResourceSchema: c.getAPIResourceSchema,
			getCRD:               c.getCRD,
			crdIndexer:           c.crdIndexer,
		}

		if err := checker.checkForConflicts(crd, apiBinding); err != nil {
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
			return nil
		}

		existingCRD, err := c.getCRD(ShadowWorkspaceName, crd.Name)
		if err != nil && !apierrors.IsNotFound(err) {
			conditions.MarkFalse(
				apiBinding,
				apisv1alpha1.APIExportValid,
				apisv1alpha1.InternalErrorReason,
				conditionsv1alpha1.ConditionSeverityError,
				"Invalid APIExport. Please contact the APIExport owner to resolve",
			)

			return fmt.Errorf(
				"error getting CRD %s|%s for APIBinding %s|%s, APIExport %s|%s, APIResourceSchema %s|%s: %w",
				ShadowWorkspaceName, crd.Name,
				bindingClusterName, apiBinding.Name,
				apiExportClusterName, apiExport.Name,
				apiExportClusterName, schemaName,
				err,
			)
		}

		// The crd was deleted and needs to be recreated. `existingCRD` might be non-nil if
		// the lister is behind, so explicitly set to nil to ensure recreation.
		if c.deletedCRDTracker.Has(crd.Name) {
			klog.V(4).Infof("Bound CRD %s|%s was deleted - need to recreate", ShadowWorkspaceName, crd.Name)
			existingCRD = nil
		}

		if existingCRD == nil {
			// Create flow

			klog.V(2).Infof("Creating CRD %s|%s for APIBinding %s|%s resource %s.%s", ShadowWorkspaceName, crd.Name, logicalcluster.From(apiBinding), apiBinding.Name, crd.Spec.Names.Plural, crd.Spec.Group)
			if _, err := c.createCRD(ctx, ShadowWorkspaceName, crd); err != nil {
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

					klog.Errorf(
						"Error creating CRD for APIBinding %s|%s, APIExport %s|%s, APIResourceSchema %s|%s: %v",
						bindingClusterName, apiBinding.Name,
						apiExportClusterName, apiExport.Name,
						apiExportClusterName, schemaName,
						err,
					)

					return nil
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

				return err
			}

			c.deletedCRDTracker.Remove(crd.Name)

			needToWaitForRequeueWhenEstablished = append(needToWaitForRequeueWhenEstablished, schemaName)
			continue
		} else {
			// Existing CRD flow
			if !apihelpers.IsCRDConditionTrue(existingCRD, apiextensionsv1.Established) {
				bs, err := json.Marshal(existingCRD.Status.Conditions)
				if err != nil {
					return err
				}
				klog.V(4).Infof("CRD %s|%s is not established: %s", ShadowWorkspaceName, crd.Name, string(bs))
				needToWaitForRequeueWhenEstablished = append(needToWaitForRequeueWhenEstablished, schemaName)
				continue
			} else if apihelpers.IsCRDConditionTrue(existingCRD, apiextensionsv1.Terminating) {
				klog.V(4).Infof("CRD %s|%s is terminating", ShadowWorkspaceName)
				needToWaitForRequeueWhenEstablished = append(needToWaitForRequeueWhenEstablished, schemaName)
				continue
			}
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

	apiBinding.Status.BoundAPIExport = &apiBinding.Spec.Reference

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

	return nil
}

func (c *controller) reconcileBound(ctx context.Context, apiBinding *apisv1alpha1.APIBinding) (rebind bool, err error) {
	apiExportClusterName, err := getAPIExportClusterName(apiBinding)
	apiBindingClusterName := logicalcluster.From(apiBinding)
	if err != nil {
		// Should never happen
		conditions.MarkFalse(
			apiBinding,
			apisv1alpha1.APIExportValid,
			apisv1alpha1.APIExportNotFoundReason,
			conditionsv1alpha1.ConditionSeverityError,
			err.Error(),
		)

		return false, nil
	}

	if referencedAPIExportChanged(apiBinding) {
		klog.V(2).Infof("APIBinding %s|%s needs rebinding because it now points to a different APIExport", apiBindingClusterName, apiBinding.Name)
		return true, nil
	}

	apiExport, err := c.getAPIExport(apiExportClusterName, apiBinding.Spec.Reference.Workspace.ExportName)
	if apierrors.IsNotFound(err) {
		conditions.MarkFalse(
			apiBinding,
			apisv1alpha1.APIExportValid,
			apisv1alpha1.APIExportNotFoundReason,
			conditionsv1alpha1.ConditionSeverityWarning,
			"APIExport %s|%s not found",
			apiExportClusterName,
			apiBinding.Spec.Reference.Workspace.ExportName,
		)

		// Return nil here so we don't retry. If/when there is an informer event for the correct APIExport, this
		// APIBinding will automatically be requeued.
		return false, nil
	}
	if err != nil {
		conditions.MarkFalse(
			apiBinding,
			apisv1alpha1.APIExportValid,
			apisv1alpha1.InternalErrorReason,
			conditionsv1alpha1.ConditionSeverityWarning,
			"Error getting APIExport %s|%s: %v",
			apiExportClusterName,
			apiBinding.Spec.Reference.Workspace.ExportName,
			err,
		)

		return false, err
	}

	var exportedSchemas []*apisv1alpha1.APIResourceSchema
	for _, schemaName := range apiExport.Spec.LatestResourceSchemas {
		apiResourceSchema, err := c.getAPIResourceSchema(apiExportClusterName, schemaName)
		if err != nil {
			klog.Errorf("Error getting APIResourceSchema %s|%s for APIBinding %s|%s: %v", apiExportClusterName, schemaName, apiBindingClusterName, apiBinding.Name, err)
			conditions.MarkFalse(
				apiBinding,
				apisv1alpha1.APIExportValid,
				apisv1alpha1.InternalErrorReason,
				conditionsv1alpha1.ConditionSeverityError,
				"An internal error occurred with the APIExport",
			)
			if apierrors.IsNotFound(err) {
				// Return nil here so we don't retry. If/when there is an informer event for
				// the correct APIExport and/or APIResourcesSchema, this APIBinding will
				// automatically be requeued.
				return false, nil
			}

			return false, err
		}

		exportedSchemas = append(exportedSchemas, apiResourceSchema)
	}

	if apiExportLatestResourceSchemasChanged(apiBinding, exportedSchemas) {
		klog.V(2).Infof("APIBinding %s|%s needs rebinding because the APIExport's latestResourceSchemas has changed", apiBindingClusterName, apiBinding.Name)
		return true, nil
	}

	return false, nil
}

func generateCRD(schema *apisv1alpha1.APIResourceSchema) (*apiextensionsv1.CustomResourceDefinition, error) {
	crd := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: string(schema.UID),
			Annotations: map[string]string{
				logicalcluster.AnnotationKey:            ShadowWorkspaceName.String(),
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

func getAPIExportClusterName(apiBinding *apisv1alpha1.APIBinding) (logicalcluster.Name, error) {
	if apiBinding.Spec.Reference.Workspace == nil {
		// cannot happen due to APIBinding validation
		return logicalcluster.Name{}, fmt.Errorf("APIBinding does not specify an APIExport")
	}

	return logicalcluster.New(apiBinding.Spec.Reference.Workspace.Path), nil
}

func referencedAPIExportChanged(apiBinding *apisv1alpha1.APIBinding) bool {
	// Can't happen because of OpenAPI, but just in case
	if apiBinding.Spec.Reference.Workspace == nil {
		return false
	}

	return *apiBinding.Spec.Reference.Workspace != *apiBinding.Status.BoundAPIExport.Workspace
}

func apiExportLatestResourceSchemasChanged(apiBinding *apisv1alpha1.APIBinding, exportedSchemas []*apisv1alpha1.APIResourceSchema) bool {
	exportedSchemaUIDs := sets.NewString()
	for _, exportedSchema := range exportedSchemas {
		exportedSchemaUIDs.Insert(string(exportedSchema.UID))
	}

	boundSchemaUIDs := sets.NewString()
	for _, boundResource := range apiBinding.Status.BoundResources {
		boundSchemaUIDs.Insert(boundResource.Schema.UID)
	}

	return !exportedSchemaUIDs.Equal(boundSchemaUIDs)
}
