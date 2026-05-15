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
	conditionsv1alpha1 "github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"

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

	// Record the export's permission claims
	apiBinding.Status.ExportPermissionClaims = apiExport.Spec.PermissionClaims

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

	// Collect the schemas.
	schemas := make(map[string]*apisv1alpha1.APIResourceSchema)
	grs := sets.New[schema.GroupResource]()
	for _, resourceSchema := range apiExport.Spec.Resources {
		sch, err := r.getAPIResourceSchema(logicalcluster.From(apiExport), resourceSchema.Schema)
		if err != nil {
			logger.Error(err, "error binding")

			conditions.MarkFalse(
				apiBinding,
				apisv1alpha2.APIExportValid,
				apisv1alpha2.InternalErrorReason,
				conditionsv1alpha1.ConditionSeverityError,
				"Invalid APIExport. Please contact the APIExport owner to resolve: APIResourceSchema %q not found", resourceSchema.Schema,
			)

			if apierrors.IsNotFound(err) {
				return reconcileStatusContinue, nil
			}

			return reconcileStatusContinue, err
		}
		schemas[resourceSchema.Schema] = sch
		grs = grs.Insert(schema.GroupResource{Group: sch.Spec.Group, Resource: sch.Spec.Names.Plural})
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
		// Read current bindings annotation to check if we need to update
		bindingsBefore := lc.Annotations[LocksBindingsAnnotationKey]
		lc, _, skipped, err = WithLockedResourcesForBindings(crds, time.Now(), lc, grs.UnsortedList(), ExpirableLock{
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

		if lc.Annotations[LocksBindingsAnnotationKey] != bindingsBefore {
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
		sch := schemas[resourceSchema.Schema]
		logger := logging.WithObject(logger, sch)

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
		existingCRD, err := r.getCRD(SystemBoundCRDsClusterName, boundCRDName(sch))
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
				SystemBoundCRDsClusterName, boundCRDName(sch),
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
			// Need to create bound CRD
			crd, err := generateCRD(sch)
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
		}

		found := false
		for i, r := range apiBinding.Status.BoundResources {
			if r.Group == sch.Spec.Group && r.Resource == sch.Spec.Names.Plural {
				apiBinding.Status.BoundResources[i] = newBoundResource
				found = true
				break
			}
		}
		if !found {
			apiBinding.Status.BoundResources = append(apiBinding.Status.BoundResources, newBoundResource)
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

func boundCRDName(schema *apisv1alpha1.APIResourceSchema) string {
	return string(schema.UID)
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
	name := boundCRDName(sch)

	if _, err := r.getCRD(SystemBoundCRDsClusterName, name); err == nil {
		// Already there. Recreation only happens via the regular Resources path; here
		// we merely need it to exist so the local DDSIF discovers it.
		if !r.deletedCRDTracker.Has(name) {
			return nil
		}
	} else if !apierrors.IsNotFound(err) {
		return err
	}

	crd, err := generateCRD(sch)
	if err != nil {
		return err
	}
	if _, err := r.createCRD(ctx, SystemBoundCRDsClusterName.Path(), crd); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	r.deletedCRDTracker.Remove(name)
	return nil
}

func generateCRD(schema *apisv1alpha1.APIResourceSchema) (*apiextensionsv1.CustomResourceDefinition, error) {
	crd := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: boundCRDName(schema),
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
