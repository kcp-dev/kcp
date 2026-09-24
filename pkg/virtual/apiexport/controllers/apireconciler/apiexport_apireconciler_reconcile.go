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

package apireconciler

import (
	"context"
	"fmt"
	"sort"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/kcp-dev/sdk/apis/apis"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/apis/v1alpha2/permissionclaims"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic/apidefinition"
	dynamiccontext "github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic/context"
	"github.com/kcp-dev/virtual-workspace-framework/pkg/forwardingregistry"

	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/virtual/apiexport/schemas"
	apiexportbuiltin "github.com/kcp-dev/kcp/pkg/virtual/apiexport/schemas/builtin"
)

func (c *APIReconciler) reconcile(ctx context.Context, apiExport *apisv1alpha2.APIExport, apiDomainKey dynamiccontext.APIDomainKey) error {
	logger := klog.FromContext(ctx)
	ctx = klog.NewContext(ctx, logger)

	if apiExport == nil || apiExport.Status.IdentityHash == "" {
		c.mutex.RLock()
		_, found := c.apiSets[apiDomainKey]
		c.mutex.RUnlock()

		if !found {
			logger.V(3).Info("no APIs found for API domain key")
			return nil
		}

		// remove the APIDomain
		c.mutex.Lock()
		defer c.mutex.Unlock()
		logger.V(2).Info("deleting APIs for API domain key")
		delete(c.apiSets, apiDomainKey)
		return nil
	}

	c.mutex.RLock()
	oldSet := c.apiSets[apiDomainKey]
	c.mutex.RUnlock()

	// Get schemas and identities for base api export.
	apiResourceSchemas, err := c.getSchemasFromAPIExport(ctx, apiExport)
	if err != nil {
		return err
	}
	identities := map[schema.GroupResource]forwardingregistry.IdentityHashesFunc{}
	sources := map[schema.GroupResource]subresourceSource{}
	for gr := range apiResourceSchemas {
		identities[gr] = staticIdentities(apiExport.Status.IdentityHash)
		sources[gr] = subresourceSource{export: apiExport, own: true}
	}

	// Custom subresource claims, keyed by the resource they hang off. A claim on
	// "widgets/frobnicate" builds no API of its own: it says that the entry
	// APIExport declares under "widgets" may be reached through this virtual
	// workspace, so it is read here and applied when "widgets" is built below.
	claimedSubresourceVerbs := map[schema.GroupResource]map[string][]string{}
	for _, pc := range apiExport.Spec.PermissionClaims {
		resource, subresource, isSubresource := strings.Cut(pc.Resource, "/")
		if !isSubresource || apisv1alpha2.IsSchemaOwnedSubresource(subresource) {
			continue
		}
		parent := schema.GroupResource{Group: pc.Group, Resource: resource}
		if claimedSubresourceVerbs[parent] == nil {
			claimedSubresourceVerbs[parent] = map[string][]string{}
		}
		claimedSubresourceVerbs[parent][subresource] = pc.Verbs
	}

	clusterName := logicalcluster.From(apiExport)

	// Find schemas for claimed resources
	claims := map[schema.GroupResource]apisv1alpha2.PermissionClaim{}
	claimsAPIBindings := false
	for _, pc := range apiExport.Spec.PermissionClaims {
		logger := logger.WithValues("claim", pc.String())
		logger.V(4).Info("evaluating claim")

		// claim references a subresource, the API comes from the parent resource claim
		if strings.Contains(pc.Resource, "/") {
			continue
		}

		// APIExport resources have priority over claimed resources
		gr := schema.GroupResource{Group: pc.Group, Resource: pc.Resource}
		if _, found := apiResourceSchemas[gr]; found {
			if otherClaim, found := claims[gr]; found {
				logger.Info("permission claim is shadowed by another claim", "claim", pc, "otherClaim", otherClaim)
				continue
			}

			logger.Info("permission claim is shadowed by exported resource", "claim", pc)
			continue
		}

		if apiexportbuiltin.IsBuiltInAPI(pc.GroupResource) {
			for _, targetGR := range claimedBuiltInGroupResources(pc) {
				if _, found := apiResourceSchemas[targetGR]; found {
					continue
				}

				schemaForClaim, err := builtInSchemaWithClusterAnnotation(clusterName, targetGR)
				if err != nil {
					return err
				}
				apiResourceSchemas[targetGR] = schemaForClaim
				claims[targetGR] = pc
			}

			continue
		}
		if pc.Group == apis.GroupName {
			apisSchema, found := schemas.ApisKcpDevSchemas[pc.Resource]
			if !found {
				logger.Info("permission claim is for an unknown resource", "claim", pc)
				continue
			}

			if pc.Resource == "apibindings" {
				claimsAPIBindings = true
			}

			apiResourceSchemas[gr] = apisSchema
			claims[gr] = pc
			continue
		}
		if pc.IdentityHash == "" {
			// Identity-agnostic claim (admitted through a PermissionClaimPolicy):
			// the identity is whatever each consumer workspace's APIBinding for
			// the claimed resource carries. Serve the resource as soon as one
			// consumer on this shard has accepted the claim and binds the
			// resource; the identity set itself is read per request.
			resolved, err := c.identityResolver.Resolve(apiExport, gr)
			if err != nil {
				return err
			}
			if len(resolved) == 0 {
				logger.V(4).Info("identity-agnostic permission claim has no consumer binding the claimed resource on this shard yet", "claim", pc)
				continue
			}

			var claimedSchema *apisv1alpha1.APIResourceSchema
			var claimedExport *apisv1alpha2.APIExport
			for _, identity := range resolved {
				claimedSchema, claimedExport, err = c.findClaimedSchema(klog.NewContext(ctx, logger), identity.IdentityHash, gr)
				if err != nil {
					return err
				}
				if claimedSchema != nil {
					break
				}
			}
			if claimedSchema == nil {
				logger.V(4).Info("no APIResourceSchema found for any identity of the identity-agnostic claim", "claim", pc)
				continue
			}

			apiResourceSchemas[gr] = claimedSchema
			claims[gr] = pc
			identities[gr] = c.dynamicIdentities(apiExport, gr)
			sources[gr] = subresourceSource{export: claimedExport, claimedVerbs: claimedSubresourceVerbs[gr]}
			continue
		}

		logger = logger.WithValues("identity", pc.IdentityHash)

		logger.V(4).Info("getting APIExports by identity")
		exports, err := c.apiExportIndexer.ByIndex(indexers.APIExportByIdentity, pc.IdentityHash)
		if err != nil {
			return err
		}

		logger.V(4).Info("got APIExports", "count", len(exports))

		// there might be multiple exports with the same identity hash all exporting the same GR.
		// This is fine. Same identity means same owner. They have to ensure the schemas are compatible.
		// The kcp server resource handlers will make sure the right structural schemas are applied. Here,
		// we can just pick one. To make it deterministic, we sort the exports.
		sort.Slice(exports, func(i, j int) bool {
			a := exports[i].(*apisv1alpha2.APIExport)
			b := exports[j].(*apisv1alpha2.APIExport)
			return a.Name < b.Name && logicalcluster.From(a).String() < logicalcluster.From(b).String()
		})

		for _, obj := range exports {
			export := obj.(*apisv1alpha2.APIExport)
			logger := logger.WithValues(logging.FromPrefix("candidateAPIExport", export)...)
			logger.V(4).Info("getting APIResourceSchemas for candidate APIExport")
			candidates, err := c.getSchemasFromAPIExport(ctx, export)
			if err != nil {
				return err
			}
			logger.V(4).Info("got APIResourceSchemas for candidate APIExport", "count", len(candidates))
			for _, apiResourceSchema := range candidates {
				logger := logger.WithValues(logging.FromPrefix("candidateAPIResourceSchema", apiResourceSchema)...)
				logger = logger.WithValues("candidateGroup", apiResourceSchema.Spec.Group, "candidateResource", apiResourceSchema.Spec.Names.Plural)
				logger.V(4).Info("evaluating candidate APIResourceSchema")
				if apiResourceSchema.Spec.Group != pc.Group || apiResourceSchema.Spec.Names.Plural != pc.Resource {
					logger.V(4).Info("not a match")
					continue
				}
				logger.V(4).Info("got a match!")
				apiResourceSchemas[gr] = apiResourceSchema
				identities[gr] = staticIdentities(pc.IdentityHash)
				claims[gr] = pc
				sources[gr] = subresourceSource{export: export, claimedVerbs: claimedSubresourceVerbs[gr]}
			}
		}
	}

	// A subresource is served under its parent, so a claim naming one whose
	// parent is not served here builds nothing at all. That is a mistake in the
	// APIExport rather than a state to wait for, and silence about it is exactly
	// what makes the resulting 404 hard to place.
	for parent, subresources := range claimedSubresourceVerbs {
		if _, found := sources[parent]; !found {
			logger.Info("custom subresources are claimed but the resource they hang off is not served by this APIExport",
				"group", parent.Group, "resource", parent.Resource, "subresources", sets.List(sets.KeySet(subresources)))
		}
	}

	// reconcile APIs for APIResourceSchemas
	newSet := apidefinition.APIDefinitionSet{}
	newGVRs := []string{}
	preservedGVR := []string{}
	for _, apiResourceSchema := range apiResourceSchemas {
		for _, version := range apiResourceSchema.Spec.Versions {
			if !version.Served {
				continue
			}

			gvr := schema.GroupVersionResource{
				Group:    apiResourceSchema.Spec.Group,
				Version:  version.Name,
				Resource: apiResourceSchema.Spec.Names.Plural,
			}

			// Custom subresources are declared on the APIExport, not on the
			// schema, so they are resolved per version before the definition is
			// reused: an export can add, drop or re-point one without the
			// schema's UID changing.
			var customSubresources []CustomSubresource
			if source, found := sources[gvr.GroupResource()]; found {
				customSubresources = c.customSubresourcesFrom(klog.NewContext(ctx, logger), source, gvr.GroupResource(), version.Name)
			}
			fingerprint := subresourcesFingerprint(customSubresources)

			oldDef, found := oldSet[gvr]
			if found {
				oldDef := oldDef.(apiResourceSchemaApiDefinition)
				if oldDef.UID == apiResourceSchema.UID && oldDef.IdentityHash == apiExport.Status.IdentityHash && oldDef.Subresources == fingerprint {
					// this is the same schema, identity and subresource set as before. no need to update.
					newSet[gvr] = oldDef
					preservedGVR = append(preservedGVR, gvrString(gvr))
					continue
				}
			}

			var labelReqs labels.Requirements
			if c, ok := claims[gvr.GroupResource()]; ok {
				key, label, err := permissionclaims.ToLabelKeyAndValue(clusterName, apiExport.Name, c)
				if err != nil {
					return fmt.Errorf("failed to convert permission claim %v to label key and value: %w", c, err)
				}
				claimLabels := []string{label}
				if gvr.GroupResource() == apisv1alpha2.Resource("apibindings") {
					_, fallbackLabel := permissionclaims.ToReflexiveAPIBindingLabelKeyAndValue(logicalcluster.From(apiExport), apiExport.Name)
					claimLabels = append(claimLabels, fallbackLabel)
				}
				req, err := labels.NewRequirement(key, selection.In, claimLabels)
				if err != nil {
					return fmt.Errorf("failed to create label requirement for permission claim %v: %w", c, err)
				}
				labelReqs = labels.Requirements{*req}
			}

			logger.Info("creating API definition", "gvr", gvr, "labels", labelReqs, "customSubresources", fingerprint)
			apiDefinition, err := c.createAPIDefinition(apiResourceSchema, version.Name, identities[gvr.GroupResource()], labelReqs, customSubresources)
			if err != nil {
				// TODO(ncdc): would be nice to expose some sort of user-visible error
				logger.Error(err, "error creating api definition", "gvr", gvr)
				continue
			}

			newSet[gvr] = apiResourceSchemaApiDefinition{
				APIDefinition: apiDefinition,
				UID:           apiResourceSchema.UID,
				IdentityHash:  apiExport.Status.IdentityHash,
				Subresources:  fingerprint,
			}
			newGVRs = append(newGVRs, gvrString(gvr))
		}
	}

	// always serve apibindings, either through a claim, or with this fallback
	if !claimsAPIBindings {
		for _, gvr := range []schema.GroupVersionResource{apisv1alpha1.SchemeGroupVersion.WithResource("apibindings"), apisv1alpha2.SchemeGroupVersion.WithResource("apibindings")} {
			d, err := c.createAPIBindingAPIDefinition(ctx, gvr.Version, clusterName, apiExport.Name)
			if err != nil {
				// TODO(ncdc): would be nice to expose some sort of user-visible error
				logger.Error(err, "error creating api definition for apibindings")
			}

			newSet[gvr] = apiResourceSchemaApiDefinition{
				APIDefinition: d,
			}
			newGVRs = append(newGVRs, gvrString(gvr))
			if _, ok := oldSet[gvr]; ok {
				preservedGVR = append(preservedGVR, gvrString(gvr))
			}
		}
	}

	// cleanup old definitions
	removedGVRs := []string{}
	for gvr, oldDef := range oldSet {
		if newDef, found := newSet[gvr]; !found || oldDef.(apiResourceSchemaApiDefinition).APIDefinition != newDef.(apiResourceSchemaApiDefinition).APIDefinition {
			removedGVRs = append(removedGVRs, gvrString(gvr))
			oldDef.TearDown()
		}
	}

	logger.V(2).Info("updating APIs", "new", newGVRs, "preserved", preservedGVR, "removed", removedGVRs)

	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.apiSets[apiDomainKey] = newSet

	return nil
}

