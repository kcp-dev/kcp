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

package ingresssplitter

import (
	"testing"

	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/clusters"
)

func TestTracker(t *testing.T) {
	tracker := newTracker()

	newIngress := func(cluster, ns, name string) *networkingv1.Ingress {
		return &networkingv1.Ingress{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					logicalcluster.AnnotationKey: cluster,
				},
				Namespace: ns,
				Name:      name,
			},
		}
	}

	newService := func(cluster, ns, name string) *corev1.Service {
		return &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{
					logicalcluster.AnnotationKey: cluster,
				},
				Namespace: ns,
				Name:      name,
			},
		}
	}

	key := func(cluster logicalcluster.Name, ns, name string) string {
		return ns + "/" + clusters.ToClusterAwareKey(cluster, name)
	}

	// Set up 4 ingresses and 2 services
	tracker.add(newIngress("cluster1", "ns1", "i1"), newService("cluster1", "ns1", "s1"))
	tracker.add(newIngress("cluster1", "ns1", "i2"), newService("cluster1", "ns1", "s1"))
	tracker.add(newIngress("cluster1", "ns1", "i3"), newService("cluster1", "ns1", "s2"))
	tracker.add(newIngress("cluster1", "ns1", "i4"), newService("cluster1", "ns1", "s2"))

	// Validate for both services
	require.ElementsMatch(t, sets.NewString(key(logicalcluster.New("cluster1"), "ns1", "i1"), key(logicalcluster.New("cluster1"), "ns1", "i2")).List(), tracker.getIngressesForService(key(logicalcluster.New("cluster1"), "ns1", "s1")).List())
	require.ElementsMatch(t, sets.NewString(key(logicalcluster.New("cluster1"), "ns1", "i3"), key(logicalcluster.New("cluster1"), "ns1", "i4")).List(), tracker.getIngressesForService(key(logicalcluster.New("cluster1"), "ns1", "s2")).List())

	// Add an ingress/service pair that already exists & make sure it doesn't show up more than once
	tracker.add(newIngress("cluster1", "ns1", "i1"), newService("cluster1", "ns1", "s1"))
	require.ElementsMatch(t, sets.NewString(key(logicalcluster.New("cluster1"), "ns1", "i1"), key(logicalcluster.New("cluster1"), "ns1", "i2")).List(), tracker.getIngressesForService(key(logicalcluster.New("cluster1"), "ns1", "s1")).List())

	// Delete 1 ingress associated with 1 service & validate
	tracker.deleteIngress(key(logicalcluster.New("cluster1"), "ns1", "i1"))
	require.ElementsMatch(t, sets.NewString(key(logicalcluster.New("cluster1"), "ns1", "i2")).List(), tracker.getIngressesForService(key(logicalcluster.New("cluster1"), "ns1", "s1")).List())

	// Deleting remaining ingress for service & validate
	tracker.deleteIngress(key(logicalcluster.New("cluster1"), "ns1", "i2"))
	require.ElementsMatch(t, sets.NewString().List(), tracker.getIngressesForService(key(logicalcluster.New("cluster1"), "ns1", "s1")).List())

	// Make sure delete for a nonexistent ingress is ok
	tracker.deleteIngress(key(logicalcluster.New("cluster1"), "ns1", "404"))
}
