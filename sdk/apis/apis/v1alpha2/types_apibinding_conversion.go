/*
Copyright 2025 The KCP Authors.

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

package v1alpha2

import (
	"encoding/json"
	"fmt"

	kubeconversion "k8s.io/apimachinery/pkg/conversion"

	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
)

const (
	AcceptablePermissionClaimsAnnotation = "apis.v1alpha2.kcp.io/acceptable-permission-claims"
)

// v1alpha2 -> v1alpha1

func Convert_v1alpha2_APIBinding_To_v1alpha1_APIBinding(in *APIBinding, out *apisv1alpha1.APIBinding, s kubeconversion.Scope) error {
	out.ObjectMeta = *in.ObjectMeta.DeepCopy()

	_, overhangingAPC, err := Convert_v1alpha2_AcceptablePermissionClaims_To_v1alpha1_AcceptablePermissionClaims(in.Spec.PermissionClaims, s)
	if err != nil {
		return err
	}
	if len(overhangingAPC) > 0 {
		encoded, err := json.Marshal(overhangingAPC)
		if err != nil {
			return fmt.Errorf("failed to encode claims as JSON: %w", err)
		}

		if out.Annotations == nil {
			out.Annotations = map[string]string{}
		}
		out.Annotations[AcceptablePermissionClaimsAnnotation] = string(encoded)
	}

	if err := Convert_v1alpha2_APIBindingSpec_To_v1alpha1_APIBindingSpec(&in.Spec, &out.Spec, s); err != nil {
		return err
	}

	if originalPermissionClaims, ok := in.Annotations[PermissionClaimsV1Alpha1Annotation]; ok {
		permissionClaims := []apisv1alpha1.AcceptablePermissionClaim{}
		if err := json.Unmarshal([]byte(originalPermissionClaims), &permissionClaims); err != nil {
			return fmt.Errorf("failed to decode schemas from JSON: %w", err)
		}

		for _, pc := range permissionClaims {
			for i, opc := range out.Spec.PermissionClaims {
				if pc.PermissionClaim.EqualGRI(opc.PermissionClaim) {
					out.Spec.PermissionClaims[i].ResourceSelector = pc.ResourceSelector
				}
			}
		}

		delete(out.Annotations, PermissionClaimsV1Alpha1Annotation)

		// make tests for equality easier to write by turning []string into nil
		if len(out.Annotations) == 0 {
			out.Annotations = nil
		}
	}

	return Convert_v1alpha2_APIBindingStatus_To_v1alpha1_APIBindingStatus(&in.Status, &out.Status, s)
}

func Convert_v1alpha2_AcceptablePermissionClaims_To_v1alpha1_AcceptablePermissionClaims(in []AcceptablePermissionClaim, s kubeconversion.Scope) (out []apisv1alpha1.AcceptablePermissionClaim, overhanging []AcceptablePermissionClaim, err error) {
	for _, apc := range in {
		if len(apc.PermissionClaim.Verbs) == 1 && apc.PermissionClaim.Verbs[0] == "*" {
			var v1apc apisv1alpha1.AcceptablePermissionClaim

			if err := Convert_v1alpha2_AcceptablePermissionClaim_To_v1alpha1_AcceptablePermissionClaim(&apc, &v1apc, s); err != nil {
				return nil, nil, err
			}

			out = append(out, v1apc)
		} else {
			overhanging = append(overhanging, apc)
		}
	}

	return
}

// Convert_v1alpha2_AcceptablePermissionClaim_To_v1alpha1_AcceptablePermissionClaim converts v1alpha2.AcceptablePermissionClaim to v1alpha1.AcceptablePermissionClaim.
// This is not a lossless conversion.
func Convert_v1alpha2_AcceptablePermissionClaim_To_v1alpha1_AcceptablePermissionClaim(in *AcceptablePermissionClaim, out *apisv1alpha1.AcceptablePermissionClaim, s kubeconversion.Scope) error {
	if err := Convert_v1alpha2_ScopedPermissionClaim_To_v1alpha1_PermissionClaim(&in.ScopedPermissionClaim, &out.PermissionClaim, s); err != nil {
		return err
	}
	out.State = apisv1alpha1.AcceptablePermissionClaimState(in.State)
	return nil
}

func Convert_v1alpha2_ScopedPermissionClaim_To_v1alpha1_PermissionClaim(in *ScopedPermissionClaim, out *apisv1alpha1.PermissionClaim, s kubeconversion.Scope) error {
	if err := Convert_v1alpha2_PermissionClaim_To_v1alpha1_PermissionClaim(&in.PermissionClaim, out, s); err != nil {
		return err
	}
	out.All = in.Selector.MatchAll
	return nil
}

// v1alpha1 -> v1alpha2

func Convert_v1alpha1_APIBinding_To_v1alpha2_APIBinding(in *apisv1alpha1.APIBinding, out *APIBinding, s kubeconversion.Scope) error {
	out.ObjectMeta = *in.ObjectMeta.DeepCopy()

	if err := Convert_v1alpha1_APIBindingSpec_To_v1alpha2_APIBindingSpec(&in.Spec, &out.Spec, s); err != nil {
		return err
	}
	if err := Convert_v1alpha1_APIBindingStatus_To_v1alpha2_APIBindingStatus(&in.Status, &out.Status, s); err != nil {
		return err
	}

	// store acceptable permission claims in annotation. this is necessary for a clean conversion of
	// ResourceSelector, which went away in v1alpha2.
	if len(in.Spec.PermissionClaims) > 0 {
		if out.Annotations == nil {
			out.Annotations = make(map[string]string)
		}
		encoded, err := json.Marshal(in.Spec.PermissionClaims)
		if err != nil {
			return err
		}
		out.Annotations[PermissionClaimsV1Alpha1Annotation] = string(encoded)
	}

	if overhangingAPC, ok := in.Annotations[AcceptablePermissionClaimsAnnotation]; ok {
		acceptablePermissionClaims := []AcceptablePermissionClaim{}
		if err := json.Unmarshal([]byte(overhangingAPC), &acceptablePermissionClaims); err != nil {
			return fmt.Errorf("failed to decode claims from JSON: %w", err)
		}

		for _, pc := range acceptablePermissionClaims {
			for i, opc := range out.Spec.PermissionClaims {
				if pc.EqualGRI(opc.PermissionClaim) {
					out.Spec.PermissionClaims[i].PermissionClaim.Verbs = pc.Verbs
				}
			}
		}

		delete(out.Annotations, AcceptablePermissionClaimsAnnotation)

		// make tests for equality easier to write by turning []string into nil
		if len(out.Annotations) == 0 {
			out.Annotations = nil
		}
	}

	for i, opc := range out.Spec.PermissionClaims {
		if len(opc.PermissionClaim.Verbs) == 0 {
			out.Spec.PermissionClaims[i].PermissionClaim.Verbs = []string{"*"}
		}
	}

	return nil
}

func Convert_v1alpha1_AcceptablePermissionClaims_To_v1alpha2_AcceptablePermissionClaims(in []apisv1alpha1.AcceptablePermissionClaim, s kubeconversion.Scope) (out []AcceptablePermissionClaim, overhanging []apisv1alpha1.AcceptablePermissionClaim, err error) {
	for _, apc := range in {
		// ResourceSelector has been removed from PermissionClaims, we can't cleanly convert it.
		if apc.PermissionClaim.All && len(apc.PermissionClaim.ResourceSelector) == 0 {
			var v2apc AcceptablePermissionClaim
			if err := Convert_v1alpha1_AcceptablePermissionClaim_To_v1alpha2_AcceptablePermissionClaim(&apc, &v2apc, s); err != nil {
				return nil, nil, err
			}
			out = append(out, v2apc)
		} else {
			overhanging = append(overhanging, apc)
		}
	}

	return
}

// Convert_v1alpha1_AcceptablePermissionClaim_To_v1alpha2_AcceptablePermissionClaim converts v1alpha1.AcceptablePermissionClaim to v1alpha2.AcceptablePermissionClaim.
// This is not a lossless conversion.
func Convert_v1alpha1_AcceptablePermissionClaim_To_v1alpha2_AcceptablePermissionClaim(in *apisv1alpha1.AcceptablePermissionClaim, out *AcceptablePermissionClaim, s kubeconversion.Scope) error {
	if err := Convert_v1alpha1_PermissionClaim_To_v1alpha2_PermissionClaim(&in.PermissionClaim, &out.PermissionClaim, s); err != nil {
		return err
	}

	out.State = AcceptablePermissionClaimState(in.State)
	out.Selector.MatchAll = in.PermissionClaim.All
	if len(out.PermissionClaim.Verbs) == 0 {
		out.PermissionClaim.Verbs = []string{"*"}
	}

	return nil
}

func Convert_v1alpha1_PermissionClaim_To_v1alpha2_ScopedPermissionClaim(in *apisv1alpha1.PermissionClaim, out *ScopedPermissionClaim, s kubeconversion.Scope) error {
	if err := Convert_v1alpha1_PermissionClaim_To_v1alpha2_PermissionClaim(in, &out.PermissionClaim, s); err != nil {
		return err
	}
	out.Selector.MatchAll = in.All
	return nil
}
