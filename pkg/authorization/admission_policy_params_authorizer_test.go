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
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/logicalcluster/v3"

	"github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
)

func TestAdmissionPolicyParamsAuthorizer(t *testing.T) {
	t.Parallel()

	const exportCluster = logicalcluster.Name("export")

	configMapParamKind := admissionregistrationv1.ParamKind{APIVersion: "v1", Kind: "ConfigMap"}
	sheriffParamKind := admissionregistrationv1.ParamKind{APIVersion: "wildwest.dev/v1alpha1", Kind: "Sheriff"}

	shard := []string{bootstrap.SystemExternalLogicalClusterAdmin}
	listConfigMaps := authorizer.AttributesRecord{ResourceRequest: true, Verb: "list", APIVersion: "v1", Resource: "configmaps"}

	cases := map[string]struct {
		groups        []string
		deepSAR       bool
		cluster       *genericapirequest.Cluster
		attr          authorizer.AttributesRecord
		noAPIExports  bool
		apiExportsErr error
		paramKinds    []admissionregistrationv1.ParamKind
		paramKindsErr error
		wantDecision  authorizer.Decision
		wantReason    string
		wantErr       bool
	}{
		"list of a built-in paramKind": {
			groups:       shard,
			attr:         listConfigMaps,
			paramKinds:   []admissionregistrationv1.ParamKind{configMapParamKind},
			wantDecision: authorizer.DecisionAllow,
			wantReason:   "resource is the paramKind of an admission policy",
		},
		"watch of a custom resource paramKind": {
			groups:       shard,
			attr:         authorizer.AttributesRecord{ResourceRequest: true, Verb: "watch", APIGroup: "wildwest.dev", APIVersion: "v1alpha1", Resource: "sheriffs"},
			paramKinds:   []admissionregistrationv1.ParamKind{configMapParamKind, sheriffParamKind},
			wantDecision: authorizer.DecisionAllow,
			wantReason:   "resource is the paramKind of an admission policy",
		},
		"get of a single param": {
			groups:       shard,
			attr:         authorizer.AttributesRecord{ResourceRequest: true, Verb: "get", APIVersion: "v1", Resource: "configmaps", Namespace: "default", Name: "policy-params"},
			paramKinds:   []admissionregistrationv1.ParamKind{configMapParamKind},
			wantDecision: authorizer.DecisionAllow,
			wantReason:   "resource is the paramKind of an admission policy",
		},
		"discovery of the group version of a paramKind": {
			groups:       shard,
			attr:         authorizer.AttributesRecord{Verb: "get", Path: "/apis/wildwest.dev/v1alpha1"},
			paramKinds:   []admissionregistrationv1.ParamKind{configMapParamKind, sheriffParamKind},
			wantDecision: authorizer.DecisionAllow,
			wantReason:   "group version of the paramKind of an admission policy",
		},
		"discovery of the core group version of a paramKind": {
			groups:       shard,
			attr:         authorizer.AttributesRecord{Verb: "get", Path: "/api/v1"},
			paramKinds:   []admissionregistrationv1.ParamKind{configMapParamKind},
			wantDecision: authorizer.DecisionAllow,
			wantReason:   "group version of the paramKind of an admission policy",
		},
		// A paramKind whose CRD does not exist yet does not resolve to a resource, but it has to be
		// discoverable for the shard to notice when it does.
		"discovery of the group version of a paramKind which is not served yet": {
			groups:       shard,
			attr:         authorizer.AttributesRecord{Verb: "get", Path: "/apis/unknown.dev/v1"},
			paramKinds:   []admissionregistrationv1.ParamKind{{APIVersion: "unknown.dev/v1", Kind: "Unknown"}},
			wantDecision: authorizer.DecisionAllow,
			wantReason:   "group version of the paramKind of an admission policy",
		},
		// Everything below locks in that the access is bounded: anything else goes through the regular chain.
		"other group versions cannot be discovered": {
			groups:       shard,
			attr:         authorizer.AttributesRecord{Verb: "get", Path: "/apis/wildwest.dev/v1beta1"},
			paramKinds:   []admissionregistrationv1.ParamKind{configMapParamKind, sheriffParamKind},
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   "not the group version of the paramKind of an admission policy",
		},
		"the core group version cannot be discovered without a paramKind in it": {
			groups:       shard,
			attr:         authorizer.AttributesRecord{Verb: "get", Path: "/api/v1"},
			paramKinds:   []admissionregistrationv1.ParamKind{sheriffParamKind},
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   "not the group version of the paramKind of an admission policy",
		},
		"the list of all groups is not admitted": {
			groups:       shard,
			attr:         authorizer.AttributesRecord{Verb: "get", Path: "/apis"},
			paramKinds:   []admissionregistrationv1.ParamKind{sheriffParamKind},
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   "neither a read nor the discovery of a resource",
		},
		"discovery in a logical cluster which does not export APIs": {
			groups:       shard,
			attr:         authorizer.AttributesRecord{Verb: "get", Path: "/apis/wildwest.dev/v1alpha1"},
			noAPIExports: true,
			paramKinds:   []admissionregistrationv1.ParamKind{sheriffParamKind},
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   "logical cluster does not export APIs",
		},
		"other identities are not admitted": {
			groups:       []string{"system:kcp:admin"},
			attr:         listConfigMaps,
			paramKinds:   []admissionregistrationv1.ParamKind{configMapParamKind},
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   "not an identity reading admission policy params",
		},
		"deep SAR is skipped": {
			groups:       shard,
			deepSAR:      true,
			attr:         listConfigMaps,
			paramKinds:   []admissionregistrationv1.ParamKind{configMapParamKind},
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   "deep SAR request",
		},
		"params cannot be written": {
			groups:       shard,
			attr:         authorizer.AttributesRecord{ResourceRequest: true, Verb: "update", APIVersion: "v1", Resource: "configmaps", Namespace: "default", Name: "policy-params"},
			paramKinds:   []admissionregistrationv1.ParamKind{configMapParamKind},
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   "neither a read nor the discovery of a resource",
		},
		"subresources of params cannot be read": {
			groups:       shard,
			attr:         authorizer.AttributesRecord{ResourceRequest: true, Verb: "get", APIGroup: "wildwest.dev", APIVersion: "v1alpha1", Resource: "sheriffs", Subresource: "status", Name: "policy-params"},
			paramKinds:   []admissionregistrationv1.ParamKind{sheriffParamKind},
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   "neither a read nor the discovery of a resource",
		},
		"non-resource requests are not admitted": {
			groups:       shard,
			attr:         authorizer.AttributesRecord{ResourceRequest: false, Verb: "get", Path: "/metrics"},
			paramKinds:   []admissionregistrationv1.ParamKind{configMapParamKind},
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   "neither a read nor the discovery of a resource",
		},
		"resources which are not a paramKind cannot be read": {
			groups:       shard,
			attr:         authorizer.AttributesRecord{ResourceRequest: true, Verb: "list", APIVersion: "v1", Resource: "secrets"},
			paramKinds:   []admissionregistrationv1.ParamKind{configMapParamKind, sheriffParamKind},
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   "resource is not the paramKind of an admission policy",
		},
		"a resource with the same name in another group cannot be read": {
			groups:       shard,
			attr:         authorizer.AttributesRecord{ResourceRequest: true, Verb: "list", APIGroup: "other.dev", APIVersion: "v1alpha1", Resource: "sheriffs"},
			paramKinds:   []admissionregistrationv1.ParamKind{sheriffParamKind},
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   "resource is not the paramKind of an admission policy",
		},
		"logical cluster without admission policies": {
			groups:       shard,
			attr:         listConfigMaps,
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   "resource is not the paramKind of an admission policy",
		},
		"paramKinds which do not resolve to a resource are ignored": {
			groups: shard,
			attr:   listConfigMaps,
			paramKinds: []admissionregistrationv1.ParamKind{
				{APIVersion: "not/a/version", Kind: "ConfigMap"},
				{APIVersion: "unknown.dev/v1", Kind: "ConfigMap"},
			},
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   "resource is not the paramKind of an admission policy",
		},
		"logical cluster which does not export APIs": {
			groups:       shard,
			attr:         listConfigMaps,
			noAPIExports: true,
			paramKinds:   []admissionregistrationv1.ParamKind{configMapParamKind},
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   "logical cluster does not export APIs",
		},
		"wildcard requests are not admitted": {
			groups:       shard,
			cluster:      &genericapirequest.Cluster{Name: "*", Wildcard: true},
			attr:         listConfigMaps,
			paramKinds:   []admissionregistrationv1.ParamKind{configMapParamKind},
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   "not a request to a single logical cluster",
		},
		"error listing APIExports": {
			groups:        shard,
			attr:          listConfigMaps,
			apiExportsErr: fmt.Errorf("informer not synced"),
			paramKinds:    []admissionregistrationv1.ParamKind{configMapParamKind},
			wantDecision:  authorizer.DecisionNoOpinion,
			wantReason:    "error listing APIExports",
			wantErr:       true,
		},
		"error listing admission policies": {
			groups:        shard,
			attr:          listConfigMaps,
			paramKindsErr: fmt.Errorf("informer not synced"),
			wantDecision:  authorizer.DecisionNoOpinion,
			wantReason:    "error listing admission policies",
			wantErr:       true,
		},
	}

	restMapper := meta.NewDefaultRESTMapper(nil)
	restMapper.Add(schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}, meta.RESTScopeNamespace)
	restMapper.Add(schema.GroupVersionKind{Version: "v1", Kind: "Secret"}, meta.RESTScopeNamespace)
	restMapper.Add(schema.GroupVersionKind{Group: "wildwest.dev", Version: "v1alpha1", Kind: "Sheriff"}, meta.RESTScopeRoot)
	restMapper.Add(schema.GroupVersionKind{Group: "other.dev", Version: "v1alpha1", Kind: "Sheriff"}, meta.RESTScopeRoot)

	for title, cas := range cases {
		t.Run(title, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			if cas.deepSAR {
				ctx = context.WithValue(ctx, deepSARKey, true)
			}
			cluster := cas.cluster
			if cluster == nil {
				cluster = &genericapirequest.Cluster{Name: exportCluster}
			}
			ctx = genericapirequest.WithCluster(ctx, *cluster)

			a := &admissionPolicyParamsAuthorizer{
				exportsAPIs: func(clusterName logicalcluster.Name) (bool, error) {
					require.Equal(t, exportCluster, clusterName)
					return !cas.noAPIExports, cas.apiExportsErr
				},
				listParamKinds: func(clusterName logicalcluster.Name) ([]admissionregistrationv1.ParamKind, error) {
					require.Equal(t, exportCluster, clusterName)
					return cas.paramKinds, cas.paramKindsErr
				},
				restMapperForCluster: func(clusterName logicalcluster.Name) meta.RESTMapper {
					require.Equal(t, exportCluster, clusterName)
					return restMapper
				},
			}

			attr := cas.attr
			attr.User = &user.DefaultInfo{Name: "shard", Groups: cas.groups}

			decision, reason, err := a.Authorize(ctx, attr)
			if cas.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, cas.wantDecision, decision)
			require.Equal(t, cas.wantReason, reason)
		})
	}
}
