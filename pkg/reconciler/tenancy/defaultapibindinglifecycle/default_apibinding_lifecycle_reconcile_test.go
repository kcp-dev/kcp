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

package defaultapibindinglifecycle

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"

	kcptesting "github.com/kcp-dev/client-go/third_party/k8s.io/client-go/testing"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
	kcpfakeclient "github.com/kcp-dev/sdk/client/clientset/versioned/cluster/fake"
	apisv1alpha2client "github.com/kcp-dev/sdk/client/clientset/versioned/typed/apis/v1alpha2"

	"github.com/kcp-dev/kcp/pkg/reconciler/committer"
	"github.com/kcp-dev/kcp/pkg/reconciler/tenancy/initialization"
)

type fakeTypeResolver struct{}

func (fakeTypeResolver) Resolve(t *tenancyv1alpha1.WorkspaceType) ([]*tenancyv1alpha1.WorkspaceType, error) {
	return []*tenancyv1alpha1.WorkspaceType{t}, nil
}

func TestReconcile(t *testing.T) {
	t.Parallel()

	clusterName := logicalcluster.Name("ws")
	exportPath := "root:provider"
	exportName := "export"
	bindingName := initialization.GenerateAPIBindingName(clusterName, exportPath, exportName)

	// The APIExport has a permission claim the existing binding is missing.
	outdatedSpec := &apisv1alpha2.APIBindingSpec{
		Reference: apisv1alpha2.BindingReference{
			Export: &apisv1alpha2.ExportBindingReference{Path: exportPath, Name: exportName},
		},
	}

	tests := []struct {
		name            string
		existingSpec    *apisv1alpha2.APIBindingSpec
		deleting        bool
		expectCreates   int
		expectPatches   int
		expectCondition bool
	}{
		{
			name:            "not found is created",
			expectCreates:   1,
			expectCondition: true,
		},
		{
			name:     "not found in a deleting logical cluster is not created",
			deleting: true,
		},
		{
			name:          "missing claim in a deleting logical cluster is still patched",
			existingSpec:  outdatedSpec,
			deleting:      true,
			expectPatches: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var existing []*apisv1alpha2.APIBinding
			var objects []runtime.Object
			if tc.existingSpec != nil {
				binding := &apisv1alpha2.APIBinding{
					ObjectMeta: metav1.ObjectMeta{
						Name:        bindingName,
						Annotations: map[string]string{logicalcluster.AnnotationKey: clusterName.String()},
					},
					Spec: *tc.existingSpec,
				}
				existing = append(existing, binding)
				objects = append(objects, binding.DeepCopy())
			}
			client := kcpfakeclient.NewSimpleClientset(objects...) //nolint:staticcheck

			c := &DefaultAPIBindingController{
				getWorkspaceType: func(path logicalcluster.Path, name string) (*tenancyv1alpha1.WorkspaceType, error) {
					return &tenancyv1alpha1.WorkspaceType{
						ObjectMeta: metav1.ObjectMeta{
							Name:        name,
							Annotations: map[string]string{logicalcluster.AnnotationKey: "root"},
						},
						Spec: tenancyv1alpha1.WorkspaceTypeSpec{
							DefaultAPIBindings:         []tenancyv1alpha1.APIExportReference{{Path: exportPath, Export: exportName}},
							DefaultAPIBindingLifecycle: ptr.To(tenancyv1alpha1.APIBindingLifecycleModeMaintain),
						},
					}, nil
				},
				transitiveTypeResolver: fakeTypeResolver{},
				listAPIBindings: func(logicalcluster.Name) ([]*apisv1alpha2.APIBinding, error) {
					return existing, nil
				},
				getAPIBinding: func(_ logicalcluster.Name, name string) (*apisv1alpha2.APIBinding, error) {
					if len(existing) == 0 || name != bindingName {
						return nil, apierrors.NewNotFound(apisv1alpha2.Resource("apibindings"), name)
					}
					return existing[0], nil
				},
				createAPIBinding: func(ctx context.Context, path logicalcluster.Path, binding *apisv1alpha2.APIBinding) (*apisv1alpha2.APIBinding, error) {
					return client.Cluster(path).ApisV1alpha2().APIBindings().Create(ctx, binding, metav1.CreateOptions{})
				},
				getAPIExport: func(_ logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error) {
					return &apisv1alpha2.APIExport{
						ObjectMeta: metav1.ObjectMeta{Name: name},
						Spec: apisv1alpha2.APIExportSpec{
							PermissionClaims: []apisv1alpha2.PermissionClaim{{
								GroupResource: apisv1alpha2.GroupResource{Resource: "configmaps"},
								Verbs:         []string{"get"},
							}},
						},
					}, nil
				},
				commitApiBinding: committer.NewCommitter[*apisv1alpha2.APIBinding, apisv1alpha2client.APIBindingInterface, *apisv1alpha2.APIBindingSpec, *apisv1alpha2.APIBindingStatus](client.ApisV1alpha2().APIBindings()),
			}

			logicalCluster := &corev1alpha1.LogicalCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name: corev1alpha1.LogicalClusterName,
					Annotations: map[string]string{
						logicalcluster.AnnotationKey:                    clusterName.String(),
						tenancyv1alpha1.LogicalClusterTypeAnnotationKey: "root:type",
					},
				},
			}
			if tc.deleting {
				now := metav1.Now()
				logicalCluster.DeletionTimestamp = &now
			}

			require.NoError(t, c.reconcile(context.Background(), logicalCluster))

			var created []*apisv1alpha2.APIBinding
			var patches []string
			for _, action := range client.Actions() {
				switch action := action.(type) {
				case kcptesting.CreateAction:
					created = append(created, action.GetObject().(*apisv1alpha2.APIBinding))
				case kcptesting.PatchAction:
					patches = append(patches, string(action.GetPatch()))
				}
			}
			require.Len(t, created, tc.expectCreates)
			require.Len(t, patches, tc.expectPatches)
			require.Equal(t, tc.expectCondition, conditions.Has(logicalCluster, tenancyv1alpha1.WorkspaceAPIBindingsReconciled))
		})
	}
}
