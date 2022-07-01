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

package clusterworkspacetype

import (
	"context"
	"testing"

	"github.com/kcp-dev/logicalcluster"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/kcp/pkg/admission/helpers"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
)

func createAttr(cwt *tenancyv1alpha1.ClusterWorkspaceType) admission.Attributes {
	return admission.NewAttributesRecord(
		helpers.ToUnstructuredOrDie(cwt),
		nil,
		tenancyv1alpha1.Kind("ClusterWorkspaceType").WithVersion("v1alpha1"),
		"",
		cwt.Name,
		tenancyv1alpha1.Resource("clusterworkspacetypes").WithVersion("v1alpha1"),
		"",
		admission.Create,
		&metav1.CreateOptions{},
		false,
		&user.DefaultInfo{},
	)
}

func updateAttr(cwt, old *tenancyv1alpha1.ClusterWorkspaceType) admission.Attributes {
	return admission.NewAttributesRecord(
		helpers.ToUnstructuredOrDie(cwt),
		helpers.ToUnstructuredOrDie(old),
		tenancyv1alpha1.Kind("ClusterWorkspaceType").WithVersion("v1alpha1"),
		"",
		cwt.Name,
		tenancyv1alpha1.Resource("clusterworkspacetypes").WithVersion("v1alpha1"),
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
		a           admission.Attributes
		clusterName logicalcluster.Name
		wantErr     bool
	}{
		{
			name: "allow non-org type in non-root",
			a: createAttr(&tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
			}),
			clusterName: logicalcluster.New("foo:bar"),
			wantErr:     false,
		},
		{
			name: "allow organization in root",
			a: createAttr(&tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name: "organization",
				},
			}),
			clusterName: logicalcluster.New("root"),
			wantErr:     false,
		},
		{
			name: "deny organization in non-root",
			a: createAttr(&tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name: "organization",
				},
			}),
			clusterName: logicalcluster.New("foo:bar"),
			wantErr:     true,
		},
		{
			name: "deny invalid name",
			a: createAttr(&tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name: "a:b",
				},
			}),
			clusterName: logicalcluster.New("foo:bar"),
			wantErr:     true,
		},
		{
			name: "deny changing type extensions",
			a: updateAttr(&tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name: "root:thing",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
					Extend: tenancyv1alpha1.ClusterWorkspaceTypeExtension{
						With: []tenancyv1alpha1.ClusterWorkspaceTypeReference{},
						Without: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
							{Path: "root:foo", Name: "Bar"},
							{Path: "root:foo", Name: "Baz"},
						},
					},
				},
			}, &tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name: "root:thing",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
					Extend: tenancyv1alpha1.ClusterWorkspaceTypeExtension{
						With: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
							{Path: "root:foo", Name: "Foo"},
						},
						Without: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
							{Path: "root:foo", Name: "Bar"},
						},
					},
				},
			}),
			clusterName: logicalcluster.New("foo:bar"),
			wantErr:     true,
		},
		{
			name: "deny changing default type",
			a: updateAttr(&tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name: "root:thing",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
					DefaultChildWorkspaceType: &tenancyv1alpha1.ClusterWorkspaceTypeReference{
						Name: "some", Path: "root",
					},
				},
			}, &tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name: "root:thing",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
					DefaultChildWorkspaceType: &tenancyv1alpha1.ClusterWorkspaceTypeReference{
						Name: "other", Path: "root",
					},
				},
			}),
			clusterName: logicalcluster.New("foo:bar"),
			wantErr:     true,
		},
		{
			name: "deny changing child types",
			a: updateAttr(&tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name: "root:thing",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
					AllowedChildWorkspaceTypes: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
						{Name: "foo", Path: "root"}, {Name: "bar", Path: "root"},
					},
				},
			}, &tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name: "root:thing",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
					AllowedChildWorkspaceTypes: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
						{Name: "chess", Path: "root"}, {Name: "checkers", Path: "root"},
					},
				},
			}),
			clusterName: logicalcluster.New("foo:bar"),
			wantErr:     true,
		},
		{
			name: "deny changing parent types",
			a: updateAttr(&tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name: "root:thing",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
					AllowedParentWorkspaceTypes: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
						{Name: "foo", Path: "root"}, {Name: "bar", Path: "root"},
					},
				},
			}, &tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name: "root:thing",
				},
				Spec: tenancyv1alpha1.ClusterWorkspaceTypeSpec{
					AllowedParentWorkspaceTypes: []tenancyv1alpha1.ClusterWorkspaceTypeReference{
						{Name: "chess", Path: "root"}, {Name: "checkers", Path: "root"},
					},
				},
			}),
			clusterName: logicalcluster.New("foo:bar"),
			wantErr:     true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := &clusterWorkspaceType{
				Handler: admission.NewHandler(admission.Create, admission.Update),
			}
			ctx := request.WithCluster(context.Background(), request.Cluster{Name: tt.clusterName})
			if err := o.Validate(ctx, tt.a, nil); (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
