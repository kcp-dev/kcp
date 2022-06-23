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
	"fmt"
	"reflect"

	"github.com/kcp-dev/logicalcluster"

	"k8s.io/apimachinery/pkg/api/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/informers"
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1/permissionclaims"
)

type permissionClaim struct {
	claim apisv1alpha1.PermissionClaim
	label string
	key   string

	lister informers.GenericInformer
}

func (c *controller) createPermissionClaimHelper(pc apisv1alpha1.PermissionClaim) (permissionClaim, schema.GroupVersionResource, error) {

	// get informer for the object.
	lister, gvr, err := c.getInformerForGroupResource(pc.Group, pc.Resource)
	if err != nil {
		return permissionClaim{}, gvr, err
	}

	key, label, err := permissionclaims.PermissionClaimToLabel(pc)
	if err != nil {
		return permissionClaim{}, gvr, err
	}

	return permissionClaim{
		claim:  pc,
		label:  label,
		key:    key,
		lister: lister,
	}, gvr, nil
}

func (c *controller) reconcilePermissionClaims(ctx context.Context, apiBinding *apisv1alpha1.APIBinding) error {
	lc := logicalcluster.From(apiBinding)

	// Get all the bound resources in the locicalcluster
	bindings, err := c.listAPIBindings(lc)
	if err != nil {
		return err
	}
	grsToBoundResource := map[apisv1alpha1.GroupResource]apisv1alpha1.BoundAPIResource{}
	for _, bindings := range bindings {
		for _, resource := range bindings.Status.BoundResources {
			grsToBoundResource[apisv1alpha1.GroupResource{Group: resource.Group, Resource: resource.Resource}] = resource
		}
	}

	invalidClaims := []apisv1alpha1.PermissionClaim{}

	klog.V(2).Infof("dealing with added claims: %#v compared to observed claims: %#v", apiBinding.Spec.AcceptedPermissionClaims, apiBinding.Status.ObservedAcceptedPermissionClaims)

	// get the added permission claims if added, add are just requeue the object.
	addedPermissionClaims := map[schema.GroupVersionResource][]permissionClaim{}
	for _, acceptedPC := range apiBinding.Spec.AcceptedPermissionClaims {
		found := false
		for _, observedPC := range apiBinding.Status.ObservedAcceptedPermissionClaims {
			if reflect.DeepEqual(acceptedPC, observedPC) {
				found = true
				break
			}
		}
		if !found {
			if acceptedPC.IdentityHash != "" && acceptedPC.IdentityHash != grsToBoundResource[acceptedPC.GroupResource].Schema.IdentityHash {
				invalidClaims = append(invalidClaims, acceptedPC)
			}
			claim, gvr, err := c.createPermissionClaimHelper(acceptedPC)
			if err != nil {
				klog.Infof("\n\n\nhere: %#v\n\n\n", err)
				return err
			}
			claims := addedPermissionClaims[gvr]
			if claims == nil {
				claims = []permissionClaim{}
			}
			claims = append(claims, claim)
			addedPermissionClaims[gvr] = claims
		}
	}

	removedPermissionClaims := map[schema.GroupVersionResource][]permissionClaim{}
	for _, observedPC := range apiBinding.Status.ObservedAcceptedPermissionClaims {
		found := false
		for _, acceptedPC := range apiBinding.Spec.AcceptedPermissionClaims {
			if reflect.DeepEqual(observedPC, acceptedPC) {
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
				claims = []permissionClaim{}
			}
			claims = append(claims, claim)
			removedPermissionClaims[gvr] = claims
		}
	}

	klog.V(2).Infof("permission controller adding permission claims: %#v, removing: %#v and found invalid claims: %#v", addedPermissionClaims, removedPermissionClaims, invalidClaims)

	for gvr, claims := range addedPermissionClaims {
		objs, err := c.dynamicClusterClient.
			Cluster(lc).
			Resource(gvr).
			List(ctx, v1.ListOptions{})
		if err != nil {
			return err
		}

		for _, claim := range claims {
			// In the future, we will need to determine if the claim matches for any of the objects.
			for _, obj := range objs.Items {
				labels := obj.GetLabels()
				if labels == nil {
					labels = map[string]string{}
				}
				labels[claim.key] = claim.label
				obj.SetLabels(labels)
				_, err := c.dynamicClusterClient.
					Cluster(lc).
					Resource(gvr).
					Namespace(obj.GetNamespace()).
					Update(ctx, &obj, v1.UpdateOptions{})
				// if we don't find it, and we can update lets continue on.
				if err != nil && !errors.IsNotFound(err) {
					return err
				}
			}
			// If a here, lets add this to Observed Permission Claims
			apiBinding.Status.ObservedAcceptedPermissionClaims = append(apiBinding.Status.ObservedAcceptedPermissionClaims, claim.claim)
		}
	}

	for gvr, claims := range removedPermissionClaims {
		for _, claim := range claims {
			selector := labels.SelectorFromSet(labels.Set(map[string]string{claim.key: claim.label}))
			objs, err := c.dynamicClusterClient.
				Cluster(lc).
				Resource(gvr).
				List(ctx, v1.ListOptions{LabelSelector: selector.String()})

			if err != nil {
				return err
			}
			for _, obj := range objs.Items {
				label := obj.GetLabels()
				if label == nil {
					continue
				}
				delete(label, claim.key)
				obj.SetLabels(label)
				_, err := c.dynamicClusterClient.
					Cluster(lc).
					Resource(gvr).
					Namespace(obj.GetNamespace()).
					Update(ctx, &obj, v1.UpdateOptions{})
				if err != nil {
					return err
				}
			}
		}
	}
	// if here, lets assume that now everything is up to date
	apiBinding.Status.ObservedAcceptedPermissionClaims = apiBinding.Spec.AcceptedPermissionClaims

	// Handle invalid claims

	if len(invalidClaims) > 0 {
		// Make a condition
		klog.V(2).Infof("Found invalid accepted claims: %v", invalidClaims)
	}
	return nil
}

func (c *controller) getInformerForGroupResource(group, resource string) (informers.GenericInformer, schema.GroupVersionResource, error) {
	listers, _ := c.dynamicDiscoverySharedInformerFactory.Listers()

	fmt.Printf("\n\n LISTERS: %v\n\n", listers)
	for gvr := range listers {
		if gvr.Group == group && gvr.Resource == resource {
			informer := c.dynamicDiscoverySharedInformerFactory.InformerForResource(gvr)
			// once we find one, return.
			return informer, gvr, nil
		}
	}
	return nil, schema.GroupVersionResource{}, fmt.Errorf("unable to find informer for %s.%s", group, resource)
}
