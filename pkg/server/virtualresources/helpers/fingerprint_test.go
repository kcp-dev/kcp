/*
Copyright 2025 The kcp Authors.

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

package helpers

import (
	"testing"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
)

func TestResourceSchemaStorageVirtualFingerprint(t *testing.T) {
	t.Parallel()
	testCases := map[string]struct {
		export              *apisv1alpha2.APIExport
		expectedFingerprint string
	}{
		"wildwest": {
			export: &apisv1alpha2.APIExport{
				ObjectMeta: metav1.ObjectMeta{
					Name: "export-1",
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "cluster-1",
					},
				},
				Spec: apisv1alpha2.APIExportSpec{
					Resources: []apisv1alpha2.ResourceSchema{
						{
							Storage: apisv1alpha2.ResourceSchemaStorage{
								Virtual: &apisv1alpha2.ResourceSchemaStorageVirtual{
									Reference: corev1.TypedLocalObjectReference{
										APIGroup: ptr.To("wildwest.dev"),
										Kind:     "CowboyEndpointSlice",
										Name:     "slice",
									},
								},
							},
						},
					},
				},
				Status: apisv1alpha2.APIExportStatus{
					IdentityHash: "hash-123",
				},
			},
			expectedFingerprint: "cluster-1|export-1|slice/CowboyEndpointSlice.wildwest.dev",
		},
		"builtin": {
			export: &apisv1alpha2.APIExport{
				ObjectMeta: metav1.ObjectMeta{
					Name: "export-1",
					Annotations: map[string]string{
						logicalcluster.AnnotationKey: "cluster-1",
					},
				},
				Spec: apisv1alpha2.APIExportSpec{
					Resources: []apisv1alpha2.ResourceSchema{
						{
							Storage: apisv1alpha2.ResourceSchemaStorage{
								Virtual: &apisv1alpha2.ResourceSchemaStorageVirtual{
									Reference: corev1.TypedLocalObjectReference{
										APIGroup: nil,
										Kind:     "EndpointSlice",
										Name:     "slice",
									},
								},
							},
						},
					},
				},
			},
			expectedFingerprint: "cluster-1|export-1|slice/EndpointSlice",
		},
	}

	for tname, tt := range testCases {
		t.Run(tname, func(t *testing.T) {
			t.Parallel()
			virtualStorage := tt.export.Spec.Resources[0].Storage.Virtual
			require.Equal(t, tt.expectedFingerprint, Fingerprint(tt.export, virtualStorage),
				"generated fingerprint doesn't match the expected one")
		})
	}
}
