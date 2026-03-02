/*
Copyright 2025 The KCP Authors.

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

package garbagecollector

import (
	"testing"

	"github.com/stretchr/testify/assert"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kcp-dev/logicalcluster/v3"
)

var (
	testNodeA = ObjectReference{
		OwnerReference: metav1.OwnerReference{
			APIVersion: "apps/v1",
			Kind:       "Deployment",
			Name:       "test-deployment",
			UID:        "uid-deployment",
		},
		Namespace:   "default",
		ClusterName: logicalcluster.Name("cluster-a"),
	}
	testNodeB = ObjectReference{
		OwnerReference: metav1.OwnerReference{
			APIVersion: "v1",
			Kind:       "Pod",
			Name:       "test-pod",
			UID:        "uid-pod",
		},
		Namespace:   "default",
		ClusterName: logicalcluster.Name("cluster-a"),
	}
	testNodeC = ObjectReference{
		OwnerReference: metav1.OwnerReference{
			APIVersion: "v1",
			Kind:       "Pod",
			Name:       "test-pod2",
			UID:        "uid-pod2",
		},
		Namespace:   "default",
		ClusterName: logicalcluster.Name("cluster-a"),
	}
)

func TestGraph_Nodes(t *testing.T) {
	t.Parallel()

	graph := NewGraph()

	t.Log("Adding Deployment node to graph")
	graph.Add(testNodeA, nil, nil)

	t.Log("Add Pod nodes with Deployment as owner")
	graph.Add(testNodeB, nil, []ObjectReference{testNodeA})
	graph.Add(testNodeC, nil, []ObjectReference{testNodeA})

	t.Log("Verify that Deployment owns the two Pods")
	owned := graph.Owned(testNodeA)
	assert.Equal(t, 2, len(owned), "expected Deployment to own 2 Pods")
	assert.Contains(t, owned, testNodeB, "expected Deployment to own testNodeB")
	assert.Contains(t, owned, testNodeC, "expected Deployment to own testNodeC")

	t.Log("Remove one Pod and verify ownership")
	removed, owned := graph.Remove(testNodeB)
	assert.True(t, removed, "expected Pod removal to succeed")
	assert.Equal(t, 0, len(owned), "expected Pod to own 0 objects upon removal")

	owned = graph.Owned(testNodeA)
	assert.Equal(t, 1, len(owned), "expected Deployment to own 1 Pod after removal")
	assert.Contains(t, owned, testNodeC, "expected Deployment to still own testNodeC")

	t.Log("Try removing Deployment with owned Pod")
	success, owned := graph.Remove(testNodeA)
	assert.False(t, success, "expected Deployment removal to fail due to owned Pods")
	assert.Equal(t, 1, len(owned), "expected Deployment to own 1 Pod during removal attempt")

	t.Log("Remove remaining Pod and verify Deployment ownership")
	graph.Remove(testNodeC)

	owned = graph.Owned(testNodeA)
	assert.Equal(t, 0, len(owned), "expected Deployment to own 0 Pods after all removals")

	t.Log("Now remove Deployment successfully")
	success, owned = graph.Remove(testNodeA)
	assert.True(t, success, "expected Deployment removal to succeed with no owned Pods")
	assert.Equal(t, 0, len(owned), "expected no owned Pods during Deployment removal")
}
