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
	claim apisv1alpha1.PermissionClaim
	label string
	key   string

	lister informers.GenericInformer
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
	lister, gvr, err := c.getInformerForGroupResource(pc.Group, pc.Resource)
	if err != nil {
		return permissionClaimHelper{}, gvr, err
	}

	key, label, err := permissionclaims.ToLabelKeyAndValue(pc)
	if err != nil {
		return permissionClaimHelper{}, gvr, err
	}

	return permissionClaimHelper{
		claim:  pc,
		label:  label,
		key:    key,
		lister: lister,
	}, gvr, nil
}

// reconcilePermissionClaims will determine the resources that need to be labeled for access by a permission claim.
// It will determine what permssions need to be added, what permissions need to be removed.
// It will also update the status if it finds an invalid permission claim.
// Permission claims are considered invalid when the identity hashes are mismatched, and when there is no dynamic informer
// for the group resource.
func (c *controller) reconcilePermissionClaims(ctx context.Context, apiBinding *apisv1alpha1.APIBinding) error {
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
			//TODO: add openAPI check such that this could not be incorrect.
			if acceptedPC.IdentityHash != "" {
				if grsToBoundResource[acceptedPC.GroupResource].Schema.IdentityHash != acceptedPC.IdentityHash {
					identityMismatch = true
					invalidClaims = append(invalidClaims, acceptedPC)
					continue
				}
			}
			claim, gvr, err := c.createPermissionClaimHelper(acceptedPC)
			if err != nil {
				return err
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

	klog.V(2).Infof("Adding permission claims %v for APIBinding %s", addedPermissionClaims, fmt.Sprintf("%s|%s", lc, apiBinding.Name))

	var allPatchErrors []error
	for gvr, claims := range addedPermissionClaims {
		for _, claim := range claims {
			//TODO: use indexer instead of lister.
			objs, err := claim.lister.Lister().List(labels.Everything())
			if err != nil {
				return err
			}
			var errs []error
			for _, obj := range objs {
				newObj := obj.DeepCopyObject()
				oldObjectMeta, err := meta.Accessor(obj)
				if err != nil {
					return err
				}
				newObjectMeta, err := meta.Accessor(newObj)
				if err != nil {
					return err
				}
				labels := newObjectMeta.GetLabels()
				if labels == nil {
					labels = map[string]string{}
				}
				labels[claim.key] = claim.label

				newObjectMeta.SetLabels(labels)
				err = c.patchGenericObject(ctx, oldObjectMeta, newObjectMeta, gvr, lc)
				if err != nil {
					errs = append(errs, err)
				}
			}
			if len(errs) > 0 {
				allPatchErrors = append(allPatchErrors, errs...)
				klog.V(4).Infof("unable to patch objects for exports %s added claim %v", fmt.Sprintf("%s|%s", lc, apiBinding.Name), claim)
				continue
			}

			// If all the listed objects are patched, lets add this to Observed Permission Claims
			apiBinding.Status.ObservedAcceptedPermissionClaims = append(apiBinding.Status.ObservedAcceptedPermissionClaims, claim.claim)
		}
	}

	klog.V(2).Infof("Removing permission claims %v for APIBinding %s", removedPermissionClaims, fmt.Sprintf("%s|%s", lc, apiBinding.Name))

	for gvr, claims := range removedPermissionClaims {
		for _, claim := range claims {
			//TODO: use indexer instead of lister.
			selector := labels.SelectorFromSet(labels.Set(map[string]string{claim.key: claim.label}))
			objs, err := claim.lister.Lister().List(selector)
			if err != nil {
				return err
			}
			var errs []error
			for _, obj := range objs {
				newObj := obj.DeepCopyObject()
				oldObjectMeta, err := meta.Accessor(obj)
				if err != nil {
					return err
				}
				newObjectMeta, err := meta.Accessor(newObj)
				if err != nil {
					return err
				}
				delete(newObjectMeta.GetLabels(), claim.key)
				err = c.patchGenericObject(ctx, oldObjectMeta, newObjectMeta, gvr, lc)
				if err != nil {
					errs = append(errs, err)
				}
			}
			if len(errs) > 0 {
				allPatchErrors = append(allPatchErrors, errs...)
				klog.V(4).Infof("unable to patch objects for exports %s removed claim %v", fmt.Sprintf("%s|%s", lc, apiBinding.Name), claim)
				continue
			}
			// remove claims assume that all new ones were added.
			apiBinding.Status.ObservedAcceptedPermissionClaims = removeClaims(apiBinding.Status.ObservedAcceptedPermissionClaims, claim)
		}
	}

	// Handle invalid claims
	if len(invalidClaims) > 0 || len(allPatchErrors) > 0 {
		klog.V(2).Infof("Found invalid claims %v for APIBinding %s", invalidClaims, fmt.Sprintf("%s|%s", lc, apiBinding.Name))
		if identityMismatch {
			conditions.MarkFalse(
				apiBinding,
				apisv1alpha1.PermissionClaimsAccepted,
				apisv1alpha1.IdentityMismatchClaimInvalidReason,
				conditionsv1alpha1.ConditionSeverityError,
				"Invalid AcceptedPermissionClaim. Please contact the APIExport owner to resolve or verify identity's bound.",
			)
			return fmt.Errorf("found identity mismatch")
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
			informer, err := c.ddsif.InformerForResource(gvr)
			// once we find one, return.
			return informer, gvr, err
		}
	}
	return nil, schema.GroupVersionResource{}, fmt.Errorf("unable to find informer for %s.%s", group, resource)
}

func (c *controller) patchGenericObject(ctx context.Context, old, new metav1.Object, gvr schema.GroupVersionResource, lc logicalcluster.Name) error {
	oldJSON, err := json.Marshal(old)
	if err != nil {
		return err
	}
	newJSON, err := json.Marshal(new)
	if err != nil {
		return err
	}
	patchBytes, err := jsonpatch.CreateMergePatch(oldJSON, newJSON)
	if err != nil {
		return err
	}

	_, err = c.dynamicClusterClient.
		Resource(gvr).
		Namespace(new.GetNamespace()).
		Patch(logicalcluster.WithCluster(ctx, lc), new.GetName(), types.MergePatchType, patchBytes, metav1.PatchOptions{})
	// if we don't find it, and we can update lets continue on.
	if err != nil && !errors.IsNotFound(err) {
		return err
	}
	return nil
}