type apiResourceSchemaApiDefinition struct {
	apidefinition.APIDefinition

	UID          types.UID
	IdentityHash string

	// Subresources fingerprints the custom subresources this definition was
	// built with, which the schema's UID says nothing about.
	Subresources string
}

// subresourceSource is where the custom subresources of one resource come from:
// the APIExport declaring them, and what this virtual workspace's own APIExport
// may reach of them.
type subresourceSource struct {
	// export declares the resource and any subresource entries under it.
	export *apisv1alpha2.APIExport

	// own says the resource is exported by this virtual workspace's own
	// APIExport, in which case every subresource entry under it is served.
	own bool

	// claimedVerbs are the verbs claimed per subresource name, for a resource
	// reached through a permission claim. A claim on the resource does not carry
	// its subresources: each is claimed as its own entry, so that claiming a
	// resource cannot pick up a verb the provider did not offer with it.
	claimedVerbs map[string][]string
}

// allSubresourceVerbs are the verbs the export's own custom subresources may be
// reached by. A subresource is not a collection, so list, watch and
// deletecollection are not among them.
var allSubresourceVerbs = []string{"create", "delete", "get", "patch", "update"}

// customSubresourcesFrom returns the custom subresource entries served under
// parent, resolved to the kind each entry's own APIResourceSchema declares.
//
// An entry is skipped, with a log line, when it is not reachable rather than not
// yet ready: a claimed subresource that this APIExport does not claim, or one
// whose schema has not arrived or serves no version. Skipping leaves the
// subresource unserved, which is a 404 at the edge -- the log line is the only
// thing that says why.
func (c *APIReconciler) customSubresourcesFrom(ctx context.Context, source subresourceSource, parent schema.GroupResource, parentVersion string) []CustomSubresource {
	logger := klog.FromContext(ctx)
	if source.export == nil {
		return nil
	}
	exportCluster := logicalcluster.From(source.export)

	var subresources []CustomSubresource
	for _, entry := range source.export.Spec.Resources {
		if !entry.IsSubresource() {
			continue
		}
		resource, subresource := entry.SplitName()
		if entry.Group != parent.Group || resource != parent.Resource {
			continue
		}

		logger := logger.WithValues("subresource", entry.Name, "schema", entry.Schema)

		verbs := allSubresourceVerbs
		if !source.own {
			claimed, isClaimed := source.claimedVerbs[subresource]
			if !isClaimed {
				logger.V(4).Info("custom subresource of a claimed resource is not claimed itself")
				continue
			}
			verbs = claimed
		}

		schemaForSubresource, err := c.apiResourceSchemaLister.Cluster(exportCluster).Get(entry.Schema)
		if err != nil {
			logger.V(3).Info("APIResourceSchema for custom subresource not available", "err", err.Error())
			continue
		}
		kind, found := servedKind(schemaForSubresource, parentVersion)
		if !found {
			logger.V(3).Info("APIResourceSchema for custom subresource serves no version")
			continue
		}

		subresources = append(subresources, CustomSubresource{
			Name:  subresource,
			Kind:  kind,
			Verbs: verbs,
		})
	}

	return subresources
}

