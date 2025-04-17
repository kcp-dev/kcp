/*
Copyright 2024 The KCP Authors.

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

package v1alpha1

import (
	"testing"

	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestPublishedResource(t *testing.T) {
	testCases := []struct {
		name     string
		resource PublishedResource
	}{
		{
			name: "basic published resource",
			resource: PublishedResource{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "instances",
					Namespace: "default",
				},
				Spec: PublishedResourceSpec{
					GroupVersionResource: GroupVersionResource{
						Group:    "corp.com",
						Resource: "instances",
					},
					LabelSelector: &metav1.LabelSelector{},
				},
				Status: PublishedResourceStatus{
					IdentityHash: "supersecrethash",
				},
			},
		},
		{
			name: "minimal published resource",
			resource: PublishedResource{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "minimal",
					Namespace: "default",
				},
				Spec: PublishedResourceSpec{
					GroupVersionResource: GroupVersionResource{
						Group:    "corp.com",
						Version:  "v1",
						Resource: "minimal",
					},
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Verify required fields are set
			require.NotEmpty(t, tc.resource.Spec.Resource, "Resource field must not be empty")

			// Verify optional fields can be nil
			if tc.resource.Spec.LabelSelector == nil {
				t.Log("LabelSelector is optional and can be nil")
			}
		})
	}
}
