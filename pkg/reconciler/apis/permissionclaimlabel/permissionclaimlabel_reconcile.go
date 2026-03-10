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

package permissionclaimlabel

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/endpoints/handlers"
	"k8s.io/klog/v2"

	kcpkubernetesinformers "github.com/kcp-dev/client-go/informers"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	conditionsv1alpha1 "github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"

	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/permissionclaim"
)

// reconcilePermissionClaims determines the resources that need to be labeled for access by a permission claim.
// It determines what permissions need to be added, what permissions need to be removed.
// It also updates the status if it finds an invalid permission claim.
// Permission claims are considered invalid when the identity hashes are mismatched, and when there is no dynamic informer
// for the group resource.
func (c *controller) reconcile(ctx context.Context, apiBinding *apisv1alpha2.APIBinding) error {
	logger := klog.FromContext(ctx)

	clusterName := logicalcluster.From(apiBinding)

	if apiBinding.Spec.Reference.Export == nil {
		return nil
	}

	exportPath := logicalcluster.NewPath(apiBinding.Spec.Reference.Export.Path)
	if exportPath.Empty() {
		exportPath = logicalcluster.From(apiBinding).Path()
	}
	apiExport, err := c.getAPIExport(exportPath, apiBinding.Spec.Reference.Export.Name)
	if err != nil {
		logger.Error(err, "error getting APIExport", "apiExportWorkspace", exportPath, "apiExportName", apiBinding.Spec.Reference.Export.Name)
		return nil // nothing we can do
	}

	logger = logging.WithObject(logger, apiExport)

	exportedClaims := sets.New[string]()
	exportedClaimsMap := make(map[string]apisv1alpha2.PermissionClaim)
	for _, claim := range apiExport.Spec.PermissionClaims {
		key := setKeyForClaim(claim)
		exportedClaims.Insert(key)
		exportedClaimsMap[key] = claim
	}

	acceptedClaims := sets.New[string]()
	acceptedClaimsMap := make(map[string]apisv1alpha2.ScopedPermissionClaim)
	for _, claim := range apiBinding.Spec.PermissionClaims {
		if claim.State == apisv1alpha2.ClaimAccepted {
			key := setKeyForClaim(claim.PermissionClaim)
			acceptedClaims.Insert(key)
			acceptedClaimsMap[key] = claim.ScopedPermissionClaim
		}
	}

	appliedClaims := sets.New[string]()
	appliedClaimsMap := make(map[string]apisv1alpha2.ScopedPermissionClaim)
	for _, claim := range apiBinding.Status.AppliedPermissionClaims {
		key := setKeyForClaim(claim.PermissionClaim)
		appliedClaims.Insert(key)
		appliedClaimsMap[key] = claim
	}

	expectedClaims := exportedClaims.Intersection(acceptedClaims)
	unexpectedClaims := acceptedClaims.Difference(expectedClaims)
	needToApply := expectedClaims.Difference(appliedClaims)
	needToRemove := appliedClaims.Difference(acceptedClaims)
	allChanges := needToApply.Union(needToRemove)

	selectorChanges := detectSelectorChanges(expectedClaims, acceptedClaims, appliedClaims, acceptedClaimsMap, appliedClaimsMap, logger)
	allChanges = allChanges.Union(selectorChanges)

	logger.V(4).Info("claim set details",
		"expected", expectedClaims,
		"unexpected", unexpectedClaims,
		"toApply", needToApply,
		"toRemove", needToRemove,
		"all", allChanges,
	)

	var allErrs []error
	applyErrors := sets.New[string]()

	for _, s := range sets.List[string](allChanges) {
		claim := claimFromSetKey(s)
		if _, nonPersisted := permissionclaim.NonPersistedResourcesClaimable[schema.GroupResource{Group: claim.Group, Resource: claim.Resource}]; nonPersisted {
			continue
		}

		claimLogger := logger.WithValues("claim", s)

		informer, gvr, err := c.getInformerForGroupResource(claim.Group, claim.Resource)
		if err != nil {
			allErrs = append(allErrs, fmt.Errorf("error getting informer for group=%q, resource=%q: %w", claim.Group, claim.Resource, err))
			if acceptedClaims.Has(s) {
				applyErrors.Insert(s)
			}
			continue
		}
		claimLogger = claimLogger.WithValues("gvr", gvr)

		claimLogger.V(4).Info("listing resources")
		objs, err := informer.Lister().ByCluster(clusterName).List(labels.Everything())
		if err != nil {
			allErrs = append(allErrs, fmt.Errorf("error listing group=%q, resource=%q: %w", claim.Group, claim.Resource, err))
			if acceptedClaims.Has(s) {
				applyErrors.Insert(s)
			}
			continue
		}

		claimLogger.V(4).Info("got resources", "count", len(objs))

		var claimErrs []error
		for _, obj := range objs {
			u, ok := obj.(*unstructured.Unstructured)
			if !ok {
				claimErrs = append(claimErrs, fmt.Errorf("unexpected type %T: %w", obj, err))
				continue
			}

			logger := logging.WithObject(logger, u)

			if gvr == apisv1alpha2.SchemeGroupVersion.WithResource("apibindings") && logicalcluster.From(u) == clusterName && u.GetName() == apiBinding.Name {
				// Don't issue a generic patch when obj == the APIBinding being reconciled. That will be covered when
				// this call to reconcile exits and the controller patches this APIBinding. Otherwise, the generic patch
				// here will conflict with the controller's attempt to update the APIBinding's status.
				continue
			}

			actualGVR := gvr
			if actualVersion := u.GetAnnotations()[handlers.KCPOriginalAPIVersionAnnotation]; actualVersion != "" {
				actualGV, err := schema.ParseGroupVersion(actualVersion)
				if err != nil {
					logger.Error(err, "error parsing original API version annotation", "annotation", actualVersion)
					claimErrs = append(claimErrs, fmt.Errorf("error parsing original API version annotation %q: %w", actualVersion, err))
					continue
				}
				actualGVR.Version = actualGV.Version
				logger.V(4).Info("using actual API version from annotation", "actual", actualVersion)
			}
			logger = logger.WithValues("actualGVR", actualGVR)

			logger.V(4).Info("patching to get claim labels updated")

			// Empty patch, allowing the admission plugin to update the resource to the correct labels
			err = c.patchGenericObject(ctx, u, actualGVR, clusterName.Path())
			if err != nil {
				patchErr := fmt.Errorf("error patching %q %s|%s/%s: %w", actualGVR, clusterName, u.GetNamespace(), u.GetName(), err)
				claimErrs = append(claimErrs, patchErr)
				continue
			}
		}

		if len(claimErrs) > 0 {
			allErrs = append(allErrs, claimErrs...)

			if acceptedClaims.Has(s) {
				applyErrors.Insert(s)
			}
		}
	}

	unexpectedOrInvalidErrors := make([]error, 0, unexpectedClaims.Len())
	for _, s := range sets.List[string](unexpectedClaims) {
		claim := claimFromSetKey(s)
		unexpectedOrInvalidErrors = append(unexpectedOrInvalidErrors, fmt.Errorf("unexpected/invalid claim for %s.%s (identity %q)", claim.Resource, claim.Group, claim.IdentityHash))
	}

	if len(unexpectedOrInvalidErrors) > 0 {
		i := len(unexpectedOrInvalidErrors)
		if i > 10 {
			i = 10
		}
		errsToDisplay := utilerrors.NewAggregate(unexpectedOrInvalidErrors[0:i])

		conditions.MarkFalse(
			apiBinding,
			apisv1alpha2.PermissionClaimsValid,
			apisv1alpha2.InvalidPermissionClaimsReason,
			conditionsv1alpha1.ConditionSeverityError,
			"%d unexpected and/or invalid permission claims (showing first %d): %s",
			len(unexpectedOrInvalidErrors),
			len(errsToDisplay.Errors()),
			errsToDisplay,
		)
	}

	// Detect verb and selector mismatches between accepted and exported claims
	claimMismatches := detectClaimMismatches(expectedClaims, acceptedClaimsMap, exportedClaimsMap, logger)
	if len(claimMismatches) > 0 {
		// Build mismatch messages
		mismatchMsgs := make([]string, 0, len(claimMismatches))
		for _, m := range claimMismatches {
			claim := claimFromSetKey(m.claim)
			var parts []string
			if m.verbMismatch {
				parts = append(parts, fmt.Sprintf("verbs: accepted %v, exported %v", m.acceptedVerbs, m.exportedVerbs))
			}
			if m.selectorMismatch {
				parts = append(parts, "selector differs from defaultSelector")
			}
			mismatchMsgs = append(mismatchMsgs, fmt.Sprintf("%s.%s (%s)", claim.Resource, claim.Group, strings.Join(parts, "; ")))
		}

		i := len(mismatchMsgs)
		if i > 5 {
			i = 5
		}

		conditions.MarkFalse(
			apiBinding,
			apisv1alpha2.PermissionClaimsValid,
			apisv1alpha2.PermissionClaimsMismatchReason,
			conditionsv1alpha1.ConditionSeverityWarning,
			"%d permission claim(s) have mismatched verbs or selectors (showing first %d): %s",
			len(claimMismatches),
			i,
			strings.Join(mismatchMsgs[:i], ", "),
		)
	}

	if len(unexpectedOrInvalidErrors) == 0 && len(claimMismatches) == 0 {
		conditions.MarkTrue(apiBinding, apisv1alpha2.PermissionClaimsValid)
	}

	fullyApplied := expectedClaims.Difference(applyErrors)
	apiBinding.Status.AppliedPermissionClaims = []apisv1alpha2.ScopedPermissionClaim{}
	for _, s := range sets.List[string](fullyApplied) {
		// fullyApplied = (exportedClaims ∩ acceptedClaims) ⊖ applyErrors,
		// hence s must be in acceptedClaims (and exportedClaims).
		apiBinding.Status.AppliedPermissionClaims = append(apiBinding.Status.AppliedPermissionClaims, acceptedClaimsMap[s])
	}

	if len(allErrs) > 0 {
		i := len(allErrs)
		if i > 10 {
			i = 10
		}
		errsToDisplay := utilerrors.NewAggregate(allErrs[0:i])

		conditions.MarkFalse(
			apiBinding,
			apisv1alpha2.PermissionClaimsApplied,
			apisv1alpha2.InternalErrorReason,
			conditionsv1alpha1.ConditionSeverityError,
			"Permission claims have not been fully applied: %v",
			errsToDisplay,
		)

		return fmt.Errorf("%d error(s) applying permission claims for APIBinding %s|%s (showing the first %d): %w",
			len(allErrs),
			clusterName,
			apiBinding.Name,
			len(errsToDisplay.Errors()),
			errsToDisplay,
		)
	} else {
		conditions.MarkTrue(apiBinding, apisv1alpha2.PermissionClaimsApplied)
	}

	return nil
}

