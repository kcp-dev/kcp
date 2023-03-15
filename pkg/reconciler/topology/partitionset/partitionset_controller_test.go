/*
Copyright 2023 The KCP Authors.

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

package partitionset

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/util/conditions"
	topologyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/topology/v1alpha1"
)

func TestReconcile(t *testing.T) {
	tests := map[string]struct {
		existingValidPartition   bool
		existingInvalidPartition bool
		listShardsError          error
		createPartitionError     bool
		deletePartitionError     bool
		withMatchLabelOverlap    bool

		wantError              bool
		wantPartitionsReady    bool
		wantPartitionsNotReady bool
		wantPartitionCount     int
		wantCountCreated       int
		wantCountDeleted       int
	}{
		"error listing shards": {
			listShardsError:        errors.New("foo"),
			wantError:              true,
			wantPartitionsNotReady: true,
		},
		"error when creating partition": {
			createPartitionError:   true,
			wantError:              true,
			wantPartitionsNotReady: true,
		},
		"error when deleting partition": {
			deletePartitionError:     true,
			existingInvalidPartition: true,
			wantError:                true,
			wantPartitionsNotReady:   true,
		},
		"Partitions created when no issue": {
			wantPartitionsReady: true,
			wantPartitionCount:  3,
			wantCountCreated:    3,
		},
		"Partitions created when one valid partition existing": {
			wantPartitionsReady:    true,
			existingValidPartition: true,
			wantPartitionCount:     3,
			wantCountCreated:       2,
		},
		"Partition deleted when one invalid partition existing": {
			wantPartitionsReady:      true,
			existingInvalidPartition: true,
			wantPartitionCount:       3,
			wantCountCreated:         3,
			wantCountDeleted:         1,
		},
		"Partitions created with selector and dimension overlapping": {
			withMatchLabelOverlap: true,
			wantPartitionsReady:   true,
			wantPartitionCount:    2,
			wantCountCreated:      2,
		},
	}

	for name, tc := range tests {
		tc := tc // to avoid t.Parallel() races

		t.Run(name, func(t *testing.T) {
			nbPartitionsCreated := 0
			nbPartitionsDeleted := 0

			c := &controller{

				listShards: func(selector labels.Selector) ([]*corev1alpha1.Shard, error) {
					if tc.listShardsError != nil {
						return nil, tc.listShardsError
					}
					shards := []*corev1alpha1.Shard{
						{
							ObjectMeta: metav1.ObjectMeta{
								Annotations: map[string]string{
									logicalcluster.AnnotationKey: "root:org:ws",
								},
								Labels: map[string]string{
									"region": "Europe",
									"cloud":  "Azure",
									"az":     "EU-1",
									"name":   "shard1",
								},
								Name: "shard1",
							},
							Spec: corev1alpha1.ShardSpec{
								VirtualWorkspaceURL: "https://server-1.kcp.dev/",
							},
						},
						{
							ObjectMeta: metav1.ObjectMeta{
								Annotations: map[string]string{
									logicalcluster.AnnotationKey: "root:org:ws",
								},
								Labels: map[string]string{
									"region": "Europe",
									"cloud":  "Azure",
									"az":     "EU-2",
									"name":   "shard2",
								},
								Name: "shard2",
							},
							Spec: corev1alpha1.ShardSpec{
								VirtualWorkspaceURL: "https://server-2.kcp.dev/",
							},
						},
						{
							ObjectMeta: metav1.ObjectMeta{
								Annotations: map[string]string{
									logicalcluster.AnnotationKey: "root:org:ws",
								},
								Labels: map[string]string{
									"region": "Europe",
									"cloud":  "Azure",
									"az":     "EU-3",
									"name":   "shard3",
								},
								Name: "shard3",
							},
							Spec: corev1alpha1.ShardSpec{
								VirtualWorkspaceURL: "https://server-3.kcp.dev/",
							},
						},
						{
							ObjectMeta: metav1.ObjectMeta{
								Annotations: map[string]string{
									logicalcluster.AnnotationKey: "root:org:ws",
								},
								Labels: map[string]string{
									"region": "Asia",
									"cloud":  "Azure",
									"az":     "cn-west-1",
									"name":   "shard5",
								},
								Name: "shard5",
							},
							Spec: corev1alpha1.ShardSpec{
								VirtualWorkspaceURL: "https://server-5.kcp.dev/",
							},
						},
					}
					if !tc.withMatchLabelOverlap {
						shards = append(shards, &corev1alpha1.Shard{
							ObjectMeta: metav1.ObjectMeta{
								Annotations: map[string]string{
									logicalcluster.AnnotationKey: "root:org:ws",
								},
								Labels: map[string]string{
									"region": "Europe",
									"cloud":  "AWS",
									"az":     "EU-3",
									"name":   "shard4",
								},
								Name: "shard4",
							},
							Spec: corev1alpha1.ShardSpec{
								VirtualWorkspaceURL: "https://server-4.kcp.dev/",
							},
						})
					}
					return shards, nil
				},

				getPartitionSet: func(clusterName logicalcluster.Name, name string) (*topologyv1alpha1.PartitionSet, error) {
					shardSelector := &metav1.LabelSelector{
						MatchExpressions: []metav1.LabelSelectorRequirement{
							{
								Key:      "region",
								Operator: metav1.LabelSelectorOpIn,
								Values:   []string{"Europe", "Asia"},
							},
						},
					}
					if tc.withMatchLabelOverlap {
						shardSelector.MatchLabels = map[string]string{"cloud": "Azure"}
					}

					return &topologyv1alpha1.PartitionSet{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: map[string]string{
								logicalcluster.AnnotationKey: clusterName.String(),
							},
							Name: name,
						},
						Spec: topologyv1alpha1.PartitionSetSpec{
							Dimensions:    []string{"region", "cloud"},
							ShardSelector: shardSelector,
						},
					}, nil
				},

				getPartitionsByPartitionSet: func(ctx context.Context, partitionSet *topologyv1alpha1.PartitionSet) ([]*topologyv1alpha1.Partition, error) {
					var partitions []*topologyv1alpha1.Partition
					if tc.existingValidPartition {
						partition := &topologyv1alpha1.Partition{
							ObjectMeta: metav1.ObjectMeta{
								Annotations: map[string]string{
									logicalcluster.AnnotationKey: "root:org:ws",
								},
								Name: "my-partitionset-1111",
							},
							Spec: topologyv1alpha1.PartitionSpec{
								Selector: &metav1.LabelSelector{
									MatchLabels: map[string]string{
										"cloud":  "Azure",
										"region": "Europe",
									},
									MatchExpressions: []metav1.LabelSelectorRequirement{
										{
											Key:      "region",
											Operator: metav1.LabelSelectorOpIn,
											Values:   []string{"Europe", "Asia"},
										},
									},
								},
							},
						}
						partitions = append(partitions, partition)
					}
					if tc.existingInvalidPartition {
						partitions = append(partitions, &topologyv1alpha1.Partition{
							ObjectMeta: metav1.ObjectMeta{
								Annotations: map[string]string{
									logicalcluster.AnnotationKey: "root:org:ws",
								},
								Name: "my-partitionset-1111",
							},
							Spec: topologyv1alpha1.PartitionSpec{
								Selector: &metav1.LabelSelector{
									MatchLabels: map[string]string{
										"cloud":  "Azure",
										"region": "Europe",
										"az":     "EU-1",
									},
									MatchExpressions: []metav1.LabelSelectorRequirement{
										{
											Key:      "region",
											Operator: metav1.LabelSelectorOpIn,
											Values:   []string{"Europe", "Asia"},
										},
									},
								},
							},
						})
					}
					return partitions, nil
				},

				createPartition: func(_ context.Context, path logicalcluster.Path, partition *topologyv1alpha1.Partition) (*topologyv1alpha1.Partition, error) {
					if tc.createPartitionError {
						return nil, fmt.Errorf("error by partition creation")
					} else {
						t.Logf("Creating Partition %s %v", path, partition)
						nbPartitionsCreated++
						return nil, nil
					}
				},

				deletePartition: func(_ context.Context, path logicalcluster.Path, partitionName string) error {
					if tc.deletePartitionError {
						return fmt.Errorf("error by partition deletion")
					} else {
						t.Logf("Deleting Partition %s %s", path, partitionName)
						nbPartitionsDeleted++
						return nil
					}
				},
			}

			clusterName, _ := logicalcluster.NewPath("root:org:ws").Name()
			partitionSet, _ := c.getPartitionSet(clusterName, "my-partitionset")
			err := c.reconcile(context.Background(), partitionSet)
			t.Logf("Error %v", err)
			t.Logf("PartitionSet %v", partitionSet)

			if tc.wantError {
				require.Error(t, err, "expected an error")
			} else {
				require.NoError(t, err, "expected no error")
				require.Equal(t, uint16(tc.wantPartitionCount), partitionSet.Status.Count)
			}

			if tc.wantPartitionsNotReady {
				requireConditionMatches(t, partitionSet,
					conditions.FalseCondition(
						topologyv1alpha1.PartitionsReady,
						topologyv1alpha1.ErrorGeneratingPartitionsReason,
						conditionsv1alpha1.ConditionSeverityError,
						"",
					),
				)
			}

			if tc.wantPartitionsReady {
				requireConditionMatches(t, partitionSet, conditions.TrueCondition(topologyv1alpha1.PartitionsReady))
			}

			require.Equal(t, tc.wantCountCreated, nbPartitionsCreated, "not the expected number of partitions created")
			require.Equal(t, tc.wantCountDeleted, nbPartitionsDeleted, "not the expected number of partitions deleted")
		})
	}
}

// requireConditionMatches looks for a condition matching c in g. LastTransitionTime and Message
// are not compared.
func requireConditionMatches(t *testing.T, g conditions.Getter, c *conditionsv1alpha1.Condition) {
	actual := conditions.Get(g, c.Type)

	require.NotNil(t, actual, "missing condition %q", c.Type)
	actual.LastTransitionTime = c.LastTransitionTime
	actual.Message = c.Message
	require.Empty(t, cmp.Diff(actual, c))
}
