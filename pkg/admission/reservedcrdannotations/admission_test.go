/*
Copyright 2022 The KCP Authors.

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

package reservedcrdannotations

import (
	"context"
	"testing"

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/kcp/pkg/admission/helpers"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
)

func createAttr(obj *apiextensions.CustomResourceDefinition) admission.Attributes {
	return admission.NewAttributesRecord(
		obj,
		nil,
		apiextensionsv1.Kind("CustomResourceDefinition").WithVersion("v1"),
		"",
		"test",
		apiextensionsv1.Resource("customresourcedefinitions").WithVersion("v1"),
		"",
		admission.Create,
		&metav1.CreateOptions{},
		false,
		&user.DefaultInfo{},
	)
}

func createAttrAPIBinding(apiBinding *apisv1alpha1.APIBinding) admission.Attributes {
	return admission.NewAttributesRecord(
		helpers.ToUnstructuredOrDie(apiBinding),
		nil,
		apisv1alpha1.Kind("APIBinding").WithVersion("v1alpha1"),
		"",
		apiBinding.Name,
		apisv1alpha1.Resource("apibindings").WithVersion("v1alpha1"),
		"",
		admission.Create,
		&metav1.CreateOptions{},
		false,
		&user.DefaultInfo{},
	)
}

func updateAttr(obj, old *apiextensions.CustomResourceDefinition) admission.Attributes {
	return admission.NewAttributesRecord(
		obj,
		old,
		apiextensionsv1.Kind("CustomResourceDefinition").WithVersion("v1"),
		"",
		"test",
		apiextensionsv1.Resource("customresourcedefinitions").WithVersion("v1"),
		"",
		admission.Update,
		&metav1.CreateOptions{},
		false,
		&user.DefaultInfo{},
	)
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name        string
		attr        admission.Attributes
		clusterName string

		wantErr bool
	}{
		{
			name: "passes create no annotation",
			attr: createAttr(&apiextensions.CustomResourceDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
			}),
			clusterName: "root:org:ws",
		},
		{
			name: "fails create annotation wrong workspace",
			attr: createAttr(&apiextensions.CustomResourceDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
					Annotations: map[string]string{
						"apis.kcp.dev/bound-crd": "true",
					},
				},
			}),
			wantErr:     true,
			clusterName: "root:org:ws",
		},
		{
			name: "fails create some other annotation",
			attr: createAttr(&apiextensions.CustomResourceDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
					Annotations: map[string]string{
						"abc.apis.kcp.dev/xyz": "true",
					},
				},
			}),
			wantErr:     true,
			clusterName: "root:org:ws",
		},
		{
			name: "passes create annotation in some other system workspace",
			attr: createAttr(&apiextensions.CustomResourceDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
					Annotations: map[string]string{
						"apis.kcp.dev/bound-crd": "true",
					},
				},
			}),
			clusterName: "system:bound-crds",
		},
		{
			name: "fails create annotation some other system workspace",
			attr: createAttr(&apiextensions.CustomResourceDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
					Annotations: map[string]string{
						"apis.kcp.dev/bound-crd": "true",
					},
				},
			}),
			wantErr:     true,
			clusterName: "system:admin",
		},
		{
			name: "passes not a CRD",
			attr: createAttrAPIBinding(&apisv1alpha1.APIBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
					Annotations: map[string]string{
						"apis.kcp.dev/bound-crd": "true",
					},
				},
			}),
			clusterName: "root:org:ws",
		},
		{
			name: "passes update no annotation",
			attr: updateAttr(&apiextensions.CustomResourceDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
			}, &apiextensions.CustomResourceDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
			}),
			clusterName: "root:org:ws",
		},
		{
			name: "fails update annotation wrong workspace",
			attr: updateAttr(&apiextensions.CustomResourceDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
					Annotations: map[string]string{
						"apis.kcp.dev/bound-crd": "true",
					},
				},
			}, &apiextensions.CustomResourceDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
			}),
			wantErr:     true,
			clusterName: "root:org:ws",
		},
		{
			name: "passes update annotation correct workspace",
			attr: updateAttr(&apiextensions.CustomResourceDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
					Annotations: map[string]string{
						"apis.kcp.dev/bound-crd": "true",
					},
				},
			}, &apiextensions.CustomResourceDefinition{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
			}),
			clusterName: "system:bound-crds",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := &reservedCRDAnnotations{
				Handler: admission.NewHandler(admission.Create, admission.Update),
			}
			var ctx context.Context

			require.NotEmpty(t, tt.clusterName, "clusterName must be set in this test")

			ctx = request.WithCluster(context.Background(), request.Cluster{Name: logicalcluster.New(tt.clusterName)})
			if err := o.Validate(ctx, tt.attr, nil); (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