func setKeyForClaim(claim apisv1alpha2.PermissionClaim) string {
	return fmt.Sprintf("%s/%s/%s", claim.Resource, claim.Group, claim.IdentityHash)
}

func claimFromSetKey(key string) apisv1alpha2.PermissionClaim {
	parts := strings.SplitN(key, "/", 3)
	return apisv1alpha2.PermissionClaim{
		GroupResource: apisv1alpha2.GroupResource{
			Group:    parts[1],
			Resource: parts[0],
		},
		IdentityHash: parts[2],
	}
}

func (c *controller) getInformerForGroupResource(group, resource string) (kcpkubernetesinformers.GenericClusterInformer, schema.GroupVersionResource, error) {
	informers, _ := c.ddsif.Informers()

	for gvr := range informers {
		if gvr.Group == group && gvr.Resource == resource {
			informer, err := c.ddsif.ForResource(gvr)
			// once we find one, return.
			return informer, gvr, err
		}
	}
	return nil, schema.GroupVersionResource{}, fmt.Errorf("unable to find informer for %s.%s", group, resource)
}

func (c *controller) patchGenericObject(ctx context.Context, obj metav1.Object, gvr schema.GroupVersionResource, lc logicalcluster.Path) error {
	_, err := c.dynamicClusterClient.
		Cluster(lc).
		Resource(gvr).
		Namespace(obj.GetNamespace()).
		Patch(ctx, obj.GetName(), types.MergePatchType, []byte("{}"), metav1.PatchOptions{})
	// if we don't find it, and we can update lets continue on.
	if err != nil && !errors.IsNotFound(err) {
		return err
	}
	return nil
}

