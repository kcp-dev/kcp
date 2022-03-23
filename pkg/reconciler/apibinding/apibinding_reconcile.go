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
	"fmt"
	"reflect"
	"sync"

	"github.com/google/go-cmp/cmp"

	"k8s.io/apiextensions-apiserver/pkg/apihelpers"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1/helper"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/third_party/conditions/util/conditions"
)

const (
	annotationBoundCRDKey      = "apis.kcp.dev/bound-crd"
	annotationSchemaClusterKey = "apis.kcp.dev/schema-cluster"
	annotationSchemaNameKey    = "apis.kcp.dev/schema-name"
)

type reconcileStatus int

const (
	reconcileStatusStop reconcileStatus = iota
	reconcileStatusContinue
)

type reconciler interface {
	reconcile(ctx context.Context, apiBinding *apisv1alpha1.APIBinding) (reconcileStatus, error)
}

type phaseReconciler struct {
	getAPIExport         func(clusterName, name string) (*apisv1alpha1.APIExport, error)
	getAPIResourceSchema func(clusterName, name string) (*apisv1alpha1.APIResourceSchema, error)
}

func (r *phaseReconciler) reconcile(_ context.Context, apiBinding *apisv1alpha1.APIBinding) (reconcileStatus, error) {
	switch apiBinding.Status.Phase {
	case "":
		apiBinding.Status.Phase = apisv1alpha1.APIBindingPhaseBinding
	case apisv1alpha1.APIBindingPhaseBound:
		apiExportClusterName := getAPIExportClusterName(apiBinding)
		if apiExportClusterName == "" {
			// Should never happen
			conditions.MarkFalse(
				apiBinding,
				apisv1alpha1.APIExportValid,
				apisv1alpha1.APIExportNotFoundReason,
				conditionsv1alpha1.ConditionSeverityError,
				"unable to determine workspace for APIExport",
			)

			// No way to proceed from here - don't retry
			return reconcileStatusStop, nil
		}

		if referencedAPIExportChanged(apiBinding) {
			klog.Infof("APIBinding %s|%s needs rebinding because it now points to a different APIExport", apiBinding.ClusterName, apiBinding.Name)

			apiBinding.Status.Phase = apisv1alpha1.APIBindingPhaseBinding

			// Commit the phase change, then wait for next update event to process binding changes
			return reconcileStatusStop, nil
		}

		apiExport, err := r.getAPIExport(apiExportClusterName, apiBinding.Spec.Reference.Workspace.ExportName)
		if err != nil {
			conditions.MarkFalse(
				apiBinding,
				apisv1alpha1.APIExportValid,
				apisv1alpha1.GetErrorReason,
				conditionsv1alpha1.ConditionSeverityWarning,
				"error getting previously bound APIExport: %v",
				err,
			)

			// Stop reconciling, but return error so we retry
			return reconcileStatusStop, fmt.Errorf("error getting APIExport %s|%s: %w", apiExportClusterName, apiBinding.Spec.Reference.Workspace.ExportName, err)
		}

		var exportedSchemas []*apisv1alpha1.APIResourceSchema
		// name -> uid
		exportedSchemaUIDs := map[string]string{}

		for _, schemaName := range apiExport.Spec.LatestResourceSchemas {
			apiResourceSchema, err := r.getAPIResourceSchema(apiExportClusterName, schemaName)
			if err != nil {
				conditions.MarkFalse(
					apiBinding,
					apisv1alpha1.APIExportValid,
					apisv1alpha1.GetErrorReason,
					conditionsv1alpha1.ConditionSeverityError,
					"error getting APIResourceSchema %s|%s: %v",
					apiExportClusterName,
					schemaName,
					err,
				)

				// No way to proceed from here - don't retry
				return reconcileStatusStop, nil
			}

			exportedSchemas = append(exportedSchemas, apiResourceSchema)
			exportedSchemaUIDs[schemaName] = string(apiResourceSchema.UID)
		}

		if apiExportLatestResourceSchemasChanged(apiBinding, exportedSchemas) {
			klog.Infof("APIBinding %s|%s needs rebinding because the APIExport's latestResourceSchemas has changed", apiBinding.ClusterName, apiBinding.Name)

			apiBinding.Status.Phase = apisv1alpha1.APIBindingPhaseBinding

			// Commit the phase change, then wait for next update event to process binding changes
			return reconcileStatusStop, nil
		}
	}

	return reconcileStatusContinue, nil
}

