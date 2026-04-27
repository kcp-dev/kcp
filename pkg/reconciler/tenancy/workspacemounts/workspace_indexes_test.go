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

package workspacemounts

import (
	"testing"

	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"

	"github.com/kcp-dev/kcp/pkg/reconciler/dynamicrestmapper"
)

// TestIndexWorkspaceByMountObject exercises the mount-reference indexer.
//
// The unknown-kind case matters because client-go's storeIndex.updateSingleIndex
// panics on any IndexFunc error (see k8s.io/client-go/tools/cache/thread_safe_store.go),
// which would tear down the apiserver. A Workspace pointing at a kind whose
// APIBinding/CRD hasn't reconciled yet is a normal, recoverable state — not a
// reason to crash the process. A malformed APIVersion, by contrast, is
// user-input that will never become valid by re-indexing the same object, so
// reporting it is preferable to silently dropping it.
func TestIndexWorkspaceByMountObject(t *testing.T) {
	tests := []struct {
		name      string
		ws        *tenancyv1alpha1.Workspace
		wantErr   bool
		wantEmpty bool
		errMsg    string
	}{
		{
			name: "unknown kind does not error",
			ws: &tenancyv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "mount-ws",
					Annotations: map[string]string{"kcp.io/cluster": "tenant-cluster"},
				},
				Spec: tenancyv1alpha1.WorkspaceSpec{
					Mount: &tenancyv1alpha1.Mount{
						Reference: tenancyv1alpha1.ObjectReference{
							APIVersion: "example.com/v1alpha1",
							Kind:       "Widget",
							Name:       "my-widget",
						},
					},
				},
			},
			wantErr:   false,
			wantEmpty: true,
			errMsg: "indexer must not return an error for an unknown-kind mount ref; " +
				"returning an error would make client-go's storeIndex panic the apiserver",
		},
		{
			name: "no mount",
			ws: &tenancyv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "plain-ws",
					Annotations: map[string]string{"kcp.io/cluster": "tenant-cluster"},
				},
			},
			wantErr:   false,
			wantEmpty: true,
		},
		{
			name: "malformed APIVersion",
			ws: &tenancyv1alpha1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "bad-ws",
					Annotations: map[string]string{"kcp.io/cluster": "tenant-cluster"},
				},
				Spec: tenancyv1alpha1.WorkspaceSpec{
					Mount: &tenancyv1alpha1.Mount{
						Reference: tenancyv1alpha1.ObjectReference{
							APIVersion: "too/many/slashes",
							Kind:       "Widget",
							Name:       "my-widget",
						},
					},
				},
			},
			wantErr: true,
			errMsg:  "malformed APIVersion should surface as an error",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dmapper := dynamicrestmapper.NewDynamicRESTMapper()

			keys, err := newIndexWorkspaceByMountObject(dmapper)(tc.ws)
			if tc.wantErr {
				require.Error(t, err, tc.errMsg)
				return
			}
			require.NoError(t, err, tc.errMsg)
			if tc.wantEmpty {
				require.Empty(t, keys, "indexer should not emit index entries for an unresolvable mount ref")
			}
		})
	}
}
