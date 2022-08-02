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

package permissionclaims

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"k8s.io/apimachinery/pkg/util/validation"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
)

// ToLabelKeyAndValue will create a safe key and value for labeling a resource to grant access
// based on the permissionClaim.
func ToLabelKeyAndValue(permissionClaim apisv1alpha1.PermissionClaim) (string, string, error) {
	bytes, err := json.Marshal(permissionClaim)
	if err != nil {
		return "", "", err
	}
	hash := fmt.Sprintf("%x", sha256.Sum224(bytes))
	labelKeyHashLength := validation.LabelValueMaxLength - len(apisv1alpha1.APIExportPermissionClaimLabelPrefix)
	return apisv1alpha1.APIExportPermissionClaimLabelPrefix + hash[0:labelKeyHashLength], hash, nil
}

// ValidateClaim will make sure that a given claim is valid, for the bound resources.
// To be valid a claim can be for a internal type. To determine this when a GR is not bound and the idenityHash is empty it is for an internal type.
// It will also be valid when the resouce for GR is bound and the identity hash's match.
// An invalid claim for the given GR and identity hash must be unusable based on the bound resources in the given workspace.
func ValidateClaim(group, resource, identityHash string, grsToBoundResource map[apisv1alpha1.GroupResource]apisv1alpha1.BoundAPIResource) (bool, error) {
	boundResource, bound := grsToBoundResource[apisv1alpha1.GroupResource{Group: group, Resource: resource}]
	// TODO add ability to check that the GR is knowable by the inernalschema's once added
	if !bound && identityHash == "" {
		return true, nil
	}
	if !bound && identityHash != "" {
		return false, fmt.Errorf("unable to find bound resource for %v.%v", resource, group)
	}
	if boundResource.Schema.IdentityHash != identityHash {
		return false, fmt.Errorf("bound resource does not match for %v.%v", resource, group)
	}
	return true, nil
}

// AllClaimLabels will return the permission claim labels for the given GR based on the bindings given.
func AllClaimLabels(group, resource string, bindings []*apisv1alpha1.APIBinding) (map[string]string, error) {
	grsToBoundResource := map[apisv1alpha1.GroupResource]apisv1alpha1.BoundAPIResource{}
	for _, binding := range bindings {
		for _, resource := range binding.Status.BoundResources {
			grsToBoundResource[apisv1alpha1.GroupResource{Group: resource.Group, Resource: resource.Resource}] = resource
		}
	}

	labels := map[string]string{}
	for _, binding := range bindings {
		// TODO: intersection of APIExport claims and AcceptedClaims.
		for _, pc := range binding.Spec.AcceptedPermissionClaims {
			if pc.Group != group || pc.Resource != resource {
				continue
			}
			if ok, _ := ValidateClaim(pc.Group, pc.Resource, pc.IdentityHash, grsToBoundResource); !ok {
				continue
			}

			k, v, err := ToLabelKeyAndValue(pc)
			if err != nil {
				return nil, err
			}

			labels[k] = v
		}
	}

	return labels, nil
}
