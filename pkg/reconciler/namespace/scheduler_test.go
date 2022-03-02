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

package namespace

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	clustertools "k8s.io/client-go/tools/clusters"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	"github.com/kcp-dev/kcp/third_party/conditions/util/conditions"
)

const (
	testClusterName      = "test-cluster"
	otherTestClusterName = "other-test-cluster"

	testLclusterName      = "test:lcluster"
	otherTestLclusterName = "test:other-lcluster"
)

type clusterFixture struct {
	cluster *workloadv1alpha1.WorkloadCluster
}

func newClusterFixture(lclusterName, clusterName string) *clusterFixture {
	f := &clusterFixture{
		cluster: &workloadv1alpha1.WorkloadCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:        clusterName,
				ClusterName: lclusterName,
			},
		},
	}
	return f
}

func defaultClusterFixture() *clusterFixture {
	return newClusterFixture(testLclusterName, testClusterName)
}

func otherClusterFixture() *clusterFixture {
	return newClusterFixture(testLclusterName, otherTestClusterName)
}

func (f *clusterFixture) withReady() *clusterFixture {
	conditions.MarkTrue(f.cluster, workloadv1alpha1.WorkloadClusterReadyCondition)
	return f
}

func (f *clusterFixture) withUnscheduable() *clusterFixture {
	f.cluster.Spec.Unschedulable = true
	return f
}

func (f *clusterFixture) withFutureEvictionTime() *clusterFixture {
	futureTime := metav1.NewTime(time.Now().Add(1 * time.Hour))
	f.cluster.Spec.EvictAfter = &futureTime
	return f
}

func (f *clusterFixture) withPassedEvictionTime() *clusterFixture {
	pastTime := metav1.NewTime(time.Now().Add(-1 * time.Hour))
	f.cluster.Spec.EvictAfter = &pastTime
	return f
}

func newTestScheduler(clusters []*workloadv1alpha1.WorkloadCluster) namespaceScheduler {
	return namespaceScheduler{
		getCluster: func(name string) (*workloadv1alpha1.WorkloadCluster, error) {
			for _, cluster := range clusters {
				if clustertools.ToClusterAwareKey(cluster.ClusterName, cluster.Name) == name {
					return cluster, nil
				}
			}
			return nil, apierrors.NewNotFound(workloadv1alpha1.Resource("workloadcluster"), name)
		},
		listClusters: func(selector labels.Selector) ([]*workloadv1alpha1.WorkloadCluster, error) {
			return clusters, nil
		},
	}
}

func TestAssignCluster(t *testing.T) {
	unknownClusterName := "unknown-cluster"

	testCases := map[string]struct {
		labels          map[string]string
		expectedCluster string
	}{
		"scheduling disabled set to empty -> no change even for unknown cluster name": {
			labels: map[string]string{
				ClusterLabel:            unknownClusterName,
				SchedulingDisabledLabel: "",
			},
			expectedCluster: unknownClusterName,
		},
		"scheduling disabled set to any value -> no change even for unknown cluster name": {
			labels: map[string]string{
				ClusterLabel:            unknownClusterName,
				SchedulingDisabledLabel: "foo",
			},
			expectedCluster: unknownClusterName,
		},
		"valid assignment -> no change": {
			labels: map[string]string{
				ClusterLabel: testClusterName,
			},
			expectedCluster: testClusterName,
		},
		"invalid assignment -> new assignment": {
			labels: map[string]string{
				ClusterLabel: unknownClusterName,
			},
			expectedCluster: testClusterName,
		},
		"no assignment -> new assignment": {
			expectedCluster: testClusterName,
		},
	}
	for testName, testCase := range testCases {
		t.Run(testName, func(t *testing.T) {
			clusters := []*workloadv1alpha1.WorkloadCluster{
				defaultClusterFixture().withReady().cluster,
			}
			scheduler := newTestScheduler(clusters)
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					ClusterName: testLclusterName,
					Labels:      testCase.labels,
				},
			}
			clusterName, err := scheduler.AssignCluster(ns)
			require.NoError(t, err)
			require.Equal(t, testCase.expectedCluster, clusterName)
		})
	}
}

func TestIsValidCluster(t *testing.T) {
	testCases := map[string]struct {
		cluster *clusterFixture
		isValid bool
	}{
		"missing -> false": {},
		"unready -> false": {
			cluster: defaultClusterFixture(),
		},
		"ready -> true": {
			cluster: defaultClusterFixture().withReady(),
			isValid: true,
		},
		"ready and passed eviction time -> false": {
			cluster: defaultClusterFixture().withReady().withPassedEvictionTime(),
		},
		"ready and future eviction time -> true": {
			cluster: defaultClusterFixture().withReady().withFutureEvictionTime(),
			isValid: true,
		},
	}
	for testName, testCase := range testCases {
		t.Run(testName, func(t *testing.T) {
			clusters := []*workloadv1alpha1.WorkloadCluster{}
			if testCase.cluster != nil {
				clusters = append(clusters, testCase.cluster.cluster)
			}
			scheduler := newTestScheduler(clusters)
			isValid, _, err := scheduler.isValidCluster(testLclusterName, testClusterName)
			require.NoError(t, err)
			require.Equal(t, testCase.isValid, isValid)
		})
	}
}

func TestPickCluster(t *testing.T) {
	testCases := map[string]struct {
		clusters        []*clusterFixture
		anyAssignment   bool
		expectedCluster string
	}{
		"ignore cluster in different logical cluster": {
			clusters: []*clusterFixture{
				newClusterFixture(otherTestLclusterName, testClusterName),
			},
		},
		"ignore unschedulable cluster": {
			clusters: []*clusterFixture{
				defaultClusterFixture().withUnscheduable(),
			},
		},
		"ignore cluster with eviction time in the past": {
			clusters: []*clusterFixture{
				defaultClusterFixture().withPassedEvictionTime(),
			},
		},
		"return a cluster with eviction time in the future": {
			clusters: []*clusterFixture{
				defaultClusterFixture().withReady().withFutureEvictionTime(),
			},
			expectedCluster: testClusterName,
		},
		"ignore a cluster that is not ready": {
			clusters: []*clusterFixture{
				defaultClusterFixture(),
			},
		},
		"1 ready cluster -> cluster name": {
			clusters: []*clusterFixture{
				defaultClusterFixture().withReady(),
			},
			expectedCluster: testClusterName,
		},
		"2 clusters -> any cluster name": {
			clusters: []*clusterFixture{
				defaultClusterFixture().withReady(),
				otherClusterFixture().withReady(),
			},
			anyAssignment: true,
		},
	}
	for testName, testCase := range testCases {
		t.Run(testName, func(t *testing.T) {
			clusters := []*workloadv1alpha1.WorkloadCluster{}
			for _, fixture := range testCase.clusters {
				clusters = append(clusters, fixture.cluster)
			}
			clusterName := pickCluster(clusters, testLclusterName)
			if testCase.anyAssignment {
				found := false
				for _, cluster := range clusters {
					if clusterName == cluster.Name {
						found = true
						break
					}
				}
				require.True(t, found, "unknown cluster name %s", clusterName)
			} else {
				require.Equal(t, testCase.expectedCluster, clusterName)
			}
		})
	}
}
