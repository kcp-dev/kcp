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
	"encoding/json"
	"fmt"

	jsonpatch "github.com/evanphx/json-patch"
	"github.com/kcp-dev/logicalcluster/v2"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	aggregateerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/informers"
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1/permissionclaims"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
)

type permissionClaimHelper struct {
	claim      apisv1alpha1.PermissionClaim
	key, label string

	informer informers.GenericInformer
}

func (p permissionClaimHelper) String() string {
	return p.claim.String()
}

type mapGVRToClaim map[schema.GroupVersionResource][]permissionClaimHelper

func (m mapGVRToClaim) String() string {
	s := "["
	for _, claims := range m {
		s += fmt.Sprintf("%s", claims)
	}
	s = s + "]"
	return s
}

func (c *controller) createPermissionClaimHelper(pc apisv1alpha1.PermissionClaim) (permissionClaimHelper, schema.GroupVersionResource, error) {

	// get informer for the object.
	informer, gvr, err := c.getInformerForGroupResource(pc.Group, pc.Resource)
	if err != nil {
		return permissionClaimHelper{}, gvr, err
	}

	key, label, err := permissionclaims.ToLabelKeyAndValue(pc)
	if err != nil {
		return permissionClaimHelper{}, gvr, err
	}

	return permissionClaimHelper{
		claim:    pc,
		key:      key,
		label:    label,
		informer: informer,
	}, gvr, nil
}

