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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/endpoints/request"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
)

func createAttr(cwt *tenancyv1alpha1.ClusterWorkspaceType) admission.Attributes {
	return admission.NewAttributesRecord(
		cwt,
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

// nolint:unused,deadcode
func updateAttr(cwt, old *tenancyv1alpha1.ClusterWorkspace) admission.Attributes {
	return admission.NewAttributesRecord(
		cwt,
		old,
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
		clusterName string
		wantErr     bool
	}{
		{
			name: "allow non-org type in non-root",
			a: createAttr(&tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test",
				},
			}),
			clusterName: "foo:bar",
			wantErr:     false,
		},
		{
			name: "allow organization in root",
			a: createAttr(&tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name: "organization",
				},
			}),
			clusterName: "root",
			wantErr:     false,
		},
		{
			name: "deny organization in non-root",
			a: createAttr(&tenancyv1alpha1.ClusterWorkspaceType{
				ObjectMeta: metav1.ObjectMeta{
					Name: "organization",
				},
			}),
			clusterName: "foo:bar",
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
