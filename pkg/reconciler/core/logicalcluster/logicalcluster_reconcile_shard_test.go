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

package logicalcluster

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
)

func TestShardAnnotationReconciler(t *testing.T) {
	t.Parallel()
	const shardName = "shard-A"
	workspacesResource := schema.GroupResource{Group: tenancyv1alpha1.SchemeGroupVersion.Group, Resource: "workspaces"}

	workspaceOwner := corev1alpha1.LogicalClusterOwner{
		APIVersion: tenancyv1alpha1.SchemeGroupVersion.String(),
		Resource:   "workspaces",
		Cluster:    "root:org",
		Name:       "ws",
		UID:        "uid-1",
	}
	mountOwner := corev1alpha1.LogicalClusterOwner{
		APIVersion: "tenancy.kcp.io/v1alpha1",
		Resource:   "mounts",
		Cluster:    "root:org",
		Namespace:  "default",
		Name:       "m1",
		UID:        "uid-2",
	}

	type getCall struct {
		owner corev1alpha1.LogicalClusterOwner
	}
	type patchCall struct {
		owner corev1alpha1.LogicalClusterOwner
		patch string
	}

	patchBody := `{"metadata":{"annotations":{"core.kcp.io/shard":"shard-A"}}}`

	for _, tc := range []struct {
		name string

		input    *corev1alpha1.LogicalCluster
		ownerObj metav1.Object
		getErr   error
		patchErr error

		expected       metav1.ObjectMeta
		wantStatus     reconcileStatus
		wantErr        bool
		wantGetCalls   []getCall
		wantPatchCalls []patchCall
	}{
		{
			name: "LC annotation already matches: no-op, continue",
			input: &corev1alpha1.LogicalCluster{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						corev1alpha1.LogicalClusterShardAnnotationKey: shardName,
					},
				},
				Spec: corev1alpha1.LogicalClusterSpec{Owner: &workspaceOwner},
			},
			expected: metav1.ObjectMeta{
				Annotations: map[string]string{
					corev1alpha1.LogicalClusterShardAnnotationKey: shardName,
				},
			},
			wantStatus: reconcileStatusContinue,
		},
		{
			name: "LC annotation absent, owner already correct: stamp LC, GET only",
			input: &corev1alpha1.LogicalCluster{
				Spec: corev1alpha1.LogicalClusterSpec{Owner: &workspaceOwner},
			},
			ownerObj: &metav1.PartialObjectMetadata{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						corev1alpha1.LogicalClusterShardAnnotationKey: shardName,
					},
				},
			},
			expected: metav1.ObjectMeta{
				Annotations: map[string]string{
					corev1alpha1.LogicalClusterShardAnnotationKey: shardName,
				},
			},
			wantStatus:   reconcileStatusStopAndRequeue,
			wantGetCalls: []getCall{{owner: workspaceOwner}},
		},
		{
			name: "LC annotation stale, owner stale: stamp LC, GET + PATCH",
			input: &corev1alpha1.LogicalCluster{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						corev1alpha1.LogicalClusterShardAnnotationKey: "shard-other",
					},
				},
				Spec: corev1alpha1.LogicalClusterSpec{Owner: &workspaceOwner},
			},
			ownerObj: &metav1.PartialObjectMetadata{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						corev1alpha1.LogicalClusterShardAnnotationKey: "shard-other",
					},
				},
			},
			expected: metav1.ObjectMeta{
				Annotations: map[string]string{
					corev1alpha1.LogicalClusterShardAnnotationKey: shardName,
				},
			},
			wantStatus:     reconcileStatusStopAndRequeue,
			wantGetCalls:   []getCall{{owner: workspaceOwner}},
			wantPatchCalls: []patchCall{{owner: workspaceOwner, patch: patchBody}},
		},
		{
			name: "Non-Workspace owner (mount): same flow, GET + PATCH",
			input: &corev1alpha1.LogicalCluster{
				Spec: corev1alpha1.LogicalClusterSpec{Owner: &mountOwner},
			},
			ownerObj: &metav1.PartialObjectMetadata{
				ObjectMeta: metav1.ObjectMeta{},
			},
			expected: metav1.ObjectMeta{
				Annotations: map[string]string{
					corev1alpha1.LogicalClusterShardAnnotationKey: shardName,
				},
			},
			wantStatus:     reconcileStatusStopAndRequeue,
			wantGetCalls:   []getCall{{owner: mountOwner}},
			wantPatchCalls: []patchCall{{owner: mountOwner, patch: patchBody}},
		},
		{
			name: "Nil Owner (root LC): stamp LC, no remote calls",
			input: &corev1alpha1.LogicalCluster{
				ObjectMeta: metav1.ObjectMeta{},
			},
			expected: metav1.ObjectMeta{
				Annotations: map[string]string{
					corev1alpha1.LogicalClusterShardAnnotationKey: shardName,
				},
			},
			wantStatus: reconcileStatusStopAndRequeue,
		},
		{
			name: "GET NotFound: LC stamp reverted, error returned",
			input: &corev1alpha1.LogicalCluster{
				Spec: corev1alpha1.LogicalClusterSpec{Owner: &workspaceOwner},
			},
			getErr:       apierrors.NewNotFound(workspacesResource, "ws"),
			expected:     metav1.ObjectMeta{},
			wantStatus:   reconcileStatusStopAndRequeue,
			wantErr:      true,
			wantGetCalls: []getCall{{owner: workspaceOwner}},
		},
		{
			name: "GET error: LC stamp reverted, error returned",
			input: &corev1alpha1.LogicalCluster{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						corev1alpha1.LogicalClusterShardAnnotationKey: "shard-other",
					},
				},
				Spec: corev1alpha1.LogicalClusterSpec{Owner: &workspaceOwner},
			},
			getErr: errors.New("boom"),
			expected: metav1.ObjectMeta{
				Annotations: map[string]string{
					corev1alpha1.LogicalClusterShardAnnotationKey: "shard-other",
				},
			},
			wantStatus:   reconcileStatusStopAndRequeue,
			wantErr:      true,
			wantGetCalls: []getCall{{owner: workspaceOwner}},
		},
		{
			name: "PATCH error: LC stamp reverted, error returned",
			input: &corev1alpha1.LogicalCluster{
				Spec: corev1alpha1.LogicalClusterSpec{Owner: &workspaceOwner},
			},
			ownerObj: &metav1.PartialObjectMetadata{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						corev1alpha1.LogicalClusterShardAnnotationKey: "shard-other",
					},
				},
			},
			patchErr:       apierrors.NewNotFound(workspacesResource, "ws"),
			expected:       metav1.ObjectMeta{},
			wantStatus:     reconcileStatusStopAndRequeue,
			wantErr:        true,
			wantGetCalls:   []getCall{{owner: workspaceOwner}},
			wantPatchCalls: []patchCall{{owner: workspaceOwner, patch: patchBody}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var gotGets []getCall
			var gotPatches []patchCall

			r := &shardAnnotationReconciler{
				shardName: shardName,
				getOwner: func(_ context.Context, owner corev1alpha1.LogicalClusterOwner) (metav1.Object, error) {
					gotGets = append(gotGets, getCall{owner: owner})
					if tc.getErr != nil {
						return nil, tc.getErr
					}
					return tc.ownerObj, nil
				},
				patchOwner: func(_ context.Context, owner corev1alpha1.LogicalClusterOwner, patch []byte) error {
					gotPatches = append(gotPatches, patchCall{owner: owner, patch: string(patch)})
					return tc.patchErr
				},
			}

			status, err := r.reconcile(context.Background(), tc.input)

			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tc.wantStatus, status)

			if diff := cmp.Diff(tc.input.ObjectMeta, tc.expected); diff != "" {
				t.Errorf("ObjectMeta diff (-got +want):\n%s", diff)
			}
			require.Equal(t, tc.wantGetCalls, gotGets)
			require.Equal(t, tc.wantPatchCalls, gotPatches)
		})
	}
}
