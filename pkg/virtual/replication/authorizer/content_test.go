/*
Copyright 2025 The kcp Authors.

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
	"testing"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	cachev1alpha1 "github.com/kcp-dev/sdk/apis/cache/v1alpha1"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	dynamiccontext "github.com/kcp-dev/virtual-workspace-framework/pkg/dynamic/context"

	"github.com/kcp-dev/kcp/pkg/reconciler/apis/apibinding"
)

type alwaysDenyAuthrizer struct{}

func (*alwaysDenyAuthrizer) Authorize(ctx context.Context, attr authorizer.Attributes) (authorizer.Decision, string, error) {
	return authorizer.DecisionDeny, "alwaysDeny", nil
}

type alwaysAllowAuthrizer struct{}

func (*alwaysAllowAuthrizer) Authorize(ctx context.Context, attr authorizer.Attributes) (authorizer.Decision, string, error) {
	return authorizer.DecisionAllow, "alwaysAllow", nil
}

func TestContentAuthorizer(t *testing.T) {
	tests := map[string]struct {
		a    contentAuthorizer
		ctx  context.Context //nolint:containedctx // Mock ctx needed by Authorizer().
		attr authorizer.Attributes

		expectedDecision authorizer.Decision
		expectedReason   string
		expectedErrorStr string
	}{
		"non-readonly verbs should fail": {
			attr: authorizer.AttributesRecord{
				Verb: "create",
			},
			expectedDecision: authorizer.DecisionDeny,
			expectedReason:   "write access to Replication virtual workspace is not allowed",
		},
		"missing API domain key in context": {
			ctx: context.Background(),
			attr: authorizer.AttributesRecord{
				Verb: "get",
			},
			expectedDecision: authorizer.DecisionNoOpinion,
			expectedErrorStr: "invalid API domain key",
		},
		"missing target cluster in context": {
			ctx: dynamiccontext.WithAPIDomainKey(context.Background(), "CachedResourceCluster/cachedresource-1"),
			attr: authorizer.AttributesRecord{
				Verb: "get",
			},
			expectedDecision: authorizer.DecisionNoOpinion,
			expectedErrorStr: "error getting valid cluster from context: no cluster in the request context",
		},
		"missing CachedResourceEndpointSlice": {
			a: contentAuthorizer{
				getCachedResourceEndpointSlice: func(cluster logicalcluster.Name, name string) (*cachev1alpha1.CachedResourceEndpointSlice, error) {
					return nil, apierrors.NewNotFound(cachev1alpha1.Resource("cachedresourceendpointslices"), name)
				},
			},
			attr: authorizer.AttributesRecord{
				Verb: "get",
			},
			ctx: dynamiccontext.WithAPIDomainKey(
				genericapirequest.WithCluster(
					context.Background(), genericapirequest.Cluster{Name: "TargetCluster"},
				),
				"CachedResourceCluster/cachedresource-1",
			),
			expectedDecision: authorizer.DecisionNoOpinion,
			expectedErrorStr: `cachedresourceendpointslices.cache.kcp.io "cachedresource-1" not found`,
		},
		"wildcard request and deny": {
			a: contentAuthorizer{
				getCachedResourceEndpointSlice: func(cluster logicalcluster.Name, name string) (*cachev1alpha1.CachedResourceEndpointSlice, error) {
					return &cachev1alpha1.CachedResourceEndpointSlice{
						ObjectMeta: metav1.ObjectMeta{
							Name: "cachedresource-1",
						},
						Spec: cachev1alpha1.CachedResourceEndpointSliceSpec{
							APIExport: cachev1alpha1.ExportBindingReference{
								Path: "root:provider",
								Name: "apiexport-1",
							},
						},
					}, nil
				},
				getAPIExportByPath: func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error) {
					return &apisv1alpha2.APIExport{
						Spec: apisv1alpha2.APIExportSpec{
							Resources: []apisv1alpha2.ResourceSchema{
								{
									Group: "group",
									Name:  "resource",
									Storage: apisv1alpha2.ResourceSchemaStorage{
										Virtual: &apisv1alpha2.ResourceSchemaStorageVirtual{
											Reference: corev1.TypedLocalObjectReference{
												APIGroup: &cachev1alpha1.SchemeGroupVersion.Group,
												Kind:     "CachedResourceEndpointSlice",
												Name:     "cachedresource-1",
											},
										},
									},
								},
							},
						},
						Status: apisv1alpha2.APIExportStatus{
							IdentityHash: "APIExportIdentity",
						},
					}, nil
				},
				newDelegatedAuthorizer: func(cluster logicalcluster.Name) (authorizer.Authorizer, error) {
					return &alwaysDenyAuthrizer{}, nil
				},
			},
			attr: authorizer.AttributesRecord{
				Verb: "get",
				User: &user.DefaultInfo{},
			},
			ctx: dynamiccontext.WithAPIDomainKey(
				genericapirequest.WithCluster(
					context.Background(), genericapirequest.Cluster{Wildcard: true},
				),
				"CachedResourceCluster/cachedresource-1",
			),
			expectedDecision: authorizer.DecisionDeny,
			expectedReason:   "alwaysDeny",
		},
		"wildcard request and allow": {
			a: contentAuthorizer{
				getCachedResourceEndpointSlice: func(cluster logicalcluster.Name, name string) (*cachev1alpha1.CachedResourceEndpointSlice, error) {
					return &cachev1alpha1.CachedResourceEndpointSlice{
						ObjectMeta: metav1.ObjectMeta{
							Name: "cachedresource-1",
						},
						Spec: cachev1alpha1.CachedResourceEndpointSliceSpec{
							APIExport: cachev1alpha1.ExportBindingReference{
								Path: "root:provider",
								Name: "apiexport-1",
							},
						},
					}, nil
				},
				getAPIExportByPath: func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error) {
					return &apisv1alpha2.APIExport{
						Spec: apisv1alpha2.APIExportSpec{
							Resources: []apisv1alpha2.ResourceSchema{
								{
									Group: "group",
									Name:  "resource",
									Storage: apisv1alpha2.ResourceSchemaStorage{
										Virtual: &apisv1alpha2.ResourceSchemaStorageVirtual{
											Reference: corev1.TypedLocalObjectReference{
												APIGroup: &cachev1alpha1.SchemeGroupVersion.Group,
												Kind:     "CachedResourceEndpointSlice",
												Name:     "cachedresource-1",
											},
										},
									},
								},
							},
						},
						Status: apisv1alpha2.APIExportStatus{
							IdentityHash: "APIExportIdentity",
						},
					}, nil
				},
				newDelegatedAuthorizer: func(cluster logicalcluster.Name) (authorizer.Authorizer, error) {
					return &alwaysAllowAuthrizer{}, nil
				},
			},
			attr: authorizer.AttributesRecord{
				Verb: "get",
				User: &user.DefaultInfo{},
			},
			ctx: dynamiccontext.WithAPIDomainKey(
				genericapirequest.WithCluster(
					context.Background(), genericapirequest.Cluster{Wildcard: true},
				),
				"CachedResourceCluster/cachedresource-1",
			),
			expectedDecision: authorizer.DecisionAllow,
			expectedReason:   "found CachedResource reference",
		},
		"cluster request and no APIBinding": {
			a: contentAuthorizer{
				getCachedResourceEndpointSlice: func(cluster logicalcluster.Name, name string) (*cachev1alpha1.CachedResourceEndpointSlice, error) {
					return &cachev1alpha1.CachedResourceEndpointSlice{
						ObjectMeta: metav1.ObjectMeta{
							Name: "cachedresource-1",
						},
						Spec: cachev1alpha1.CachedResourceEndpointSliceSpec{
							APIExport: cachev1alpha1.ExportBindingReference{
								Path: "root:provider",
								Name: "apiexport-1",
							},
						},
					}, nil
				},
				getAPIExportByPath: func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error) {
					return &apisv1alpha2.APIExport{
						Spec: apisv1alpha2.APIExportSpec{
							Resources: []apisv1alpha2.ResourceSchema{
								{
									Group: "group",
									Name:  "resource",
									Storage: apisv1alpha2.ResourceSchemaStorage{
										Virtual: &apisv1alpha2.ResourceSchemaStorageVirtual{
											Reference: corev1.TypedLocalObjectReference{
												APIGroup: &cachev1alpha1.SchemeGroupVersion.Group,
												Kind:     "CachedResourceEndpointSlice",
												Name:     "cachedresource-1",
											},
										},
									},
								},
							},
						},
						Status: apisv1alpha2.APIExportStatus{
							IdentityHash: "APIExportIdentity",
						},
					}, nil
				},
				getLogicalCluster: func(clusterName logicalcluster.Name) (*corev1alpha1.LogicalCluster, error) {
					return &corev1alpha1.LogicalCluster{}, nil
				},
				getAPIBinding: func(cluster logicalcluster.Name, name string) (*apisv1alpha2.APIBinding, error) {
					return nil, nil
				},
			},
			attr: authorizer.AttributesRecord{
				Verb:            "get",
				User:            &user.DefaultInfo{},
				APIGroup:        "group",
				APIVersion:      "v1",
				Resource:        "resources",
				ResourceRequest: true,
			},
			ctx: dynamiccontext.WithAPIDomainKey(
				genericapirequest.WithCluster(
					context.Background(), genericapirequest.Cluster{Name: "TargetCluster"},
				),
				"CachedResourceCluster/cachedresource-1",
			),
			expectedDecision: authorizer.DecisionDeny,
			expectedReason:   "could not find suitable APIBinding in target logical cluster",
		},
		"cluster request and deny": {
			a: contentAuthorizer{
				getCachedResourceEndpointSlice: func(cluster logicalcluster.Name, name string) (*cachev1alpha1.CachedResourceEndpointSlice, error) {
					return &cachev1alpha1.CachedResourceEndpointSlice{
						ObjectMeta: metav1.ObjectMeta{
							Name: "cachedresource-1",
						},
						Spec: cachev1alpha1.CachedResourceEndpointSliceSpec{
							APIExport: cachev1alpha1.ExportBindingReference{
								Path: "root:provider",
								Name: "apiexport-1",
							},
						},
					}, nil
				},
				getAPIExportByPath: func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error) {
					return &apisv1alpha2.APIExport{
						Spec: apisv1alpha2.APIExportSpec{
							Resources: []apisv1alpha2.ResourceSchema{
								{
									Group: "group",
									Name:  "resource",
									Storage: apisv1alpha2.ResourceSchemaStorage{
										Virtual: &apisv1alpha2.ResourceSchemaStorageVirtual{
											Reference: corev1.TypedLocalObjectReference{
												APIGroup: &cachev1alpha1.SchemeGroupVersion.Group,
												Kind:     "CachedResourceEndpointSlice",
												Name:     "cachedresource-1",
											},
										},
									},
								},
							},
						},
						Status: apisv1alpha2.APIExportStatus{
							IdentityHash: "APIExportIdentity",
						},
					}, nil
				},
				getLogicalCluster: func(clusterName logicalcluster.Name) (*corev1alpha1.LogicalCluster, error) {
					return &corev1alpha1.LogicalCluster{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: map[string]string{
								apibinding.ResourceBindingsAnnotationKey: `{"resource.group": {"n": "apibinding-1"}}`,
							},
						},
					}, nil
				},
				getAPIBinding: func(cluster logicalcluster.Name, name string) (*apisv1alpha2.APIBinding, error) {
					return &apisv1alpha2.APIBinding{
						Spec: apisv1alpha2.APIBindingSpec{
							Reference: apisv1alpha2.BindingReference{
								Export: &apisv1alpha2.ExportBindingReference{
									Path: "root:provider",
									Name: "apiexport-1",
								},
							},
						},
					}, nil
				},
				newDelegatedAuthorizer: func(cluster logicalcluster.Name) (authorizer.Authorizer, error) {
					return &alwaysDenyAuthrizer{}, nil
				},
			},
			attr: authorizer.AttributesRecord{
				Verb:            "get",
				User:            &user.DefaultInfo{},
				APIGroup:        "group",
				APIVersion:      "v1",
				Resource:        "resources",
				ResourceRequest: true,
			},
			ctx: dynamiccontext.WithAPIDomainKey(
				genericapirequest.WithCluster(
					context.Background(), genericapirequest.Cluster{Name: "TargetCluster"},
				),
				"CachedResourceCluster/cachedresource-1",
			),
			expectedDecision: authorizer.DecisionDeny,
			expectedReason:   "alwaysDeny",
		},
		"cluster request and allow": {
			a: contentAuthorizer{
				getCachedResourceEndpointSlice: func(cluster logicalcluster.Name, name string) (*cachev1alpha1.CachedResourceEndpointSlice, error) {
					return &cachev1alpha1.CachedResourceEndpointSlice{
						ObjectMeta: metav1.ObjectMeta{
							Name: "cachedresource-1",
						},
						Spec: cachev1alpha1.CachedResourceEndpointSliceSpec{
							APIExport: cachev1alpha1.ExportBindingReference{
								Path: "root:provider",
								Name: "apiexport-1",
							},
						},
					}, nil
				},
				getAPIExportByPath: func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error) {
					return &apisv1alpha2.APIExport{
						Spec: apisv1alpha2.APIExportSpec{
							Resources: []apisv1alpha2.ResourceSchema{
								{
									Group: "group",
									Name:  "resource",
									Storage: apisv1alpha2.ResourceSchemaStorage{
										Virtual: &apisv1alpha2.ResourceSchemaStorageVirtual{
											Reference: corev1.TypedLocalObjectReference{
												APIGroup: &cachev1alpha1.SchemeGroupVersion.Group,
												Kind:     "CachedResourceEndpointSlice",
												Name:     "cachedresource-1",
											},
										},
									},
								},
							},
						},
						Status: apisv1alpha2.APIExportStatus{
							IdentityHash: "APIExportIdentity",
						},
					}, nil
				},
				getLogicalCluster: func(clusterName logicalcluster.Name) (*corev1alpha1.LogicalCluster, error) {
					return &corev1alpha1.LogicalCluster{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: map[string]string{
								apibinding.ResourceBindingsAnnotationKey: `{"resource.group": {"n": "apibinding-1"}}`,
							},
						},
					}, nil
				},
				getAPIBinding: func(cluster logicalcluster.Name, name string) (*apisv1alpha2.APIBinding, error) {
					return &apisv1alpha2.APIBinding{
						Spec: apisv1alpha2.APIBindingSpec{
							Reference: apisv1alpha2.BindingReference{
								Export: &apisv1alpha2.ExportBindingReference{
									Path: "root:provider",
									Name: "apiexport-1",
								},
							},
						},
					}, nil
				},
				newDelegatedAuthorizer: func(cluster logicalcluster.Name) (authorizer.Authorizer, error) {
					return &alwaysAllowAuthrizer{}, nil
				},
			},
			attr: authorizer.AttributesRecord{
				Verb:            "get",
				User:            &user.DefaultInfo{},
				APIGroup:        "group",
				APIVersion:      "v1",
				Resource:        "resources",
				ResourceRequest: true,
			},
			ctx: dynamiccontext.WithAPIDomainKey(
				genericapirequest.WithCluster(
					context.Background(), genericapirequest.Cluster{Name: "TargetCluster"},
				),
				"CachedResourceCluster/cachedresource-1",
			),
			expectedDecision: authorizer.DecisionAllow,
			expectedReason:   "found CachedResource reference",
		},
	}
	for tname, tt := range tests {
		t.Run(tname, func(t *testing.T) {
			dec, reason, err := tt.a.Authorize(tt.ctx, tt.attr)
			if tt.expectedErrorStr == "" {
				require.NoError(t, err, "was not expecting to return an error")
			} else {
				require.Equal(t, tt.expectedErrorStr, err.Error(), "unexpected error")
			}
			require.Equal(t, tt.expectedDecision, dec, "unexpected decision")
			require.Equal(t, tt.expectedReason, reason, "unexpected reason")
		})
	}
}
