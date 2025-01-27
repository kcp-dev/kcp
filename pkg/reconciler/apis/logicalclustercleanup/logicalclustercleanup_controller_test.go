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

package logicalclustercleanup

import (
	"context"
	goerrors "errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
)

func TestReconciler(t *testing.T) {
	expired := time.Now().Add(-1).UTC().Format(time.RFC3339)
	notExpired := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)

	tests := map[string]struct {
		logicalCluster *corev1alpha1.LogicalCluster
		crds           []*apiextensionsv1.CustomResourceDefinition
		apiBindings    []*apisv1alpha1.APIBinding
		updateError    error

		want    *corev1alpha1.LogicalCluster
		wantErr bool
	}{
		"no LogicalCluster is ignored": {},
		"no annotations leads to migration – no crds or bindings": {
			logicalCluster: &corev1alpha1.LogicalCluster{},
			want: &corev1alpha1.LogicalCluster{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				"internal.apis.kcp.io/resource-bindings": "{}",
			}}},
		},
		"update error": {
			logicalCluster: &corev1alpha1.LogicalCluster{},
			updateError:    errors.NewInternalError(goerrors.New("")),
			wantErr:        true,
		},
		"no annotations leads to migration – with crds and bindings": {
			logicalCluster: &corev1alpha1.LogicalCluster{},
			crds: []*apiextensionsv1.CustomResourceDefinition{
				newCRD("group", "unestablisheds"),
				withEstablished(newCRD("group", "crd1s")),
				withEstablished(newCRD("group", "crd2s")),
			},
			apiBindings: []*apisv1alpha1.APIBinding{
				&newAPIBinding().WithName("binding1").WithBoundResources("group", "as", "group", "bs").APIBinding,
				&newAPIBinding().WithName("binding2").WithBoundResources("group", "cs", "group", "ds").APIBinding,
			},
			want: &corev1alpha1.LogicalCluster{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				"internal.apis.kcp.io/resource-bindings": `{"as.group":{"n":"binding1"},"bs.group":{"n":"binding1"},"crd1s.group":{"c":true},"crd2s.group":{"c":true},"cs.group":{"n":"binding2"},"ds.group":{"n":"binding2"}}`,
			}}},
		},
		"with annotation, only CRDs are added": {
			logicalCluster: &corev1alpha1.LogicalCluster{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				"internal.apis.kcp.io/resource-bindings": `{"as.group":{"n":"binding1"},"crd1s.group":{"c":true}}`,
			}}},
			crds: []*apiextensionsv1.CustomResourceDefinition{
				newCRD("group", "unestablisheds"),
				withEstablished(newCRD("group", "crd1s")),
				withEstablished(newCRD("group", "crd2s")),
			},
			apiBindings: []*apisv1alpha1.APIBinding{
				&newAPIBinding().WithName("binding1").WithBoundResources("group", "as", "group", "bs").APIBinding,
				&newAPIBinding().WithName("binding2").WithBoundResources("group", "cs", "group", "ds").APIBinding,
			},
			want: &corev1alpha1.LogicalCluster{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				"internal.apis.kcp.io/resource-bindings": `{"as.group":{"n":"binding1"},"crd1s.group":{"c":true},"crd2s.group":{"c":true}}`,
			}}},
		},
		"CRDs are not removed by default, but bindings are": {
			logicalCluster: &corev1alpha1.LogicalCluster{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				"internal.apis.kcp.io/resource-bindings": `{"as.group":{"n":"binding1"},"bs.group":{"n":"binding1"},"crd1s.group":{"c":true},"crd2s.group":{"c":true},"cs.group":{"n":"binding2"},"ds.group":{"n":"binding2"}}`,
			}}},
			apiBindings: []*apisv1alpha1.APIBinding{
				&newAPIBinding().WithName("binding1").WithBoundResources("group", "as", "group", "bs").APIBinding,
			},
			want: &corev1alpha1.LogicalCluster{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				"internal.apis.kcp.io/resource-bindings": `{"as.group":{"n":"binding1"},"bs.group":{"n":"binding1"},"crd1s.group":{"c":true},"crd2s.group":{"c":true}}`,
			}}},
		},
		"Expired CRDs that don't exist are removed": {
			logicalCluster: &corev1alpha1.LogicalCluster{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				"internal.apis.kcp.io/resource-bindings": fmt.Sprintf(`{"crd1s.group":{"c":true,"e":%q},"crd2s.group":{"c":true,"e":%q},"crd3s.group":{"c":true,"e":%q}}`, expired, expired, notExpired),
			}}},
			crds: []*apiextensionsv1.CustomResourceDefinition{
				newCRD("group", "crd1s"),
			},
			want: &corev1alpha1.LogicalCluster{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				"internal.apis.kcp.io/resource-bindings": fmt.Sprintf(`{"crd1s.group":{"c":true},"crd3s.group":{"c":true,"e":%q}}`, notExpired),
			}}},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			var got *corev1alpha1.LogicalCluster
			c := &controller{
				getLogicalCluster: func(clusterName logicalcluster.Name) (*corev1alpha1.LogicalCluster, error) {
					if tt.logicalCluster == nil {
						return nil, errors.NewNotFound(corev1alpha1.Resource("logicalclusters"), string(clusterName))
					}
					return tt.logicalCluster, nil
				},
				updateLogicalCluster: func(ctx context.Context, lc *corev1alpha1.LogicalCluster) error {
					if tt.updateError != nil {
						return tt.updateError
					}
					got = lc
					return nil
				},
				listsCRDs: func(clusterName logicalcluster.Name) ([]*apiextensionsv1.CustomResourceDefinition, error) {
					return tt.crds, nil
				},
				listAPIBindings: func(clusterName logicalcluster.Name) ([]*apisv1alpha1.APIBinding, error) {
					return tt.apiBindings, nil
				},
			}
			if err := c.process(context.Background(), cache.ToClusterAwareKey("foo", "", "cluster")); (err != nil) != tt.wantErr {
				t.Errorf("process() error = %v, wantErr %v", err, tt.wantErr)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("process() -want +got:\n%s", diff)
			}
		})
	}
}

type bindingBuilder struct {
	apisv1alpha1.APIBinding
}

func newAPIBinding() *bindingBuilder {
	b := new(bindingBuilder)
	return b
}

func (b *bindingBuilder) WithName(name string) *bindingBuilder {
	b.Name = name
	return b
}

func (b *bindingBuilder) WithBoundResources(boundResources ...string) *bindingBuilder {
	for i := 0; i < len(boundResources); i += 2 {
		group, resource := boundResources[i], boundResources[i+1]
		b.Status.BoundResources = append(b.Status.BoundResources, apisv1alpha1.BoundAPIResource{
			Group:    group,
			Resource: resource,
		})
	}
	return b
}

func newCRD(group, resource string) *apiextensionsv1.CustomResourceDefinition {
	return &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("%s.%s", resource, group),
		},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: group,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural: resource,
			},
		},
		Status: apiextensionsv1.CustomResourceDefinitionStatus{
			AcceptedNames: apiextensionsv1.CustomResourceDefinitionNames{
				Plural: resource,
			},
		},
	}
}

func withEstablished(crd *apiextensionsv1.CustomResourceDefinition) *apiextensionsv1.CustomResourceDefinition {
	crd = crd.DeepCopy()
	crd.Status.Conditions = append(crd.Status.Conditions, apiextensionsv1.CustomResourceDefinitionCondition{
		Type:   apiextensionsv1.Established,
		Status: apiextensionsv1.ConditionTrue,
	})
	return crd
}