func detectSelectorChanges(
	expectedClaims, acceptedClaims, appliedClaims sets.Set[string],
	acceptedClaimsMap, appliedClaimsMap map[string]apisv1alpha2.ScopedPermissionClaim,
	logger klog.Logger,
) sets.Set[string] {
	selectorChanges := sets.New[string]()
	for key := range expectedClaims {
		if acceptedClaims.Has(key) && appliedClaims.Has(key) {
			acceptedClaim := acceptedClaimsMap[key]
			appliedClaim := appliedClaimsMap[key]
			if !reflect.DeepEqual(acceptedClaim.Selector, appliedClaim.Selector) {
				selectorChanges.Insert(key)
				logger.V(4).Info("detected selector change for claim", "claim", key,
					"oldSelector", appliedClaim.Selector,
					"newSelector", acceptedClaim.Selector)
			}
		}
	}
	return selectorChanges
}

// claimMismatch represents a mismatch between accepted and exported permission claims.
type claimMismatch struct {
	claim            string
	verbMismatch     bool
	acceptedVerbs    []string
	exportedVerbs    []string
	selectorMismatch bool
}

// detectClaimMismatches compares accepted claims against exported claims to find
// verb and selector mismatches. A mismatch occurs when:
// - Verbs: accepted verbs differ from exported verbs (wildcard "*" is excluded from comparison)
// - Selector: accepted selector differs from the export's defaultSelector.
func detectClaimMismatches(
	expectedClaims sets.Set[string],
	acceptedClaimsMap map[string]apisv1alpha2.ScopedPermissionClaim,
	exportedClaimsMap map[string]apisv1alpha2.PermissionClaim,
	logger klog.Logger,
) []claimMismatch {
	var mismatches []claimMismatch

	for key := range expectedClaims {
		acceptedClaim, hasAccepted := acceptedClaimsMap[key]
		exportedClaim, hasExported := exportedClaimsMap[key]
		if !hasAccepted || !hasExported {
			continue
		}

		mismatch := claimMismatch{claim: key}

		// Check verb mismatch (ignoring wildcard)
		acceptedVerbs := sets.New(acceptedClaim.Verbs...)
		exportedVerbs := sets.New(exportedClaim.Verbs...)

		// If either side has wildcard, consider verbs as matching
		if !acceptedVerbs.Has("*") && !exportedVerbs.Has("*") {
			if !acceptedVerbs.Equal(exportedVerbs) {
				mismatch.verbMismatch = true
				mismatch.acceptedVerbs = acceptedClaim.Verbs
				mismatch.exportedVerbs = exportedClaim.Verbs
			}
		}

		// Check selector mismatch against defaultSelector
		if exportedClaim.DefaultSelector != nil {
			if !reflect.DeepEqual(acceptedClaim.Selector, *exportedClaim.DefaultSelector) {
				mismatch.selectorMismatch = true
			}
		}

		if mismatch.verbMismatch || mismatch.selectorMismatch {
			logger.V(4).Info("detected claim mismatch",
				"claim", key,
				"verbMismatch", mismatch.verbMismatch,
				"selectorMismatch", mismatch.selectorMismatch)
			mismatches = append(mismatches, mismatch)
		}
	}

	return mismatches
}
