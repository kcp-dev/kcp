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

package apiexport

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/user"

	adminv1alpha1 "github.com/kcp-dev/sdk/apis/admin/v1alpha1"
	"github.com/kcp-dev/sdk/apis/apis"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	kcpinformers "github.com/kcp-dev/sdk/client/informers/externalversions"

	kcpinitializers "github.com/kcp-dev/kcp/pkg/admission/initializers"
	"github.com/kcp-dev/kcp/pkg/permissionclaim"
	builtinapiexport "github.com/kcp-dev/kcp/pkg/virtual/apiexport/schemas/builtin"
)

// PluginName is the name used to identify this admission webhook.
const PluginName = "apis.kcp.io/APIExport"

// Register registers the reserved name admission webhook.
func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName,
		func(_ io.Reader) (admission.Interface, error) {
			return NewAPIExportAdmission(builtinapiexport.IsBuiltInAPI), nil
		})
}

// APIExportAdmission is an admission plugin for checking APIExport validity.
//
// Besides the structural checks it enforces PermissionClaimPolicies, the
// installation-wide objects stored in the Admin workspace:
//
//   - a permission claim without an identityHash on a non-built-in group is
//     only allowed if a policy grants it to one of the API groups this
//     APIExport itself exports (the "claimer" group);
//   - an APIExport may only export resources in a group reserved by a policy
//     if the requesting user is one of that policy's providers. The check runs
//     when the set of reserved groups on the export grows, so unrelated
//     updates by less privileged principals keep working.
type APIExportAdmission struct {
	*admission.Handler

	isBuiltIn func(apis.GroupResource) bool

	// listPolicies returns every PermissionClaimPolicy known through the cache.
	// It is nil until the informer is wired, and returns ok=false until the
	// informer has synced, in which case identity-less claims are rejected and
	// reservation checks are skipped (see validatePolicies).
	listPolicies func() (policies []*adminv1alpha1.PermissionClaimPolicy, ok bool, err error)
}

var (
	_ = admission.ValidationInterface(&APIExportAdmission{})
	_ = kcpinitializers.WantsKcpInformers(&APIExportAdmission{})
)

// NewAPIExportAdmission constructs a new APIExportAdmission admission plugin.
func NewAPIExportAdmission(isBuiltIn func(apis.GroupResource) bool) *APIExportAdmission {
	return &APIExportAdmission{
		Handler:   admission.NewHandler(admission.Create, admission.Update),
		isBuiltIn: isBuiltIn,
	}
}

// SetKcpInformers wires the cache-backed PermissionClaimPolicy informer. Policies
// live only in the cache server (written through the Admin workspace), so the
// global factory is the one that sees them.
func (e *APIExportAdmission) SetKcpInformers(_, global kcpinformers.SharedInformerFactory) {
	informer := global.Admin().V1alpha1().PermissionClaimPolicies()
	synced := informer.Informer().HasSynced
	lister := informer.Lister()
	e.listPolicies = func() ([]*adminv1alpha1.PermissionClaimPolicy, bool, error) {
		if !synced() {
			return nil, false, nil
		}
		policies, err := lister.List(labels.Everything())
		return policies, true, err
	}
}

// Validate ensures that the APIExport is valid.
func (e *APIExportAdmission) Validate(ctx context.Context, a admission.Attributes, _ admission.ObjectInterfaces) (err error) {
	if a.GetResource().GroupResource() != apisv1alpha2.Resource("apiexports") || a.GetKind().GroupKind() != apisv1alpha2.Kind("APIExport") {
		return nil
	}

	u, ok := a.GetObject().(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("unexpected type %T", a.GetObject())
	}

	var old *apisv1alpha2.APIExport
	if a.GetOperation() == admission.Update {
		if oldU, ok := a.GetOldObject().(*unstructured.Unstructured); ok {
			old, err = decodeAPIExport(oldU, a.GetKind().GroupVersion().Version)
			if err != nil {
				return fmt.Errorf("failed to decode old APIExport: %w", err)
			}
		}
	}

	switch a.GetKind().GroupVersion().Version {
	case apisv1alpha1.SchemeGroupVersion.Version:
		// v1alpha1 is deprecated, but we still need to support it for a while
		// for backward compatibility.
		// We have one non-shared validation, which checks if annotations, carrying overhanging
		// resource schemas, are set correctly.

		ae := &apisv1alpha1.APIExport{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, ae); err != nil {
			return fmt.Errorf("failed to convert unstructured to APIExport: %w", err)
		}

		// Before we convert to v1alpha2, we need to validate the annotations overhanging:
		if err := validateOverhangingResourceSchemas(ctx, a, ae); err != nil {
			return admission.NewForbidden(a, err)
		}
		if err := validateOverhangingPermissionClaims(ctx, a, ae); err != nil {
			return admission.NewForbidden(a, err)
		}

		v2 := new(apisv1alpha2.APIExport)
		err := apisv1alpha2.Convert_v1alpha1_APIExport_To_v1alpha2_APIExport(ae, v2, nil)
		if err != nil {
			return fmt.Errorf("failed to convert v1alpha1 APIExport to v1alpha2: %w", err)
		}

		if err := e.validatev1alpha2(ctx, a, v2, old); err != nil {
			return err
		}
	case apisv1alpha2.SchemeGroupVersion.Version:
		// v1alpha2 is the current version.
		ae := &apisv1alpha2.APIExport{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, ae); err != nil {
			return fmt.Errorf("failed to convert unstructured to APIExport: %w", err)
		}
		if err := e.validatev1alpha2(ctx, a, ae, old); err != nil {
			return err
		}

	default:
		return admission.NewForbidden(a,
			field.Invalid(
				field.NewPath("apiVersion"),
				a.GetKind().GroupVersion().String(),
				fmt.Sprintf("unsupported API version %s", a.GetKind().GroupVersion().String())))
	}
	return nil
}

