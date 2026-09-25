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

package authorizer

import (
	"context"
	"fmt"
	"strings"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	rbacregistryvalidation "k8s.io/kubernetes/pkg/registry/rbac/validation"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/kcp-dev/sdk/apis/apis"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	apisv1alpha2informers "github.com/kcp-dev/sdk/client/informers/externalversions/apis/v1alpha2"
	dynamiccontext "github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic/context"

	"github.com/kcp-dev/kcp/pkg/authorization/delegated"
	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/permissionclaim"
	builtinapiexport "github.com/kcp-dev/kcp/pkg/virtual/apiexport/schemas/builtin"
)

type maximalPermissionAuthorizer struct {
	getAPIExport            func(clusterName, apiExportName string) (*apisv1alpha2.APIExport, error)
	newDeepSARAuthorizer    func(clusterName logicalcluster.Name) (authorizer.Authorizer, error)
	getAPIExportsByIdentity func(identityHash string) ([]*apisv1alpha2.APIExport, error)

	// resolveIdentities returns the identity hashes an identity-agnostic claim
	// resolves to: the single identity bound in the request's workspace, or -
	// for a wildcard request, where no single workspace can be named - every
	// identity the claim resolves to on this shard.
	resolveIdentities func(export *apisv1alpha2.APIExport, cluster logicalcluster.Name, wildcard bool, gr schema.GroupResource) ([]string, error)

	isBuiltIn func(gr apis.GroupResource) bool
}

// NewMaximalPermissionAuthorizer creates an authorizer that checks the maximal permission policy
// for the requested resource if the resource is a claimed resource in the requested API export.
// The check is omitted if the requested resource itself is not associated with an API export.
//
// If the request is a cluster request the authorizer skips authorization if the request is not for a bound resource.
// If the request is a wildcard request this check is skipped because no unique API binding can be determined.
func NewMaximalPermissionAuthorizer(deepSARClient kcpkubernetesclientset.ClusterInterface, apiExportInformer apisv1alpha2informers.APIExportClusterInformer, apiBindingInformer apisv1alpha2informers.APIBindingClusterInformer) authorizer.Authorizer {
	apiExportLister := apiExportInformer.Lister()
	apiExportIndexer := apiExportInformer.Informer().GetIndexer()
	resolver := permissionclaim.NewIdentityResolver(apiBindingInformer.Informer().GetIndexer())

	return &maximalPermissionAuthorizer{
		getAPIExport: func(clusterName, apiExportName string) (*apisv1alpha2.APIExport, error) {
			return apiExportLister.Cluster(logicalcluster.Name(clusterName)).Get(apiExportName)
		},
		getAPIExportsByIdentity: func(identityHash string) ([]*apisv1alpha2.APIExport, error) {
			return indexers.ByIndex[*apisv1alpha2.APIExport](apiExportIndexer, indexers.APIExportByIdentity, identityHash)
		},
		newDeepSARAuthorizer: func(clusterName logicalcluster.Name) (authorizer.Authorizer, error) {
			return delegated.NewDelegatedAuthorizer(clusterName, deepSARClient, delegated.Options{})
		},
		resolveIdentities: func(export *apisv1alpha2.APIExport, cluster logicalcluster.Name, wildcard bool, gr schema.GroupResource) ([]string, error) {
			if wildcard {
				return resolver.IdentityHashes(export, gr)
			}
			identity, found, err := resolver.ResolveInCluster(cluster, gr)
			if err != nil || !found {
				return nil, err
			}
			return []string{identity.IdentityHash}, nil
		},
		isBuiltIn: builtinapiexport.IsBuiltInAPI,
	}
}

