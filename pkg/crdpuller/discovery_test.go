/*
Copyright 2021 The KCP Authors.

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

package crdpuller

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestPuller(t *testing.T) {
	getCRDCount := 0
	getCRDName := ""

	puller := &schemaPuller{
		serverGroupsAndResources: func() ([]*metav1.APIGroup, []*metav1.APIResourceList, error) {
			return []*metav1.APIGroup{
					{
						Name: "",
						Versions: []metav1.GroupVersionForDiscovery{
							{
								GroupVersion: "v1",
								Version:      "v1",
							},
						},
						PreferredVersion: metav1.GroupVersionForDiscovery{
							GroupVersion: "v1",
							Version:      "v1",
						},
					},
					{
						Name: "metrics.k8s.io",
						Versions: []metav1.GroupVersionForDiscovery{
							{
								GroupVersion: "metrics.k8s.io/v1beta1",
								Version:      "v1beta1",
							},
						},
						PreferredVersion: metav1.GroupVersionForDiscovery{
							GroupVersion: "metrics.k8s.io/v1beta1",
							Version:      "v1beta1",
						},
					},
				}, []*metav1.APIResourceList{
					{
						GroupVersion: "v1",
						APIResources: []metav1.APIResource{
							{
								Name:       "pods",
								Namespaced: true,
								Kind:       "Pod",
							},
						},
					},
					{
						GroupVersion: "metrics.k8s.io/v1beta1",
						APIResources: []metav1.APIResource{
							{
								Name:       "pods",
								Namespaced: true,
								Kind:       "Pod",
							},
						},
					},
				}, nil
		},
		serverPreferredResources: func() ([]*metav1.APIResourceList, error) {
			return []*metav1.APIResourceList{
				{
					GroupVersion: "v1",
					APIResources: []metav1.APIResource{
						{
							Name:       "pods",
							Namespaced: false,
							Kind:       "Pod",
						},
					},
				},
			}, nil
		},
		getCRD: func(ctx context.Context, name string) (*apiextensionsv1.CustomResourceDefinition, error) {
			getCRDCount++
			getCRDName = name
			return &apiextensionsv1.CustomResourceDefinition{}, nil
		},
		resourceFor: func(groupResource schema.GroupResource) (schema.GroupResource, error) {
			return groupResource, nil
		},
	}

	_, err := puller.PullCRDs(context.Background(), "pods")
	require.NoError(t, err, "error pulling")

	require.Equal(t, 1, getCRDCount)
	require.Equal(t, "pods.core", getCRDName)
}