// decodeAPIExport converts an unstructured APIExport of the given served
// version into the internal v1alpha2 shape.
func decodeAPIExport(u *unstructured.Unstructured, version string) (*apisv1alpha2.APIExport, error) {
	switch version {
	case apisv1alpha1.SchemeGroupVersion.Version:
		v1 := &apisv1alpha1.APIExport{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, v1); err != nil {
			return nil, err
		}
		v2 := &apisv1alpha2.APIExport{}
		if err := apisv1alpha2.Convert_v1alpha1_APIExport_To_v1alpha2_APIExport(v1, v2, nil); err != nil {
			return nil, err
		}
		return v2, nil
	case apisv1alpha2.SchemeGroupVersion.Version:
		v2 := &apisv1alpha2.APIExport{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, v2); err != nil {
			return nil, err
		}
		return v2, nil
	default:
		return nil, fmt.Errorf("unsupported API version %s", version)
	}
}

func (e *APIExportAdmission) validatev1alpha2(_ context.Context, a admission.Attributes, ae, old *apisv1alpha2.APIExport) (err error) {
	// A custom subresource entry is only meaningful next to the resource it hangs
	// off, so collect the exported resources before validating the entries.
	exported := sets.New[string]()
	for _, rs := range ae.Spec.Resources {
		if !rs.IsSubresource() {
			exported.Insert(rs.Group + "/" + rs.Name)
		}
	}

	for i, rs := range ae.Spec.Resources {
		if err := validateResourceSchema(rs, exported, field.NewPath("spec").Child("resources").Index(i)); err != nil {
			return admission.NewForbidden(a, err)
		}
	}

	return e.validatePolicies(a, ae, old)
}

// validatePolicies enforces PermissionClaimPolicies: identity-less claims and
// reserved API groups. Until the policy informer has synced, no policy is
// known: identity-less claims are rejected exactly as before this feature, and
// reservation checks are skipped because there is nothing to check against.
func (e *APIExportAdmission) validatePolicies(a admission.Attributes, ae, old *apisv1alpha2.APIExport) error {
	var policies []*adminv1alpha1.PermissionClaimPolicy
	if e.listPolicies != nil {
		var ok bool
		var err error
		policies, ok, err = e.listPolicies()
		if err != nil {
			return fmt.Errorf("error listing PermissionClaimPolicies: %w", err)
		}
		if !ok {
			policies = nil
		}
	}

	exportedGroups := groupsOf(ae)

	for i, pc := range ae.Spec.PermissionClaims {
		if !permissionclaim.IsIdentityAgnostic(pc, e.isBuiltIn) {
			continue
		}
		if !claimAllowed(policies, exportedGroups, pc.Group) {
			return admission.NewForbidden(a,
				field.Invalid(
					field.NewPath("spec").
						Child("permissionClaims").
						Index(i).
						Child("identityHash"),
					"",
					fmt.Sprintf("identityHash is required for API types that are not built-in, and no PermissionClaimPolicy allows an APIExport exporting %s to claim group %q without one", exportedGroupsForMessage(exportedGroups), pc.Group)))
		}
	}

	// Reserved groups: only check the groups newly added by this write, so an
	// update by a less privileged principal that does not touch reserved
	// groups keeps working.
	oldGroups := sets.New[string]()
	if old != nil {
		oldGroups = groupsOf(old)
	}
	for i, rs := range ae.Spec.Resources {
		group := rs.Group
		if oldGroups.Has(group) {
			continue
		}
		// Every policy reserving the group must admit the user. Requiring all
		// of them - rather than any one - means a second policy over the same
		// group can only narrow access, never widen it, which matches how
		// overlapping maximal permission policies compose.
		for _, policy := range policies {
			if !policy.Reserves(group) || isProvider(policy, a.GetUserInfo()) {
				continue
			}
			return admission.NewForbidden(a,
				field.Forbidden(
					field.NewPath("spec").Child("resources").Index(i).Child("group"),
					fmt.Sprintf("API group %q is reserved by PermissionClaimPolicy %q; only its providers may export it", group, policy.Name)))
		}
	}

	return nil
}