func (a *maximalPermissionAuthorizer) Authorize(ctx context.Context, attr authorizer.Attributes) (authorizer.Decision, string, error) {
	apiDomainKey := dynamiccontext.APIDomainKeyFrom(ctx)
	parts := strings.Split(string(apiDomainKey), "/")
	if len(parts) < 2 {
		return authorizer.DecisionNoOpinion, "", fmt.Errorf("invalid API domain key")
	}

	claimingAPIExportCluster := parts[0]
	claimingAPIExportName := parts[1]

	claimingAPIExport, err := a.getAPIExport(claimingAPIExportCluster, claimingAPIExportName)
	if kerrors.IsNotFound(err) {
		return authorizer.DecisionNoOpinion, "", fmt.Errorf("API export not found: %w", err)
	}
	if err != nil {
		return authorizer.DecisionNoOpinion, "", err
	}

	claimedIdentityHash, claim, found := getClaimedIdentity(claimingAPIExport, attr)
	if !found {
		// it's a resource in the claiming API export, hence unclaimed
		return authorizer.DecisionAllow, fmt.Sprintf("unclaimed resource in API export: %q, workspace :%q",
			claimingAPIExport.Name, logicalcluster.From(claimingAPIExport)), nil
	}

	claimedIdentityHashes := []string{claimedIdentityHash}
	switch {
	case claimedIdentityHash != "":
		// the claim names the identity of the producing APIExport.

	case !permissionclaim.IsIdentityAgnostic(claim, a.isBuiltIn):
		// it's a native k8s resource (secret, configmap, ...), or a system kcp CRD resource (apis.kcp.io)
		// For neither case a maximum permission policy can exist.
		return authorizer.DecisionAllow, fmt.Sprintf("unclaimable resource, identity hash not set in claiming API export: %q, workspace :%q",
			claimingAPIExport.Name, logicalcluster.From(claimingAPIExport)), nil

	default:
		// Identity-agnostic claim: the producing APIExport is whatever the
		// consumer workspace bound. Resolving it here is what keeps the
		// producer's maximal permission policy in force for these claims.
		cluster, err := genericapirequest.ValidClusterFrom(ctx)
		if err != nil {
			return authorizer.DecisionNoOpinion, "", fmt.Errorf("error getting valid cluster from context: %w", err)
		}
		gr := permissionclaim.NormalizeEventGroupResource(schema.GroupResource{Group: attr.GetAPIGroup(), Resource: attr.GetResource()})
		claimedIdentityHashes, err = a.resolveIdentities(claimingAPIExport, cluster.Name, cluster.Wildcard, gr)
		if err != nil {
			return authorizer.DecisionNoOpinion, "", fmt.Errorf("error resolving identities for claimed resource %q: %w", gr, err)
		}
		if len(claimedIdentityHashes) == 0 {
			// No binding serves the claimed resource here (yet). Deny rather
			// than fall through unchecked; the caller retries.
			return authorizer.DecisionDeny, fmt.Sprintf("no APIBinding providing claimed resource %q found for identity-agnostic claim of API export: %q", gr, claimingAPIExport.Name), nil
		}
	}

	var apiExportsProvidingClaimedResources []*apisv1alpha2.APIExport
	for _, identityHash := range claimedIdentityHashes {
		exports, err := a.getAPIExportsByIdentity(identityHash)
		if err != nil {
			return authorizer.DecisionNoOpinion, "", fmt.Errorf("error getting API export identity: %q: %w", identityHash, err)
		}
		if len(exports) == 0 {
			// a claimed identity hash exists but not API export can be found referring to it (potentially eventually consistent).
			// In this case be safe and deny the request, forcing the caller to retry.
			return authorizer.DecisionDeny, fmt.Sprintf("no API export providing claimed resources found for identity hash: %q", identityHash), nil
		}
		apiExportsProvidingClaimedResources = append(apiExportsProvidingClaimedResources, exports...)
	}

	// multiple claimed API exports can share the same identity hash (even in different workspaces).
	// All maximum permission policies must grant access because at this point no deterministic API export can be picked.
	for _, apiExportProvidingClaimedResource := range apiExportsProvidingClaimedResources {
		if apiExportProvidingClaimedResource.Spec.MaximalPermissionPolicy == nil {
			continue
		}

		if apiExportProvidingClaimedResource.Spec.MaximalPermissionPolicy.Local == nil {
			continue
		}

		authz, err := a.newDeepSARAuthorizer(logicalcluster.From(apiExportProvidingClaimedResource))
		if err != nil {
			return authorizer.DecisionNoOpinion, "", fmt.Errorf("error executing deep SAR in API export name: %q, workspace: %q: %w",
				apiExportProvidingClaimedResource.Name, logicalcluster.From(apiExportProvidingClaimedResource), err)
		}

		dec, reason, err := authz.Authorize(ctx, prefixAttributes(attr))
		if err != nil {
			return authorizer.DecisionNoOpinion, "", fmt.Errorf("error authorizing against API export name: %q, workspace: %q: %w",
				apiExportProvidingClaimedResource.Name, logicalcluster.From(apiExportProvidingClaimedResource), err)
		}

		// all maximum permission policies must grant access
		if dec != authorizer.DecisionAllow {
			return authorizer.DecisionNoOpinion, fmt.Sprintf("API export: %q, workspace: %q RBAC decision: %v",
				apiExportProvidingClaimedResource.Name, logicalcluster.From(apiExportProvidingClaimedResource), reason), nil
		}
	}

	return authorizer.DecisionAllow, "all claimed API exports granted access", nil
}

