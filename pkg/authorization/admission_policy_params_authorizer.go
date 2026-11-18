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

package authorization

import (
	"context"
	"strings"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	rbacregistryvalidation "k8s.io/kubernetes/pkg/registry/rbac/validation"

	kcpkubernetesinformers "github.com/kcp-dev/client-go/informers"
	"github.com/kcp-dev/logicalcluster/v3"
	kcpinformers "github.com/kcp-dev/sdk/client/informers/externalversions"

	"github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
)

// admissionPolicyParamsVerbs are the verbs needed to run an informer on the params
// of an admission policy and to get a single param on a cache miss.
var admissionPolicyParamsVerbs = sets.New("get", "list", "watch")

// NewAdmissionPolicyParamsAuthorizer admits shards to read the params of Validating- and
// MutatingAdmissionPolicies in the logical cluster of an APIExport.
//
// A policy in the logical cluster of an APIExport is applied to the bound resources in the
// logical clusters of all consumers, which can be hosted on other shards. Those shards have
// to read the params of the policy, which live next to the policy.
//
// Kubernetes reads params with the privileges of the API server itself and instead requires
// read access to the params from whoever creates the policy and its binding. This authorizer
// gives the shards the same access across shards, but nothing more: only resources which a
// policy of the logical cluster references as paramKind can be read, and only in logical
// clusters which export APIs.
//
// A shard does not know the resources of logical clusters hosted on other shards, so it also
// has to discover which resource a paramKind is. For that it can read the discovery document
// of the group version of a paramKind, under the same conditions.
func NewAdmissionPolicyParamsAuthorizer(
	localKubeInformers kcpkubernetesinformers.SharedInformerFactory,
	localKcpInformers kcpinformers.SharedInformerFactory,
	restMapperForCluster func(clusterName logicalcluster.Name) meta.RESTMapper,
	mutatingAdmissionPolicyEnabled bool,
) authorizer.Authorizer {
	apiExportLister := localKcpInformers.Apis().V1alpha2().APIExports().Lister()
	validatingPolicyLister := localKubeInformers.Admissionregistration().V1().ValidatingAdmissionPolicies().Lister()

	listMutatingPolicies := func(logicalcluster.Name) ([]*admissionregistrationv1.MutatingAdmissionPolicy, error) {
		return nil, nil
	}
	if mutatingAdmissionPolicyEnabled {
		mutatingPolicyLister := localKubeInformers.Admissionregistration().V1().MutatingAdmissionPolicies().Lister()
		listMutatingPolicies = func(clusterName logicalcluster.Name) ([]*admissionregistrationv1.MutatingAdmissionPolicy, error) {
			return mutatingPolicyLister.Cluster(clusterName).List(labels.Everything())
		}
	}

	return &admissionPolicyParamsAuthorizer{
		exportsAPIs: func(clusterName logicalcluster.Name) (bool, error) {
			apiExports, err := apiExportLister.Cluster(clusterName).List(labels.Everything())
			return len(apiExports) > 0, err
		},
		listParamKinds: func(clusterName logicalcluster.Name) ([]admissionregistrationv1.ParamKind, error) {
			validatingPolicies, err := validatingPolicyLister.Cluster(clusterName).List(labels.Everything())
			if err != nil {
				return nil, err
			}
			mutatingPolicies, err := listMutatingPolicies(clusterName)
			if err != nil {
				return nil, err
			}

			var paramKinds []admissionregistrationv1.ParamKind
			for _, policy := range validatingPolicies {
				if policy.Spec.ParamKind != nil {
					paramKinds = append(paramKinds, *policy.Spec.ParamKind)
				}
			}
			for _, policy := range mutatingPolicies {
				if policy.Spec.ParamKind != nil {
					paramKinds = append(paramKinds, *policy.Spec.ParamKind)
				}
			}
			return paramKinds, nil
		},
		restMapperForCluster: restMapperForCluster,
	}
}

type admissionPolicyParamsAuthorizer struct {
	// exportsAPIs returns whether the logical cluster has any APIExport.
	exportsAPIs func(clusterName logicalcluster.Name) (bool, error)
	// listParamKinds returns the paramKinds of all admission policies in the logical cluster.
	listParamKinds func(clusterName logicalcluster.Name) ([]admissionregistrationv1.ParamKind, error)
	// restMapperForCluster returns the REST mapper to resolve a paramKind to a resource in the logical cluster.
	restMapperForCluster func(clusterName logicalcluster.Name) meta.RESTMapper
}

