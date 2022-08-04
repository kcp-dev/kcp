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

package clusterworkspaceshard

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/kcp/pkg/admission/helpers"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
)

func createAttr(shard *shardBuilder) admission.Attributes {
	return admission.NewAttributesRecord(
		helpers.ToUnstructuredOrDie(shard.ClusterWorkspaceShard),
		nil,
		tenancyv1alpha1.Kind("ClusterWorkspaceShard").WithVersion("v1alpha1"),
		"",
		shard.Name,
		tenancyv1alpha1.Resource("clusterworkspaceshards").WithVersion("v1alpha1"),
		"",
		admission.Create,
		&metav1.CreateOptions{},
		false,
		&user.DefaultInfo{},
	)
}

func updateAttr(shard, old *shardBuilder) admission.Attributes {
	return admission.NewAttributesRecord(
		helpers.ToUnstructuredOrDie(shard.ClusterWorkspaceShard),
		helpers.ToUnstructuredOrDie(old.ClusterWorkspaceShard),
		tenancyv1alpha1.Kind("ClusterWorkspace").WithVersion("v1alpha1"),
		"",
		shard.Name,
		tenancyv1alpha1.Resource("clusterworkspaceshards").WithVersion("v1alpha1"),
		"",
		admission.Update,
		&metav1.CreateOptions{},
		false,
		&user.DefaultInfo{},
	)
}

type shardBuilder struct {
	*tenancyv1alpha1.ClusterWorkspaceShard
}

func newTestShard() *shardBuilder {
	return &shardBuilder{
		ClusterWorkspaceShard: &tenancyv1alpha1.ClusterWorkspaceShard{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test",
			},
		},
	}
}

func (b *shardBuilder) baseURL(u string) *shardBuilder {
	b.Spec.BaseURL = u
	return b
}

func (b *shardBuilder) externalURL(u string) *shardBuilder {
	b.Spec.ExternalURL = u
	return b
}

func (b *shardBuilder) virtualWorkspaceURL(u string) *shardBuilder {
	b.Spec.VirtualWorkspaceURL = u
	return b
}

func TestAdmitIgnoresOtherResources(t *testing.T) {
	o := &clusterWorkspaceShard{
		Handler: admission.NewHandler(admission.Create, admission.Update),
	}

	ctx := request.WithCluster(context.Background(), request.Cluster{Name: logicalcluster.New("root:org")})

	a := admission.NewAttributesRecord(
		&unstructured.Unstructured{},
		nil,
		tenancyv1alpha1.Kind("ClusterWorkspace").WithVersion("v1alpha1"),
		"",
		"test",
		tenancyv1alpha1.Resource("clusterworkspaces").WithVersion("v1alpha1"),
		"",
		admission.Create,
		&metav1.CreateOptions{},
		false,
		&user.DefaultInfo{},
	)

	err := o.Admit(ctx, a, nil)
	require.NoError(t, err)
	require.Equal(t, &unstructured.Unstructured{}, a.GetObject())
}

func TestNoOp(t *testing.T) {
	shard := newTestShard().baseURL("https://boston2.kcp.dev").externalURL("https://kcp2.dev").virtualWorkspaceURL("https://boston2.kcp.dev")
	attrs := map[string]admission.Attributes{
		"create": createAttr(shard),
		"update": updateAttr(shard, shard),
	}

	unstructuredShard := helpers.ToUnstructuredOrDie(shard.ClusterWorkspaceShard)

	for aType, a := range attrs {
		t.Run(aType, func(t *testing.T) {
			o := &clusterWorkspaceShard{
				Handler: admission.NewHandler(admission.Create, admission.Update),
			}

			ctx := request.WithCluster(context.Background(), request.Cluster{Name: logicalcluster.New("root:org")})

			err := o.Admit(ctx, a, nil)
			require.NoError(t, err)
			require.Empty(t, cmp.Diff(unstructuredShard, a.GetObject()))

			err = o.Validate(ctx, a, nil)
			require.NoError(t, err)
			require.Empty(t, cmp.Diff(unstructuredShard, a.GetObject()))
		})
	}
}

