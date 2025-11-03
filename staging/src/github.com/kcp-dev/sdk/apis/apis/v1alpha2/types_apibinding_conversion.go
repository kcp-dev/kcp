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

	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
)

const (
	AcceptablePermissionClaimsAnnotation     = "apis.v1alpha2.kcp.io/acceptable-permission-claims"
	StatusPermissionClaimsAnnotation         = "apis.v1alpha2.kcp.io/status-export-permission-claims"
	StatusAppliedClaimsAnnotation            = "apis.v1alpha2.kcp.io/status-applied-permission-claims"
	StatusPermissionClaimsV1Alpha1Annotation = "apis.v1alpha2.kcp.io/v1alpha1-status-export-permission-claims"
	StatusAppliedClaimsV1Alpha1Annotation    = "apis.v1alpha2.kcp.io/v1alpha1-status-applied-permission-claims"
)

// v1alpha2 -> v1alpha1 conversions.

func Convert_v1alpha2_APIBinding_To_v1alpha1_APIBinding(in *APIBinding, out *apisv1alpha1.APIBinding, s kubeconversion.Scope) error {
	out.ObjectMeta = *in.ObjectMeta.DeepCopy()

	// before converting the spec, figure out which PermissionClaims could not be represented in v1alpha1 and
	// retain them via an annotation
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

	// After converting the spec, read the retained information from the annotation and update PermissionClaims
	// with All and ResourceSelectors that are not present in v1alpha2 (but saved in the annotation)
	if originalPermissionClaims, ok := in.Annotations[PermissionClaimsV1Alpha1Annotation]; ok {
		permissionClaims := []apisv1alpha1.AcceptablePermissionClaim{}
		if err := json.Unmarshal([]byte(originalPermissionClaims), &permissionClaims); err != nil {
			return fmt.Errorf("failed to decode schemas from JSON: %w", err)
		}

		for _, pc := range permissionClaims {
			for i, opc := range out.Spec.PermissionClaims {
				if pc.PermissionClaim.EqualGRI(opc.PermissionClaim) {
					out.Spec.PermissionClaims[i].All = pc.All
					out.Spec.PermissionClaims[i].ResourceSelector = pc.ResourceSelector
				}
			}
		}

		delete(out.Annotations, PermissionClaimsV1Alpha1Annotation)
	}

	// Determines any overhanging v1alpha2.PermissionClaims from v1alpha2.APIBindingStatus.ExportPermissionClaims that we need to store in an annotation.
	_, overhangingStatusPC, err := Convert_v1alpha2_PermissionClaims_To_v1alpha1_PermissionClaims(in.Status.ExportPermissionClaims, s)
	if err != nil {
		return err
	}
	if len(overhangingStatusPC) > 0 {
		encoded, err := json.Marshal(overhangingStatusPC)
		if err != nil {
			return fmt.Errorf("failed to encode claims as JSON: %w", err)
		}

		if out.Annotations == nil {
			out.Annotations = map[string]string{}
		}
		out.Annotations[StatusPermissionClaimsAnnotation] = string(encoded)
	}

	// Determines any overhanging v1alpha2.ScopedPermissionClaims from v1alpha2.APIBindingStatus.AppliedPermissionClaims that we need to store in an annotation.
	_, overhangingStatusAPC, err := Convert_v1alpha2_ScopedPermissionClaims_To_v1alpha1_PermissionClaims(in.Status.AppliedPermissionClaims, s)
	if err != nil {
		return err
	}
	if len(overhangingStatusAPC) > 0 {
		encoded, err := json.Marshal(overhangingStatusAPC)
		if err != nil {
			return fmt.Errorf("failed to encode claims as JSON: %w", err)
		}

		if out.Annotations == nil {
			out.Annotations = map[string]string{}
		}
		out.Annotations[StatusAppliedClaimsAnnotation] = string(encoded)
	}

	if err := Convert_v1alpha2_APIBindingStatus_To_v1alpha1_APIBindingStatus(&in.Status, &out.Status, s); err != nil {
		return err
	}

	// Restores information about "All" and "ResourceSelector" for Status.ExportPermissionClaims that has been lost in a
	// prior v1alpha1->v1alpha2 conversion by reading it from an annotation (this annotation *should* always be there when
	// a prior v1alpha1->v1alpha2 conversion happened, but we don't know if the object has been converted previously).
	if originalPermissionClaims, ok := in.Annotations[StatusPermissionClaimsV1Alpha1Annotation]; ok {
		permissionClaims := []apisv1alpha1.PermissionClaim{}
		if err := json.Unmarshal([]byte(originalPermissionClaims), &permissionClaims); err != nil {
			return fmt.Errorf("failed to decode schemas from JSON: %w", err)
		}

		for _, pc := range permissionClaims {
			for i, opc := range out.Status.ExportPermissionClaims {
				if pc.EqualGRI(opc) {
					out.Status.ExportPermissionClaims[i].All = pc.All
					out.Status.ExportPermissionClaims[i].ResourceSelector = pc.ResourceSelector
				}
			}
		}

		delete(out.Annotations, StatusPermissionClaimsV1Alpha1Annotation)
	}

	// Same as above here, but we are restoring to Status.AppliedClaims.
	if originalPermissionClaims, ok := in.Annotations[StatusAppliedClaimsV1Alpha1Annotation]; ok {
		permissionClaims := []apisv1alpha1.PermissionClaim{}
		if err := json.Unmarshal([]byte(originalPermissionClaims), &permissionClaims); err != nil {
			return fmt.Errorf("failed to decode schemas from JSON: %w", err)
		}

		for _, pc := range permissionClaims {
			for i, opc := range out.Status.AppliedPermissionClaims {
				if pc.EqualGRI(opc) {
					// This is necessary because we default to matchAll=true in the other conversion.
					// So in the case of a resource selector being set, the stored "all" is likely false,
					// and we need to restore it.
					out.Status.AppliedPermissionClaims[i].All = pc.All
					out.Status.AppliedPermissionClaims[i].ResourceSelector = pc.ResourceSelector
				}
			}
		}

		delete(out.Annotations, StatusAppliedClaimsV1Alpha1Annotation)
	}

	// make tests for equality easier to write by turning []string into nil.
	if len(out.Annotations) == 0 {
		out.Annotations = nil
	}

	return nil
}

