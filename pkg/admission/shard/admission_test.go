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

package shard

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/kcp/pkg/admission/helpers"
	corev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
)

func createAttr(shard *corev1alpha1.Shard) admission.Attributes {
	return admission.NewAttributesRecord(
		helpers.ToUnstructuredOrDie(shard),
		nil,
		corev1alpha1.Kind("Shard").WithVersion("v1alpha1"),
		"",
		shard.Name,
		corev1alpha1.Resource("shards").WithVersion("v1alpha1"),
		"",
		admission.Create,
		&metav1.CreateOptions{},
		false,
		&user.DefaultInfo{},
	)
}

func updateAttr(shard, old *corev1alpha1.Shard) admission.Attributes {
	return admission.NewAttributesRecord(
		helpers.ToUnstructuredOrDie(shard),
		helpers.ToUnstructuredOrDie(old),
		tenancyv1alpha1.Kind("ClusterWorkspace").WithVersion("v1alpha1"),
		"",
		shard.Name,
		corev1alpha1.Resource("shards").WithVersion("v1alpha1"),
		"",
		admission.Update,
		&metav1.CreateOptions{},
		false,
		&user.DefaultInfo{},
	)
}

type shardBuilder struct {
	*corev1alpha1.Shard
}

func newShard() *shardBuilder {
	return &shardBuilder{
		Shard: &corev1alpha1.Shard{
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
	o := &shard{
		Handler: admission.NewHandler(admission.Create, admission.Update),
	}

	ctx := request.WithCluster(context.Background(), request.Cluster{Name: "root:org"})

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
		shard    *corev1alpha1.Shard
		expected *corev1alpha1.Shard
	}{
		{
			name:     "nothing set",
			shard:    newShard().Shard,
			expected: newShard().Shard,
		},
		{
			name:     "all set",
			shard:    newShard().baseURL("https://base").externalURL("https://external").virtualWorkspaceURL("https://virtual").Shard,
			expected: newShard().baseURL("https://base").externalURL("https://external").virtualWorkspaceURL("https://virtual").Shard,
		},
		{
			name:     "defaulting external URL",
			shard:    newShard().baseURL("https://base").virtualWorkspaceURL("https://virtual").Shard,
			expected: newShard().baseURL("https://base").externalURL("https://base").virtualWorkspaceURL("https://virtual").Shard,
		},
		{
			name:     "defaulting virtual workspace URL",
			shard:    newShard().baseURL("https://base").externalURL("https://external").Shard,
			expected: newShard().baseURL("https://base").externalURL("https://external").virtualWorkspaceURL("https://base").Shard,
		},
	}
	for _, tt := range tests {
		attrs := map[string]admission.Attributes{
			"create": createAttr(tt.shard),
			"update": updateAttr(tt.shard, tt.shard),
		}

		for aType, a := range attrs {
			t.Run(tt.name+" "+aType, func(t *testing.T) {
				o := &shard{
					Handler: admission.NewHandler(admission.Create, admission.Update),
				}

				ctx := request.WithCluster(context.Background(), request.Cluster{Name: "root:or"})
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