func TestAdmit(t *testing.T) {
	tests := []struct {
		name                      string
		emptyExternalAddress      bool
		noExternalAddressProvider bool
		emptyShardExternalURL     bool
		emptyShardBaseURL         bool
		expectedObj               *shardBuilder
	}{
		{
			name:        "default baseURL to shardBaseURL when both shardBaseURL and externalAddress are set",
			expectedObj: newTestShard().baseURL("https://shard.base").externalURL("https://shard.external").virtualWorkspaceURL("https://shard.base"),
		},
		{
			name:              "default baseURL to externalAddress when only externalAddress is set",
			emptyShardBaseURL: true,
			expectedObj:       newTestShard().baseURL("https://external.kcp.dev").externalURL("https://shard.external").virtualWorkspaceURL("https://external.kcp.dev"),
		},
		{
			name:        "default externalURL to shardExternalURL when both shardExternalURL and externalAddress are set",
			expectedObj: newTestShard().baseURL("https://shard.base").externalURL("https://shard.external").virtualWorkspaceURL("https://shard.base"),
		},
		{
			name:                  "default externalURL to externalAddress when only externalAddress is set",
			emptyShardExternalURL: true,
			expectedObj:           newTestShard().baseURL("https://shard.base").externalURL("https://external.kcp.dev").virtualWorkspaceURL("https://shard.base"),
		},
	}
	for _, tt := range tests {
		shard := newTestShard()
		attrs := map[string]admission.Attributes{
			"create": createAttr(shard),
			"update": updateAttr(shard, shard),
		}

		for aType, a := range attrs {
			t.Run(tt.name+" - "+aType, func(t *testing.T) {
				o := &clusterWorkspaceShard{
					Handler:                 admission.NewHandler(admission.Create, admission.Update),
					externalAddressProvider: func() string { return "external.kcp.dev" },
					shardExternalURL:        "https://shard.external",
					shardBaseURL:            "https://shard.base",
				}

				if tt.noExternalAddressProvider {
					o.externalAddressProvider = nil
				} else if tt.emptyExternalAddress {
					o.externalAddressProvider = func() string {
						return ""
					}
				}

				if tt.emptyShardExternalURL {
					o.shardExternalURL = ""
				}

				if tt.emptyShardBaseURL {
					o.shardBaseURL = ""
				}

				ctx := request.WithCluster(context.Background(), request.Cluster{Name: logicalcluster.New("root:org")})
				err := o.Admit(ctx, a, nil)
				require.NoError(t, err)

				got, ok := a.GetObject().(*unstructured.Unstructured)
				require.True(t, ok, "expected unstructured, got %T", a.GetObject())

				expected := helpers.ToUnstructuredOrDie(tt.expectedObj.ClusterWorkspaceShard)
				require.Empty(t, cmp.Diff(expected, got))
			})
		}
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		a       admission.Attributes
		wantErr bool
	}{
		{
			name: "accept non-empty baseURL and externalURL on update",
			a: updateAttr(
				newTestShard().baseURL("https://updatedBase").externalURL("https://updatedExternal"),
				newTestShard().baseURL("https://base").externalURL("https://external"),
			),
		},
		{
			name: "accept non-empty baseURL and externalURL on create",
			a:    createAttr(newTestShard().baseURL("https://base").externalURL("https://external")),
		},
		{
			name: "reject empty baseURL on update",
			a: updateAttr(
				newTestShard().baseURL("").externalURL("https://updatedExternal"),
				newTestShard().baseURL("https://base").externalURL("https://external"),
			),
			wantErr: true,
		},
		{
			name:    "reject empty baseURL on create",
			a:       createAttr(newTestShard().externalURL("https://external")),
			wantErr: true,
		},
		{
			name: "reject empty externalURL on update",
			a: updateAttr(
				newTestShard().baseURL("https://base").externalURL(""),
				newTestShard().baseURL("https://base").externalURL("https://external"),
			),
			wantErr: true,
		},
		{
			name:    "reject empty externalURL on create",
			a:       createAttr(newTestShard().baseURL("https://base").externalURL("")),
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := &clusterWorkspaceShard{
				Handler: admission.NewHandler(admission.Create, admission.Update),
			}
			ctx := request.WithCluster(context.Background(), request.Cluster{Name: logicalcluster.New("root")})
			if err := o.Validate(ctx, tt.a, nil); (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestRegister(t *testing.T) {
	type args struct {
		plugins *admission.Plugins
	}
	tests := []struct {
		name string
		args args
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			Register(tt.args.plugins)
		})
	}
}

func Test_clusterWorkspaceShard_Admit(t *testing.T) {
	type fields struct {
		Handler                 *admission.Handler
		shardBaseURL            string
		shardExternalURL        string
		externalAddressProvider func() string
	}
	type args struct {
		in0 context.Context
		a   admission.Attributes
		in2 admission.ObjectInterfaces
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		wantErr bool
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := &clusterWorkspaceShard{
				Handler:                 tt.fields.Handler,
				shardBaseURL:            tt.fields.shardBaseURL,
				shardExternalURL:        tt.fields.shardExternalURL,
				externalAddressProvider: tt.fields.externalAddressProvider,
			}
			if err := o.Admit(tt.args.in0, tt.args.a, tt.args.in2); (err != nil) != tt.wantErr {
				t.Errorf("Admit() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func Test_clusterWorkspaceShard_SetExternalAddressProvider(t *testing.T) {
	type fields struct {
		Handler                 *admission.Handler
		shardBaseURL            string
		shardExternalURL        string
		externalAddressProvider func() string
	}
	type args struct {
		externalAddressProvider func() string
	}
	tests := []struct {
		name   string
		fields fields
		args   args
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := &clusterWorkspaceShard{
				Handler:                 tt.fields.Handler,
				shardBaseURL:            tt.fields.shardBaseURL,
				shardExternalURL:        tt.fields.shardExternalURL,
				externalAddressProvider: tt.fields.externalAddressProvider,
			}
			o.SetExternalAddressProvider(tt.args.externalAddressProvider)
		})
	}
}

func Test_clusterWorkspaceShard_SetShardBaseURL(t *testing.T) {
	type fields struct {
		Handler                 *admission.Handler
		shardBaseURL            string
		shardExternalURL        string
		externalAddressProvider func() string
	}
	type args struct {
		shardBaseURL string
	}
	tests := []struct {
		name   string
		fields fields
		args   args
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := &clusterWorkspaceShard{
				Handler:                 tt.fields.Handler,
				shardBaseURL:            tt.fields.shardBaseURL,
				shardExternalURL:        tt.fields.shardExternalURL,
				externalAddressProvider: tt.fields.externalAddressProvider,
			}
			o.SetShardBaseURL(tt.args.shardBaseURL)
		})
	}
}

func Test_clusterWorkspaceShard_SetShardExternalURL(t *testing.T) {
	type fields struct {
		Handler                 *admission.Handler
		shardBaseURL            string
		shardExternalURL        string
		externalAddressProvider func() string
	}
	type args struct {
		shardExternalURL string
	}
	tests := []struct {
		name   string
		fields fields
		args   args
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := &clusterWorkspaceShard{
				Handler:                 tt.fields.Handler,
				shardBaseURL:            tt.fields.shardBaseURL,
				shardExternalURL:        tt.fields.shardExternalURL,
				externalAddressProvider: tt.fields.externalAddressProvider,
			}
			o.SetShardExternalURL(tt.args.shardExternalURL)
		})
	}
}

func Test_clusterWorkspaceShard_Validate(t *testing.T) {
	type fields struct {
		Handler                 *admission.Handler
		shardBaseURL            string
		shardExternalURL        string
		externalAddressProvider func() string
	}
	type args struct {
		in0 context.Context
		a   admission.Attributes
		in2 admission.ObjectInterfaces
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		wantErr bool
	}{
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := &clusterWorkspaceShard{
				Handler:                 tt.fields.Handler,
				shardBaseURL:            tt.fields.shardBaseURL,
				shardExternalURL:        tt.fields.shardExternalURL,
				externalAddressProvider: tt.fields.externalAddressProvider,
			}
			if err := o.Validate(tt.args.in0, tt.args.a, tt.args.in2); (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
