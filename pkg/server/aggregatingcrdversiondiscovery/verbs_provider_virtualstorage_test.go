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

package aggregatingcrdversiondiscovery

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"

	"github.com/kcp-dev/kcp/pkg/cache/client/shard"
)

func Test_newVirtualStorageVerbsProvider(t *testing.T) {
	res := schema.GroupVersionResource{
		Group:    "wildwest.dev",
		Version:  "v1alpha1",
		Resource: "sheriffs",
	}
	schName := "today.sheriffs.wildwest.dev"

	vrSliceKind := schema.GroupVersionKind{
		Group:   "myendpointslice.example.com",
		Version: "v1",
		Kind:    "MyEndpointSlice",
	}
	vrSliceName := "my-endpoint-slice"
	tests := map[string]struct {
		apiExport         *apisv1alpha2.APIExport
		opts              *virtualStorageClientOptions
		wantResourceVerbs []string
		wantStatusVerbs   []string
		wantScaleVerbs    []string
		wantErr           error
	}{
		"resource not in VW": {
			opts: &virtualStorageClientOptions{
				VWClientConfig: &rest.Config{},
				APIResourceDiscoveryForResource: func(_ context.Context, _ *rest.Config, groupVersion schema.GroupVersion) (*metav1.APIResourceList, error) {
					return &metav1.APIResourceList{
						APIResources: []metav1.APIResource{},
					}, nil
				},
				RESTMappingFor: func(cluster logicalcluster.Name, gk schema.GroupKind) (*meta.RESTMapping, error) {
					return &meta.RESTMapping{
						Resource: res,
					}, nil
				},
				GetUnstructuredEndpointSlice: func(ctx context.Context, cluster logicalcluster.Name, shard shard.Name, gvr schema.GroupVersionResource, name string) (*unstructured.Unstructured, error) {
					return &unstructured.Unstructured{
						Object: map[string]interface{}{
							"status": map[string]interface{}{
								"endpoints": []interface{}{
									map[string]interface{}{"url": "https://shard-1/my-endpoint-url"},
								},
							},
						},
					}, nil
				},
				ThisShardVirtualWorkspaceURLGetter: func() string { return "https://shard-1" },
			},
			apiExport: &apisv1alpha2.APIExport{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						shard.AnnotationKey:          "shard-1",
						logicalcluster.AnnotationKey: "cluster-1",
					},
				},
				Spec: apisv1alpha2.APIExportSpec{
					Resources: []apisv1alpha2.ResourceSchema{
						{
							Group:  res.Group,
							Name:   res.Resource,
							Schema: schName,
							Storage: apisv1alpha2.ResourceSchemaStorage{
								Virtual: &apisv1alpha2.ResourceSchemaStorageVirtual{
									Reference: corev1.TypedLocalObjectReference{
										APIGroup: ptr.To(vrSliceKind.GroupVersion().Identifier()),
										Kind:     vrSliceKind.Kind,
										Name:     vrSliceName,
									},
									IdentityHash: "vr-identity-123",
								},
							},
						},
					},
				},
				Status: apisv1alpha2.APIExportStatus{
					IdentityHash: "apiexport-identity-123",
				},
			},
			wantResourceVerbs: nil,
			wantStatusVerbs:   nil,
			wantScaleVerbs:    nil,
		},
		"missing VW url in endpoint slice": {
			opts: &virtualStorageClientOptions{
				RESTMappingFor: func(cluster logicalcluster.Name, gk schema.GroupKind) (*meta.RESTMapping, error) {
					return &meta.RESTMapping{
						Resource: res,
					}, nil
				},
				GetUnstructuredEndpointSlice: func(ctx context.Context, cluster logicalcluster.Name, shard shard.Name, gvr schema.GroupVersionResource, name string) (*unstructured.Unstructured, error) {
					return &unstructured.Unstructured{
						Object: map[string]interface{}{},
					}, nil
				},
			},
			apiExport: &apisv1alpha2.APIExport{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						shard.AnnotationKey:          "shard-1",
						logicalcluster.AnnotationKey: "cluster-1",
					},
				},
				Spec: apisv1alpha2.APIExportSpec{
					Resources: []apisv1alpha2.ResourceSchema{
						{
							Group:  res.Group,
							Name:   res.Resource,
							Schema: schName,
							Storage: apisv1alpha2.ResourceSchemaStorage{
								Virtual: &apisv1alpha2.ResourceSchemaStorageVirtual{
									Reference: corev1.TypedLocalObjectReference{
										APIGroup: ptr.To(vrSliceKind.GroupVersion().Identifier()),
										Kind:     vrSliceKind.Kind,
										Name:     vrSliceName,
									},
									IdentityHash: "vr-identity-123",
								},
							},
						},
					},
				},
				Status: apisv1alpha2.APIExportStatus{
					IdentityHash: "apiexport-identity-123",
				},
			},
			wantErr: fmt.Errorf("failed to retrieve virtual workspace URL: missing status"),
		},
		"resource with verbs in VW": {
			opts: &virtualStorageClientOptions{
				VWClientConfig: &rest.Config{},
				APIResourceDiscoveryForResource: func(_ context.Context, _ *rest.Config, groupVersion schema.GroupVersion) (*metav1.APIResourceList, error) {
					return &metav1.APIResourceList{
						APIResources: []metav1.APIResource{
							{
								Name:    "sheriffs",
								Group:   "wildwest.dev",
								Version: "v1alpha1",
								Verbs:   []string{"delete", "deletecollection", "get", "list", "patch", "create", "update", "watch"},
							},
							{
								Name:    "sheriffs/status",
								Group:   "wildwest.dev",
								Version: "v1alpha1",
								Verbs:   []string{"get", "list", "watch"},
							},
						},
					}, nil
				},
				RESTMappingFor: func(cluster logicalcluster.Name, gk schema.GroupKind) (*meta.RESTMapping, error) {
					return &meta.RESTMapping{
						Resource: res,
					}, nil
				},
				GetUnstructuredEndpointSlice: func(ctx context.Context, cluster logicalcluster.Name, shard shard.Name, gvr schema.GroupVersionResource, name string) (*unstructured.Unstructured, error) {
					return &unstructured.Unstructured{
						Object: map[string]interface{}{
							"status": map[string]interface{}{
								"endpoints": []interface{}{
									map[string]interface{}{"url": "https://shard-1/my-endpoint-url"},
								},
							},
						},
					}, nil
				},
				ThisShardVirtualWorkspaceURLGetter: func() string { return "https://shard-1" },
			},
			apiExport: &apisv1alpha2.APIExport{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						shard.AnnotationKey:          "shard-1",
						logicalcluster.AnnotationKey: "cluster-1",
					},
				},
				Spec: apisv1alpha2.APIExportSpec{
					Resources: []apisv1alpha2.ResourceSchema{
						{
							Group:  res.Group,
							Name:   res.Resource,
							Schema: schName,
							Storage: apisv1alpha2.ResourceSchemaStorage{
								Virtual: &apisv1alpha2.ResourceSchemaStorageVirtual{
									Reference: corev1.TypedLocalObjectReference{
										APIGroup: ptr.To(vrSliceKind.GroupVersion().Identifier()),
										Kind:     vrSliceKind.Kind,
										Name:     vrSliceName,
									},
									IdentityHash: "vr-identity-123",
								},
							},
						},
					},
				},
				Status: apisv1alpha2.APIExportStatus{
					IdentityHash: "apiexport-identity-123",
				},
			},
			wantResourceVerbs: []string{"delete", "deletecollection", "get", "list", "patch", "create", "update", "watch"},
			wantStatusVerbs:   []string{"get", "list", "watch"},
			wantScaleVerbs:    nil,
		},
	}
	for tname, tt := range tests {
		t.Run(tname, func(t *testing.T) {
			p, err := newVirtualStorageVerbsProvider(context.Background(), res, "vr-identity-123", tt.apiExport, tt.opts)
			if tt.wantErr != nil {
				require.Equal(t, tt.wantErr.Error(), err.Error())
				return
			} else if err != nil {
				require.NoError(t, err)
				return
			}

			require.Equal(t, tt.wantResourceVerbs, p.resource(), "unexpected resource verbs: has %v", p.resourceVerbs)
			require.Equal(t, tt.wantStatusVerbs, p.statusSubresource(), "unexpected status subresource verbs: has %v", p.resourceVerbs)
			require.Equal(t, tt.wantScaleVerbs, p.scaleSubresource(), "unexpected scale subresource verbs: has %v", p.resourceVerbs)
		})
	}
}
