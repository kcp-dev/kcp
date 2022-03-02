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
	"math/rand"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/klog/v2"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	"github.com/kcp-dev/kcp/third_party/conditions/util/conditions"
)

type getClusterFunc func(name string) (*workloadv1alpha1.WorkloadCluster, error)
type listClustersFunc func(selector labels.Selector) ([]*workloadv1alpha1.WorkloadCluster, error)

type namespaceScheduler struct {
	getCluster   getClusterFunc
	listClusters listClustersFunc
}

// AssignCluster returns the name of the cluster to assign to the provided
// namespace. The current cluster assignment will be returned if it is valid or if
// the automatic scheduling is disabled for the namespace. An new assignment will
// be attempted if the current assignment is empty or invalid.
func (s *namespaceScheduler) AssignCluster(ns *corev1.Namespace) (string, error) {
	assignedCluster := ns.Labels[ClusterLabel]

	schedulingDisabled := !scheduleRequirement.Matches(labels.Set(ns.Labels))
	if schedulingDisabled {
		klog.Infof("Automatic scheduling is disabled for namespace %s|%s", ns.ClusterName, ns.Name)
		return assignedCluster, nil
	}

	if assignedCluster != "" {
		isValid, invalidMsg, err := s.isValidCluster(ns.ClusterName, assignedCluster)
		if err != nil {
			return "", err
		}
		if isValid {
			return assignedCluster, nil
		}
		// A new cluster needs to be assigned
		klog.V(5).Infof("Cluster %s|%s %s", ns.ClusterName, assignedCluster, invalidMsg)
	}

	allClusters, err := s.listClusters(labels.Everything())
	if err != nil {
		return "", err
	}
	return pickCluster(allClusters, ns.ClusterName), nil
}

// isValidCluster checks whether the given cluster name exists and is valid for
// the purposes of any namespace already scheduled to it (i.e., if it reports
// as Ready, and any evictAfter value, if specified, has not yet passed).
//
// It doesn't take into account Unschedulable, and should only be used when
// determining if a cluster that a namespace has already been assigned to
// should keep having that namespace.
func (s *namespaceScheduler) isValidCluster(lclusterName, clusterName string) (
	isValid bool, invalidMsg string, err error) {

	cluster, err := s.getCluster(clusters.ToClusterAwareKey(lclusterName, clusterName))
	if apierrors.IsNotFound(err) {
		return false, "does not exist", nil
	}
	if err != nil {
		return false, "", err
	}
	// TODO(marun) Stop duplicating these checks here and in pickCluster
	if ready := conditions.IsTrue(cluster, workloadv1alpha1.WorkloadClusterReadyCondition); !ready {
		return false, "is not reporting ready", nil
	}
	if evictAfter := cluster.Spec.EvictAfter; evictAfter != nil && evictAfter.Time.Before(time.Now()) {
		return false, "is cordoned", nil
	}
	return true, "", nil
}

// pickCluster attempts to choose a cluster in the given logical
// cluster to assign to a namespace. If a suitable cluster is
// identified, its name will be returned. Otherwise, an empty string
// will be returned.
func pickCluster(allClusters []*workloadv1alpha1.WorkloadCluster, lclusterName string) string {
	var clusters []*workloadv1alpha1.WorkloadCluster
	for i := range allClusters {
		// Only include Clusters that are in the logical cluster
		if allClusters[i].ClusterName != lclusterName {
			klog.V(2).InfoS("pickCluster: excluding cluster with different metadata.clusterName",
				"ns.clusterName", lclusterName, "check", allClusters[i].ClusterName)
			continue
		}
		if allClusters[i].Spec.Unschedulable {
			klog.V(2).InfoS("pickCluster: excluding unschedulable cluster", "metadata.name", allClusters[i].Name)
			continue
		}
		if evictAfter := allClusters[i].Spec.EvictAfter; evictAfter != nil && evictAfter.Time.Before(time.Now()) {
			klog.V(2).InfoS("pickCluster: excluding cluster with evictAfter value that has passed",
				"metadata.name", allClusters[i].Name)
			continue
		}
		if !conditions.IsTrue(allClusters[i], workloadv1alpha1.WorkloadClusterReadyCondition) {
			klog.V(2).InfoS("pickCluster: excluding not-ready cluster", "metadata.name", allClusters[i].Name)
			continue
		}

		klog.V(2).InfoS("pickCluster: found a ready candidate", "metadata.name", allClusters[i].Name)
		clusters = append(clusters, allClusters[i])
	}

	newClusterName := ""
	if len(clusters) > 0 {
		// Select a cluster at random.
		cluster := clusters[rand.Intn(len(clusters))]
		newClusterName = cluster.Name
	}

	return newClusterName
}
