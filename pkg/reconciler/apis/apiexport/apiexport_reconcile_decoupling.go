/*
Copyright 2026 The kcp Authors.

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
	"fmt"
	"sort"
	"strings"
	"sync"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/kcp-dev/sdk/apis/apis"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	conditionsv1alpha1 "github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"

	configrootphase0 "github.com/kcp-dev/kcp/config/root-phase0"
	"github.com/kcp-dev/kcp/pkg/identity"
	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/reconciler/apis/apibinding"
)

const (
	// WellKnownIdentityPrefix prefixes the well-known identities of kcp's core
	// exports in the root workspace on installations created with identity
	// decoupling enabled (enhancements KEP 0008). No user logical cluster is
	// named "system", so no owner-prefixed identity can collide with them.
	WellKnownIdentityPrefix = "system-"

	// ClaimGrantVerb is the verb an RBAC claim grant checks in the granting
	// export's workspace.
	ClaimGrantVerb = "claim"
	// ClaimGrantUserPrefix prefixes the user name a claiming export is
	// represented by when an RBAC claim grant is evaluated:
	// "apis.kcp.io:export:<logical cluster name>:<export name>".
	ClaimGrantUserPrefix = "apis.kcp.io:export:"
	// ClaimGrantGroup is the group every claiming export carries when an RBAC
	// claim grant is evaluated.
	ClaimGrantGroup = "apis.kcp.io:exports"

	identityPinInvalidReason = "IdentityPinInvalid"
)

var (
	coreExportNamesOnce sync.Once
	coreExportNames     sets.Set[string]
)

// isCoreExport reports whether the export is one of kcp's core exports in
// the root workspace.
func isCoreExport(apiExport *apisv1alpha2.APIExport) bool {
	if logicalcluster.From(apiExport) != core.RootCluster {
		return false
	}
	coreExportNamesOnce.Do(func() {
		coreExportNames = sets.New(configrootphase0.CoreAPIExportNames()...)
	})
	return coreExportNames.Has(apiExport.Name)
}

// WellKnownIdentity returns the well-known identity of a core export.
func WellKnownIdentity(exportName string) string {
	return WellKnownIdentityPrefix + exportName
}

// reconcile runs the identity (and endpoint slice) reconciliation, then the
// decoupling bookkeeping that is independent of how the identity was
// assigned: the schema and storage prefix in effect for every resource, and
// the resolution of permission claims that name no identity.
func (c *controller) reconcile(ctx context.Context, apiExport *apisv1alpha2.APIExport) error {
	err := c.reconcileIdentityAndSlice(ctx, apiExport)
	c.reconcileResources(ctx, apiExport)
	c.reconcileClaims(ctx, apiExport)
	return err
}

// reconcileDecoupledIdentity assigns and validates the export's identity
// under the APIIdentityDecoupling feature gate (enhancements KEP 0008). The
// identity is an opaque, owner-prefixed identifier assigned once; no
// identity secret is created or read for it.
//
//   - An export that already has an identity keeps it untouched, whatever
//     its secret says.
//   - A core export in the root workspace gets its well-known identity.
//   - spec.identity.value pins the identity, provided it carries the
//     export's own cluster prefix and no other export holds it.
//   - An export with a pre-created secretRef is hashed from the secret one
//     last time, so flows that pre-create secrets to pin an identity keep
//     landing on it during the deprecation window.
//   - Otherwise a fresh owner-prefixed identity is generated.
func (c *controller) reconcileDecoupledIdentity(ctx context.Context, clusterName logicalcluster.Name, apiExport *apisv1alpha2.APIExport) error {
	if apiExport.Status.IdentityHash != "" {
		if apiExport.Status.Identity == nil {
			apiExport.Status.Identity = &apisv1alpha2.IdentityStatus{Source: apisv1alpha2.IdentitySourceLegacy}
		}
		conditions.MarkTrue(apiExport, apisv1alpha2.APIExportIdentityValid)
		return nil
	}

	switch {
	case isCoreExport(apiExport):
		apiExport.Status.IdentityHash = WellKnownIdentity(apiExport.Name)
		apiExport.Status.Identity = &apisv1alpha2.IdentityStatus{Source: apisv1alpha2.IdentitySourceWellKnown}

	case apiExport.Spec.Identity != nil && apiExport.Spec.Identity.Value != "":
		value := apiExport.Spec.Identity.Value
		if !strings.HasPrefix(value, clusterName.String()+"-") {
			conditions.MarkFalse(apiExport, apisv1alpha2.APIExportIdentityValid, identityPinInvalidReason, conditionsv1alpha1.ConditionSeverityError,
				"spec.identity.value %q must be prefixed with this export's logical cluster name %q followed by \"-\"", value, clusterName)
			return nil
		}
		holders, err := c.getAPIExportsByIdentity(value)
		if err != nil {
			return err
		}
		for _, holder := range holders {
			if logicalcluster.From(holder) != clusterName || holder.Name != apiExport.Name {
				conditions.MarkFalse(apiExport, apisv1alpha2.APIExportIdentityValid, identityPinInvalidReason, conditionsv1alpha1.ConditionSeverityError,
					"spec.identity.value %q is held by APIExport %s|%s", value, logicalcluster.From(holder), holder.Name)
				return nil
			}
		}
		apiExport.Status.IdentityHash = value
		apiExport.Status.Identity = &apisv1alpha2.IdentityStatus{Source: apisv1alpha2.IdentitySourcePinned}

	case apiExport.Spec.Identity != nil && apiExport.Spec.Identity.SecretRef != nil: //nolint:staticcheck // secretRef is honored during its deprecation window
		if err := c.updateOrVerifyIdentitySecretHash(ctx, clusterName, apiExport); err != nil {
			conditions.MarkFalse(apiExport, apisv1alpha2.APIExportIdentityValid, apisv1alpha2.IdentityVerificationFailedReason, conditionsv1alpha1.ConditionSeverityError, "%v", err)
			return nil
		}
		apiExport.Status.Identity = &apisv1alpha2.IdentityStatus{Source: apisv1alpha2.IdentitySourceLegacy}
		return nil

	default:
		id, err := identity.GenerateIdentity(clusterName)
		if err != nil {
			conditions.MarkFalse(apiExport, apisv1alpha2.APIExportIdentityValid, apisv1alpha2.IdentityGenerationFailedReason, conditionsv1alpha1.ConditionSeverityError, "Error generating identity: %v", err)
			return err
		}
		apiExport.Status.IdentityHash = id
		apiExport.Status.Identity = &apisv1alpha2.IdentityStatus{Source: apisv1alpha2.IdentitySourceGenerated}
	}
	conditions.MarkTrue(apiExport, apisv1alpha2.APIExportIdentityValid)
	return nil
}

// reconcileResources records, per resource of the export, the schema and
// the storage prefix in effect, and the StorageShared and ResourcesValid
// conditions. For crd storage both are the export's own; for adopted storage
// they are the source export's, provided the source exists and consents.
func (c *controller) reconcileResources(_ context.Context, apiExport *apisv1alpha2.APIExport) {
	resources := make([]apisv1alpha2.ResourceStatus, 0, len(apiExport.Spec.Resources))
	storage := make([]apisv1alpha2.ResourceStorageStatus, 0, len(apiExport.Spec.Resources))
	shared := false
	var problems []string
	exportCluster := logicalcluster.From(apiExport)

	for _, rs := range apiExport.Spec.Resources {
		gr := apisv1alpha2.GroupResource{Group: rs.Group, Resource: rs.Name}
		switch {
		case rs.Storage.Adopted != nil:
			shared = true
			from := rs.Storage.Adopted.From
			path := logicalcluster.NewPath(from.Path)
			if path.Empty() {
				path = exportCluster.Path()
			}
			source, err := c.getAPIExportByPath(path, from.Name)
			if err != nil {
				problems = append(problems, fmt.Sprintf("%s.%s: source APIExport %s|%s: %v", rs.Name, rs.Group, path, from.Name, err))
				continue
			}
			var sourceResource *apisv1alpha2.ResourceSchema
			for i := range source.Spec.Resources {
				if source.Spec.Resources[i].Group == rs.Group && source.Spec.Resources[i].Name == rs.Name {
					sourceResource = &source.Spec.Resources[i]
				}
			}
			switch {
			case sourceResource == nil || sourceResource.Storage.CRD == nil:
				problems = append(problems, fmt.Sprintf("%s.%s: source APIExport %s|%s does not serve it with crd storage", rs.Name, rs.Group, path, from.Name))
				continue
			case !apibinding.SharedWith(sourceResource.Storage.CRD.SharedWith, apiExport):
				problems = append(problems, fmt.Sprintf("%s.%s: source APIExport %s|%s does not list this export in crd.sharedWith", rs.Name, rs.Group, path, from.Name))
				continue
			case rs.Schema != "" && rs.Schema != sourceResource.Schema:
				problems = append(problems, fmt.Sprintf("%s.%s: schema %q must be the source's schema %q or empty", rs.Name, rs.Group, rs.Schema, sourceResource.Schema))
				continue
			case source.Status.IdentityHash == "":
				problems = append(problems, fmt.Sprintf("%s.%s: source APIExport %s|%s has no identity yet", rs.Name, rs.Group, path, from.Name))
				continue
			}
			resources = append(resources, apisv1alpha2.ResourceStatus{GroupResource: gr, Schema: sourceResource.Schema, SchemaCluster: logicalcluster.From(source).String()})
			storage = append(storage, apisv1alpha2.ResourceStorageStatus{GroupResource: gr, Keys: []string{source.Status.IdentityHash}})

		case rs.Storage.CRD != nil:
			if len(rs.Storage.CRD.SharedWith) > 0 {
				shared = true
			}
			resources = append(resources, apisv1alpha2.ResourceStatus{GroupResource: gr, Schema: rs.Schema, SchemaCluster: exportCluster.String()})
			if apiExport.Status.IdentityHash != "" {
				storage = append(storage, apisv1alpha2.ResourceStorageStatus{GroupResource: gr, Keys: []string{apiExport.Status.IdentityHash}})
			}

		default:
			resources = append(resources, apisv1alpha2.ResourceStatus{GroupResource: gr, Schema: rs.Schema, SchemaCluster: exportCluster.String()})
		}
	}

	apiExport.Status.Resources = resources
	if len(storage) > 0 {
		apiExport.Status.Storage = &apisv1alpha2.StorageStatus{Resources: storage}
	} else {
		apiExport.Status.Storage = nil
	}

	if len(problems) > 0 {
		sort.Strings(problems)
		reason := apisv1alpha2.StorageSourceInvalidReason
		if strings.Contains(problems[0], "crd.sharedWith") {
			reason = apisv1alpha2.StorageNotSharedReason
		}
		conditions.MarkFalse(apiExport, apisv1alpha2.ResourcesValid, reason, conditionsv1alpha1.ConditionSeverityError, "%s", strings.Join(problems, "; "))
	} else {
		conditions.MarkTrue(apiExport, apisv1alpha2.ResourcesValid)
	}
	if shared {
		conditions.MarkTrue(apiExport, apisv1alpha2.StorageShared)
	} else {
		conditions.Delete(apiExport, apisv1alpha2.StorageShared)
	}
}

// grantMatch is a claim grant that applies to the reconciled export.
type grantMatch struct {
	export *apisv1alpha2.APIExport
	grant  apisv1alpha2.ClaimGrant
}

// reconcileClaims resolves every permission claim of the export that names no
// identity: through a grant of the claimed export (claimableBy), then through
// the export reference on the claim. The result is recorded in
// status.permissionClaims, which ResolvedPermissionClaims merges into the
// claims everything else matches against. Unresolved claims are inert.
func (c *controller) reconcileClaims(ctx context.Context, apiExport *apisv1alpha2.APIExport) {
	logger := klog.FromContext(ctx)
	resolved := make([]apisv1alpha2.ResolvedPermissionClaim, 0, len(apiExport.Spec.PermissionClaims))
	var unresolved, ambiguous, aliased []string

	for _, pc := range apiExport.Spec.PermissionClaims {
		gr := apisv1alpha2.GroupResource{Group: pc.Group, Resource: pc.Resource}
		switch {
		case pc.IdentityHash != "":
			entry := apisv1alpha2.ResolvedPermissionClaim{GroupResource: gr, IdentityHash: pc.IdentityHash, ResolvedBy: apisv1alpha2.ClaimResolvedByIdentityHash}
			if canonical := c.canonicalIdentityHash(pc.IdentityHash, pc.Group, pc.Resource); canonical != pc.IdentityHash {
				// the claim names an alias of a converted identity; the
				// identity stays as named (matching normalizes it) but the
				// owner is told to update before the alias is retired.
				aliased = append(aliased, fmt.Sprintf("%s.%s: %s -> %s", pc.Resource, pc.Group, pc.IdentityHash, canonical))
			}
			resolved = append(resolved, entry)
			continue
		case c.isBuiltIn(pc.GroupResource) || pc.Group == apis.GroupName:
			resolved = append(resolved, apisv1alpha2.ResolvedPermissionClaim{GroupResource: gr, ResolvedBy: apisv1alpha2.ClaimResolvedBuiltIn})
			continue
		}

		// subresource claims resolve with their parent resource.
		resource := pc.Resource
		if i := strings.Index(resource, "/"); i >= 0 {
			resource = resource[:i]
		}

		grants, err := c.grantsFor(ctx, apiExport, pc.Group, resource)
		if err != nil {
			logger.Error(err, "error evaluating claim grants", "claim", pc.String())
			unresolved = append(unresolved, fmt.Sprintf("%s.%s: %v", pc.Resource, pc.Group, err))
			continue
		}
		if pc.Export != nil && len(grants) > 1 {
			// the export reference disambiguates among several granting exports.
			filtered := grants[:0]
			for _, g := range grants {
				if g.export.Name == pc.Export.Name && c.exportAtPath(g.export, pc.Export.Path) {
					filtered = append(filtered, g)
				}
			}
			grants = filtered
		}
		switch {
		case len(grants) == 1:
			g := grants[0]
			entry := apisv1alpha2.ResolvedPermissionClaim{GroupResource: gr, IdentityHash: g.export.Status.IdentityHash, ResolvedBy: apisv1alpha2.ClaimResolvedByGrant, Delegated: g.grant.Delegated}
			if len(g.grant.Verbs) > 0 {
				capped := sets.New(pc.Verbs...)
				if capped.Has("*") {
					capped = sets.New(g.grant.Verbs...)
				} else {
					capped = capped.Intersection(sets.New(g.grant.Verbs...))
				}
				entry.Verbs = sets.List(capped)
			}
			resolved = append(resolved, entry)
		case len(grants) > 1:
			names := make([]string, 0, len(grants))
			for _, g := range grants {
				names = append(names, logicalcluster.From(g.export).String()+"|"+g.export.Name)
			}
			sort.Strings(names)
			ambiguous = append(ambiguous, fmt.Sprintf("%s.%s is granted by several exports: %s; name one with export", pc.Resource, pc.Group, strings.Join(names, ", ")))
		case pc.Export != nil:
			path := logicalcluster.NewPath(pc.Export.Path)
			if path.Empty() {
				path = logicalcluster.From(apiExport).Path()
			}
			claimed, err := c.getAPIExportByPath(path, pc.Export.Name)
			if err != nil {
				unresolved = append(unresolved, fmt.Sprintf("%s.%s: APIExport %s|%s: %v", pc.Resource, pc.Group, path, pc.Export.Name, err))
				continue
			}
			if claimed.Status.IdentityHash == "" {
				unresolved = append(unresolved, fmt.Sprintf("%s.%s: APIExport %s|%s has no identity yet", pc.Resource, pc.Group, path, pc.Export.Name))
				continue
			}
			resolved = append(resolved, apisv1alpha2.ResolvedPermissionClaim{GroupResource: gr, IdentityHash: claimed.Status.IdentityHash, ResolvedBy: apisv1alpha2.ClaimResolvedByExport})
		default:
			unresolved = append(unresolved, fmt.Sprintf("%s.%s: no identityHash, no export and no grant", pc.Resource, pc.Group))
		}
	}

	apiExport.Status.PermissionClaims = resolved
	switch {
	case len(ambiguous) > 0:
		conditions.MarkFalse(apiExport, apisv1alpha2.ClaimsResolved, apisv1alpha2.ClaimAmbiguousReason, conditionsv1alpha1.ConditionSeverityError, "%s", strings.Join(ambiguous, "; "))
	case len(unresolved) > 0:
		conditions.MarkFalse(apiExport, apisv1alpha2.ClaimsResolved, apisv1alpha2.ClaimUnresolvedReason, conditionsv1alpha1.ConditionSeverityWarning, "%s", strings.Join(unresolved, "; "))
	default:
		conditions.MarkTrue(apiExport, apisv1alpha2.ClaimsResolved)
	}
	if len(aliased) > 0 {
		conditions.MarkFalse(apiExport, apisv1alpha2.ClaimIdentityAliased, "AliasReferenced", conditionsv1alpha1.ConditionSeverityWarning,
			"permission claims reference identity aliases that will be retired; update them: %s", strings.Join(aliased, "; "))
	} else {
		conditions.Delete(apiExport, apisv1alpha2.ClaimIdentityAliased)
	}
}

// exportAtPath reports whether the export lives at the given workspace path,
// by cluster name or canonical path. An empty path never matches.
func (c *controller) exportAtPath(export *apisv1alpha2.APIExport, path string) bool {
	if path == "" {
		return false
	}
	return path == logicalcluster.From(export).String() || path == export.Annotations[core.LogicalClusterPathAnnotationKey]
}

// grantsFor returns the exports granting the reconciled export a claim on the
// group/resource, one entry per granting export (the first matching grant of
// an export wins).
func (c *controller) grantsFor(ctx context.Context, apiExport *apisv1alpha2.APIExport, group, resource string) ([]grantMatch, error) {
	cluster := logicalcluster.From(apiExport)
	seen := sets.New[string]()
	var matches []grantMatch
	add := func(export *apisv1alpha2.APIExport, grant apisv1alpha2.ClaimGrant) {
		key := logicalcluster.From(export).String() + "|" + export.Name
		if seen.Has(key) || export.Status.IdentityHash == "" {
			return
		}
		seen.Insert(key)
		matches = append(matches, grantMatch{export: export, grant: grant})
	}

	// explicit grants, by cluster name and by canonical path
	keys := []string{indexers.ClaimGrantExportKey(cluster.String(), apiExport.Name, group, resource)}
	if canonical := apiExport.Annotations[core.LogicalClusterPathAnnotationKey]; canonical != "" && canonical != cluster.String() {
		keys = append(keys, indexers.ClaimGrantExportKey(canonical, apiExport.Name, group, resource))
	}
	for _, key := range keys {
		exports, err := c.listAPIExportsByClaimGrant(key)
		if err != nil {
			return nil, err
		}
		for _, export := range exports {
			for _, rs := range export.Spec.Resources {
				if rs.Group != group || rs.Name != resource {
					continue
				}
				for _, grant := range rs.ClaimableBy {
					if grant.Export != nil && grant.Export.Name == apiExport.Name && (grant.Export.Path == cluster.String() || c.exportAtPath(apiExport, grant.Export.Path)) {
						add(export, grant)
					}
				}
			}
		}
	}

	// selector grants
	exports, err := c.listAPIExportsByClaimGrant(indexers.ClaimGrantSelectorKey(group, resource))
	if err != nil {
		return nil, err
	}
	for _, export := range exports {
		for _, rs := range export.Spec.Resources {
			if rs.Group != group || rs.Name != resource {
				continue
			}
			for _, grant := range rs.ClaimableBy {
				if grant.ExportSelector == nil {
					continue
				}
				selector, err := metav1.LabelSelectorAsSelector(grant.ExportSelector)
				if err != nil {
					continue
				}
				if selector.Matches(labels.Set(apiExport.Labels)) {
					add(export, grant)
				}
			}
		}
	}

	// RBAC grants: the claiming export, represented as
	// apis.kcp.io:export:<cluster>:<name>, must be allowed the "claim" verb on
	// the granting export in the granting export's workspace.
	exports, err = c.listAPIExportsByClaimGrant(indexers.ClaimGrantRBACKey(group, resource))
	if err != nil {
		return nil, err
	}
	for _, export := range exports {
		for _, rs := range export.Spec.Resources {
			if rs.Group != group || rs.Name != resource {
				continue
			}
			for _, grant := range rs.ClaimableBy {
				if grant.RBAC == nil {
					continue
				}
				allowed, err := c.rbacGrantAllows(ctx, export, apiExport)
				if err != nil {
					return nil, err
				}
				if allowed {
					add(export, grant)
				}
			}
		}
	}
	return matches, nil
}

// rbacGrantAllows evaluates an RBAC claim grant: a SubjectAccessReview in the
// granting export's workspace for the claiming export's synthetic user.
func (c *controller) rbacGrantAllows(ctx context.Context, granting, claiming *apisv1alpha2.APIExport) (bool, error) {
	if c.newDeepSARAuthorizer == nil {
		return false, nil
	}
	authz, err := c.newDeepSARAuthorizer(logicalcluster.From(granting))
	if err != nil {
		return false, err
	}
	decision, _, err := authz.Authorize(ctx, authorizer.AttributesRecord{
		User: &user.DefaultInfo{
			Name:   ClaimGrantUserPrefix + logicalcluster.From(claiming).String() + ":" + claiming.Name,
			Groups: []string{ClaimGrantGroup},
		},
		Verb:            ClaimGrantVerb,
		APIGroup:        apis.GroupName,
		APIVersion:      "v1alpha2",
		Resource:        "apiexports",
		Name:            granting.Name,
		ResourceRequest: true,
	})
	if err != nil {
		if errors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return decision == authorizer.DecisionAllow, nil
}