func getAPIExportClusterName(apiBinding *apisv1alpha1.APIBinding) string {
	org, _, err := helper.ParseLogicalClusterName(apiBinding.ClusterName)
	if err != nil {
		// should never happen
		klog.Errorf("Error parsing logical cluster %q: %v", apiBinding.ClusterName, err)
		return ""
	}

	return helper.EncodeOrganizationAndClusterWorkspace(org, apiBinding.Spec.Reference.Workspace.WorkspaceName)
}

func referencedAPIExportChanged(apiBinding *apisv1alpha1.APIBinding) bool {
	// Can't happen because of OpenAPI, but just in case
	if apiBinding.Spec.Reference.Workspace == nil {
		return false
	}

	if apiBinding.Status.BoundAPIExport == nil {
		return false
	}
	if apiBinding.Status.BoundAPIExport.Workspace == nil {
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

type workspaceAPIExportReferenceReconciler struct {
	getAPIExport         func(clusterName, name string) (*apisv1alpha1.APIExport, error)
	getAPIResourceSchema func(clusterName, name string) (*apisv1alpha1.APIResourceSchema, error)
	getCRD               func(clusterName, name string) (*apiextensionsv1.CustomResourceDefinition, error)
	createCRD            func(ctx context.Context, crd *apiextensionsv1.CustomResourceDefinition) (*apiextensionsv1.CustomResourceDefinition, error)
	updateCRD            func(ctx context.Context, crd *apiextensionsv1.CustomResourceDefinition) (*apiextensionsv1.CustomResourceDefinition, error)

	deletedCRDTracker *lockedStringSet
}

func (r *workspaceAPIExportReferenceReconciler) reconcile(ctx context.Context, apiBinding *apisv1alpha1.APIBinding) (reconcileStatus, error) {
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
		return reconcileStatusStop, nil
	}

	apiExportClusterName := getAPIExportClusterName(apiBinding)
	if apiExportClusterName == "" {
		// this should not happen because of OpenAPI
		conditions.MarkFalse(
			apiBinding,
			apisv1alpha1.APIExportValid,
			apisv1alpha1.APIExportInvalidReferenceReason,
			conditionsv1alpha1.ConditionSeverityError,
			"Missing APIExport cluster name",
		)
		return reconcileStatusStop, nil
	}

	apiExport, err := r.getAPIExport(apiExportClusterName, workspaceRef.ExportName)
	if err != nil && apierrors.IsNotFound(err) {
		conditions.MarkFalse(
			apiBinding,
			apisv1alpha1.APIExportValid,
			apisv1alpha1.APIExportNotFoundReason,
			conditionsv1alpha1.ConditionSeverityError,
			"APIExport not found",
		)
		return reconcileStatusStop, nil // don't retry, only when APIExport shows up with new event
	} else if err != nil {
		conditions.MarkUnknown(
			apiBinding,
			apisv1alpha1.APIExportValid,
			apisv1alpha1.GetErrorReason,
			"error getting APIExport: %v",
			err,
		)
		return reconcileStatusStop, err // temporary error, retry
	}

	var boundResources []apisv1alpha1.BoundAPIResource
	needToWaitForRequeue := false

	for _, schemaName := range apiExport.Spec.LatestResourceSchemas {
		schema, err := r.getAPIResourceSchema(apiExportClusterName, schemaName)
		if err != nil && apierrors.IsNotFound(err) {
			klog.Errorf("APIResourceSchema %s|%s not found", apiExportClusterName, schemaName)
			// TODO(ncdc): not sure if we should expose this level of detail to the binding user?
			conditions.MarkFalse(
				apiBinding,
				apisv1alpha1.APIExportValid,
				apisv1alpha1.APIExportNotFoundReason,
				conditionsv1alpha1.ConditionSeverityError,
				"Associated APIResourceSchema %s|%s not found: %v",
				apiExportClusterName,
				schemaName,
				err,
			)

			return reconcileStatusStop, nil // don't retry
		} else if err != nil {
			klog.Errorf("Error getting APIResourceSchema %s|%s", apiExportClusterName, schemaName)
			// TODO(ncdc): not sure if we should expose this level of detail to the binding user?
			conditions.MarkFalse(
				apiBinding,
				apisv1alpha1.APIExportValid,
				apisv1alpha1.GetErrorReason,
				conditionsv1alpha1.ConditionSeverityError,
				"Error getting associated APIResourceSchema %s|%s: %v",
				apiExportClusterName,
				schemaName,
				err,
			)

			return reconcileStatusStop, err // temporary error, retry
		}

		crd, err := crdFromAPIResourceSchema(schema)
		if err != nil {
			// TODO(ncdc): not sure if we should expose this level of detail to the binding user?
			conditions.MarkFalse(
				apiBinding,
				apisv1alpha1.APIExportValid,
				apisv1alpha1.InvalidSchemaReason,
				conditionsv1alpha1.ConditionSeverityError,
				"error converting APIResourceSchema %s|%s to a CRD: %v",
				schema.ClusterName,
				schema.Name,
				err,
			)

			return reconcileStatusStop, nil // don't retry
		}

		existingCRD, err := r.getCRD(shadowWorkspaceName, crd.Name)
		if err != nil && !apierrors.IsNotFound(err) {
			// TODO(ncdc): do we want to consider this a hard error? Or should we proceed to try to create if
			// we're unable to do the get?
			conditions.MarkFalse(
				apiBinding,
				apisv1alpha1.CRDReady,
				apisv1alpha1.GetErrorReason,
				conditionsv1alpha1.ConditionSeverityError,
				"error getting bound CRD %s: %v",
				crd.Name,
				err,
			)

			return reconcileStatusStop, err // temporary error, retry
		}

		// If the deletedCRDTracker has this CRD in it, it means it was deleted. There is a chance that the cache
		// still has the CRD in it, and existingCRD is populated instead of being nil. If that's the case, forcibly
		// set existingCRD to nil so we know we need to (re)create the CRD.
		if r.deletedCRDTracker.Has(crd.Name) {
			klog.Infof("Bound CRD %s|%s was deleted - need to recreate", shadowWorkspaceName, crd.Name)
			existingCRD = nil
		}

		if existingCRD == nil {
			// Create flow
			if _, err := r.createCRD(ctx, crd); err != nil {
				if !apierrors.IsAlreadyExists(err) {
					conditions.MarkFalse(
						apiBinding,
						apisv1alpha1.CRDReady,
						apisv1alpha1.CreateErrorReason,
						conditionsv1alpha1.ConditionSeverityError,
						"error creating CRD %s: %v",
						crd.Name,
						err,
					)

					// Only return for non already-exists errors
					return reconcileStatusStop, err // temporary error, retry
				}

				// If we got an already-exists error, proceed below to set condition
			}

			r.deletedCRDTracker.Remove(crd.Name)

			conditions.MarkFalse(
				apiBinding,
				apisv1alpha1.CRDReady,
				apisv1alpha1.WaitingForEstablishedReason,
				conditionsv1alpha1.ConditionSeverityInfo,
				"waiting for CRD to be established",
			)

			needToWaitForRequeue = true
		} else {
			// We never set Conversion, so nil it out before comparing
			existingCRD = existingCRD.DeepCopy()
			existingCRD.Spec.Conversion = nil

			if !reflect.DeepEqual(existingCRD.Spec, crd.Spec) {
				klog.Infof("CRD diff: %s", cmp.Diff(existingCRD.Spec, crd.Spec))
				crd.ResourceVersion = existingCRD.ResourceVersion

				if _, err := r.updateCRD(ctx, crd); err != nil {
					conditions.MarkFalse(
						apiBinding,
						apisv1alpha1.CRDReady,
						apisv1alpha1.UpdateErrorReason,
						conditionsv1alpha1.ConditionSeverityWarning,
						"error updating CRD %s to latest schema: %v",
						crd.Name,
						err,
					)
					return reconcileStatusStop, err // retry
				}

				needToWaitForRequeue = true
			} else {
				if !apihelpers.IsCRDConditionTrue(existingCRD, apiextensionsv1.Established) || apihelpers.IsCRDConditionTrue(existingCRD, apiextensionsv1.Terminating) {
					needToWaitForRequeue = true
				}
			}
		}

		boundResources = append(boundResources, apisv1alpha1.BoundAPIResource{
			Group:    schema.Spec.Group,
			Resource: schema.Spec.Names.Plural,
			Schema: apisv1alpha1.BoundAPIResourceSchema{
				Name: schema.Name,
				UID:  string(schema.UID),
			},
			StorageVersions: crd.Status.StoredVersions,
		})
	}

	conditions.MarkTrue(apiBinding, apisv1alpha1.APIExportValid)

	if needToWaitForRequeue {
		return reconcileStatusStop, nil
	}

	conditions.MarkTrue(apiBinding, apisv1alpha1.CRDReady)
	apiBinding.Status.Initializers = []string{}
	apiBinding.Status.Phase = apisv1alpha1.APIBindingPhaseBound
	apiBinding.Status.BoundAPIExport = &apiBinding.Spec.Reference
	apiBinding.Status.BoundResources = boundResources

	return reconcileStatusContinue, nil
}

func (c *controller) reconcile(ctx context.Context, apiBinding *apisv1alpha1.APIBinding) error {
	reconcilers := []reconciler{
		&phaseReconciler{
			getAPIExport:         c.getAPIExport,
			getAPIResourceSchema: c.getAPIResourceSchema,
		},
		&workspaceAPIExportReferenceReconciler{
			getAPIExport:         c.getAPIExport,
			getAPIResourceSchema: c.getAPIResourceSchema,
			getCRD:               c.getCRD,
			createCRD:            c.createCRD,
			updateCRD:            c.updateCRD,
			deletedCRDTracker:    c.deletedCRDTracker,
		},
	}

	var errs []error

	for _, r := range reconcilers {
		status, err := r.reconcile(ctx, apiBinding)
		if err != nil {
			errs = append(errs, err)
		}
		if status == reconcileStatusStop {
			break
		}
	}

	return errors.NewAggregate(errs)
}

func (c *controller) getAPIExport(clusterName, name string) (*apisv1alpha1.APIExport, error) {
	return c.apiExportsLister.Get(clusters.ToClusterAwareKey(clusterName, name))
}

func (c *controller) getAPIResourceSchema(clusterName, name string) (*apisv1alpha1.APIResourceSchema, error) {
	return c.apiResourceSchemaLister.Get(clusters.ToClusterAwareKey(clusterName, name))
}

func (c *controller) getCRD(clusterName, name string) (*apiextensionsv1.CustomResourceDefinition, error) {
	return c.crdLister.Get(clusters.ToClusterAwareKey(clusterName, name))
}

func (c *controller) createCRD(ctx context.Context, crd *apiextensionsv1.CustomResourceDefinition) (*apiextensionsv1.CustomResourceDefinition, error) {
	return c.crdClusterClient.Cluster(crd.ClusterName).ApiextensionsV1().CustomResourceDefinitions().Create(ctx, crd, metav1.CreateOptions{})
}

func (c *controller) updateCRD(ctx context.Context, crd *apiextensionsv1.CustomResourceDefinition) (*apiextensionsv1.CustomResourceDefinition, error) {
	return c.crdClusterClient.Cluster(crd.ClusterName).ApiextensionsV1().CustomResourceDefinitions().Update(ctx, crd, metav1.UpdateOptions{})
}

func crdFromAPIResourceSchema(schema *apisv1alpha1.APIResourceSchema) (*apiextensionsv1.CustomResourceDefinition, error) {
	crd := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			ClusterName: shadowWorkspaceName,
			Name:        string(schema.UID),
			Annotations: map[string]string{
				annotationBoundCRDKey:      "",
				annotationSchemaClusterKey: schema.ClusterName,
				annotationSchemaNameKey:    schema.Name,
			},
		},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: schema.Spec.Group,
			Names: schema.Spec.Names,
			Scope: schema.Spec.Scope,
		},
	}

	for _, version := range schema.Spec.Versions {
		crdVersion := apiextensionsv1.CustomResourceDefinitionVersion{
			Name:                     version.Name,
			Served:                   version.Served,
			Storage:                  version.Storage,
			Deprecated:               version.Deprecated,
			DeprecationWarning:       version.DeprecationWarning,
			Subresources:             version.Subresources,
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

// lockedStringSet guards a sets.String with an RWMutex.
type lockedStringSet struct {
	lock sync.RWMutex
	s    sets.String
}

func newLockedStringSet(s ...string) *lockedStringSet {
	return &lockedStringSet{
		s: sets.NewString(s...),
	}
}

func (l *lockedStringSet) Add(s string) {
	l.lock.Lock()
	l.s.Insert(s)
	l.lock.Unlock()
}

func (l *lockedStringSet) Remove(s string) {
	l.lock.Lock()
	l.s.Delete(s)
	l.lock.Unlock()
}

func (l *lockedStringSet) Has(s string) bool {
	l.lock.RLock()
	defer l.lock.RUnlock()
	return l.s.Has(s)
}
