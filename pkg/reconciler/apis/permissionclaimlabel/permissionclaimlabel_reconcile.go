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

package permissionclaimlabel

import (
	"context"
	"fmt"
	"strings"

	"github.com/kcp-dev/logicalcluster/v2"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	aggregateerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	kubernetesinformers "k8s.io/client-go/informers"
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/logging"
)

// reconcilePermissionClaims determines the resources that need to be labeled for access by a permission claim.
// It determines what permissions need to be added, what permissions need to be removed.
// It also updates the status if it finds an invalid permission claim.
// Permission claims are considered invalid when the identity hashes are mismatched, and when there is no dynamic informer
// for the group resource.
func (c *controller) reconcile(ctx context.Context, apiBinding *apisv1alpha1.APIBinding) error {
	logger := klog.FromContext(ctx)

	clusterName := logicalcluster.From(apiBinding)

	if apiBinding.Status.BoundAPIExport == nil {
		return nil
	}

	exportClusterName := apiBinding.Status.BoundAPIExport.Workspace.Path
	exportName := apiBinding.Status.BoundAPIExport.Workspace.ExportName
	apiExport, err := c.getAPIExport(logicalcluster.New(exportClusterName), exportName)
	if err != nil {
		logger.Error(err, "error getting APIExport", "apiExportWorkspace", exportClusterName, "apiExportName", exportName)
		return nil // nothing we can do
	}

	logger = logging.WithObject(logger, apiExport)

	exportedClaims := sets.NewString()
	for _, claim := range apiExport.Spec.PermissionClaims {
		exportedClaims.Insert(setKeyForClaim(claim))
	}

	acceptedClaims := sets.NewString()
	for _, claim := range apiBinding.Spec.PermissionClaims {
		if claim.State == apisv1alpha1.ClaimAccepted {
			acceptedClaims.Insert(setKeyForClaim(claim.PermissionClaim))
		}
	}

	appliedClaims := sets.NewString()
	for _, claim := range apiBinding.Status.AppliedPermissionClaims {
		appliedClaims.Insert(setKeyForClaim(claim))
	}

	expectedClaims := exportedClaims.Intersection(acceptedClaims)
	unexpectedClaims := acceptedClaims.Difference(expectedClaims)
	needToApply := expectedClaims.Difference(appliedClaims)
	needToRemove := appliedClaims.Difference(acceptedClaims)
	allChanges := needToApply.Union(needToRemove)

	logger.V(6).Info("claim set details",
		"expected", expectedClaims,
		"unexpected", unexpectedClaims,
		"toApply", needToApply,
		"toRemove", needToRemove,
		"all", allChanges,
	)

	var allErrs []error
	applyErrors := sets.NewString()

	for _, s := range allChanges.List() {
		claim := claimFromSetKey(s)
		claimLogger := logger.WithValues("claim", s)

		informer, gvr, err := c.getInformerForGroupResource(claim.Group, claim.Resource)
		if err != nil {
			allErrs = append(allErrs, fmt.Errorf("error getting informer for group=%q, resource=%q: %w", claim.Group, claim.Resource, err))
			if acceptedClaims.Has(s) {
				applyErrors.Insert(s)
			}
			continue
		}

		claimLogger.V(4).Info("listing resources")
		objs, err := informer.Informer().GetIndexer().ByIndex(indexers.ByLogicalCluster, clusterName.String())
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
			logger.V(4).Info("patching to get claim labels updated")

			// Empty patch, allowing the admission plugin to update the resource to the correct labels
			err = c.patchGenericObject(ctx, u, gvr, clusterName)
			if err != nil {
				patchErr := fmt.Errorf("error patching %q %s|%s/%s: %w", gvr, clusterName, u.GetNamespace(), u.GetName(), err)
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

	var unexpectedOrInvalidErrors []error
	for _, s := range unexpectedClaims.List() {
		claim := claimFromSetKey(s)
		unexpectedOrInvalidErrors = append(unexpectedOrInvalidErrors, fmt.Errorf("unexpected/invalid claim for %s.%s (identity %q)", claim.Resource, claim.Group, claim.IdentityHash))
	}
	if len(unexpectedOrInvalidErrors) > 0 {
		i := len(unexpectedOrInvalidErrors)
		if i > 10 {
			i = 10
		}
		errsToDisplay := aggregateerrors.NewAggregate(unexpectedOrInvalidErrors[0:i])

		conditions.MarkFalse(
			apiBinding,
			apisv1alpha1.PermissionClaimsValid,
			apisv1alpha1.InvalidPermissionClaimsReason,
			conditionsv1alpha1.ConditionSeverityError,
			"%d unexpected and/or invalid permission claims (showing first %d): %s",
			len(unexpectedOrInvalidErrors),
			len(errsToDisplay.Errors()),
			errsToDisplay,
		)
	} else {
		conditions.MarkTrue(apiBinding, apisv1alpha1.PermissionClaimsValid)
	}

	fullyApplied := expectedClaims.Difference(applyErrors)
	apiBinding.Status.AppliedPermissionClaims = []apisv1alpha1.PermissionClaim{}
	for _, s := range fullyApplied.UnsortedList() {
		claim := claimFromSetKey(s)
		apiBinding.Status.AppliedPermissionClaims = append(apiBinding.Status.AppliedPermissionClaims, claim)
	}

	if len(allErrs) > 0 {
		i := len(allErrs)
		if i > 10 {
			i = 10
		}
		errsToDisplay := aggregateerrors.NewAggregate(allErrs[0:i])

		conditions.MarkFalse(
			apiBinding,
			apisv1alpha1.PermissionClaimsApplied,
			apisv1alpha1.InternalErrorReason,
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
		conditions.MarkTrue(apiBinding, apisv1alpha1.PermissionClaimsApplied)
	}

	return nil
}

func setKeyForClaim(claim apisv1alpha1.PermissionClaim) string {
	return fmt.Sprintf("%s/%s/%s", claim.Resource, claim.Group, claim.IdentityHash)
}

func claimFromSetKey(key string) apisv1alpha1.PermissionClaim {
	parts := strings.SplitN(key, "/", 3)
	return apisv1alpha1.PermissionClaim{
		GroupResource: apisv1alpha1.GroupResource{
			Group:    parts[1],
			Resource: parts[0],
		},
		IdentityHash: parts[2],
	}
}

func (c *controller) getInformerForGroupResource(group, resource string) (kubernetesinformers.GenericInformer, schema.GroupVersionResource, error) {
	listers, _ := c.ddsif.Listers()

	for gvr := range listers {
		if gvr.Group == group && gvr.Resource == resource {
			informer, err := c.ddsif.ForResource(gvr)
			// once we find one, return.
			return informer, gvr, err
		}
	}
	return nil, schema.GroupVersionResource{}, fmt.Errorf("unable to find informer for %s.%s", group, resource)
}

func (c *controller) patchGenericObject(ctx context.Context, obj metav1.Object, gvr schema.GroupVersionResource, lc logicalcluster.Name) error {
	_, err := c.dynamicClusterClient.
		Resource(gvr).
		Namespace(obj.GetNamespace()).
		Patch(logicalcluster.WithCluster(ctx, lc), obj.GetName(), types.MergePatchType, []byte("{}"), metav1.PatchOptions{})
	// if we don't find it, and we can update lets continue on.
	if err != nil && !errors.IsNotFound(err) {
		return err
	}
	return nil
}
