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

package namespacelifecycle

import (
	"context"
	"testing"

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/endpoints/request"

	tenancyv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
)

func TestAdmit(t *testing.T) {
	now := metav1.Now()

	tests := []struct {
		name      string
		workspace *tenancyv1beta1.Workspace
		namespace string
		wantErr   bool
	}{
		{
			name:      "delete immortal namespace in workspace",
			namespace: "default",
			workspace: &tenancyv1beta1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "root:org|test",
				},
			},
			wantErr: true,
		},
		{
			name:      "delete regular namespace in workspace",
			namespace: "test",
			workspace: &tenancyv1beta1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "root:org|test",
				},
			},
			wantErr: false,
		},
		{
			name:      "delete immortal namespace in deleting workspace",
			namespace: "default",
			workspace: &tenancyv1beta1.Workspace{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "root:org|test",
					DeletionTimestamp: &now,
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler, err := newWorkspaceNamespaceLifecycle()
			require.NoError(t, err, "error creating admission plugin")
			handler.getWorkspace = func(clusterName logicalcluster.Name, name string) (*tenancyv1beta1.Workspace, error) {
				return tt.workspace, nil
			}

			a := admission.NewAttributesRecord(
				nil,
				nil,
				corev1.SchemeGroupVersion.WithKind("Namespace").GroupKind().WithVersion("version"),
				"",
				tt.namespace,
				corev1.Resource("namespaces").WithVersion("version"),
				"",
				admission.Delete,
				&metav1.DeleteOptions{},
				false,
				nil,
			)

			ctx := request.WithCluster(context.Background(), request.Cluster{Name: logicalcluster.New("root:org:test")})
			if err := handler.Admit(ctx, a, nil); (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
