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
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/kcp-dev/logicalcluster/v3"

	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	cachev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/cache/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/util/conditions"
)

func TestReconcileReplicateResourceSchema(t *testing.T) {
	tests := map[string]struct {
		CachedResource     *cachev1alpha1.CachedResource
		reconciler         *replicateResourceSchema
		expectedErr        error
		expectedStatus     reconcileStatus
		expectedConditions conditionsv1alpha1.Conditions
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
			reconciler:     &replicateResourceSchema{},
			expectedStatus: reconcileStatusContinue,
		},
		"has CachedResourceSchemaSourceValid=false condition, and should abort": {
			CachedResource: &cachev1alpha1.CachedResource{
				Status: cachev1alpha1.CachedResourceStatus{
					Conditions: conditionsv1alpha1.Conditions{
						*conditions.FalseCondition(
							cachev1alpha1.CachedResourceSchemaSourceValid,
							cachev1alpha1.SchemaSourceNotReadyReason,
							conditionsv1alpha1.ConditionSeverityError,
							"",
						),
					},
				},
			},
			reconciler:     &replicateResourceSchema{},
			expectedStatus: reconcileStatusStopAndRequeue,
			expectedConditions: conditionsv1alpha1.Conditions{
				*conditions.FalseCondition(
					cachev1alpha1.CachedResourceSchemaSourceValid,
					cachev1alpha1.SchemaSourceNotReadyReason,
					conditionsv1alpha1.ConditionSeverityError,
					"",
				),
			},
		},

		//
		// APIResourceSchemaSource
		//

		"APIResourceSchemaSource with replicated schema": {
			CachedResource: &cachev1alpha1.CachedResource{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cowboys-cr",
					UID:  "cowboys-cr-uid",
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "consumer_cluster_name",
					},
				},
				Status: cachev1alpha1.CachedResourceStatus{
					ResourceSchemaSource: &cachev1alpha1.CachedResourceSchemaSource{
						APIResourceSchema: &cachev1alpha1.APIResourceSchemaSource{},
					},
				},
			},
			reconciler: &replicateResourceSchema{
				getLocalAPIResourceSchema: func(cluster logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error) {
					return &apisv1alpha1.APIResourceSchema{}, nil
				},
			},
			expectedStatus: reconcileStatusContinue,
		},
		"APIResourceSchemaSource with missing replicated schema and missing source schema": {
			CachedResource: &cachev1alpha1.CachedResource{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cowboys-cr",
					UID:  "cowboys-cr-uid",
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "consumer_cluster_name",
					},
				},
				Status: cachev1alpha1.CachedResourceStatus{
					ResourceSchemaSource: &cachev1alpha1.CachedResourceSchemaSource{
						APIResourceSchema: &cachev1alpha1.APIResourceSchemaSource{
							ClusterName: "providers_cowboys_cluster_name",
							Name:        "missing-cowboys-schema",
						},
					},
				},
			},
			reconciler: &replicateResourceSchema{
				getAPIResourceSchema: func(cluster logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error) {
					return nil, apierrors.NewNotFound(apisv1alpha1.Resource("apiresourceschemas"), name)
				},
				getLocalAPIResourceSchema: func(cluster logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error) {
					return nil, apierrors.NewNotFound(apisv1alpha1.Resource("apiresourceschemas"), name)
				},
			},
			expectedStatus: reconcileStatusStopAndRequeue,
			expectedConditions: conditionsv1alpha1.Conditions{
				*conditions.FalseCondition(
					cachev1alpha1.CachedResourceSourceSchemaReplicated,
					cachev1alpha1.SourceSchemaReplicatedFailedReason,
					conditionsv1alpha1.ConditionSeverityError,
					`Failed to get source APIResourceSchema: apiresourceschemas.apis.kcp.io "missing-cowboys-schema" not found.`,
				),
			},
			expectedErr: fmt.Errorf(`apiresourceschemas.apis.kcp.io "missing-cowboys-schema" not found`),
		},
		"APIResourceSchemaSource with missing replicated schema and invalid source schema": {
			CachedResource: &cachev1alpha1.CachedResource{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cowboys-cr",
					UID:  "cowboys-cr-uid",
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "consumer_cluster_name",
					},
				},
				Spec: cachev1alpha1.CachedResourceSpec{
					GroupVersionResource: cachev1alpha1.GroupVersionResource{
						Group:    "wildwest.dev",
						Version:  "v1alpha1",
						Resource: "cowboys",
					},
				},
				Status: cachev1alpha1.CachedResourceStatus{
					ResourceSchemaSource: &cachev1alpha1.CachedResourceSchemaSource{
						APIResourceSchema: &cachev1alpha1.APIResourceSchemaSource{
							ClusterName: "providers_cowboys_cluster_name",
							Name:        "today.cowboys.wildwest.dev",
						},
					},
				},
			},
			reconciler: &replicateResourceSchema{
				getLocalAPIResourceSchema: func(cluster logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error) {
					return nil, apierrors.NewNotFound(apisv1alpha1.Resource("apiresourceschemas"), name)
				},
				getAPIResourceSchema: func(cluster logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error) {
					m := map[logicalcluster.Name]map[string]*apisv1alpha1.APIResourceSchema{
						"providers_cowboys_cluster_name": {
							"today.cowboys.wildwest.dev": {
								Spec: apisv1alpha1.APIResourceSchemaSpec{
									Group: "calmwest.org",
									Names: apiextensionsv1.CustomResourceDefinitionNames{
										Plural: "cowgirls",
									},
									Versions: []apisv1alpha1.APIResourceVersion{
										{
											Name: "v1",
										},
									},
								},
							},
						},
					}
					if sch := m[cluster][name]; sch != nil {
						return sch, nil
					}
					return nil, apierrors.NewNotFound(apisv1alpha1.Resource("apiresourceschemas"), name)
				},
			},
			expectedStatus: reconcileStatusStop,
			expectedConditions: conditionsv1alpha1.Conditions{
				*conditions.FalseCondition(
					cachev1alpha1.CachedResourceSchemaSourceValid,
					cachev1alpha1.SchemaSourceInvalidReason,
					conditionsv1alpha1.ConditionSeverityError,
					`Schema is not valid. Please contact the APIExport owner to resolve.`,
				),
			},
		},
		"APIResourceSchemaSource with missing replicated schema and failing create": {
			CachedResource: &cachev1alpha1.CachedResource{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cowboys-cr",
					UID:  "cowboys-cr-uid",
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "consumer_cluster_name",
					},
				},
				Spec: cachev1alpha1.CachedResourceSpec{
					GroupVersionResource: cachev1alpha1.GroupVersionResource{
						Group:    "wildwest.dev",
						Version:  "v1alpha1",
						Resource: "cowboys",
					},
				},
				Status: cachev1alpha1.CachedResourceStatus{
					ResourceSchemaSource: &cachev1alpha1.CachedResourceSchemaSource{
						APIResourceSchema: &cachev1alpha1.APIResourceSchemaSource{
							ClusterName: "providers_cowboys_cluster_name",
							Name:        "today.cowboys.wildwest.dev",
						},
					},
				},
			},
			reconciler: &replicateResourceSchema{
				getLocalAPIResourceSchema: func(cluster logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error) {
					return nil, apierrors.NewNotFound(apisv1alpha1.Resource("apiresourceschemas"), name)
				},
				getAPIResourceSchema: func(cluster logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error) {
					m := map[logicalcluster.Name]map[string]*apisv1alpha1.APIResourceSchema{
						"providers_cowboys_cluster_name": {
							"today.cowboys.wildwest.dev": {
								Spec: apisv1alpha1.APIResourceSchemaSpec{
									Group: "wildwest.dev",
									Names: apiextensionsv1.CustomResourceDefinitionNames{
										Plural: "cowboys",
									},
									Versions: []apisv1alpha1.APIResourceVersion{
										{
											Name: "v1alpha1",
										},
									},
								},
							},
						},
					}
					if sch := m[cluster][name]; sch != nil {
						return sch, nil
					}
					return nil, apierrors.NewNotFound(apisv1alpha1.Resource("apiresourceschemas"), name)
				},
				createCachedAPIResourceSchema: func(ctx context.Context, cluster logicalcluster.Name, sch *apisv1alpha1.APIResourceSchema) error {
					return fmt.Errorf("create failed")
				},
			},
			expectedStatus: reconcileStatusStopAndRequeue,
			expectedConditions: conditionsv1alpha1.Conditions{
				*conditions.FalseCondition(
					cachev1alpha1.CachedResourceSourceSchemaReplicated,
					cachev1alpha1.SourceSchemaReplicatedFailedReason,
					conditionsv1alpha1.ConditionSeverityError,
					`Failed to store schema: create failed.`,
				),
			},
			expectedErr: fmt.Errorf("create failed"),
		},
		"APIResourceSchemaSource with missing replicated schema succeeds": {
			CachedResource: &cachev1alpha1.CachedResource{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cowboys-cr",
					UID:  "cowboys-cr-uid",
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "consumer_cluster_name",
					},
				},
				Spec: cachev1alpha1.CachedResourceSpec{
					GroupVersionResource: cachev1alpha1.GroupVersionResource{
						Group:    "wildwest.dev",
						Version:  "v1alpha1",
						Resource: "cowboys",
					},
				},
				Status: cachev1alpha1.CachedResourceStatus{
					ResourceSchemaSource: &cachev1alpha1.CachedResourceSchemaSource{
						APIResourceSchema: &cachev1alpha1.APIResourceSchemaSource{
							ClusterName: "providers_cowboys_cluster_name",
							Name:        "today.cowboys.wildwest.dev",
						},
					},
				},
			},
			reconciler: &replicateResourceSchema{
				getLocalAPIResourceSchema: func(cluster logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error) {
					return nil, apierrors.NewNotFound(apisv1alpha1.Resource("apiresourceschemas"), name)
				},
				getAPIResourceSchema: func(cluster logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error) {
					m := map[logicalcluster.Name]map[string]*apisv1alpha1.APIResourceSchema{
						"providers_cowboys_cluster_name": {
							"today.cowboys.wildwest.dev": {
								Spec: apisv1alpha1.APIResourceSchemaSpec{
									Group: "wildwest.dev",
									Names: apiextensionsv1.CustomResourceDefinitionNames{
										Plural: "cowboys",
									},
									Versions: []apisv1alpha1.APIResourceVersion{
										{
											Name: "v1alpha1",
										},
									},
								},
							},
						},
					}
					if sch := m[cluster][name]; sch != nil {
						return sch, nil
					}
					return nil, apierrors.NewNotFound(apisv1alpha1.Resource("apiresourceschemas"), name)
				},
				createCachedAPIResourceSchema: func(ctx context.Context, cluster logicalcluster.Name, sch *apisv1alpha1.APIResourceSchema) error {
					return nil
				},
			},
			expectedStatus: reconcileStatusStopAndRequeue,
			expectedConditions: conditionsv1alpha1.Conditions{
				*conditions.TrueCondition(cachev1alpha1.CachedResourceSourceSchemaReplicated),
			},
			expectedErr: nil,
		},

		//
		// CRDSchemaSource
		//

		"CRDSchemaSource with up-to-date replicated schema": {
			CachedResource: &cachev1alpha1.CachedResource{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cowboys-cr",
					UID:  "cowboys-cr-uid",
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "consumer_cluster_name",
					},
				},
				Spec: cachev1alpha1.CachedResourceSpec{
					GroupVersionResource: cachev1alpha1.GroupVersionResource{
						Group:    "wildwest.dev",
						Version:  "v1alpha1",
						Resource: "cowboys",
					},
				},
				Status: cachev1alpha1.CachedResourceStatus{
					ResourceSchemaSource: &cachev1alpha1.CachedResourceSchemaSource{
						CRD: &cachev1alpha1.CRDSchemaSource{
							Name:            "cowboys-crd",
							ResourceVersion: "latest",
						},
					},
				},
			},
			reconciler: &replicateResourceSchema{
				getCRD: func(ctx context.Context, cluster logicalcluster.Name, name string) (*apiextensionsv1.CustomResourceDefinition, error) {
					return &apiextensionsv1.CustomResourceDefinition{
						ObjectMeta: metav1.ObjectMeta{
							Name:            "cowboys-crd",
							ResourceVersion: "latest",
						},
						Spec: apiextensionsv1.CustomResourceDefinitionSpec{
							Group: "wildwest.dev",
							Names: apiextensionsv1.CustomResourceDefinitionNames{
								Plural: "cowboys",
							},
						},
						Status: apiextensionsv1.CustomResourceDefinitionStatus{
							Conditions: []apiextensionsv1.CustomResourceDefinitionCondition{
								{
									Type:   apiextensionsv1.Established,
									Status: apiextensionsv1.ConditionTrue,
								},
							},
							StoredVersions: []string{"v1alpha1"},
						},
					}, nil
				},
				getLocalAPIResourceSchema: func(cluster logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error) {
					m := map[logicalcluster.Name]map[string]*apisv1alpha1.APIResourceSchema{
						"providers_cowboys_cluster_name": {
							"today.cowboys.wildwest.dev": {
								Spec: apisv1alpha1.APIResourceSchemaSpec{
									Group: "wildwest.dev",
									Names: apiextensionsv1.CustomResourceDefinitionNames{
										Plural: "cowboys",
									},
									Versions: []apisv1alpha1.APIResourceVersion{
										{
											Name: "v1alpha1",
										},
									},
								},
							},
						},
						"consumer_cluster_name": {
							"cachedresources-cache-kcp-io-cowboys-cr-uid.cowboys.wildwest.dev": &apisv1alpha1.APIResourceSchema{},
						},
					}
					if sch := m[cluster][name]; sch != nil {
						return sch, nil
					}
					return nil, apierrors.NewNotFound(apisv1alpha1.Resource("apiresourceschemas"), name)
				},
				getAPIResourceSchema: func(cluster logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error) {
					m := map[logicalcluster.Name]map[string]*apisv1alpha1.APIResourceSchema{
						"providers_cowboys_cluster_name": {
							"today.cowboys.wildwest.dev": {
								Spec: apisv1alpha1.APIResourceSchemaSpec{
									Group: "wildwest.dev",
									Names: apiextensionsv1.CustomResourceDefinitionNames{
										Plural: "cowboys",
									},
									Versions: []apisv1alpha1.APIResourceVersion{
										{
											Name: "v1alpha1",
										},
									},
								},
							},
						},
						"consumer_cluster_name": {
							"cachedresources-cache-kcp-io-cowboys-cr-uid.cowboys.wildwest.dev": &apisv1alpha1.APIResourceSchema{},
						},
					}
					if sch := m[cluster][name]; sch != nil {
						return sch, nil
					}
					return nil, apierrors.NewNotFound(apisv1alpha1.Resource("apiresourceschemas"), name)
				},
			},
			expectedStatus: reconcileStatusContinue,
		},
		"CRDSchemaSource with missing replicated schema fails": {
			CachedResource: &cachev1alpha1.CachedResource{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cowboys-cr",
					UID:  "cowboys-cr-uid",
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "consumer_cluster_name",
					},
				},
				Spec: cachev1alpha1.CachedResourceSpec{
					GroupVersionResource: cachev1alpha1.GroupVersionResource{
						Group:    "wildwest.dev",
						Version:  "v1alpha1",
						Resource: "cowboys",
					},
				},
				Status: cachev1alpha1.CachedResourceStatus{
					ResourceSchemaSource: &cachev1alpha1.CachedResourceSchemaSource{
						CRD: &cachev1alpha1.CRDSchemaSource{
							Name:            "cowboys-crd",
							ResourceVersion: "latest",
						},
					},
				},
			},
			reconciler: &replicateResourceSchema{
				getCRD: func(ctx context.Context, cluster logicalcluster.Name, name string) (*apiextensionsv1.CustomResourceDefinition, error) {
					return &apiextensionsv1.CustomResourceDefinition{
						ObjectMeta: metav1.ObjectMeta{
							Name:            "cowboys-crd",
							ResourceVersion: "latest",
						},
						Spec: apiextensionsv1.CustomResourceDefinitionSpec{
							Group: "wildwest.dev",
							Names: apiextensionsv1.CustomResourceDefinitionNames{
								Plural: "cowboys",
							},
						},
						Status: apiextensionsv1.CustomResourceDefinitionStatus{
							Conditions: []apiextensionsv1.CustomResourceDefinitionCondition{
								{
									Type:   apiextensionsv1.Established,
									Status: apiextensionsv1.ConditionTrue,
								},
							},
							StoredVersions: []string{"v1alpha1"},
						},
					}, nil
				},
				getLocalAPIResourceSchema: func(cluster logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error) {
					return nil, apierrors.NewNotFound(apisv1alpha1.Resource("apiresourceschemas"), name)
				},
				getAPIResourceSchema: func(cluster logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error) {
					m := map[logicalcluster.Name]map[string]*apisv1alpha1.APIResourceSchema{
						"providers_cowboys_cluster_name": {
							"today.cowboys.wildwest.dev": {
								Spec: apisv1alpha1.APIResourceSchemaSpec{
									Group: "wildwest.dev",
									Names: apiextensionsv1.CustomResourceDefinitionNames{
										Plural: "cowboys",
									},
									Versions: []apisv1alpha1.APIResourceVersion{
										{
											Name: "v1alpha1",
										},
									},
								},
							},
						},
					}
					if sch := m[cluster][name]; sch != nil {
						return sch, nil
					}
					return nil, apierrors.NewNotFound(apisv1alpha1.Resource("apiresourceschemas"), name)
				},
				createCachedAPIResourceSchema: func(ctx context.Context, cluster logicalcluster.Name, sch *apisv1alpha1.APIResourceSchema) error {
					return fmt.Errorf("create failed")
				},
			},
			expectedStatus: reconcileStatusStopAndRequeue,
			expectedConditions: conditionsv1alpha1.Conditions{
				*conditions.FalseCondition(
					cachev1alpha1.CachedResourceSourceSchemaReplicated,
					cachev1alpha1.SourceSchemaReplicatedFailedReason,
					conditionsv1alpha1.ConditionSeverityError,
					`Failed to replicate schema: create failed.`,
				),
			},
			expectedErr: fmt.Errorf("create failed"),
		},
		"CRDSchemaSource with missing replicated schema succeeds": {
			CachedResource: &cachev1alpha1.CachedResource{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cowboys-cr",
					UID:  "cowboys-cr-uid",
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "consumer_cluster_name",
					},
				},
				Spec: cachev1alpha1.CachedResourceSpec{
					GroupVersionResource: cachev1alpha1.GroupVersionResource{
						Group:    "wildwest.dev",
						Version:  "v1alpha1",
						Resource: "cowboys",
					},
				},
				Status: cachev1alpha1.CachedResourceStatus{
					ResourceSchemaSource: &cachev1alpha1.CachedResourceSchemaSource{
						CRD: &cachev1alpha1.CRDSchemaSource{
							Name:            "cowboys-crd",
							ResourceVersion: "latest",
						},
					},
				},
			},
			reconciler: &replicateResourceSchema{
				getCRD: func(ctx context.Context, cluster logicalcluster.Name, name string) (*apiextensionsv1.CustomResourceDefinition, error) {
					return &apiextensionsv1.CustomResourceDefinition{
						ObjectMeta: metav1.ObjectMeta{
							Name:            "cowboys-crd",
							ResourceVersion: "latest",
						},
						Spec: apiextensionsv1.CustomResourceDefinitionSpec{
							Group: "wildwest.dev",
							Names: apiextensionsv1.CustomResourceDefinitionNames{
								Plural: "cowboys",
							},
						},
						Status: apiextensionsv1.CustomResourceDefinitionStatus{
							Conditions: []apiextensionsv1.CustomResourceDefinitionCondition{
								{
									Type:   apiextensionsv1.Established,
									Status: apiextensionsv1.ConditionTrue,
								},
							},
							StoredVersions: []string{"v1alpha1"},
						},
					}, nil
				},
				getLocalAPIResourceSchema: func(cluster logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error) {
					return nil, apierrors.NewNotFound(apisv1alpha1.Resource("apiresourceschemas"), name)
				},
				getAPIResourceSchema: func(cluster logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error) {
					m := map[logicalcluster.Name]map[string]*apisv1alpha1.APIResourceSchema{
						"providers_cowboys_cluster_name": {
							"today.cowboys.wildwest.dev": {
								Spec: apisv1alpha1.APIResourceSchemaSpec{
									Group: "wildwest.dev",
									Names: apiextensionsv1.CustomResourceDefinitionNames{
										Plural: "cowboys",
									},
									Versions: []apisv1alpha1.APIResourceVersion{
										{
											Name: "v1alpha1",
										},
									},
								},
							},
						},
					}
					if sch := m[cluster][name]; sch != nil {
						return sch, nil
					}
					return nil, apierrors.NewNotFound(apisv1alpha1.Resource("apiresourceschemas"), name)
				},
				createCachedAPIResourceSchema: func(ctx context.Context, cluster logicalcluster.Name, sch *apisv1alpha1.APIResourceSchema) error {
					return nil
				},
			},
			expectedStatus: reconcileStatusStopAndRequeue,
			expectedConditions: conditionsv1alpha1.Conditions{
				*conditions.TrueCondition(cachev1alpha1.CachedResourceSourceSchemaReplicated),
			},
		},
		"CRDSchemaSource with out-of-date replicated schema fails": {
			CachedResource: &cachev1alpha1.CachedResource{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cowboys-cr",
					UID:  "cowboys-cr-uid",
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "consumer_cluster_name",
					},
				},
				Spec: cachev1alpha1.CachedResourceSpec{
					GroupVersionResource: cachev1alpha1.GroupVersionResource{
						Group:    "wildwest.dev",
						Version:  "v1alpha1",
						Resource: "cowboys",
					},
				},
				Status: cachev1alpha1.CachedResourceStatus{
					ResourceSchemaSource: &cachev1alpha1.CachedResourceSchemaSource{
						CRD: &cachev1alpha1.CRDSchemaSource{
							Name:            "cowboys-crd",
							ResourceVersion: "old",
						},
					},
				},
			},
			reconciler: &replicateResourceSchema{
				getCRD: func(ctx context.Context, cluster logicalcluster.Name, name string) (*apiextensionsv1.CustomResourceDefinition, error) {
					return &apiextensionsv1.CustomResourceDefinition{
						ObjectMeta: metav1.ObjectMeta{
							Name:            "cowboys-crd",
							ResourceVersion: "latest",
						},
						Spec: apiextensionsv1.CustomResourceDefinitionSpec{
							Group: "wildwest.dev",
							Names: apiextensionsv1.CustomResourceDefinitionNames{
								Plural: "cowboys",
							},
						},
						Status: apiextensionsv1.CustomResourceDefinitionStatus{
							Conditions: []apiextensionsv1.CustomResourceDefinitionCondition{
								{
									Type:   apiextensionsv1.Established,
									Status: apiextensionsv1.ConditionTrue,
								},
							},
							StoredVersions: []string{"v1alpha1"},
						},
					}, nil
				},
				getLocalAPIResourceSchema: func(cluster logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error) {
					m := map[logicalcluster.Name]map[string]*apisv1alpha1.APIResourceSchema{
						"providers_cowboys_cluster_name": {
							"today.cowboys.wildwest.dev": {
								Spec: apisv1alpha1.APIResourceSchemaSpec{
									Group: "wildwest.dev",
									Names: apiextensionsv1.CustomResourceDefinitionNames{
										Plural: "cowboys",
									},
									Versions: []apisv1alpha1.APIResourceVersion{
										{
											Name: "v1alpha1",
										},
									},
								},
							},
						},
						"consumer_cluster_name": {
							"cachedresources-cache-kcp-io-cowboys-cr-uid.cowboys.wildwest.dev": &apisv1alpha1.APIResourceSchema{},
						},
					}
					if sch := m[cluster][name]; sch != nil {
						return sch, nil
					}
					return nil, apierrors.NewNotFound(apisv1alpha1.Resource("apiresourceschemas"), name)
				},
				updateCreateAPIResourceSchema: func(ctx context.Context, cluster logicalcluster.Name, sch *apisv1alpha1.APIResourceSchema) error {
					return fmt.Errorf("update failed")
				},
			},
			expectedStatus: reconcileStatusStopAndRequeue,
			expectedConditions: conditionsv1alpha1.Conditions{
				*conditions.FalseCondition(
					cachev1alpha1.CachedResourceSourceSchemaReplicated,
					cachev1alpha1.SourceSchemaReplicatedFailedReason,
					conditionsv1alpha1.ConditionSeverityError,
					`Failed to update the replicated schema: update failed.`,
				),
			},
			expectedErr: fmt.Errorf("update failed"),
		},
		"CRDSchemaSource with out-of-date replicated schema succeeds": {
			CachedResource: &cachev1alpha1.CachedResource{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cowboys-cr",
					UID:  "cowboys-cr-uid",
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "consumer_cluster_name",
					},
				},
				Spec: cachev1alpha1.CachedResourceSpec{
					GroupVersionResource: cachev1alpha1.GroupVersionResource{
						Group:    "wildwest.dev",
						Version:  "v1alpha1",
						Resource: "cowboys",
					},
				},
				Status: cachev1alpha1.CachedResourceStatus{
					ResourceSchemaSource: &cachev1alpha1.CachedResourceSchemaSource{
						CRD: &cachev1alpha1.CRDSchemaSource{
							Name:            "cowboys-crd",
							ResourceVersion: "old",
						},
					},
				},
			},
			reconciler: &replicateResourceSchema{
				getCRD: func(ctx context.Context, cluster logicalcluster.Name, name string) (*apiextensionsv1.CustomResourceDefinition, error) {
					return &apiextensionsv1.CustomResourceDefinition{
						ObjectMeta: metav1.ObjectMeta{
							Name:            "cowboys-crd",
							ResourceVersion: "latest",
						},
						Spec: apiextensionsv1.CustomResourceDefinitionSpec{
							Group: "wildwest.dev",
							Names: apiextensionsv1.CustomResourceDefinitionNames{
								Plural: "cowboys",
							},
						},
						Status: apiextensionsv1.CustomResourceDefinitionStatus{
							Conditions: []apiextensionsv1.CustomResourceDefinitionCondition{
								{
									Type:   apiextensionsv1.Established,
									Status: apiextensionsv1.ConditionTrue,
								},
							},
							StoredVersions: []string{"v1alpha1"},
						},
					}, nil
				},
				getLocalAPIResourceSchema: func(cluster logicalcluster.Name, name string) (*apisv1alpha1.APIResourceSchema, error) {
					m := map[logicalcluster.Name]map[string]*apisv1alpha1.APIResourceSchema{
						"providers_cowboys_cluster_name": {
							"today.cowboys.wildwest.dev": {
								Spec: apisv1alpha1.APIResourceSchemaSpec{
									Group: "wildwest.dev",
									Names: apiextensionsv1.CustomResourceDefinitionNames{
										Plural: "cowboys",
									},
									Versions: []apisv1alpha1.APIResourceVersion{
										{
											Name: "v1alpha1",
										},
									},
								},
							},
						},
						"consumer_cluster_name": {
							"cachedresources-cache-kcp-io-cowboys-cr-uid.cowboys.wildwest.dev": &apisv1alpha1.APIResourceSchema{},
						},
					}
					if sch := m[cluster][name]; sch != nil {
						return sch, nil
					}
					return nil, apierrors.NewNotFound(apisv1alpha1.Resource("apiresourceschemas"), name)
				},
				updateCreateAPIResourceSchema: func(ctx context.Context, cluster logicalcluster.Name, sch *apisv1alpha1.APIResourceSchema) error {
					return nil
				},
			},
			expectedStatus: reconcileStatusStopAndRequeue,
			expectedConditions: conditionsv1alpha1.Conditions{
				*conditions.TrueCondition(cachev1alpha1.CachedResourceSourceSchemaReplicated),
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
		})
	}
}
