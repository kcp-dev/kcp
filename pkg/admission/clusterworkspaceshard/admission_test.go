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
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/kcp/pkg/admission/helpers"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
)

func createAttr(shard *tenancyv1alpha1.ClusterWorkspaceShard) admission.Attributes {
	return admission.NewAttributesRecord(
		helpers.ToUnstructuredOrDie(shard),
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

func updateAttr(shard, old *tenancyv1alpha1.ClusterWorkspaceShard) admission.Attributes {
	return admission.NewAttributesRecord(
		helpers.ToUnstructuredOrDie(shard),
		helpers.ToUnstructuredOrDie(old),
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

func newShard() *shardBuilder {
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

func TestAdmit(t *testing.T) {
	tests := []struct {
		name     string
		shard    *tenancyv1alpha1.ClusterWorkspaceShard
		expected *tenancyv1alpha1.ClusterWorkspaceShard
	}{
		{
			name:     "nothing set",
			shard:    newShard().ClusterWorkspaceShard,
			expected: newShard().ClusterWorkspaceShard,
		},
		{
			name:     "all set",
			shard:    newShard().baseURL("https://base").externalURL("https://external").virtualWorkspaceURL("https://virtual").ClusterWorkspaceShard,
			expected: newShard().baseURL("https://base").externalURL("https://external").virtualWorkspaceURL("https://virtual").ClusterWorkspaceShard,
		},
		{
			name:     "defaulting external URL",
			shard:    newShard().baseURL("https://base").virtualWorkspaceURL("https://virtual").ClusterWorkspaceShard,
			expected: newShard().baseURL("https://base").externalURL("https://base").virtualWorkspaceURL("https://virtual").ClusterWorkspaceShard,
		},
		{
			name:     "defaulting virtual workspace URL",
			shard:    newShard().baseURL("https://base").externalURL("https://external").ClusterWorkspaceShard,
			expected: newShard().baseURL("https://base").externalURL("https://external").virtualWorkspaceURL("https://base").ClusterWorkspaceShard,
		},
	}
	for _, tt := range tests {
		attrs := map[string]admission.Attributes{
			"create": createAttr(tt.shard),
			"update": updateAttr(tt.shard, tt.shard),
		}

		for aType, a := range attrs {
			t.Run(tt.name+" "+aType, func(t *testing.T) {
				o := &clusterWorkspaceShard{
					Handler: admission.NewHandler(admission.Create, admission.Update),
				}

				ctx := request.WithCluster(context.Background(), request.Cluster{Name: logicalcluster.New("root:org")})
				err := o.Admit(ctx, a, nil)
				require.NoError(t, err)

				got, ok := a.GetObject().(*unstructured.Unstructured)
				require.True(t, ok, "expected unstructured, got %T", a.GetObject())

				expected := helpers.ToUnstructuredOrDie(tt.expected)
				require.Empty(t, cmp.Diff(expected, got))
			})
		}
	}
}
