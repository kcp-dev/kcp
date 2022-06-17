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
	"k8s.io/client-go/restmapper"
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1/permissionclaims"
)

type permissionClaim struct {
	claim apisv1alpha1.PermissionClaim
	label string
	key   string
}

func (c *controller) reconcilePermissionClaims(ctx context.Context, apiBinding *apisv1alpha1.APIBinding) error {
	// this will be responsible for making sure that the the observed state is equal to the accepted state. it should handle GC of claims from the given objects.

	lc := logicalcluster.From(apiBinding)

	dc := c.discoveryClusterClient.WithCluster(lc)

	// TODO: this is not scalable and we will need to figure out a better way.
	// One option is an informer/cache
	apiGroupResources, err := restmapper.GetAPIGroupResources(dc)
	if err != nil {
		return err
	}
	mapper := restmapper.NewDiscoveryRESTMapper(apiGroupResources)

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
			gvr, err := mapper.ResourcesFor(schema.GroupVersionResource{Group: acceptedPC.Group, Resource: acceptedPC.Resource})

			if err != nil {
				return err
			}
			if len(gvr) == 0 {
				return fmt.Errorf("unable to find the version for resource %v", acceptedPC.GroupResource)
			}
			key, label, err := permissionclaims.PermissionClaimToLabel(acceptedPC)
			if err != nil {
				return err
			}

			claims := addedPermissionClaims[gvr[0]]
			if claims == nil {
				claims = []permissionClaim{}
			}
			claims = append(claims, permissionClaim{
				claim: acceptedPC,
				label: label,
				key:   key,
			})
			addedPermissionClaims[gvr[0]] = claims
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
			gvr, err := mapper.ResourcesFor(schema.GroupVersionResource{Group: observedPC.Group, Resource: observedPC.Resource})
			if err != nil {
				return err
			}
			if len(gvr) == 0 {
				return fmt.Errorf("unable to find the version for resource %v", observedPC.GroupResource)
			}
			key, label, err := permissionclaims.PermissionClaimToLabel(observedPC)
			if err != nil {
				return err
			}
			claims := removedPermissionClaims[gvr[0]]
			if claims == nil {
				claims = []permissionClaim{}
			}
			claims = append(claims, permissionClaim{
				claim: observedPC,
				label: label,
				key:   key,
			})
			removedPermissionClaims[gvr[0]] = claims
		}
	}

	klog.V(2).Infof("permission controller adding permission claims: %#v, removing: %#v", addedPermissionClaims, removedPermissionClaims)

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
	return nil
}
