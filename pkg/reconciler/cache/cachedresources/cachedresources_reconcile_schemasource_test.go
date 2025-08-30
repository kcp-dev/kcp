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

package cachedresources

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"

	"github.com/kcp-dev/logicalcluster/v3"

	"github.com/kcp-dev/kcp/pkg/reconciler/apis/apibinding"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha2"
	cachev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/cache/v1alpha1"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/util/conditions"
)

func TestReconcileSchemaSource(t *testing.T) {
	tests := map[string]struct {
		CachedResource     *cachev1alpha1.CachedResource
		reconciler         *schemaSource
		expectedErr        error
		expectedStatus     reconcileStatus
		expectedConditions conditionsv1alpha1.Conditions
		expectedSchemaSrc  *cachev1alpha1.CachedResourceSchemaSource
	}{
		//
		// Common
		//

		"has deletion timestamp and should skip": {
			CachedResource: &cachev1alpha1.CachedResource{
				ObjectMeta: metav1.ObjectMeta{
					DeletionTimestamp: ptr.To(metav1.Now()),
				},
			},
			reconciler:     &schemaSource{},
			expectedStatus: reconcileStatusContinue,
		},
		"has CachedResourceSchemaSourceValid=true condition and should skip": {
			CachedResource: &cachev1alpha1.CachedResource{
				Status: cachev1alpha1.CachedResourceStatus{
					Conditions: conditionsv1alpha1.Conditions{
						*conditions.TrueCondition(
							cachev1alpha1.CachedResourceSchemaSourceValid,
						),
					},
				},
			},
			reconciler:     &schemaSource{},
			expectedStatus: reconcileStatusContinue,
			expectedConditions: conditionsv1alpha1.Conditions{
				*conditions.TrueCondition(
					cachev1alpha1.CachedResourceSchemaSourceValid,
				),
			},
		},
		"bad internal.apis.kcp.io/resource-bindings annotation on LogicalCluster": {
			CachedResource: &cachev1alpha1.CachedResource{
				Spec: cachev1alpha1.CachedResourceSpec{
					GroupVersionResource: cachev1alpha1.GroupVersionResource{
						Group:    "wildwest.dev",
						Version:  "v1alpha1",
						Resource: "cowboys",
					},
				},
			},
			reconciler: &schemaSource{
				getLogicalCluster: func(cluster logicalcluster.Name) (*corev1alpha1.LogicalCluster, error) {
					return &corev1alpha1.LogicalCluster{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: map[string]string{
								apibinding.ResourceBindingsAnnotationKey: `{"cowboys.wildwest.dev": {}}`,
							},
						},
					}, nil
				},
			},
			expectedStatus: reconcileStatusStop,
			expectedConditions: conditionsv1alpha1.Conditions{
				*conditions.FalseCondition(
					cachev1alpha1.CachedResourceSchemaSourceValid,
					cachev1alpha1.SchemaSourceNotReadyReason,
					conditionsv1alpha1.ConditionSeverityError,
					"API not ready.",
				),
			},
			expectedSchemaSrc: nil,
		},

		//
		// APIResourceSchemaSource
		//

		"APIResourceSchemaSource with missing resource": {
			CachedResource: &cachev1alpha1.CachedResource{
				Spec: cachev1alpha1.CachedResourceSpec{
					GroupVersionResource: cachev1alpha1.GroupVersionResource{
						Group:    "wildwest.dev",
						Version:  "v1alpha1",
						Resource: "cowboys",
					},
				},
			},
			reconciler: &schemaSource{
				getLogicalCluster: func(cluster logicalcluster.Name) (*corev1alpha1.LogicalCluster, error) {
					return &corev1alpha1.LogicalCluster{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: map[string]string{
								apibinding.ResourceBindingsAnnotationKey: `{"cowboys.wildwest.dev": {"n": "cowboys-binding"}}`,
							},
						},
					}, nil
				},
				getAPIBinding: func(cluster logicalcluster.Name, name string) (*apisv1alpha2.APIBinding, error) {
					return &apisv1alpha2.APIBinding{
						Spec: apisv1alpha2.APIBindingSpec{
							Reference: apisv1alpha2.BindingReference{
								Export: &apisv1alpha2.ExportBindingReference{
									Path: "providers:cowboys",
									Name: "cowboys-export",
								},
							},
						},
					}, nil
				},
				getAPIExport: func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error) {
					return &apisv1alpha2.APIExport{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: map[string]string{
								logicalcluster.AnnotationKey: "providers_cowboys_cluster_name",
							},
						},
						Spec: apisv1alpha2.APIExportSpec{
							Resources: []apisv1alpha2.ResourceSchema{},
						},
					}, nil
				},
			},
			expectedStatus: reconcileStatusStop,
			expectedConditions: conditionsv1alpha1.Conditions{
				*conditions.FalseCondition(
					cachev1alpha1.CachedResourceSchemaSourceValid,
					cachev1alpha1.SchemaSourceInvalidReason,
					conditionsv1alpha1.ConditionSeverityError,
					"No valid schema available in APIExport providers:cowboys:cowboys-export. Please contact the APIExport owner to resolve.",
				),
			},
			expectedSchemaSrc: nil,
		},
		"APIResourceSchemaSource with non-CRD storage": {
			CachedResource: &cachev1alpha1.CachedResource{
				Spec: cachev1alpha1.CachedResourceSpec{
					GroupVersionResource: cachev1alpha1.GroupVersionResource{
						Group:    "wildwest.dev",
						Version:  "v1alpha1",
						Resource: "cowboys",
					},
				},
			},
			reconciler: &schemaSource{
				getLogicalCluster: func(cluster logicalcluster.Name) (*corev1alpha1.LogicalCluster, error) {
					return &corev1alpha1.LogicalCluster{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: map[string]string{
								apibinding.ResourceBindingsAnnotationKey: `{"cowboys.wildwest.dev": {"n": "cowboys-binding"}}`,
							},
						},
					}, nil
				},
				getAPIBinding: func(cluster logicalcluster.Name, name string) (*apisv1alpha2.APIBinding, error) {
					return &apisv1alpha2.APIBinding{
						Spec: apisv1alpha2.APIBindingSpec{
							Reference: apisv1alpha2.BindingReference{
								Export: &apisv1alpha2.ExportBindingReference{
									Path: "providers:cowboys",
									Name: "cowboys-export",
								},
							},
						},
					}, nil
				},
				getAPIExport: func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error) {
					return &apisv1alpha2.APIExport{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: map[string]string{
								logicalcluster.AnnotationKey: "providers_cowboys_cluster_name",
							},
						},
						Spec: apisv1alpha2.APIExportSpec{
							Resources: []apisv1alpha2.ResourceSchema{
								{
									Group:   "wildwest.dev",
									Name:    "cowboys",
									Schema:  "today.cowboys.wildwest.dev",
									Storage: apisv1alpha2.ResourceSchemaStorage{},
								},
							},
						},
					}, nil
				},
			},
			expectedStatus: reconcileStatusStop,
			expectedConditions: conditionsv1alpha1.Conditions{
				*conditions.FalseCondition(
					cachev1alpha1.CachedResourceSchemaSourceValid,
					cachev1alpha1.SchemaSourceInvalidReason,
					conditionsv1alpha1.ConditionSeverityError,
					"Schema today.cowboys.wildwest.dev in APIExport providers:cowboys:cowboys-export is incompatible. Please contact the APIExport owner to resolve.",
				),
			},
			expectedSchemaSrc: nil,
		},
		"APIResourceSchemaSource": {
			CachedResource: &cachev1alpha1.CachedResource{
				Spec: cachev1alpha1.CachedResourceSpec{
					GroupVersionResource: cachev1alpha1.GroupVersionResource{
						Group:    "wildwest.dev",
						Version:  "v1alpha1",
						Resource: "cowboys",
					},
				},
			},
			reconciler: &schemaSource{
				getLogicalCluster: func(cluster logicalcluster.Name) (*corev1alpha1.LogicalCluster, error) {
					return &corev1alpha1.LogicalCluster{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: map[string]string{
								apibinding.ResourceBindingsAnnotationKey: `{"cowboys.wildwest.dev": {"n": "cowboys-binding"}}`,
							},
						},
					}, nil
				},
				getAPIBinding: func(cluster logicalcluster.Name, name string) (*apisv1alpha2.APIBinding, error) {
					return &apisv1alpha2.APIBinding{
						Spec: apisv1alpha2.APIBindingSpec{
							Reference: apisv1alpha2.BindingReference{
								Export: &apisv1alpha2.ExportBindingReference{
									Path: "providers:cowboys",
									Name: "cowboys-export",
								},
							},
						},
					}, nil
				},
				getAPIExport: func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error) {
					return &apisv1alpha2.APIExport{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: map[string]string{
								logicalcluster.AnnotationKey: "providers_cowboys_cluster_name",
							},
						},
						Spec: apisv1alpha2.APIExportSpec{
							Resources: []apisv1alpha2.ResourceSchema{
								{
									Group:  "wildwest.dev",
									Name:   "cowboys",
									Schema: "today.cowboys.wildwest.dev",
									Storage: apisv1alpha2.ResourceSchemaStorage{
										CRD: &apisv1alpha2.ResourceSchemaStorageCRD{},
									},
								},
							},
						},
					}, nil
				},
				getAPIResourceSchema: func(cluster logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error) {
					return &apisv1alpha1.APIResourceSchema{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: map[string]string{
								logicalcluster.AnnotationKey: "providers_cowboys_cluster_name",
							},
							Name: "today.cowboys.wildwest.dev",
						},
						Spec: apisv1alpha1.APIResourceSchemaSpec{
							Versions: []apisv1alpha1.APIResourceVersion{
								{
									Name: "v1alpha1",
								},
							},
						},
					}, nil
				},
			},
			expectedStatus: reconcileStatusContinue,
			expectedConditions: conditionsv1alpha1.Conditions{
				*conditions.TrueCondition(
					cachev1alpha1.CachedResourceSchemaSourceValid,
				),
			},
			expectedSchemaSrc: &cachev1alpha1.CachedResourceSchemaSource{
				APIResourceSchema: &cachev1alpha1.APIResourceSchemaSource{
					ClusterName: "providers_cowboys_cluster_name",
					Name:        "today.cowboys.wildwest.dev",
				},
			},
		},

		//
		// CRDSchemaSource
		//

		"CRDSchemaSource but version is missing": {
			CachedResource: &cachev1alpha1.CachedResource{
				Spec: cachev1alpha1.CachedResourceSpec{
					GroupVersionResource: cachev1alpha1.GroupVersionResource{
						Group:    "wildwest.dev",
						Version:  "v1alpha1",
						Resource: "cowboys",
					},
				},
			},
			reconciler: &schemaSource{
				getLogicalCluster: func(cluster logicalcluster.Name) (*corev1alpha1.LogicalCluster, error) {
					return &corev1alpha1.LogicalCluster{}, nil
				},
				listCRDsByGR: func(cluster logicalcluster.Name, gr schema.GroupResource) ([]*apiextensionsv1.CustomResourceDefinition, error) {
					return []*apiextensionsv1.CustomResourceDefinition{
						{
							ObjectMeta: metav1.ObjectMeta{
								Name: "cowboys-crd",
							},
							Status: apiextensionsv1.CustomResourceDefinitionStatus{
								StoredVersions: []string{"v1alpha2"},
								Conditions: []apiextensionsv1.CustomResourceDefinitionCondition{
									{
										Type:   apiextensionsv1.Established,
										Status: apiextensionsv1.ConditionTrue,
									},
								},
							},
						},
					}, nil
				},
			},
			expectedStatus: reconcileStatusStop,
			expectedConditions: conditionsv1alpha1.Conditions{
				*conditions.FalseCondition(
					cachev1alpha1.CachedResourceSchemaSourceValid,
					cachev1alpha1.SchemaSourceInvalidReason,
					conditionsv1alpha1.ConditionSeverityError,
					"CRD cowboys-crd does not define the requested resource version.",
				),
			},
			expectedSchemaSrc: nil,
		},
		"CRDSchemaSource but CRD is not Established": {
			CachedResource: &cachev1alpha1.CachedResource{
				Spec: cachev1alpha1.CachedResourceSpec{
					GroupVersionResource: cachev1alpha1.GroupVersionResource{
						Group:    "wildwest.dev",
						Version:  "v1alpha1",
						Resource: "cowboys",
					},
				},
			},
			reconciler: &schemaSource{
				getLogicalCluster: func(cluster logicalcluster.Name) (*corev1alpha1.LogicalCluster, error) {
					return &corev1alpha1.LogicalCluster{}, nil
				},
				listCRDsByGR: func(cluster logicalcluster.Name, gr schema.GroupResource) ([]*apiextensionsv1.CustomResourceDefinition, error) {
					return []*apiextensionsv1.CustomResourceDefinition{
						{
							ObjectMeta: metav1.ObjectMeta{
								Name: "cowboys-crd",
							},
							Status: apiextensionsv1.CustomResourceDefinitionStatus{
								StoredVersions: []string{"v1alpha1"},
								Conditions: []apiextensionsv1.CustomResourceDefinitionCondition{
									{
										Type:   apiextensionsv1.Established,
										Status: apiextensionsv1.ConditionFalse,
									},
								},
							},
						},
					}, nil
				},
			},
			expectedStatus: reconcileStatusStop,
			expectedConditions: conditionsv1alpha1.Conditions{
				*conditions.FalseCondition(
					cachev1alpha1.CachedResourceSchemaSourceValid,
					cachev1alpha1.SchemaSourceNotReadyReason,
					conditionsv1alpha1.ConditionSeverityError,
					"API not ready.",
				),
			},
			expectedSchemaSrc: nil,
		},
		"CRDSchemaSource": {
			CachedResource: &cachev1alpha1.CachedResource{
				Spec: cachev1alpha1.CachedResourceSpec{
					GroupVersionResource: cachev1alpha1.GroupVersionResource{
						Group:    "wildwest.dev",
						Version:  "v1alpha1",
						Resource: "cowboys",
					},
				},
			},
			reconciler: &schemaSource{
				getLogicalCluster: func(cluster logicalcluster.Name) (*corev1alpha1.LogicalCluster, error) {
					return &corev1alpha1.LogicalCluster{}, nil
				},
				listCRDsByGR: func(cluster logicalcluster.Name, gr schema.GroupResource) ([]*apiextensionsv1.CustomResourceDefinition, error) {
					return []*apiextensionsv1.CustomResourceDefinition{
						{
							ObjectMeta: metav1.ObjectMeta{
								Name: "cowboys-crd",
							},
							Status: apiextensionsv1.CustomResourceDefinitionStatus{
								StoredVersions: []string{"v1alpha1"},
								Conditions: []apiextensionsv1.CustomResourceDefinitionCondition{
									{
										Type:   apiextensionsv1.Established,
										Status: apiextensionsv1.ConditionTrue,
									},
								},
							},
						},
					}, nil
				},
			},
			expectedStatus: reconcileStatusStopAndRequeue,
			expectedConditions: conditionsv1alpha1.Conditions{
				*conditions.TrueCondition(
					cachev1alpha1.CachedResourceSchemaSourceValid,
				),
			},
			expectedSchemaSrc: &cachev1alpha1.CachedResourceSchemaSource{
				CRD: &cachev1alpha1.CRDSchemaSource{
					Name: "cowboys-crd",
				},
			},
		},
		"CRDSchemaSource with claimed CRD": {
			CachedResource: &cachev1alpha1.CachedResource{
				Spec: cachev1alpha1.CachedResourceSpec{
					GroupVersionResource: cachev1alpha1.GroupVersionResource{
						Group:    "wildwest.dev",
						Version:  "v1alpha1",
						Resource: "cowboys",
					},
				},
			},
			reconciler: &schemaSource{
				getLogicalCluster: func(cluster logicalcluster.Name) (*corev1alpha1.LogicalCluster, error) {
					return &corev1alpha1.LogicalCluster{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: map[string]string{
								apibinding.ResourceBindingsAnnotationKey: `{"cowboys.wildwest.dev": {"c": true}}`,
							},
						},
					}, nil
				},
				listCRDsByGR: func(cluster logicalcluster.Name, gr schema.GroupResource) ([]*apiextensionsv1.CustomResourceDefinition, error) {
					return []*apiextensionsv1.CustomResourceDefinition{
						{
							ObjectMeta: metav1.ObjectMeta{
								Name: "cowboys-crd",
							},
							Status: apiextensionsv1.CustomResourceDefinitionStatus{
								StoredVersions: []string{"v1alpha1"},
								Conditions: []apiextensionsv1.CustomResourceDefinitionCondition{
									{
										Type:   apiextensionsv1.Established,
										Status: apiextensionsv1.ConditionTrue,
									},
								},
							},
						},
					}, nil
				},
			},
			expectedStatus: reconcileStatusStopAndRequeue,
			expectedConditions: conditionsv1alpha1.Conditions{
				*conditions.TrueCondition(
					cachev1alpha1.CachedResourceSchemaSourceValid,
				),
			},
			expectedSchemaSrc: &cachev1alpha1.CachedResourceSchemaSource{
				CRD: &cachev1alpha1.CRDSchemaSource{
					Name: "cowboys-crd",
				},
			},
		},
	}

	for testName, tt := range tests {
		t.Run(testName, func(t *testing.T) {
			status, err := tt.reconciler.reconcile(context.Background(), tt.CachedResource)

			resetLastTransitionTime(tt.expectedConditions)
			resetLastTransitionTime(tt.CachedResource.Status.Conditions)

			if tt.expectedErr != nil {
				require.Error(t, err)
				require.Equal(t, tt.expectedErr.Error(), err.Error())
			} else {
				require.NoError(t, err)
			}

			require.Equal(t, tt.expectedStatus, status, "reconcile status mismatch")
			require.Equal(t, tt.expectedConditions, tt.CachedResource.Status.Conditions, "conditions mismatch")
			require.Equal(t, tt.expectedSchemaSrc, tt.CachedResource.Status.ResourceSchemaSource, "ResourceSchemaSource mismatch")
		})
	}
}

func resetLastTransitionTime(conditions conditionsv1alpha1.Conditions) {
	// We don't care about LastTransitionTime.
	for i := range conditions {
		conditions[i].LastTransitionTime = metav1.Time{}
	}
}
