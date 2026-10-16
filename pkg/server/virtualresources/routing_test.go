/*
Copyright 2026 The kcp Authors.

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

package virtualresources

import (
	"testing"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"

	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
)

func virtualRef(name string) apisv1alpha2.ResourceSchemaStorageVirtual {
	return apisv1alpha2.ResourceSchemaStorageVirtual{
		Reference: corev1.TypedLocalObjectReference{
			APIGroup: ptr.To("compute.example.com"),
			Kind:     "VirtualMachineEndpointSlice",
			Name:     name,
		},
	}
}

func TestResolveVirtualStorage(t *testing.T) {
	t.Parallel()

	gr := schema.GroupResource{Group: "compute.example.com", Resource: "virtualmachines"}

	// A resource stored in etcd that nonetheless carries two custom subresources,
	// each declared as its own "<resource>/<subresource>" entry.
	crdStoredWithSubresources := &apisv1alpha2.APIExport{
		Spec: apisv1alpha2.APIExportSpec{
			Resources: []apisv1alpha2.ResourceSchema{
				{
					Group:   gr.Group,
					Name:    gr.Resource,
					Schema:  "v1alpha1.virtualmachines.compute.example.com",
					Storage: apisv1alpha2.ResourceSchemaStorage{CRD: &apisv1alpha2.ResourceSchemaStorageCRD{}},
				},
				{
					Group:   gr.Group,
					Name:    gr.Resource + "/ssh",
					Schema:  "v1alpha1.ssh.compute.example.com",
					Storage: apisv1alpha2.ResourceSchemaStorage{Virtual: ptr.To(virtualRef("ssh-slice"))},
				},
				{
					Group:   gr.Group,
					Name:    gr.Resource + "/reboot",
					Schema:  "v1alpha1.reboot.compute.example.com",
					Storage: apisv1alpha2.ResourceSchemaStorage{Virtual: ptr.To(virtualRef("reboot-slice"))},
				},
			},
		},
	}

	fullyVirtual := &apisv1alpha2.APIExport{
		Spec: apisv1alpha2.APIExportSpec{
			Resources: []apisv1alpha2.ResourceSchema{{
				Group:   gr.Group,
				Name:    gr.Resource,
				Schema:  "v1alpha1.virtualmachines.compute.example.com",
				Storage: apisv1alpha2.ResourceSchemaStorage{Virtual: ptr.To(virtualRef("parent-slice"))},
			}},
		},
	}

	for name, tc := range map[string]struct {
		apiExport   *apisv1alpha2.APIExport
		subresource string

		wantSlice string
	}{
		"connect subresource on a CRD-stored parent is served remotely": {
			apiExport: crdStoredWithSubresources, subresource: "ssh",
			wantSlice: "ssh-slice",
		},
		"call subresource on a CRD-stored parent is served remotely, without pinning": {
			apiExport: crdStoredWithSubresources, subresource: "reboot",
			wantSlice: "reboot-slice",
		},
		"the CRD-stored parent itself stays in etcd": {
			apiExport: crdStoredWithSubresources, subresource: "",
			wantSlice: "",
		},
		"status is not a custom subresource, so the caller asks for the parent": {
			apiExport: crdStoredWithSubresources, subresource: "",
			wantSlice: "",
		},
		"an undeclared subresource on a CRD-stored parent is not hijacked": {
			apiExport: crdStoredWithSubresources, subresource: "logs",
			wantSlice: "",
		},
		"a fully virtual resource still resolves to its parent storage": {
			apiExport: fullyVirtual, subresource: "",
			wantSlice: "parent-slice",
		},
		"a fully virtual resource serves the parent entry when no subresource is named": {
			apiExport: fullyVirtual, subresource: "",
			wantSlice: "parent-slice",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := resolveVirtualStorage(tc.apiExport, gr, tc.subresource)

			if tc.wantSlice == "" {
				require.Nil(t, got, "expected the request to fall through to CRD storage")
				return
			}

			require.NotNil(t, got, "expected the request to be routed to a virtual workspace")
			require.Equal(t, tc.wantSlice, got.Reference.Name)
		})
	}
}

func TestResolveVirtualStorageUnknownResource(t *testing.T) {
	t.Parallel()

	got := resolveVirtualStorage(
		&apisv1alpha2.APIExport{},
		schema.GroupResource{Group: "compute.example.com", Resource: "virtualmachines"},
		"ssh",
	)

	require.Nil(t, got)
}