func (a *admissionPolicyParamsAuthorizer) Authorize(ctx context.Context, attr authorizer.Attributes) (authorizer.Decision, string, error) {
	if IsDeepSubjectAccessReviewFrom(ctx, attr) {
		// a deep SAR reviews another identity, the caller's group must not admit the payload request
		return authorizer.DecisionNoOpinion, "deep SAR request", nil
	}

	if !rbacregistryvalidation.EffectiveGroups(ctx, attr.GetUser()).Has(bootstrap.SystemExternalLogicalClusterAdmin) {
		return authorizer.DecisionNoOpinion, "not an identity reading admission policy params", nil
	}

	isRead := attr.IsResourceRequest() && attr.GetSubresource() == "" && admissionPolicyParamsVerbs.Has(attr.GetVerb())
	discoveredGV, isDiscovery := groupVersionOfDiscovery(attr)
	if !isRead && !isDiscovery {
		return authorizer.DecisionNoOpinion, "neither a read nor the discovery of a resource", nil
	}

	cluster := genericapirequest.ClusterFrom(ctx)
	if cluster == nil || cluster.Wildcard || cluster.Name.Empty() {
		return authorizer.DecisionNoOpinion, "not a request to a single logical cluster", nil
	}

	exportsAPIs, err := a.exportsAPIs(cluster.Name)
	if err != nil {
		return authorizer.DecisionNoOpinion, "error listing APIExports", err
	}
	if !exportsAPIs {
		return authorizer.DecisionNoOpinion, "logical cluster does not export APIs", nil
	}

	paramKinds, err := a.listParamKinds(cluster.Name)
	if err != nil {
		return authorizer.DecisionNoOpinion, "error listing admission policies", err
	}

	requested := schema.GroupResource{Group: attr.GetAPIGroup(), Resource: attr.GetResource()}
	restMapper := a.restMapperForCluster(cluster.Name)
	for _, paramKind := range paramKinds {
		gv, err := schema.ParseGroupVersion(paramKind.APIVersion)
		if err != nil {
			continue
		}

		if isDiscovery {
			if gv == discoveredGV {
				return authorizer.DecisionAllow, "group version of the paramKind of an admission policy", nil
			}
			continue
		}

		mapping, err := restMapper.RESTMapping(schema.GroupKind{Group: gv.Group, Kind: paramKind.Kind}, gv.Version)
		if err != nil {
			// a paramKind which does not resolve to a resource cannot be read by a policy either
			continue
		}
		if mapping.Resource.GroupResource() == requested {
			return authorizer.DecisionAllow, "resource is the paramKind of an admission policy", nil
		}
	}

	if isDiscovery {
		return authorizer.DecisionNoOpinion, "not the group version of the paramKind of an admission policy", nil
	}
	return authorizer.DecisionNoOpinion, "resource is not the paramKind of an admission policy", nil
}

// groupVersionOfDiscovery returns the group version whose discovery document is requested,
// which is served at /api/<version> for the core group and at /apis/<group>/<version> otherwise.
func groupVersionOfDiscovery(attr authorizer.Attributes) (schema.GroupVersion, bool) {
	if attr.IsResourceRequest() || attr.GetVerb() != "get" {
		return schema.GroupVersion{}, false
	}

	switch segments := strings.Split(strings.Trim(attr.GetPath(), "/"), "/"); {
	case len(segments) == 2 && segments[0] == "api":
		return schema.GroupVersion{Version: segments[1]}, true
	case len(segments) == 3 && segments[0] == "apis":
		return schema.GroupVersion{Group: segments[1], Version: segments[2]}, true
	default:
		return schema.GroupVersion{}, false
	}
}

// ConditionsAwareAuthorize. This authorizer is not conditions-aware and therefore returns unconditional Authorize decision.
func (a *admissionPolicyParamsAuthorizer) ConditionsAwareAuthorize(ctx context.Context, attrs authorizer.Attributes) authorizer.ConditionsAwareDecision {
	return authorizer.ConditionsAwareDecisionFromParts(a.Authorize(ctx, attrs))
}

// EvaluateConditions fails closed because this authorizer is not conditions-aware.
func (a *admissionPolicyParamsAuthorizer) EvaluateConditions(_ context.Context, _ authorizer.ConditionsAwareDecision, _ authorizer.ConditionsData) (authorizer.Decision, string, error) {
	return authorizer.DecisionDeny, "", authorizer.ErrorConditionEvaluationNotSupported
}
