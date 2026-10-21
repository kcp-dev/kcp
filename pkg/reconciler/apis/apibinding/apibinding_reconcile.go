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

package apibinding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"k8s.io/apiextensions-apiserver/pkg/apihelpers"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/apiserver"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	migrationv1alpha1 "github.com/kcp-dev/sdk/apis/migration/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"

	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/logging"
)

type reconcileStatus int

const (
	reconcileStatusStopAndRequeue reconcileStatus = iota
	reconcileStatusContinue
)

type reconciler interface {
	reconcile(ctx context.Context, this *apisv1alpha2.APIBinding) (reconcileStatus, error)
}

func (c *controller) reconcile(ctx context.Context, apiBinding *apisv1alpha2.APIBinding) (bool, error) {
	reconcilers := []reconciler{
		// Runs first so that an annotation-only change can preempt the status changes from
		// bindingReconciler.
		&permissionClaimMaterialiserReconciler{controller: c},
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

	return requeue, utilerrors.NewAggregate(errs)
}

type summaryReconciler struct {
	*controller
}

func (r *summaryReconciler) reconcile(ctx context.Context, apiBinding *apisv1alpha2.APIBinding) (reconcileStatus, error) {
	// The only condition that reflects if the APIBinding is Ready is InitialBindingCompleted. Other conditions
	// (e.g. APIExportValid) may revert to false after the initial binding has completed, but those must not affect
	// the readiness.
	conditions.SetSummary(
		apiBinding,
		conditions.WithConditions(
			apisv1alpha2.InitialBindingCompleted,
		),
	)

	return reconcileStatusContinue, nil
}

type phaseReconciler struct {
	newReconciler     reconciler
	bindingReconciler reconciler
}

func (r *phaseReconciler) reconcile(ctx context.Context, apiBinding *apisv1alpha2.APIBinding) (reconcileStatus, error) {
	switch apiBinding.Status.Phase {
	case "":
		return r.newReconciler.reconcile(ctx, apiBinding)
	case apisv1alpha2.APIBindingPhaseBinding, apisv1alpha2.APIBindingPhaseBound:
		return r.bindingReconciler.reconcile(ctx, apiBinding)
	}

	// should never happen
	return reconcileStatusContinue, nil
}

type newReconciler struct {
	*controller
}

func (r *newReconciler) reconcile(ctx context.Context, apiBinding *apisv1alpha2.APIBinding) (reconcileStatus, error) {
	apiBinding.Status.Phase = apisv1alpha2.APIBindingPhaseBinding

	conditions.MarkFalse(
		apiBinding,
		apisv1alpha2.InitialBindingCompleted,
		apisv1alpha2.WaitingForEstablishedReason,
		conditionsv1alpha1.ConditionSeverityInfo,
		"Waiting for API(s) to be established",
	)

	return reconcileStatusStopAndRequeue, nil
}

type bindingReconciler struct {
	*controller
}

func (r *bindingReconciler) reconcile(ctx context.Context, apiBinding *apisv1alpha2.APIBinding) (reconcileStatus, error) {
	logger := klog.FromContext(ctx)

	// Check for valid APIExport reference
	workspaceRef := apiBinding.Spec.Reference.Export
	if workspaceRef == nil {
		// this should not happen because of validation.
		conditions.MarkFalse(
			apiBinding,
			apisv1alpha2.APIExportValid,
			apisv1alpha2.APIExportInvalidReferenceReason,
			conditionsv1alpha1.ConditionSeverityError,
			"Missing APIExport reference",
		)
		return reconcileStatusContinue, nil
	}
	apiExportPath := logicalcluster.NewPath(apiBinding.Spec.Reference.Export.Path)
	if apiExportPath.Empty() {
		apiExportPath = logicalcluster.From(apiBinding).Path()
	}
	apiExport, err := r.controller.getAPIExportByPath(apiExportPath, workspaceRef.Name)
	if apierrors.IsNotFound(err) {
		conditions.MarkFalse(
			apiBinding,
			apisv1alpha2.APIExportValid,
			apisv1alpha2.APIExportNotFoundReason,
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
			apisv1alpha2.APIExportValid,
			apisv1alpha2.InternalErrorReason,
			conditionsv1alpha1.ConditionSeverityError,
			"Error getting APIExport %s|%s: %v",
			apiExportPath,
			workspaceRef.Name,
			err,
		)
		return reconcileStatusContinue, err
	}
	logger = logging.WithObject(logger, apiExport)

	// Record the export's permission claims, with the identities kcp
	// resolved for claims that name an export or rely on a grant.
	apiBinding.Status.ExportPermissionClaims = apiExport.ResolvedPermissionClaims()

	// Make sure the APIExport has an identity
	if apiExport.Status.IdentityHash == "" {
		conditions.MarkFalse(
			apiBinding,
			apisv1alpha2.APIExportValid,
			"MissingIdentityHash",
			conditionsv1alpha1.ConditionSeverityWarning,
			"APIExport %s|%s is missing status.identityHash",
			apiExportPath,
			workspaceRef.Name,
		)
		return reconcileStatusContinue, nil
	}

	// Record the APIExport's host cluster name for lookup in webhooks.
	// The full path is unreliable for this purpose.
	apiBinding.Status.APIExportClusterName = logicalcluster.From(apiExport).String()

	// Resolve the schemas and default storage keys. A resource with crd
	// storage is served with the export's own schema and stored under the
	// export's identity. A resource with adopted storage is served with the
	// source export's schema and starts on the source's storage prefix,
	// provided the source consents (crd.sharedWith).
	resolved := make(map[schema.GroupResource]resolvedResource)
	grs := sets.New[schema.GroupResource]()
	for _, resourceSchema := range apiExport.Spec.Resources {
		res, err := r.resolveResource(apiExport, resourceSchema)
		if err != nil {
			logger.Error(err, "error binding")

			reason := apisv1alpha2.InternalErrorReason
			var invalid *invalidResourceError
			if errors.As(err, &invalid) {
				reason = invalid.reason
			}
			conditions.MarkFalse(
				apiBinding,
				apisv1alpha2.APIExportValid,
				reason,
				conditionsv1alpha1.ConditionSeverityError,
				"Invalid APIExport. Please contact the APIExport owner to resolve: %v", err,
			)

			if apierrors.IsNotFound(err) || invalid != nil {
				return reconcileStatusContinue, nil //nolint:nilerr // reported on the binding; a retry cannot fix it
			}

			return reconcileStatusContinue, err
		}
		// keyed by the export's resource entry; the lock set uses the schema's
		// group and plural, which is what is served.
		resolved[schema.GroupResource{Group: resourceSchema.Group, Resource: resourceSchema.Name}] = res
		grs = grs.Insert(schema.GroupResource{Group: res.schema.Spec.Group, Resource: res.schema.Spec.Names.Plural})
	}

	crds, err := r.listCRDs(logicalcluster.From(apiBinding))
	if err != nil {
		return reconcileStatusContinue, err
	}

	var skipped map[schema.GroupResource]Lock
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// Look up LogicalCluster which acts as our lock to avoid having multiple bindings
		// and/or CRDs owning the same resource.
		lc, err := r.getLogicalCluster(logicalcluster.From(apiBinding))
		if err != nil {
			conditions.MarkFalse(
				apiBinding,
				apisv1alpha2.BindingUpToDate,
				apisv1alpha2.LogicalClusterNotFoundReason,
				conditionsv1alpha1.ConditionSeverityError,
				"Unable to bind APIs: %v",
				err,
			)

			// Only change InitialBindingCompleted if it's false.
			if conditions.IsFalse(apiBinding, apisv1alpha2.InitialBindingCompleted) {
				conditions.MarkFalse(
					apiBinding,
					apisv1alpha2.InitialBindingCompleted,
					apisv1alpha2.LogicalClusterNotFoundReason,
					conditionsv1alpha1.ConditionSeverityError,
					"Unable to bind APIs: %v",
					err,
				)
			}

			// wait until it shows up. Bindings don't work without.
			return err
		}

		// Lock resources before going any further. Not being able to lock all resources is NOT an error.
		// It will be reflected in the BindingUpToDate and InitialBindingCompleted conditions.
		// TODO(sttts): removed schemas never get unlocked. We need a distinguishable way
		//              for intentional removal of schemas, versus movement of schemas
		//              to another APIExport.
		previousResourceBindings := lc.Annotations[ResourceBindingsAnnotationKey]
		lc, _, skipped, err = WithLockedResources(crds, time.Now(), lc, grs.UnsortedList(), ExpirableLock{
			Lock: Lock{Name: apiBinding.Name},
		})
		if err != nil {
			logger.Error(err, "error locking resources", "skipped", skipped)

			conditions.MarkFalse(
				apiBinding,
				apisv1alpha2.BindingUpToDate,
				apisv1alpha2.NamingConflictsReason,
				conditionsv1alpha1.ConditionSeverityError,
				"Unable to bind APIs: %v",
				err,
			)

			// Only change InitialBindingCompleted if it's false.
			if conditions.IsFalse(apiBinding, apisv1alpha2.InitialBindingCompleted) {
				conditions.MarkFalse(
					apiBinding,
					apisv1alpha2.InitialBindingCompleted,
					apisv1alpha2.NamingConflictsReason,
					conditionsv1alpha1.ConditionSeverityError,
					"Unable to bind APIs: %v",
					err,
				)
			}

			return err
		}

		if lc.Annotations[ResourceBindingsAnnotationKey] != previousResourceBindings {
			if err := r.updateLogicalCluster(ctx, lc); err != nil {
				return err
			}
		}

		if len(skipped) > 0 {
			logger.V(4).Info("skipped resources", "resources", skipped)
		}

		return nil
	}); err != nil {
		logger.Error(err, "error updating LogicalCluster")
		return reconcileStatusContinue, err
	}

	checker, err := newConflictChecker(logicalcluster.From(apiBinding), r.listAPIBindings, r.getAPIResourceSchema, r.getCRD, r.listCRDs)
	if err != nil {
		return reconcileStatusContinue, err
	}
	var needToWaitForRequeueWhenEstablished []string
	for _, resourceSchema := range apiExport.Spec.Resources {
		res, ok := resolved[schema.GroupResource{Group: resourceSchema.Group, Resource: resourceSchema.Name}]
		if !ok {
			continue
		}
		sch := res.schema
		logger := logging.WithObject(logger, sch)

		// The storage key of the bound resource: what the binding already
		// uses, or the resolved default for a new binding. It never flips in
		// place; only the storage migrator moves data.
		storageKey := res.storageKey
		for _, br := range apiBinding.Status.BoundResources {
			if br.Group == sch.Spec.Group && br.Resource == sch.Spec.Names.Plural {
				if key := br.StorageKey(); key != "" {
					storageKey = key
				}
				break
			}
		}
		boundCRDName := indexers.BoundCRDNameForKey(string(sch.UID), apiExport.Status.IdentityHash, storageKey)

		if _, ok := skipped[schema.GroupResource{Group: sch.Spec.Group, Resource: sch.Spec.Names.Plural}]; ok {
			// This resource was skipped because it's already locked by another binding.
			continue
		}

		// A resource will be served if the group resource is locked by this binding AND there are no
		// naming conflicts with other bindings or CRDs. The former is critical, the latter is advisory.
		if err := checker.Check(apiBinding, sch); err != nil {
			conditions.MarkFalse(
				apiBinding,
				apisv1alpha2.BindingUpToDate,
				apisv1alpha2.NamingConflictsReason,
				conditionsv1alpha1.ConditionSeverityError,
				"Unable to bind APIs: %v",
				err,
			)

			// Only change InitialBindingCompleted if it's false.
			if conditions.IsFalse(apiBinding, apisv1alpha2.InitialBindingCompleted) {
				conditions.MarkFalse(
					apiBinding,
					apisv1alpha2.InitialBindingCompleted,
					apisv1alpha2.NamingConflictsReason,
					conditionsv1alpha1.ConditionSeverityError,
					"Unable to bind APIs: %v",
					err,
				)
			}
			return reconcileStatusStopAndRequeue, err
		}

		// If there are multiple versions, the conversion strategy must be defined in the APIResourceSchema
		if len(sch.Spec.Versions) > 1 && sch.Spec.Conversion == nil {
			conditions.MarkFalse(
				apiBinding,
				apisv1alpha2.APIExportValid,
				apisv1alpha2.InternalErrorReason,
				conditionsv1alpha1.ConditionSeverityError,
				"Invalid APIExport. Please contact the APIExport owner to resolve",
			)

			return reconcileStatusContinue, fmt.Errorf(
				"conversion strategy not specified %s|%s for APIBinding %s|%s, APIExport %s|%s, APIResourceSchema %s|%s: %w",
				apiExportPath, resourceSchema.Schema,
				logicalcluster.From(apiBinding), apiBinding.Name,
				apiExportPath, apiExport.Name,
				apiExportPath, resourceSchema.Schema,
				err,
			)
		}

		// Try to get the bound CRD
		existingCRD, err := r.getCRD(SystemBoundCRDsClusterName, boundCRDName)
		if err != nil && !apierrors.IsNotFound(err) {
			conditions.MarkFalse(
				apiBinding,
				apisv1alpha2.APIExportValid,
				apisv1alpha2.InternalErrorReason,
				conditionsv1alpha1.ConditionSeverityError,
				"Invalid APIExport. Please contact the APIExport owner to resolve",
			)

			return reconcileStatusContinue, fmt.Errorf(
				"error getting CRD %s|%s for APIBinding %s|%s, APIExport %s|%s, APIResourceSchema %s|%s: %w",
				SystemBoundCRDsClusterName, boundCRDName,
				logicalcluster.From(apiBinding), apiBinding.Name,
				apiExportPath, apiExport.Name,
				apiExportPath, resourceSchema.Schema,
				err,
			)
		}

		if err == nil {
			// Bound CRD already exists
			if !apihelpers.IsCRDConditionTrue(existingCRD, apiextensionsv1.Established) {
				logger.V(4).Info("CRD is not established", "conditions", fmt.Sprintf("%#v", existingCRD.Status.Conditions))
				needToWaitForRequeueWhenEstablished = append(needToWaitForRequeueWhenEstablished, resourceSchema.Schema)
				continue
			} else if apihelpers.IsCRDConditionTrue(existingCRD, apiextensionsv1.Terminating) {
				logger.V(4).Info("CRD is terminating")
				needToWaitForRequeueWhenEstablished = append(needToWaitForRequeueWhenEstablished, resourceSchema.Schema)
				continue
			}
		} else {
			// While the binding is mid storage re-keying the storage migrator
			// owns the bound CRD lifecycle: it recreates the CRD to force the
			// serving storage to be rebuilt against the target storage key.
			// Creating it here would race that rebuild.
			if conditions.IsFalse(apiBinding, apisv1alpha2.StorageRekeyCompleted) {
				logger.V(4).Info("not creating bound CRD: binding is mid storage re-keying", "schema", resourceSchema.Schema)
				needToWaitForRequeueWhenEstablished = append(needToWaitForRequeueWhenEstablished, resourceSchema.Schema)
				continue
			}

			// Need to create bound CRD
			crd, err := GenerateBoundCRDNamed(sch, boundCRDName)
			if err != nil {
				logger.Error(err, "error generating CRD")

				conditions.MarkFalse(
					apiBinding,
					apisv1alpha2.APIExportValid,
					apisv1alpha2.InternalErrorReason,
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
			if _, err := r.createCRD(ctx, SystemBoundCRDsClusterName.Path(), crd); err != nil {
				schemaClusterName := logicalcluster.From(sch)
				if apierrors.IsInvalid(err) {
					status := apierrors.APIStatus(nil)
					// The error is guaranteed to implement APIStatus here
					errors.As(err, &status)
					conditions.MarkFalse(
						apiBinding,
						apisv1alpha2.BindingUpToDate,
						apisv1alpha2.APIResourceSchemaInvalidReason,
						conditionsv1alpha1.ConditionSeverityError,
						"APIResourceSchema %s|%s is invalid: %v",
						schemaClusterName, resourceSchema.Schema, status.Status().Details.Causes,
					)
					// Only change InitialBindingCompleted if it's false
					if conditions.IsFalse(apiBinding, apisv1alpha2.InitialBindingCompleted) {
						conditions.MarkFalse(
							apiBinding,
							apisv1alpha2.InitialBindingCompleted,
							apisv1alpha2.APIResourceSchemaInvalidReason,
							conditionsv1alpha1.ConditionSeverityError,
							"APIResourceSchema %s|%s is invalid: %v",
							schemaClusterName, resourceSchema.Schema, status.Status().Details.Causes,
						)
					}

					logger.Error(err, "error creating CRD")

					return reconcileStatusContinue, nil
				}

				conditions.MarkFalse(
					apiBinding,
					apisv1alpha2.BindingUpToDate,
					apisv1alpha2.InternalErrorReason,
					conditionsv1alpha1.ConditionSeverityError,
					"An internal error prevented the APIBinding process from completing. Please contact your system administrator for assistance",
				)
				// Only change InitialBindingCompleted if it's false
				if conditions.IsFalse(apiBinding, apisv1alpha2.InitialBindingCompleted) {
					conditions.MarkFalse(
						apiBinding,
						apisv1alpha2.InitialBindingCompleted,
						apisv1alpha2.InternalErrorReason,
						conditionsv1alpha1.ConditionSeverityError,
						"An internal error prevented the APIBinding process from completing. Please contact your system administrator for assistance",
					)
				}

				return reconcileStatusContinue, err
			}

			r.deletedCRDTracker.Remove(crd.Name)

			needToWaitForRequeueWhenEstablished = append(needToWaitForRequeueWhenEstablished, resourceSchema.Schema)
			continue
		}

		// Merge any current storage versions with new ones
		storageVersions := sets.New[string]()
		if resourceSchema.Storage.CRD != nil {
			// Only resources with CRD storage need to track storage versions.

			if existingCRD != nil {
				storageVersions.Insert(existingCRD.Status.StoredVersions...)
			}

			for _, b := range apiBinding.Status.BoundResources {
				if b.Group == sch.Spec.Group && b.Resource == sch.Spec.Names.Plural {
					storageVersions.Insert(b.StorageVersions...)
					break
				}
			}
		}

		sortedStorageVersions := sets.List[string](storageVersions)
		sort.Strings(sortedStorageVersions)

		// Upsert the BoundAPIResource for this APIResourceSchema
		newBoundResource := apisv1alpha2.BoundAPIResource{
			Group:    sch.Spec.Group,
			Resource: sch.Spec.Names.Plural,
			Schema: apisv1alpha2.BoundAPIResourceSchema{
				Name:         sch.Name,
				UID:          string(sch.UID),
				IdentityHash: apiExport.Status.IdentityHash,
			},
			StorageVersions: sortedStorageVersions,
			// a fresh binding stores under the resolved default: the export's
			// identity, or the source's identity for an adopted resource.
			Storage: apisv1alpha2.BoundAPIResourceStorage{Key: storageKey},
		}

		index := -1
		for i, r := range apiBinding.Status.BoundResources {
			if r.Group == sch.Spec.Group && r.Resource == sch.Spec.Names.Plural {
				// The storage key keys the etcd prefix of the bound instances
				// and must never flip in place: existing data lives under the
				// current prefix. Keep serving it and preserve the drain
				// bookkeeping; only the storage migrator moves data, flips
				// and prunes. A binding created before storage keys were
				// tracked served from the identity it was bound with.
				newBoundResource.Storage = r.Storage
				if newBoundResource.Storage.Key == "" {
					newBoundResource.Storage.Key = r.StorageKey()
				}
				if newBoundResource.Storage.Key == "" {
					newBoundResource.Storage.Key = storageKey
				}
				apiBinding.Status.BoundResources[i] = newBoundResource
				index = i
				break
			}
		}
		if index < 0 {
			apiBinding.Status.BoundResources = append(apiBinding.Status.BoundResources, newBoundResource)
			index = len(apiBinding.Status.BoundResources) - 1
		}

		// An active storage re-keying of the export names the prefix the
		// bound instances have to move to. Record it as a pending key; the
		// storage migrator drains the current prefix onto it, flips and
		// prunes.
		if active, ok := migrationv1alpha1.ParseActiveRekey(apiExport.Annotations[migrationv1alpha1.ActiveRekeyAnnotationKey]); ok && active.Covers(sch.Spec.Group, sch.Spec.Names.Plural) {
			br := &apiBinding.Status.BoundResources[index]
			if br.StorageKey() != active.TargetKey {
				br.Storage.PendingKeys = sets.List(sets.New(br.Storage.PendingKeys...).Insert(active.TargetKey))
			}
			if len(br.Storage.PendingKeys) > 0 {
				conditions.MarkFalse(
					apiBinding,
					apisv1alpha2.StorageRekeyCompleted,
					apisv1alpha2.StorageRekeyInProgressReason,
					conditionsv1alpha1.ConditionSeverityInfo,
					"bound resources are being drained onto storage key %s of APIExport %s",
					active.TargetKey, apiExport.Name,
				)
			}
		}
	}

	conditions.MarkTrue(apiBinding, apisv1alpha2.APIExportValid)

	if len(needToWaitForRequeueWhenEstablished) > 0 {
		sort.Strings(needToWaitForRequeueWhenEstablished)

		conditions.MarkFalse(
			apiBinding,
			apisv1alpha2.BindingUpToDate,
			apisv1alpha2.WaitingForEstablishedReason,
			conditionsv1alpha1.ConditionSeverityInfo,
			"Waiting for API(s) to be established: %s",
			strings.Join(needToWaitForRequeueWhenEstablished, ", "),
		)

		// Only change InitialBindingCompleted if it's false
		if conditions.IsFalse(apiBinding, apisv1alpha2.InitialBindingCompleted) {
			conditions.MarkFalse(
				apiBinding,
				apisv1alpha2.InitialBindingCompleted,
				apisv1alpha2.WaitingForEstablishedReason,
				conditionsv1alpha1.ConditionSeverityInfo,
				"Waiting for API(s) to be established: %s",
				strings.Join(needToWaitForRequeueWhenEstablished, ", "),
			)
		}
	} else if len(skipped) > 0 {
		conditions.MarkFalse(
			apiBinding,
			apisv1alpha2.BindingUpToDate,
			apisv1alpha2.NamingConflictsReason,
			conditionsv1alpha1.ConditionSeverityError,
			"Unable to bind APIs because they are bound by other APIBindings or CRDs: %v", skipped,
		)

		// Only change InitialBindingCompleted if it's false
		if conditions.IsFalse(apiBinding, apisv1alpha2.InitialBindingCompleted) {
			conditions.MarkFalse(
				apiBinding,
				apisv1alpha2.InitialBindingCompleted,
				apisv1alpha2.NamingConflictsReason,
				conditionsv1alpha1.ConditionSeverityError,
				"Unable to bind APIs because they are bound by other APIBindings or CRDs: %v", skipped,
			)
		}
	} else {
		conditions.MarkTrue(apiBinding, apisv1alpha2.InitialBindingCompleted)
		conditions.MarkTrue(apiBinding, apisv1alpha2.BindingUpToDate)
		apiBinding.Status.Phase = apisv1alpha2.APIBindingPhaseBound
	}

	return reconcileStatusContinue, nil
}

// BoundCRDName returns the name used for a bound CRD in system:bound-crds
// serving a resource stored under its export's identity. Resources on another
// storage key use indexers.BoundCRDNameForKey.
func BoundCRDName(schema *apisv1alpha1.APIResourceSchema) string {
	return string(schema.UID)
}

// resolvedResource is an export resource with its schema and the storage key
// a new binding of it starts on.
type resolvedResource struct {
	schema     *apisv1alpha1.APIResourceSchema
	storageKey string
}

// invalidResourceError is a permanent misconfiguration of an export resource,
// reported on the binding with the given reason and not retried.
type invalidResourceError struct {
	reason string
	err    error
}

func (e *invalidResourceError) Error() string { return e.err.Error() }
func (e *invalidResourceError) Unwrap() error { return e.err }

// resolveResource resolves the schema and default storage key of one export
// resource. For crd (and virtual) storage the schema is the one the export
// references in its own workspace and the key is the export's identity. For
// adopted storage the source export is looked up, its consent checked
// (crd.sharedWith must list this export for the resource), and the resource is
// served with the source's schema from the source's storage prefix.
func (r *bindingReconciler) resolveResource(apiExport *apisv1alpha2.APIExport, resourceSchema apisv1alpha2.ResourceSchema) (resolvedResource, error) {
	exportCluster := logicalcluster.From(apiExport)
	if resourceSchema.Storage.Adopted == nil {
		sch, err := r.getAPIResourceSchema(exportCluster, resourceSchema.Schema)
		if err != nil {
			return resolvedResource{}, fmt.Errorf("APIResourceSchema %q: %w", resourceSchema.Schema, err)
		}
		return resolvedResource{schema: sch, storageKey: apiExport.Status.IdentityHash}, nil
	}

	from := resourceSchema.Storage.Adopted.From
	sourcePath := logicalcluster.NewPath(from.Path)
	if sourcePath.Empty() {
		sourcePath = exportCluster.Path()
	}
	source, err := r.controller.getAPIExportByPath(sourcePath, from.Name)
	if err != nil {
		return resolvedResource{}, &invalidResourceError{reason: apisv1alpha2.StorageSourceInvalidReason,
			err: fmt.Errorf("adopted storage of %s.%s: source APIExport %s|%s: %w", resourceSchema.Name, resourceSchema.Group, sourcePath, from.Name, err)}
	}
	if source.Status.IdentityHash == "" {
		return resolvedResource{}, &invalidResourceError{reason: apisv1alpha2.StorageSourceInvalidReason,
			err: fmt.Errorf("adopted storage of %s.%s: source APIExport %s|%s has no identity yet", resourceSchema.Name, resourceSchema.Group, sourcePath, from.Name)}
	}
	var sourceResource *apisv1alpha2.ResourceSchema
	for i := range source.Spec.Resources {
		if source.Spec.Resources[i].Group == resourceSchema.Group && source.Spec.Resources[i].Name == resourceSchema.Name {
			sourceResource = &source.Spec.Resources[i]
			break
		}
	}
	if sourceResource == nil || sourceResource.Storage.CRD == nil {
		return resolvedResource{}, &invalidResourceError{reason: apisv1alpha2.StorageSourceInvalidReason,
			err: fmt.Errorf("adopted storage of %s.%s: source APIExport %s|%s does not serve it with crd storage", resourceSchema.Name, resourceSchema.Group, sourcePath, from.Name)}
	}
	if !SharedWith(sourceResource.Storage.CRD.SharedWith, apiExport) {
		return resolvedResource{}, &invalidResourceError{reason: apisv1alpha2.StorageNotSharedReason,
			err: fmt.Errorf("adopted storage of %s.%s: source APIExport %s|%s does not list this export in crd.sharedWith", resourceSchema.Name, resourceSchema.Group, sourcePath, from.Name)}
	}
	schemaName := sourceResource.Schema
	if resourceSchema.Schema != "" && resourceSchema.Schema != schemaName {
		return resolvedResource{}, &invalidResourceError{reason: apisv1alpha2.StorageSourceInvalidReason,
			err: fmt.Errorf("adopted storage of %s.%s: schema %q must be the source's schema %q or empty", resourceSchema.Name, resourceSchema.Group, resourceSchema.Schema, schemaName)}
	}
	sch, err := r.getAPIResourceSchema(logicalcluster.From(source), schemaName)
	if err != nil {
		return resolvedResource{}, fmt.Errorf("adopted storage of %s.%s: source APIResourceSchema %s|%s: %w", resourceSchema.Name, resourceSchema.Group, logicalcluster.From(source), schemaName, err)
	}
	return resolvedResource{schema: sch, storageKey: source.Status.IdentityHash}, nil
}

// SharedWith reports whether the sharedWith list of a resource names the
// export: by name and by path, where the path may be the export's logical
// cluster name, its canonical workspace path, or empty for the source's own
// workspace (compared against the export's cluster by the caller).
func SharedWith(sharedWith []apisv1alpha2.ExportBindingReference, apiExport *apisv1alpha2.APIExport) bool {
	cluster := logicalcluster.From(apiExport).String()
	canonical := apiExport.Annotations[core.LogicalClusterPathAnnotationKey]
	for _, ref := range sharedWith {
		if ref.Name != apiExport.Name {
			continue
		}
		if ref.Path == cluster || (canonical != "" && ref.Path == canonical) {
			return true
		}
	}
	return false
}

// permissionClaimMaterialiserReconciler ensures that, for each accepted permission claim
// with a non-empty identity hash, the bound CRD for the claimed resource exists in the
// system:bound-crds workspace on this shard. Without this, the permissionclaimlabel
// controller fails with "unable to find informer for <group>.<resource>" on shards where
// no other APIBinding has bound the producing APIExport (issue #4087 for future reference).
//
// This is a pure side effect on a different object (system:bound-crds CRDs); it does not
// mutate the APIBinding. The bound CRD's lifecycle is governed by CRDCleanup, which uses
// indexers.APIBindingByAcceptedClaimIdentityAndGR to discover APIBindings still
// referencing the CRD via permission claims (in addition to the existing BoundResources
// reference path).
type permissionClaimMaterialiserReconciler struct {
	*controller
}

// reconcile materialises bound CRDs for accepted permission claims on a best-effort
// basis. Errors are logged and the loop continues rather than returning, by design:
//
//   - Per-claim independence: a failure resolving one claim must not skip subsequent
//     claims that are perfectly resolvable on this pass.
//   - Cross-shard eventual consistency: the APIExport lookup by IdentityHash goes
//     through the cache-server-fed informer. On a fresh shard the foreign APIExport
//     may simply not have arrived yet; requeuing the APIBinding would hot-loop. The
//     real feedback loop is indexers.IndexAPIBindingByAcceptedClaimedGroupResource,
//     which re-enqueues the APIBinding when the missing APIExport/Schema appears.
//   - Idempotent shared-target writes: ensurePermissionClaimBoundCRD writes into
//     system:bound-crds, which every shard's apibinding controller writes into.
//     AlreadyExists is expected, not a failure to surface on APIBinding status.
//   - Decoupled from APIBinding readiness: this is a side effect on a different
//     object. The BoundResources path gates Ready; failing the whole reconcile here
//     would taint status for an unrelated concern.
//   - Ordering with permissionclaimlabel is event-driven, not synchronously gated:
//     that controller re-triggers on CRD/APIBinding events, so "materialiser must
//     succeed first" is achieved by event ordering, not by error propagation here.
func (r *permissionClaimMaterialiserReconciler) reconcile(ctx context.Context, apiBinding *apisv1alpha2.APIBinding) (reconcileStatus, error) {
	logger := klog.FromContext(ctx)

	for _, claim := range apiBinding.Spec.PermissionClaims {
		if claim.State != apisv1alpha2.ClaimAccepted {
			continue
		}
		// An empty identity hash means the claim targets a built-in or local resource
		// (e.g. core configmaps/secrets) whose informer is always available.
		if claim.IdentityHash == "" {
			continue
		}

		exports, err := r.controller.getAPIExportsByIdentity(claim.IdentityHash)
		if err != nil {
			logger.V(2).Info("error looking up APIExports by identity for permission claim",
				"identity", claim.IdentityHash, "group", claim.Group, "resource", claim.Resource, "err", err.Error())
			continue
		}
		if len(exports) == 0 {
			logger.V(4).Info("no APIExport found for permission claim identity",
				"identity", claim.IdentityHash, "group", claim.Group, "resource", claim.Resource)
			continue
		}

		// Pick a deterministic export. Multiple exports may share the same identity
		// hash (the apiexport reconciler treats this as same-owner same-schema).
		sort.Slice(exports, func(i, j int) bool {
			a, b := exports[i], exports[j]
			ka := logicalcluster.From(a).String() + "/" + a.Name
			kb := logicalcluster.From(b).String() + "/" + b.Name
			return ka < kb
		})

		var producerSchema *apisv1alpha1.APIResourceSchema
		for _, exp := range exports {
			for _, res := range exp.Spec.Resources {
				if res.Group != claim.Group || res.Name != claim.Resource {
					continue
				}
				sch, err := r.getAPIResourceSchema(logicalcluster.From(exp), res.Schema)
				if err != nil {
					continue
				}
				producerSchema = sch
				break
			}
			if producerSchema != nil {
				break
			}
		}
		if producerSchema == nil {
			logger.V(4).Info("no APIResourceSchema found for permission claim",
				"identity", claim.IdentityHash, "group", claim.Group, "resource", claim.Resource)
			continue
		}

		if err := r.ensurePermissionClaimBoundCRD(ctx, producerSchema); err != nil {
			logger.V(2).Info("error ensuring bound CRD for permission claim",
				"schema", producerSchema.Name, "err", err.Error())
			continue
		}
	}

	return reconcileStatusContinue, nil
}

// ensurePermissionClaimBoundCRD creates the bound CRD for the given APIResourceSchema in
// system:bound-crds if it does not already exist. It is idempotent and safe to call from
// any shard's apibinding controller. The CRD is named by the schema UID so that
// concurrent creators converge on the same object.
func (r *permissionClaimMaterialiserReconciler) ensurePermissionClaimBoundCRD(ctx context.Context, sch *apisv1alpha1.APIResourceSchema) error {
	name := BoundCRDName(sch)

	if _, err := r.getCRD(SystemBoundCRDsClusterName, name); err == nil {
		// Already there. Recreation only happens via the regular Resources path; here
		// we merely need it to exist so the local DDSIF discovers it.
		if !r.deletedCRDTracker.Has(name) {
			return nil
		}
	} else if !apierrors.IsNotFound(err) {
		return err
	}

	crd, err := GenerateBoundCRD(sch)
	if err != nil {
		return err
	}
	if _, err := r.createCRD(ctx, SystemBoundCRDsClusterName.Path(), crd); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	r.deletedCRDTracker.Remove(name)
	return nil
}

// GenerateBoundCRD creates a CRD object from an APIResourceSchema for use in
// system:bound-crds, named for a resource stored under its export's identity.
func GenerateBoundCRD(schema *apisv1alpha1.APIResourceSchema) (*apiextensionsv1.CustomResourceDefinition, error) {
	return GenerateBoundCRDNamed(schema, BoundCRDName(schema))
}

// GenerateBoundCRDNamed creates a CRD object from an APIResourceSchema for
// use in system:bound-crds under the given name (see
// indexers.BoundCRDNameForKey for the per-storage-key variants).
func GenerateBoundCRDNamed(schema *apisv1alpha1.APIResourceSchema, name string) (*apiextensionsv1.CustomResourceDefinition, error) {
	crd := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Annotations: map[string]string{
				logicalcluster.AnnotationKey:            SystemBoundCRDsClusterName.String(),
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

	switch schema.Spec.NameValidation {
	case "PathSegmentName":
		crd.Annotations[apiserver.KcpValidateNameAnnotationKey] = "path-segment"
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
			SelectableFields:         version.SelectableFields,
		}

		var validation apiextensionsv1.CustomResourceValidation
		if err := json.Unmarshal(version.Schema.Raw, &validation.OpenAPIV3Schema); err != nil {
			return nil, err
		}
		crdVersion.Schema = &validation

		crd.Spec.Versions = append(crd.Spec.Versions, crdVersion)
	}

	if len(schema.Spec.Versions) > 1 && schema.Spec.Conversion == nil {
		return nil, fmt.Errorf("multiple versions specified for %s but no conversion strategy", schema.Name)
	}

	if len(schema.Spec.Versions) > 1 {
		conversion := &apiextensionsv1.CustomResourceConversion{
			Strategy: apiextensionsv1.ConversionStrategyType(schema.Spec.Conversion.Strategy),
		}

		if schema.Spec.Conversion.Strategy == "Webhook" {
			conversion.Webhook = &apiextensionsv1.WebhookConversion{
				ConversionReviewVersions: schema.Spec.Conversion.Webhook.ConversionReviewVersions,
				ClientConfig: &apiextensionsv1.WebhookClientConfig{
					URL:      &(schema.Spec.Conversion.Webhook.ClientConfig.URL),
					CABundle: schema.Spec.Conversion.Webhook.ClientConfig.CABundle,
				},
			}
		}

		crd.Spec.Conversion = conversion
	}

	return crd, nil
}