// servedKind returns the kind a subresource schema speaks, preferring the
// parent's version where the schema serves it so that a request and its
// subresource agree on a version wherever they can.
func servedKind(apiResourceSchema *apisv1alpha1.APIResourceSchema, preferredVersion string) (schema.GroupVersionKind, bool) {
	gvk := schema.GroupVersionKind{Group: apiResourceSchema.Spec.Group, Kind: apiResourceSchema.Spec.Names.Kind}
	for _, version := range apiResourceSchema.Spec.Versions {
		if !version.Served {
			continue
		}
		if version.Name == preferredVersion {
			gvk.Version = version.Name
			return gvk, true
		}
		if gvk.Version == "" {
			gvk.Version = version.Name
		}
	}
	return gvk, gvk.Version != ""
}

func gvrString(gvr schema.GroupVersionResource) string {
	group := gvr.Group
	if group == "" {
		group = "core"
	}
	return fmt.Sprintf("%s.%s.%s", gvr.Resource, gvr.Version, group)
}

func (c *APIReconciler) getSchemasFromAPIExport(ctx context.Context, apiExport *apisv1alpha2.APIExport) (map[schema.GroupResource]*apisv1alpha1.APIResourceSchema, error) {
	logger := klog.FromContext(ctx)
	apiResourceSchemas := map[schema.GroupResource]*apisv1alpha1.APIResourceSchema{}
	for _, resourceSchema := range apiExport.Spec.Resources {
		// A custom subresource is a separate entry named "<resource>/<subresource>",
		// served by the virtual workspace its own storage names rather than by this
		// APIExport virtual workspace.
		if resourceSchema.IsSubresource() {
			continue
		}

		apiExportClusterName := logicalcluster.From(apiExport)
		apiResourceSchema, err := c.apiResourceSchemaLister.Cluster(apiExportClusterName).Get(resourceSchema.Schema)
		if err != nil && !apierrors.IsNotFound(err) {
			return nil, err
		}
		if apierrors.IsNotFound(err) {
			logger.WithValues(
				"schema", resourceSchema.Schema,
				"exportClusterName", apiExportClusterName,
				"exportName", apiExport.Name,
			).V(3).Info("APIResourceSchema for APIExport not found")
			continue
		}
		apiResourceSchemas[schema.GroupResource{Group: apiResourceSchema.Spec.Group, Resource: apiResourceSchema.Spec.Names.Plural}] = apiResourceSchema
	}

	return apiResourceSchemas, nil
}

