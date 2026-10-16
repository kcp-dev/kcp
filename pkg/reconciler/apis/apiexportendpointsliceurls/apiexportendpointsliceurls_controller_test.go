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

package apiexportendpointsliceurls

import (
	"context"
	"errors"
	"testing"

	"github.com/go-logr/logr"
	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/utils/ptr"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
	apisv1alpha1apply "github.com/kcp-dev/sdk/client/applyconfiguration/apis/v1alpha1"

	"github.com/kcp-dev/kcp/pkg/indexers"
)

func TestReconcile(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		input               *apisv1alpha1.APIExportEndpointSlice
		endpointsReconciler *endpointsReconciler
		expectedConditions  []*conditionsv1alpha1.Condition
		expectedError       error
	}{
		"condition not ready": {
			input: &apisv1alpha1.APIExportEndpointSlice{
				Status: apisv1alpha1.APIExportEndpointSliceStatus{
					Conditions: []conditionsv1alpha1.Condition{
						{
							Type:   apisv1alpha2.APIExportValid,
							Status: corev1.ConditionFalse,
						},
					},
				},
			},
			endpointsReconciler: &endpointsReconciler{},
		},
		"empty selector": {
			input: &apisv1alpha1.APIExportEndpointSlice{
				Spec: apisv1alpha1.APIExportEndpointSliceSpec{
					APIExport: apisv1alpha1.ExportBindingReference{
						Name: "my-export",
						Path: "root:org:ws",
					},
				},
				Status: apisv1alpha1.APIExportEndpointSliceStatus{
					Conditions: []conditionsv1alpha1.Condition{
						{
							Type:   apisv1alpha2.APIExportValid,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
			endpointsReconciler: &endpointsReconciler{
				getMyShard: func() (*corev1alpha1.Shard, error) {
					return &corev1alpha1.Shard{
						ObjectMeta: metav1.ObjectMeta{
							Name: "shard1",
						},
					}, nil
				},
				getAPIExport: func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error) {
					return &apisv1alpha2.APIExport{
						ObjectMeta: metav1.ObjectMeta{
							Name: "my-export",
						},
					}, nil
				},
			},
		},
		"invalid selector": {
			input: &apisv1alpha1.APIExportEndpointSlice{
				Status: apisv1alpha1.APIExportEndpointSliceStatus{
					ShardSelector: ",",
					Conditions: []conditionsv1alpha1.Condition{
						{
							Type:   apisv1alpha2.APIExportValid,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
			endpointsReconciler: &endpointsReconciler{},
			expectedError:       errors.New("invalid selector: ,"),
		},
		"error getting apiExport": {
			input: &apisv1alpha1.APIExportEndpointSlice{
				Status: apisv1alpha1.APIExportEndpointSliceStatus{
					ShardSelector: "shared=foo",
					Conditions: []conditionsv1alpha1.Condition{
						{
							Type:   apisv1alpha2.APIExportValid,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
			endpointsReconciler: &endpointsReconciler{
				getAPIExport: func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error) {
					return nil, errors.New("lost in space")
				},
			},
			expectedError: errors.New("lost in space"),
		},
		"update endpoint - not my shard - no update": {
			input: &apisv1alpha1.APIExportEndpointSlice{
				Spec: apisv1alpha1.APIExportEndpointSliceSpec{
					APIExport: apisv1alpha1.ExportBindingReference{
						Path: "root:org:ws",
						Name: "my-export",
					},
				},
				Status: apisv1alpha1.APIExportEndpointSliceStatus{
					ShardSelector: "shared=foo",
					Conditions: []conditionsv1alpha1.Condition{
						{
							Type:   apisv1alpha2.APIExportValid,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
			endpointsReconciler: &endpointsReconciler{
				thisShard: "shard2",
				getAPIExport: func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error) {
					return &apisv1alpha2.APIExport{}, nil
				},
				getMyShard: func() (*corev1alpha1.Shard, error) {
					return &corev1alpha1.Shard{
						ObjectMeta: metav1.ObjectMeta{
							Name: "shard1",
						},
					}, nil
				},
				patchAPIExportEndpointSlice: func(ctx context.Context, cluster logicalcluster.Path, patch *apisv1alpha1apply.APIExportEndpointSliceApplyConfiguration) error {
					if len(patch.Status.APIExportEndpoints) != 1 && patch.Status.APIExportEndpoints[0].URL != ptr.To("") {
						return errors.New("unexpected update")
					}
					return nil
				},
			},
		},
		"my shard, no consumers": {
			input: &apisv1alpha1.APIExportEndpointSlice{
				Spec: apisv1alpha1.APIExportEndpointSliceSpec{
					APIExport: apisv1alpha1.ExportBindingReference{
						Path: "root:org:ws",
						Name: "my-export",
					},
				},
				Status: apisv1alpha1.APIExportEndpointSliceStatus{
					ShardSelector: "shared=foo",
					Conditions: []conditionsv1alpha1.Condition{
						{
							Type:   apisv1alpha2.APIExportValid,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
			endpointsReconciler: &endpointsReconciler{
				thisShard: "shard1",
				getAPIExport: func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error) {
					return &apisv1alpha2.APIExport{}, nil
				},
				getMyShard: func() (*corev1alpha1.Shard, error) {
					return &corev1alpha1.Shard{
						ObjectMeta: metav1.ObjectMeta{
							Name: "shard1",
						},
						Spec: corev1alpha1.ShardSpec{
							VirtualWorkspaceURL: "https://server-1.kcp.dev/",
						},
					}, nil
				},
				listAPIBindingsByAPIExport: func(apiexport *apisv1alpha2.APIExport) ([]*apisv1alpha2.APIBinding, error) {
					return nil, nil
				},
				patchAPIExportEndpointSlice: func(ctx context.Context, cluster logicalcluster.Path, patch *apisv1alpha1apply.APIExportEndpointSliceApplyConfiguration) error {
					if patch.Status.APIExportEndpoints != nil {
						return errors.New("unexpected update")
					}
					return nil
				},
			},
		},
		"my shard, consumer went away, remove url": {
			input: &apisv1alpha1.APIExportEndpointSlice{
				Spec: apisv1alpha1.APIExportEndpointSliceSpec{
					APIExport: apisv1alpha1.ExportBindingReference{
						Path: "root:org:ws",
						Name: "my-export",
					},
				},
				Status: apisv1alpha1.APIExportEndpointSliceStatus{
					ShardSelector: "shared=foo",
					Conditions: []conditionsv1alpha1.Condition{
						{
							Type:   apisv1alpha2.APIExportValid,
							Status: corev1.ConditionTrue,
						},
					},
					APIExportEndpoints: []apisv1alpha1.APIExportEndpoint{
						{
							URL: "https://server-1.kcp.dev/who-took-the-cookie-from-the-cookie-jar",
						},
					},
				},
			},
			endpointsReconciler: &endpointsReconciler{
				thisShard: "shard1",
				getAPIExport: func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error) {
					return &apisv1alpha2.APIExport{}, nil
				},
				getMyShard: func() (*corev1alpha1.Shard, error) {
					return &corev1alpha1.Shard{
						ObjectMeta: metav1.ObjectMeta{
							Name: "shard1",
						},
						Spec: corev1alpha1.ShardSpec{
							VirtualWorkspaceURL: "https://server-1.kcp.dev/",
						},
					}, nil
				},
				listAPIBindingsByAPIExport: func(apiexport *apisv1alpha2.APIExport) ([]*apisv1alpha2.APIBinding, error) {
					return nil, nil
				},
				patchAPIExportEndpointSlice: func(ctx context.Context, cluster logicalcluster.Path, patch *apisv1alpha1apply.APIExportEndpointSliceApplyConfiguration) error {
					if patch.Status.APIExportEndpoints != nil {
						return errors.New("unexpected update")
					}
					return nil
				},
			},
		},
		"my shard, consumer exists, add url": {
			input: &apisv1alpha1.APIExportEndpointSlice{
				Spec: apisv1alpha1.APIExportEndpointSliceSpec{
					APIExport: apisv1alpha1.ExportBindingReference{
						Path: "root:org:ws",
						Name: "my-export",
					},
				},
				Status: apisv1alpha1.APIExportEndpointSliceStatus{
					ShardSelector: "shared=foo",
					Conditions: []conditionsv1alpha1.Condition{
						{
							Type:   apisv1alpha2.APIExportValid,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
			endpointsReconciler: &endpointsReconciler{
				thisShard: "shard1",
				getAPIExport: func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error) {
					return &apisv1alpha2.APIExport{
						ObjectMeta: metav1.ObjectMeta{
							Name: "my-export",
						},
					}, nil
				},
				getMyShard: func() (*corev1alpha1.Shard, error) {
					return &corev1alpha1.Shard{
						ObjectMeta: metav1.ObjectMeta{
							Name: "shard1",
						},
						Spec: corev1alpha1.ShardSpec{
							VirtualWorkspaceURL: "https://server-1.kcp.dev/",
						},
					}, nil
				},
				listAPIBindingsByAPIExport: func(apiexport *apisv1alpha2.APIExport) ([]*apisv1alpha2.APIBinding, error) {
					return []*apisv1alpha2.APIBinding{
						{
							ObjectMeta: metav1.ObjectMeta{
								Name: "my-binding",
							},
						},
					}, nil
				},
				patchAPIExportEndpointSlice: func(ctx context.Context, cluster logicalcluster.Path, patch *apisv1alpha1apply.APIExportEndpointSliceApplyConfiguration) error {
					if len(patch.Status.APIExportEndpoints) != 1 {
						t.Fatalf("unexpected update: %v", patch)
					}
					url := ptr.Deref(patch.Status.APIExportEndpoints[0].URL, "")
					if url != "https://server-1.kcp.dev/services/apiexport/my-export" {
						t.Fatalf("unexpected update: %v", patch)
					}
					return nil
				},
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := &controller{
				getMyShard:                  tc.endpointsReconciler.getMyShard,
				getAPIExport:                tc.endpointsReconciler.getAPIExport,
				listAPIBindingsByAPIExport:  tc.endpointsReconciler.listAPIBindingsByAPIExport,
				patchAPIExportEndpointSlice: tc.endpointsReconciler.patchAPIExportEndpointSlice,
				thisShard:                   tc.endpointsReconciler.thisShard,
			}
			input := tc.input.DeepCopy()
			_, err := c.reconcile(context.Background(), input)
			if tc.expectedError != nil {
				require.Error(t, err, tc.expectedError.Error())
			} else {
				require.NoError(t, err, "expected no error")
			}

			for _, expectedCondition := range tc.expectedConditions {
				requireConditionMatches(t, input, expectedCondition)
			}
		})
	}
}

func TestEnqueueAPIExportEndpointSliceByAPIBinding(t *testing.T) {
	t.Parallel()

	const (
		exportCluster   = "exportcluster"
		consumerCluster = "consumercluster"
		exportPath      = "root:org:ws"
		exportName      = "my-export"
	)

	export := &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name: exportName,
			Annotations: map[string]string{
				logicalcluster.AnnotationKey:         exportCluster,
				core.LogicalClusterPathAnnotationKey: exportPath,
			},
		},
	}
	slice := func(cluster, name, refPath, refName string) *apisv1alpha1.APIExportEndpointSlice {
		return &apisv1alpha1.APIExportEndpointSlice{
			ObjectMeta: metav1.ObjectMeta{
				Name:        name,
				Annotations: map[string]string{logicalcluster.AnnotationKey: cluster},
			},
			Spec: apisv1alpha1.APIExportEndpointSliceSpec{
				APIExport: apisv1alpha1.ExportBindingReference{Path: refPath, Name: refName},
			},
		}
	}
	binding := func(cluster, refPath string) *apisv1alpha2.APIBinding {
		return &apisv1alpha2.APIBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "my-binding",
				Annotations: map[string]string{logicalcluster.AnnotationKey: cluster},
			},
			Spec: apisv1alpha2.APIBindingSpec{
				Reference: apisv1alpha2.BindingReference{
					Export: &apisv1alpha2.ExportBindingReference{Path: refPath, Name: exportName},
				},
			},
		}
	}
	exportFound := func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error) {
		if name == exportName && (path.String() == exportPath || path.String() == exportCluster) {
			return export, nil
		}
		return nil, apierrors.NewNotFound(apisv1alpha2.Resource("apiexports"), path.Join(name).String())
	}
	exportNotFound := func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error) {
		return nil, apierrors.NewNotFound(apisv1alpha2.Resource("apiexports"), path.Join(name).String())
	}

	// Slices that live next to the export, on another shard, so this shard only
	// sees them through the cache.
	pathless := slice(exportCluster, "pathless", "", exportName)
	explicitPath := slice(exportCluster, "explicit-path", exportPath, exportName)
	clusterPath := slice(exportCluster, "cluster-path", exportCluster, exportName)
	unrelated := slice(exportCluster, "unrelated", "", "other-export")
	// A slice in a third workspace that references the export by canonical path.
	// Unlike explicitPath, it is not also indexed under the export's cluster
	// name, so only resolving the export can connect it to a binding that
	// references the export by cluster name.
	remoteExplicitPath := slice("slicecluster", "remote-explicit-path", exportPath, exportName)

	tests := map[string]struct {
		localSlices  []*apisv1alpha1.APIExportEndpointSlice
		globalSlices []*apisv1alpha1.APIExportEndpointSlice
		binding      *apisv1alpha2.APIBinding
		getAPIExport func(path logicalcluster.Path, name string) (*apisv1alpha2.APIExport, error)
		expected     []*apisv1alpha1.APIExportEndpointSlice
	}{
		"binding by canonical path finds a path-less slice in the export's workspace": {
			globalSlices: []*apisv1alpha1.APIExportEndpointSlice{pathless, unrelated},
			binding:      binding(consumerCluster, exportPath),
			getAPIExport: exportFound,
			expected:     []*apisv1alpha1.APIExportEndpointSlice{pathless},
		},
		"binding by canonical path finds every spelling of the reference": {
			globalSlices: []*apisv1alpha1.APIExportEndpointSlice{pathless, explicitPath, clusterPath, unrelated},
			binding:      binding(consumerCluster, exportPath),
			getAPIExport: exportFound,
			expected:     []*apisv1alpha1.APIExportEndpointSlice{pathless, explicitPath, clusterPath},
		},
		"binding by logical cluster name finds a remote slice that spells out the canonical path": {
			globalSlices: []*apisv1alpha1.APIExportEndpointSlice{remoteExplicitPath, unrelated},
			binding:      binding(consumerCluster, exportCluster),
			getAPIExport: exportFound,
			expected:     []*apisv1alpha1.APIExportEndpointSlice{remoteExplicitPath},
		},
		"local binding finds the path-less slice next to it": {
			localSlices:  []*apisv1alpha1.APIExportEndpointSlice{pathless, unrelated},
			binding:      binding(exportCluster, ""),
			getAPIExport: exportFound,
			expected:     []*apisv1alpha1.APIExportEndpointSlice{pathless},
		},
		"slices are found in the local and the global indexer": {
			localSlices:  []*apisv1alpha1.APIExportEndpointSlice{explicitPath},
			globalSlices: []*apisv1alpha1.APIExportEndpointSlice{pathless},
			binding:      binding(consumerCluster, exportPath),
			getAPIExport: exportFound,
			expected:     []*apisv1alpha1.APIExportEndpointSlice{explicitPath, pathless},
		},
		"export not in the cache yet falls back to the reference as written": {
			globalSlices: []*apisv1alpha1.APIExportEndpointSlice{pathless, explicitPath},
			binding:      binding(consumerCluster, exportPath),
			getAPIExport: exportNotFound,
			expected:     []*apisv1alpha1.APIExportEndpointSlice{explicitPath},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			newIndexer := func(slices []*apisv1alpha1.APIExportEndpointSlice) cache.Indexer {
				indexer := cache.NewIndexer(kcpcache.MetaClusterNamespaceKeyFunc, cache.Indexers{})
				indexers.AddIfNotPresentOrDie(indexer, cache.Indexers{
					indexers.APIExportEndpointSliceByAPIExport: indexers.IndexAPIExportEndpointSliceByAPIExport,
				})
				for _, s := range slices {
					require.NoError(t, indexer.Add(s))
				}
				return indexer
			}

			c := &controller{
				queue: workqueue.NewTypedRateLimitingQueue(
					workqueue.DefaultTypedControllerRateLimiter[string](),
				),
				getAPIExport:                        tc.getAPIExport,
				apiExportEndpointSliceIndexer:       newIndexer(tc.localSlices),
				globalAPIExportEndpointSliceIndexer: newIndexer(tc.globalSlices),
			}
			t.Cleanup(c.queue.ShutDown)

			c.enqueueAPIExportEndpointSliceByAPIBinding(tc.binding, logr.Discard())

			expected := sets.New[string]()
			for _, s := range tc.expected {
				key, err := kcpcache.MetaClusterNamespaceKeyFunc(s)
				require.NoError(t, err)
				expected.Insert(key)
			}
			got := sets.New[string]()
			for c.queue.Len() > 0 {
				key, _ := c.queue.Get()
				got.Insert(key)
				c.queue.Done(key)
			}
			require.Equal(t, sets.List(expected), sets.List(got))
		})
	}
}

// requireConditionMatches looks for a condition matching c in g. LastTransitionTime and Message
// are not compared.
func requireConditionMatches(t *testing.T, g conditions.Getter, c *conditionsv1alpha1.Condition) {
	t.Helper()
	actual := conditions.Get(g, c.Type)
	require.NotNil(t, actual, "missing condition %q", c.Type)
	actual.LastTransitionTime = c.LastTransitionTime
	actual.Message = c.Message
	require.Empty(t, cmp.Diff(actual, c))
}