// reconcilePermissionClaims will determine the resources that need to be labeled for access by a permission claim.
// It will determine what permssions need to be added, what permissions need to be removed.
// It will also update the status if it finds an invalid permission claim.
// Permission claims are considered invalid when the identity hashes are mismatched, and when there is no dynamic informer
// for the group resource.
func (c *controller) reconcile(ctx context.Context, apiBinding *apisv1alpha1.APIBinding) error {
	logger := klog.FromContext(ctx)
	identityMismatch := false
	lc := logicalcluster.From(apiBinding)

	// Get all the bound resources in the locicalcluster
	bindings, err := c.listAPIBindings(lc)
	if err != nil {
		return err
	}

	// Building a map, for all the bound resources in the logical cluster based on GR, to validate that a GR
	// coming from a export, has the same identity hash when adding permission claims.
	grsToBoundResource := map[apisv1alpha1.GroupResource]apisv1alpha1.BoundAPIResource{}
	for _, binding := range bindings {
		for _, resource := range binding.Status.BoundResources {
			grsToBoundResource[apisv1alpha1.GroupResource{Group: resource.Group, Resource: resource.Resource}] = resource
		}
	}

	invalidClaims := []apisv1alpha1.PermissionClaim{}
	// get the added permission claims if added, add are just requeue the object.
	addedPermissionClaims := mapGVRToClaim{}
	for _, acceptedPC := range apiBinding.Spec.AcceptedPermissionClaims {
		found := false
		for _, observedPC := range apiBinding.Status.ObservedAcceptedPermissionClaims {
			if acceptedPC.Equal(observedPC) {
				found = true
				break
			}
		}
		if !found {
			ok, err := permissionclaims.ValidateClaim(acceptedPC.Group, acceptedPC.Resource, acceptedPC.IdentityHash, grsToBoundResource)
			if !ok {
				identityMismatch = true
				invalidClaims = append(invalidClaims, acceptedPC)
				identityMismatch = true
				klog.V(3).Infof("invalid claim %v - reason %v", acceptedPC, err)
				continue
			}

			claim, gvr, err := c.createPermissionClaimHelper(acceptedPC)
			if err != nil {
				invalidClaims = append(invalidClaims, acceptedPC)
				klog.Errorf("unable to create permission claim helper %v", err)
				continue
			}

			addedPermissionClaims[gvr] = append(addedPermissionClaims[gvr], claim)
		}
	}

	removedPermissionClaims := mapGVRToClaim{}
	for _, observedPC := range apiBinding.Status.ObservedAcceptedPermissionClaims {
		found := false
		for _, acceptedPC := range apiBinding.Spec.AcceptedPermissionClaims {
			if observedPC.Equal(acceptedPC) {
				found = true
				break
			}
		}
		if !found {
			claim, gvr, err := c.createPermissionClaimHelper(observedPC)
			if err != nil {
				return err
			}
			claims := removedPermissionClaims[gvr]
			if claims == nil {
				claims = []permissionClaimHelper{}
			}
			claims = append(claims, claim)
			removedPermissionClaims[gvr] = claims
		}
	}

	if len(addedPermissionClaims) != 0 {
		logger.V(2).Info("adding permission claims", "addedPermissionClaims", addedPermissionClaims)
	}

	var allPatchErrors []error
	for gvr, claims := range addedPermissionClaims {
		for _, claim := range claims {
			// TODO: use indexer instead of lister.
			if !claim.informer.Informer().HasSynced() {
				return fmt.Errorf("unable sync cache")
			}
			var errs []error
			// objs, err := c.dynamicClusterClient.Resource(gvr).List(logicalcluster.WithCluster(ctx, lc), metav1.ListOptions{})
			objs, err := claim.informer.Lister().List(labels.Everything())
			if err != nil {
				errs = append(errs, err)
				klog.V(4).Infof("uanble to list objects for %s.%s", gvr.Resource, gvr.Group)
			}
			klog.V(6).Infof("reconciling %v objs of type: %v.%v", len(objs), gvr.Resource, gvr.Group)
			for _, obj := range objs {
				// o, ok := obj.(runtime.Object)
				// if !ok {
				// 	return fmt.Errorf("expected type %v got %T", "runtime.Object", obj)
				// }
				oldObjectMeta, err := meta.Accessor(obj.DeepCopyObject())
				if err != nil {
					return err
				}
				// Empty Patch, will relay on the admission to add the resources.
				err = c.patchGenericObject(ctx, oldObjectMeta, gvr, lc)
				if err != nil {
					errs = append(errs, err)
				}
			}
			if len(errs) > 0 {
				allPatchErrors = append(allPatchErrors, errs...)
				logger.V(4).Info("unable to patch objects for added claim", "PermissionClaim", claim)
				continue
			}

			// If all the listed objects are patched, lets add this to Observed Permission Claims
			apiBinding.Status.ObservedAcceptedPermissionClaims = append(apiBinding.Status.ObservedAcceptedPermissionClaims, claim.claim)
		}
	}

	if len(removedPermissionClaims) != 0 {
		logger.V(2).Info("removing permission claims", "removedPermissionClaims", removedPermissionClaims)
	}

	for gvr, claims := range removedPermissionClaims {
		for _, claim := range claims {
			// TODO: use indexer instead of lister.
			if !claim.informer.Informer().HasSynced() {
				return fmt.Errorf("unable sync cache")
			}
			selector := labels.SelectorFromSet(labels.Set(map[string]string{claim.key: claim.label}))
			objs, err := claim.informer.Lister().List(selector)
			if err != nil {
				return err
			}
			var errs []error
			for _, obj := range objs {
				oldObjectMeta, err := meta.Accessor(obj)
				if err != nil {
					return err
				}
				// Empty Patch, will relay on the admission to add the resources.
				err = c.patchGenericObject(ctx, oldObjectMeta, gvr, lc)
				if err != nil {
					errs = append(errs, err)
				}
			}
			if len(errs) > 0 {
				allPatchErrors = append(allPatchErrors, errs...)
				logger.V(4).Info("unable to patch objects for removed claim", "PermissionClaim", claim)
				continue
			}
			// remove claims assume that all new ones were added.
			apiBinding.Status.ObservedAcceptedPermissionClaims = removeClaims(apiBinding.Status.ObservedAcceptedPermissionClaims, claim)
		}
	}

	// Handle invalid claims
	if len(invalidClaims) > 0 || len(allPatchErrors) > 0 {
		logger.V(2).Info("found invalid claims", "invalidClaims", invalidClaims)
		if identityMismatch {
			conditions.MarkFalse(
				apiBinding,
				apisv1alpha1.PermissionClaimsAccepted,
				apisv1alpha1.IdentityMismatchClaimInvalidReason,
				conditionsv1alpha1.ConditionSeverityError,
				"Invalid AcceptedPermissionClaim. Please contact the APIExport owner to resolve or verify identity's bound.",
			)
			return nil
		} else {
			conditions.MarkFalse(
				apiBinding,
				apisv1alpha1.PermissionClaimsAccepted,
				apisv1alpha1.UnknownPermissionClaimInvalidReason,
				conditionsv1alpha1.ConditionSeverityError,
				"Claims have not been fully synced",
			)
			return aggregateerrors.NewAggregate(allPatchErrors)
		}
	}

	conditions.MarkTrue(apiBinding, apisv1alpha1.PermissionClaimsAccepted)

	return nil
}

func removeClaims(claims []apisv1alpha1.PermissionClaim, removedClaim permissionClaimHelper) []apisv1alpha1.PermissionClaim {
	newClaims := []apisv1alpha1.PermissionClaim{}
	for _, c := range claims {
		if !c.Equal(removedClaim.claim) {
			newClaims = append(newClaims, c)
		}
	}

	return newClaims
}

func (c *controller) getInformerForGroupResource(group, resource string) (informers.GenericInformer, schema.GroupVersionResource, error) {
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
	objJSON, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	patchBytes, err := jsonpatch.CreateMergePatch(objJSON, objJSON)
	if err != nil {
		return err
	}

	_, err = c.dynamicClusterClient.
		Resource(gvr).
		Namespace(obj.GetNamespace()).
		Patch(logicalcluster.WithCluster(ctx, lc), obj.GetName(), types.MergePatchType, patchBytes, metav1.PatchOptions{})
	// if we don't find it, and we can update lets continue on.
	if err != nil && !errors.IsNotFound(err) {
		return err
	}
	return nil
}
