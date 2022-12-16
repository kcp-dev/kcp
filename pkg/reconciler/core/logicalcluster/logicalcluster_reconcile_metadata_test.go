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

package logicalcluster

import (
	"context"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	corev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
)

func TestReconcileMetadata(t *testing.T) {
	date, err := time.Parse(time.RFC3339, "2006-01-02T15:04:05Z")
	require.NoError(t, err)

	for _, testCase := range []struct {
		name       string
		input      *corev1alpha1.LogicalCluster
		expected   metav1.ObjectMeta
		wantStatus reconcileStatus
	}{
		{
			name: "adds entirely missing labels and annotations",
			input: &corev1alpha1.LogicalCluster{
				Status: corev1alpha1.LogicalClusterStatus{
					Phase: tenancyv1alpha1.WorkspacePhaseReady,
					Initializers: []tenancyv1alpha1.WorkspaceInitializer{
						"pluto", "venus", "apollo",
					},
				},
			},
			expected: metav1.ObjectMeta{
				Labels: map[string]string{
					"tenancy.kcp.dev/phase": "Ready",
					"initializer.internal.kcp.dev/2eadcbf778956517ec99fd1c1c32a9b13c": "2eadcbf778956517ec99fd1c1c32a9b13cbae759770fc37c341c7fe8",
					"initializer.internal.kcp.dev/aceeb26461953562d30366db65b200f642": "aceeb26461953562d30366db65b200f64241f9e5fe888892d52eea5c",
					"initializer.internal.kcp.dev/ccf53a4988ae8515ee77131ef507cabaf1": "ccf53a4988ae8515ee77131ef507cabaf18822766c2a4cff33b24eb8",
				},
			},
			wantStatus: reconcileStatusStopAndRequeue,
		},
		{
			name: "shows phase Deleting when deletion timestamp is set",
			input: &corev1alpha1.LogicalCluster{
				ObjectMeta: metav1.ObjectMeta{
					DeletionTimestamp: &metav1.Time{Time: date},
				},
				Status: corev1alpha1.LogicalClusterStatus{
					Phase: tenancyv1alpha1.WorkspacePhaseReady,
				},
			},
			expected: metav1.ObjectMeta{
				DeletionTimestamp: &metav1.Time{Time: date},
				Labels: map[string]string{
					"tenancy.kcp.dev/phase": "Deleting",
				},
			},
			wantStatus: reconcileStatusStopAndRequeue,
		},
		{
			name: "adds partially missing labels",
			input: &corev1alpha1.LogicalCluster{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"tenancy.kcp.dev/phase": "Ready",
						"initializer.internal.kcp.dev/2eadcbf778956517ec99fd1c1c32a9b13c": "2eadcbf778956517ec99fd1c1c32a9b13cbae759770fc37c341c7fe8",
						"initializer.internal.kcp.dev/aceeb26461953562d30366db65b200f642": "aceeb26461953562d30366db65b200f64241f9e5fe888892d52eea5c",
					},
				},
				Status: corev1alpha1.LogicalClusterStatus{
					Phase: tenancyv1alpha1.WorkspacePhaseReady,
					Initializers: []tenancyv1alpha1.WorkspaceInitializer{
						"pluto", "venus", "apollo",
					},
				},
			},
			expected: metav1.ObjectMeta{
				Labels: map[string]string{
					"tenancy.kcp.dev/phase": "Ready",
					"initializer.internal.kcp.dev/2eadcbf778956517ec99fd1c1c32a9b13c": "2eadcbf778956517ec99fd1c1c32a9b13cbae759770fc37c341c7fe8",
					"initializer.internal.kcp.dev/aceeb26461953562d30366db65b200f642": "aceeb26461953562d30366db65b200f64241f9e5fe888892d52eea5c",
					"initializer.internal.kcp.dev/ccf53a4988ae8515ee77131ef507cabaf1": "ccf53a4988ae8515ee77131ef507cabaf18822766c2a4cff33b24eb8",
				},
			},
			wantStatus: reconcileStatusStopAndRequeue,
		},
		{
			name: "removes previously-needed labels removed on mutation that removes initializer",
			input: &corev1alpha1.LogicalCluster{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"tenancy.kcp.dev/phase": "Ready",
						"initializer.internal.kcp.dev/2eadcbf778956517ec99fd1c1c32a9b13c": "2eadcbf778956517ec99fd1c1c32a9b13cbae759770fc37c341c7fe8",
						"initializer.internal.kcp.dev/aceeb26461953562d30366db65b200f642": "aceeb26461953562d30366db65b200f64241f9e5fe888892d52eea5c",
					},
				},
				Status: corev1alpha1.LogicalClusterStatus{
					Phase: tenancyv1alpha1.WorkspacePhaseReady,
					Initializers: []tenancyv1alpha1.WorkspaceInitializer{
						"pluto",
					},
				},
			},
			expected: metav1.ObjectMeta{
				Labels: map[string]string{
					"tenancy.kcp.dev/phase": "Ready",
					"initializer.internal.kcp.dev/2eadcbf778956517ec99fd1c1c32a9b13c": "2eadcbf778956517ec99fd1c1c32a9b13cbae759770fc37c341c7fe8",
				},
			},
			wantStatus: reconcileStatusStopAndRequeue,
		},
		{
			name: "does nothing when labels match",
			input: &corev1alpha1.LogicalCluster{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"tenancy.kcp.dev/phase": "Ready",
						"initializer.internal.kcp.dev/2eadcbf778956517ec99fd1c1c32a9b13c": "2eadcbf778956517ec99fd1c1c32a9b13cbae759770fc37c341c7fe8",
						"initializer.internal.kcp.dev/aceeb26461953562d30366db65b200f642": "aceeb26461953562d30366db65b200f64241f9e5fe888892d52eea5c",
						"initializer.internal.kcp.dev/ccf53a4988ae8515ee77131ef507cabaf1": "ccf53a4988ae8515ee77131ef507cabaf18822766c2a4cff33b24eb8",
					},
				},
				Status: corev1alpha1.LogicalClusterStatus{
					Phase: tenancyv1alpha1.WorkspacePhaseReady,
					Initializers: []tenancyv1alpha1.WorkspaceInitializer{
						"pluto", "venus", "apollo",
					},
				},
			},
			expected: metav1.ObjectMeta{
				Labels: map[string]string{
					"tenancy.kcp.dev/phase": "Ready",
					"initializer.internal.kcp.dev/2eadcbf778956517ec99fd1c1c32a9b13c": "2eadcbf778956517ec99fd1c1c32a9b13cbae759770fc37c341c7fe8",
					"initializer.internal.kcp.dev/aceeb26461953562d30366db65b200f642": "aceeb26461953562d30366db65b200f64241f9e5fe888892d52eea5c",
					"initializer.internal.kcp.dev/ccf53a4988ae8515ee77131ef507cabaf1": "ccf53a4988ae8515ee77131ef507cabaf18822766c2a4cff33b24eb8",
				},
			},
			wantStatus: reconcileStatusContinue,
		},
		{
			name: "removes everything but owner username when ready",
			input: &corev1alpha1.LogicalCluster{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"tenancy.kcp.dev/phase": "Ready",
					},
					Annotations: map[string]string{
						"a":                                  "b",
						"experimental.tenancy.kcp.dev/owner": `{"username":"user-1","groups":["a","b"],"uid":"123","extra":{"c":["d"]}}`,
					},
				},
				Status: corev1alpha1.LogicalClusterStatus{
					Phase: tenancyv1alpha1.WorkspacePhaseReady,
				},
			},
			expected: metav1.ObjectMeta{
				Labels: map[string]string{
					"tenancy.kcp.dev/phase": "Ready",
				},
				Annotations: map[string]string{
					"a":                                  "b",
					"experimental.tenancy.kcp.dev/owner": `{"username":"user-1"}`,
				},
			},
			wantStatus: reconcileStatusStopAndRequeue,
		},
		{
			name: "delete invalid owner annotation when ready",
			input: &corev1alpha1.LogicalCluster{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"tenancy.kcp.dev/phase": "Ready",
					},
					Annotations: map[string]string{
						"a":                                  "b",
						"experimental.tenancy.kcp.dev/owner": `{"username":}`,
					},
				},
				Status: corev1alpha1.LogicalClusterStatus{
					Phase: tenancyv1alpha1.WorkspacePhaseReady,
				},
			},
			expected: metav1.ObjectMeta{
				Labels: map[string]string{
					"tenancy.kcp.dev/phase": "Ready",
				},
				Annotations: map[string]string{
					"a": "b",
				},
			},
			wantStatus: reconcileStatusStopAndRequeue,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			reconciler := metaDataReconciler{}
			status, err := reconciler.reconcile(context.Background(), testCase.input)

			require.NoError(t, err)
			require.Equal(t, testCase.wantStatus, status)

			if diff := cmp.Diff(testCase.input.ObjectMeta, testCase.expected); diff != "" {
				t.Errorf("invalid output after reconciling metadata: %v", diff)
			}
		})
	}
}