// Convert_v1alpha2_AcceptablePermissionClaims_To_v1alpha1_AcceptablePermissionClaims converts v1alpha2.AcceptablePermissionClaims
// to v1alpha1.AcceptablePermissionClaims. This is not a lossless conversion, verbs and selectors are lost in this conversion.
// For lossless conversion use Convert_v1alpha2_APIBinding_To_v1alpha1_APIBinding.
func Convert_v1alpha2_AcceptablePermissionClaims_To_v1alpha1_AcceptablePermissionClaims(in []AcceptablePermissionClaim, s kubeconversion.Scope) (out []apisv1alpha1.AcceptablePermissionClaim, overhanging []AcceptablePermissionClaim, err error) {
	for _, apc := range in {
		if len(apc.PermissionClaim.Verbs) == 1 && apc.PermissionClaim.Verbs[0] == "*" && apc.Selector.MatchAll {
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

// Convert_v1alpha2_ScopedPermissionClaims_To_v1alpha1_PermissionClaims converts v1alpha2.ScopedPermissionClaims
// to v1alpha1.PermissionClaims. This is not a lossless conversion, verbs and selectors are lost in this conversion.
// For lossless conversion use Convert_v1alpha2_APIBinding_To_v1alpha1_APIBinding.
func Convert_v1alpha2_ScopedPermissionClaims_To_v1alpha1_PermissionClaims(in []ScopedPermissionClaim, s kubeconversion.Scope) (out []apisv1alpha1.PermissionClaim, overhanging []ScopedPermissionClaim, err error) {
	for _, spc := range in {
		if len(spc.PermissionClaim.Verbs) == 1 && spc.PermissionClaim.Verbs[0] == "*" && spc.Selector.MatchAll {
			var v1pc apisv1alpha1.PermissionClaim

			if err := Convert_v1alpha2_ScopedPermissionClaim_To_v1alpha1_PermissionClaim(&spc, &v1pc, s); err != nil {
				return nil, nil, err
			}

			out = append(out, v1pc)
		} else {
			overhanging = append(overhanging, spc)
		}
	}

	return
}

// Convert_v1alpha2_AcceptablePermissionClaim_To_v1alpha1_AcceptablePermissionClaim converts v1alpha2.AcceptablePermissionClaim
// to v1alpha1.AcceptablePermissionClaim. This is not a lossless conversion, selectors are lost in this conversion.
// For lossless conversion use Convert_v1alpha2_APIBinding_To_v1alpha1_APIBinding.
func Convert_v1alpha2_AcceptablePermissionClaim_To_v1alpha1_AcceptablePermissionClaim(in *AcceptablePermissionClaim, out *apisv1alpha1.AcceptablePermissionClaim, s kubeconversion.Scope) error {
	if err := Convert_v1alpha2_ScopedPermissionClaim_To_v1alpha1_PermissionClaim(&in.ScopedPermissionClaim, &out.PermissionClaim, s); err != nil {
		return err
	}
	out.State = apisv1alpha1.AcceptablePermissionClaimState(in.State)
	return nil
}

// Convert_v1alpha2_ScopedPermissionClaim_To_v1alpha1_PermissionClaim converts v1alhpa2.ScopedPermissionClaim to v1alpha1.PermissionClaim.
// This is not a lossless conversion, for lossless conversion use Convert_v1alpha2_APIBinding_To_v1alpha1_APIBinding.
func Convert_v1alpha2_ScopedPermissionClaim_To_v1alpha1_PermissionClaim(in *ScopedPermissionClaim, out *apisv1alpha1.PermissionClaim, s kubeconversion.Scope) error {
	if err := Convert_v1alpha2_PermissionClaim_To_v1alpha1_PermissionClaim(&in.PermissionClaim, out, s); err != nil {
		return err
	}
	out.All = in.Selector.MatchAll
	return nil
}

// v1alpha1 -> v1alpha2 conversions.

func Convert_v1alpha1_APIBinding_To_v1alpha2_APIBinding(in *apisv1alpha1.APIBinding, out *APIBinding, s kubeconversion.Scope) error {
	out.ObjectMeta = *in.ObjectMeta.DeepCopy()

	if err := Convert_v1alpha1_APIBindingSpec_To_v1alpha2_APIBindingSpec(&in.Spec, &out.Spec, s); err != nil {
		return err
	}

	// Store v1alpha1 acceptable permission claims in annotation. This is necessary for a clean conversion of
	// ResourceSelector, which went away in v1alpha2.
	_, overhangingV1PC, err := Convert_v1alpha1_AcceptablePermissionClaims_To_v1alpha2_AcceptablePermissionClaims(in.Spec.PermissionClaims, s)
	if err != nil {
		return err
	}
	if len(overhangingV1PC) > 0 {
		if out.Annotations == nil {
			out.Annotations = make(map[string]string)
		}
		encoded, err := json.Marshal(overhangingV1PC)
		if err != nil {
			return err
		}
		out.Annotations[PermissionClaimsV1Alpha1Annotation] = string(encoded)
	}

	// Store v1alpha2 permission claims in annotation. This is necessary for a clean conversion of
	// verbs and label selectors, which were not existing in v1alpha1.
	if overhangingAPC, ok := in.Annotations[AcceptablePermissionClaimsAnnotation]; ok {
		acceptablePermissionClaims := []AcceptablePermissionClaim{}
		if err := json.Unmarshal([]byte(overhangingAPC), &acceptablePermissionClaims); err != nil {
			return fmt.Errorf("failed to decode claims from JSON: %w", err)
		}

		for _, pc := range acceptablePermissionClaims {
			for i, opc := range out.Spec.PermissionClaims {
				if pc.EqualGRI(opc.PermissionClaim) {
					out.Spec.PermissionClaims[i].PermissionClaim.Verbs = pc.Verbs
					out.Spec.PermissionClaims[i].Selector = pc.Selector
				}
			}
		}

		delete(out.Annotations, AcceptablePermissionClaimsAnnotation)

		// Make tests for equality easier to write by turning []string into nil.
		if len(out.Annotations) == 0 {
			out.Annotations = nil
		}
	}

	for i, opc := range out.Spec.PermissionClaims {
		if len(opc.PermissionClaim.Verbs) == 0 {
			out.Spec.PermissionClaims[i].PermissionClaim.Verbs = []string{"*"}
		}
		// This is handling a special case where PermissionClaim had ResourceSelector in v1alpha1.
		// That field doesn't exist in v1alpha2 and it always resulted in `MatchAll = true` behavior,
		// so we set it here explicitly.
		if !opc.Selector.MatchAll && len(opc.Selector.MatchLabels) == 0 && len(opc.Selector.MatchExpressions) == 0 {
			out.Spec.PermissionClaims[i].Selector.MatchAll = true
		}
	}

	if err := Convert_v1alpha1_APIBindingStatus_To_v1alpha2_APIBindingStatus(&in.Status, &out.Status, s); err != nil {
		return err
	}

	// Store permission claims in annotation. This is necessary for a clean conversion of
	// fields All and FieldSelector, which went away in v1alpha2.
	if len(in.Status.ExportPermissionClaims) > 0 {
		if out.Annotations == nil {
			out.Annotations = make(map[string]string)
		}
		encoded, err := json.Marshal(in.Status.ExportPermissionClaims)
		if err != nil {
			return err
		}
		out.Annotations[StatusPermissionClaimsV1Alpha1Annotation] = string(encoded)
	}

	// If there are any v1alpha1.PermissionClaims in status.AppliedPermissionClaims that are not compatible with v1alpha2.PermissionClaims
	// (this is essentially only a resource selector), then we need to store them in an annotation for round-trips.
	_, overhangingV1AppliedPC, err := Convert_v1alpha1_PermissionClaims_To_v1alpha2_PermissionClaims(in.Status.AppliedPermissionClaims, s)
	if err != nil {
		return err
	}
	if len(overhangingV1AppliedPC) > 0 {
		if out.Annotations == nil {
			out.Annotations = make(map[string]string)
		}
		encoded, err := json.Marshal(overhangingV1AppliedPC)
		if err != nil {
			return err
		}
		out.Annotations[StatusAppliedClaimsV1Alpha1Annotation] = string(encoded)
	}

	// If we stored status permission claims in annotations during the v1alpha2->v1alpha1 conversion,
	// we restore the verbs here.
	if overhangingStatusPC, ok := in.Annotations[StatusPermissionClaimsAnnotation]; ok {
		permissionClaims := []PermissionClaim{}

		if err := json.Unmarshal([]byte(overhangingStatusPC), &permissionClaims); err != nil {
			return fmt.Errorf("failed to decode claims from JSON: %w", err)
		}

		for _, pc := range permissionClaims {
			for i, opc := range out.Status.ExportPermissionClaims {
				if pc.EqualGRI(opc) {
					out.Status.ExportPermissionClaims[i].Verbs = pc.Verbs
				}
			}
		}

		delete(out.Annotations, StatusPermissionClaimsAnnotation)
	}

	// If we stored status applied permission claims in annotations during the v1alpha2->v1alpha1 conversion,
	// we restore both verbs and selector here.
	if overhangingStatusAPC, ok := in.Annotations[StatusAppliedClaimsAnnotation]; ok {
		permissionClaims := []ScopedPermissionClaim{}

		if err := json.Unmarshal([]byte(overhangingStatusAPC), &permissionClaims); err != nil {
			return fmt.Errorf("failed to decode claims from JSON: %w", err)
		}

		for _, pc := range permissionClaims {
			for i, opc := range out.Status.AppliedPermissionClaims {
				if pc.EqualGRI(opc.PermissionClaim) {
					out.Status.AppliedPermissionClaims[i].Verbs = pc.Verbs
					out.Status.AppliedPermissionClaims[i].Selector = pc.Selector
				}
			}
		}

		delete(out.Annotations, StatusAppliedClaimsAnnotation)
	}

	// If the export permission claims in status do not have a verb yet,
	// they should default to "*" as verb.
	for i, pc := range out.Status.ExportPermissionClaims {
		if len(pc.Verbs) == 0 {
			out.Status.ExportPermissionClaims[i].Verbs = []string{"*"}
		}
	}

	for i, spc := range out.Status.AppliedPermissionClaims {
		// Same here, we are defaulting the verbs to "*" if none were set.
		if len(spc.PermissionClaim.Verbs) == 0 {
			out.Status.AppliedPermissionClaims[i].PermissionClaim.Verbs = []string{"*"}
		}
		// This is handling a special case where PermissionClaim had ResourceSelector in v1alpha1.
		// That field doesn't exist in v1alpha2 and it always resulted in `MatchAll = true` behavior,
		// so we set it here explicitly.
		if !spc.Selector.MatchAll && len(spc.Selector.MatchLabels) == 0 && len(spc.Selector.MatchExpressions) == 0 {
			out.Status.AppliedPermissionClaims[i].Selector.MatchAll = true
		}
	}

	// Make tests for equality easier to write by turning []string into nil.
	if len(out.Annotations) == 0 {
		out.Annotations = nil
	}

	return nil
}

// Convert_v1alpha1_AcceptablePermissionClaims_To_v1alpha2_AcceptablePermissionClaims converts []v1alpha1.AcceptablePermissionClaim
// to []v1alpha2.AcceptablePermissionClaim. This is not a lossless conversion, for lossless conversion use
// Convert_v1alpha1_APIBinding_To_v1alpha2_APIBinding.
func Convert_v1alpha1_AcceptablePermissionClaims_To_v1alpha2_AcceptablePermissionClaims(in []apisv1alpha1.AcceptablePermissionClaim, s kubeconversion.Scope) (out []AcceptablePermissionClaim, overhanging []apisv1alpha1.AcceptablePermissionClaim, err error) {
	for _, pc := range in {
		if len(pc.ResourceSelector) > 0 {
			overhanging = append(overhanging, pc)
		} else {
			var v2pc AcceptablePermissionClaim
			if err := Convert_v1alpha1_AcceptablePermissionClaim_To_v1alpha2_AcceptablePermissionClaim(&pc, &v2pc, s); err != nil {
				return nil, nil, err
			}
			out = append(out, v2pc)
		}
	}

	return out, overhanging, nil
}

// Convert_v1alpha1_PermissionClaims_To_v1alpha2_PermissionClaims converts []v1alpha1.PermissionClaim
// to []v1alpha2.PermissionClaim. This is not a lossless conversion, for lossless conversion use
// Convert_v1alpha1_APIBinding_To_v1alpha2_APIBinding.
func Convert_v1alpha1_PermissionClaims_To_v1alpha2_PermissionClaims(in []apisv1alpha1.PermissionClaim, s kubeconversion.Scope) (out []PermissionClaim, overhanging []apisv1alpha1.PermissionClaim, err error) {
	for _, pc := range in {
		if len(pc.ResourceSelector) > 0 {
			overhanging = append(overhanging, pc)
		} else {
			var v2pc PermissionClaim
			if err := Convert_v1alpha1_PermissionClaim_To_v1alpha2_PermissionClaim(&pc, &v2pc, s); err != nil {
				return nil, nil, err
			}
			out = append(out, v2pc)
		}
	}

	return out, overhanging, nil
}

// Convert_v1alpha1_AcceptablePermissionClaim_To_v1alpha2_AcceptablePermissionClaim converts v1alpha1.AcceptablePermissionClaim
// to v1alpha2.AcceptablePermissionClaim. This is not a lossless conversion, for lossless conversion use
// Convert_v1alpha1_APIBinding_To_v1alpha2_APIBinding.
func Convert_v1alpha1_AcceptablePermissionClaim_To_v1alpha2_AcceptablePermissionClaim(in *apisv1alpha1.AcceptablePermissionClaim, out *AcceptablePermissionClaim, s kubeconversion.Scope) error {
	if err := Convert_v1alpha1_PermissionClaim_To_v1alpha2_ScopedPermissionClaim(&in.PermissionClaim, &out.ScopedPermissionClaim, s); err != nil {
		return err
	}
	out.State = AcceptablePermissionClaimState(in.State)

	return nil
}

// Convert_v1alpha1_PermissionClaim_To_v1alpha2_ScopedPermissionClaim converts v1alpha1.PermissionClaim
// to v1alpha2.PermissionClaim. This is not a lossless conversion, for lossless conversion use
// Convert_v1alpha1_APIBinding_To_v1alpha2_APIBinding.
func Convert_v1alpha1_PermissionClaim_To_v1alpha2_ScopedPermissionClaim(in *apisv1alpha1.PermissionClaim, out *ScopedPermissionClaim, s kubeconversion.Scope) error {
	if err := Convert_v1alpha1_PermissionClaim_To_v1alpha2_PermissionClaim(in, &out.PermissionClaim, s); err != nil {
		return err
	}
	out.Selector.MatchAll = in.All

	return nil
}