// getClaimedIdentity returns the identity hash the claiming APIExport names
// for the requested resource, together with the claim itself so the caller can
// tell an identity-less built-in claim from an identity-agnostic one.
func getClaimedIdentity(apiExport *apisv1alpha2.APIExport, attr authorizer.Attributes) (string, apisv1alpha2.PermissionClaim, bool) {
	normalizedGR := permissionclaim.NormalizeEventGroupResource(schema.GroupResource{Group: attr.GetAPIGroup(), Resource: attr.GetResource()})
	for i := range apiExport.Spec.PermissionClaims {
		if apiExport.Spec.PermissionClaims[i].Resource == normalizedGR.Resource &&
			apiExport.Spec.PermissionClaims[i].Group == normalizedGR.Group {
			return apiExport.Spec.PermissionClaims[i].IdentityHash, apiExport.Spec.PermissionClaims[i], true
		}
	}
	return "", apisv1alpha2.PermissionClaim{}, false
}

func prefixAttributes(attr authorizer.Attributes) *authorizer.AttributesRecord {
	// Use rbacregistryvalidation.PrefixUser to properly handle ServiceAccount
	// rewriting. For SAs with a cluster scope, this rewrites the user name from
	// "system:serviceaccount:<ns>:<name>" to
	// "apis.kcp.io:binding:system:kcp:serviceaccount:<cluster>:<ns>:<name>"
	// which allows RBAC to be set up for cross-cluster SA access.
	prefixedUser := rbacregistryvalidation.PrefixUser(attr.GetUser(), apisv1alpha1.MaximalPermissionPolicyRBACUserGroupPrefix)

	// Strip scope-related Extra keys from ServiceAccounts only. The maximal permission
	// policy check runs in the workspace where the claimed APIExport lives (e.g., root
	// for tenancy.kcp.io), but ServiceAccount tokens are scoped to their originating workspace.
	// Without stripping scopes, the deep SAR would be denied due to scope mismatch.
	// Regular users don't have this scoping issue, so we preserve their Extra fields.
	if rbacregistryvalidation.IsServiceAccount(attr.GetUser()) {
		prefixedUser = stripScopesFromUser(prefixedUser)
	}

	attr = &authorizer.AttributesRecord{
		User:            prefixedUser,
		Verb:            attr.GetVerb(),
		Namespace:       attr.GetNamespace(),
		APIGroup:        attr.GetAPIGroup(),
		APIVersion:      attr.GetAPIVersion(),
		Resource:        attr.GetResource(),
		Subresource:     attr.GetSubresource(),
		Name:            attr.GetName(),
		ResourceRequest: attr.IsResourceRequest(),
		Path:            attr.GetPath(),
	}

	return attr.(*authorizer.AttributesRecord)
}

// stripScopesFromUser returns a copy of the user with scope-related Extra keys removed.
func stripScopesFromUser(u user.Info) user.Info {
	extra := u.GetExtra()
	if extra == nil {
		return u
	}

	// Check if we need to strip anything
	_, hasScopes := extra[rbacregistryvalidation.ScopeExtraKey]
	_, hasClusterName := extra["authentication.kcp.io/cluster-name"]
	if !hasScopes && !hasClusterName {
		return u
	}

	// Create new Extra map without scope keys
	newExtra := make(map[string][]string, len(extra))
	for k, v := range extra {
		if k == rbacregistryvalidation.ScopeExtraKey || k == "authentication.kcp.io/cluster-name" {
			continue
		}
		newExtra[k] = v
	}

	return &user.DefaultInfo{
		Name:   u.GetName(),
		UID:    u.GetUID(),
		Groups: u.GetGroups(),
		Extra:  newExtra,
	}
}