// groupsOf returns the API groups the export serves through its resources.
func groupsOf(ae *apisv1alpha2.APIExport) sets.Set[string] {
	groups := sets.New[string]()
	for _, rs := range ae.Spec.Resources {
		groups.Insert(rs.Group)
	}
	return groups
}

func exportedGroupsForMessage(groups sets.Set[string]) string {
	if groups.Len() == 0 {
		return "no API group"
	}
	return strings.Join(sets.List(groups), ", ")
}

// claimAllowed reports whether any policy lets an APIExport exporting one of
// the given claimer groups claim the target group without an identity hash.
func claimAllowed(policies []*adminv1alpha1.PermissionClaimPolicy, claimers sets.Set[string], group string) bool {
	for _, policy := range policies {
		for claimer := range claimers {
			if policy.Allows(claimer, group) {
				return true
			}
		}
	}
	return false
}

// isProvider reports whether the user matches one of the policy's providers.
func isProvider(policy *adminv1alpha1.PermissionClaimPolicy, u user.Info) bool {
	if u == nil {
		return false
	}
	for _, subject := range policy.Spec.Providers {
		switch subject.Kind {
		case adminv1alpha1.PermissionClaimPolicySubjectUser:
			if subject.Name == u.GetName() {
				return true
			}
		case adminv1alpha1.PermissionClaimPolicySubjectGroup:
			if slices.Contains(u.GetGroups(), subject.Name) {
				return true
			}
		}
	}
	return false
}

func validateResourceSchema(resourceSchema apisv1alpha2.ResourceSchema, exported sets.Set[string], path *field.Path) *field.Error {
	group := resourceSchema.Group
	if group == "" {
		group = "core"
	}

	resource, subresource := resourceSchema.SplitName()

	// The schema is named after whichever part of the entry it describes: a
	// subresource entry describes the subresource's own kind, not its parent's.
	schemaFor := resource
	if resourceSchema.IsSubresource() {
		schemaFor = subresource
	}

	// TODO(mjudeikis): Once v1alpha1 is removed, we can relax this if we chose to.
	// We should revisit this once we have a better understanding of the APIExport usage patterns.
	expectedSuffix := fmt.Sprintf(".%s.%s", schemaFor, group)
	if !strings.HasSuffix(resourceSchema.Schema, expectedSuffix) {
		return field.Invalid(path.Child("schema"), resourceSchema.Schema, fmt.Sprintf("must end in %s", expectedSuffix))
	}

	if !resourceSchema.IsSubresource() {
		return nil
	}

	// status and scale belong to the object's shape and are declared on the
	// APIResourceSchema, served by the parent's own storage. Allowing an export to
	// redeclare either would mean two different things answering for one path.
	if subresource == "status" || subresource == "scale" {
		return field.Invalid(path.Child("name"), resourceSchema.Name,
			"status and scale are declared on the APIResourceSchema, not as custom subresources")
	}

	// A subresource is always served remotely: there is no CustomResourceDefinition
	// for it to live in.
	if resourceSchema.Storage.CRD != nil || resourceSchema.Storage.Virtual == nil {
		return field.Invalid(path.Child("storage"), resourceSchema.Storage,
			"a custom subresource must use virtual storage")
	}
	if resourceSchema.Storage.Virtual.Reference.Kind == "" || resourceSchema.Storage.Virtual.Reference.Name == "" {
		return field.Required(path.Child("storage").Child("virtual").Child("reference"),
			"a custom subresource must name the object carrying its virtual workspace URL")
	}

	// Without its parent the entry describes a subresource of nothing, and would
	// be served under a resource this export does not offer.
	if !exported.Has(resourceSchema.Group + "/" + resource) {
		return field.Invalid(path.Child("name"), resourceSchema.Name,
			fmt.Sprintf("resource %q is not exported by this APIExport", resource))
	}

	return nil
}