// claimedBuiltInGroupResources returns all built-in group/resources to register for a claim.
// For events, we intentionally register both API groups so both API paths are served:
// - core/v1 events    (group "")
// - events.k8s.io/v1  (group "events.k8s.io").
func claimedBuiltInGroupResources(pc apisv1alpha2.PermissionClaim) []schema.GroupResource {
	primary := schema.GroupResource{Group: pc.Group, Resource: pc.Resource}
	if pc.Resource != "events" || (pc.Group != "" && pc.Group != "events.k8s.io") {
		return []schema.GroupResource{primary}
	}

	otherGroup := "events.k8s.io"
	if pc.Group == "events.k8s.io" {
		otherGroup = ""
	}

	return []schema.GroupResource{
		primary,
		{Group: otherGroup, Resource: "events"},
	}
}

func builtInSchemaWithClusterAnnotation(clusterName logicalcluster.Name, gr schema.GroupResource) (*apisv1alpha1.APIResourceSchema, error) {
	internalSchema, err := apiexportbuiltin.GetBuiltInAPISchema(apisv1alpha1.GroupResource{
		Group:    gr.Group,
		Resource: gr.Resource,
	})
	if err != nil {
		return nil, err
	}

	shallow := *internalSchema
	if shallow.Annotations == nil {
		shallow.Annotations = make(map[string]string)
	}
	shallow.Annotations[logicalcluster.AnnotationKey] = clusterName.String()

	return &shallow, nil
}