func validateOverhangingResourceSchemas(_ context.Context, _ admission.Attributes, ae *apisv1alpha1.APIExport) error {
	// TODO(mjudeikis): Remove this once we are sure that all APIExport objects are
	// converted to v1alpha2.
	if _, ok := ae.Annotations[apisv1alpha2.ResourceSchemasAnnotation]; ok {
		// validate if we can decode overhanging resource schemas. If not, we will fail.
		var overhanging []apisv1alpha2.ResourceSchema //nolint:prealloc
		if err := json.Unmarshal([]byte(ae.Annotations[apisv1alpha2.ResourceSchemasAnnotation]), &overhanging); err != nil {
			return field.Invalid(field.NewPath("metadata").Child("annotations").Key(apisv1alpha2.ResourceSchemasAnnotation), ae.Annotations[apisv1alpha2.ResourceSchemasAnnotation], "failed to decode overhanging resource schemas")
		}

		// validate duplicates. We could have duplicates in annotations itself, or in spec + annotations.
		// We convert to v2 to check for duplicates.
		v2Schemas := make([]apisv1alpha2.ResourceSchema, len(ae.Spec.LatestResourceSchemas))
		err := apisv1alpha2.Convert_v1alpha1_LatestResourceSchema_To_v1alpha2_ResourceSchema(ae.Spec.LatestResourceSchemas, &v2Schemas)
		if err != nil {
			return field.Invalid(field.NewPath("spec").Child("latestResourceSchemas"), ae.Spec.LatestResourceSchemas, "failed to convert spec.LatestResourceSchema")
		}
		overhanging = append(overhanging, v2Schemas...)

		seen := map[string]struct{}{}
		for _, rs := range overhanging {
			if _, ok := seen[rs.Schema]; ok {
				return field.Invalid(field.NewPath("metadata").Child("annotations").Key(apisv1alpha2.ResourceSchemasAnnotation), ae.Annotations[apisv1alpha2.ResourceSchemasAnnotation], "duplicate resource schema")
			}
			seen[rs.Schema] = struct{}{}
		}
	}
	return nil
}

func validateOverhangingPermissionClaims(_ context.Context, _ admission.Attributes, ae *apisv1alpha1.APIExport) error {
	// TODO(xmudrii): Remove this once we are sure that all APIExport objects are
	// converted to v1alpha2.
	if _, ok := ae.Annotations[apisv1alpha2.PermissionClaimsAnnotation]; ok {
		// validate if we can decode overhanging permission claims. If not, we will fail.
		var overhanging []apisv1alpha2.PermissionClaim
		if err := json.Unmarshal([]byte(ae.Annotations[apisv1alpha2.PermissionClaimsAnnotation]), &overhanging); err != nil {
			return field.Invalid(field.NewPath("metadata").Child("annotations").Key(apisv1alpha2.PermissionClaimsAnnotation), ae.Annotations[apisv1alpha2.PermissionClaimsAnnotation], "failed to decode overhanging permission claims")
		}

		// validate mismatches. We could have mismatches between the spec and the annotation
		// (e.g. a resource present in the annotation, but not in the spec).
		// We convert to v2 to check for mismatches.
		v2Claims := make([]apisv1alpha2.PermissionClaim, len(ae.Spec.PermissionClaims))
		for i, v1pc := range ae.Spec.PermissionClaims {
			var v2pc apisv1alpha2.PermissionClaim
			err := apisv1alpha2.Convert_v1alpha1_PermissionClaim_To_v1alpha2_PermissionClaim(&v1pc, &v2pc, nil)
			if err != nil {
				return field.Invalid(field.NewPath("spec").Child("permissionClaims").Index(i), ae.Spec.PermissionClaims, "failed to convert spec.PermissionClaims")
			}
			v2Claims = append(v2Claims, v2pc)
		}

		for _, o := range overhanging {
			var found bool
			for _, pc := range v2Claims {
				if pc.EqualGRI(o) {
					found = true

					break
				}
			}
			if !found {
				return field.Invalid(field.NewPath("metadata").Child("annotations").Key(apisv1alpha2.PermissionClaimsAnnotation), ae.Annotations[apisv1alpha2.PermissionClaimsAnnotation], "permission claims defined in annotation do not match permission claims defined in spec")
			}
		}
	}
	return nil
}
