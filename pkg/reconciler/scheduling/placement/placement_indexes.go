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

package placement

import (
	"fmt"

	"github.com/kcp-dev/apimachinery/pkg/logicalcluster"

	corev1 "k8s.io/api/core/v1"

	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
)

// indexLocationDomainByAssignmentLabel is an index function that maps an domain assignment label to
// the location domain.
func indexLocationDomainByAssignmentLabel(obj interface{}) ([]string, error) {
	domain, ok := obj.(*schedulingv1alpha1.LocationDomain)
	if !ok {
		return []string{}, fmt.Errorf("obj is supposed to be an LocationDomain, but is %T", obj)
	}

	labelValue := schedulingv1alpha1.LocationDomainAssignmentLabelValue(logicalcluster.From(domain), domain.Name)
	return []string{labelValue}, nil
}

// indexWorkloadClustersByWorkspace maps negotiation workspaces to workload clusters.
func indexWorkloadClustersByWorkspace(obj interface{}) ([]string, error) {
	cluster, ok := obj.(*workloadv1alpha1.WorkloadCluster)
	if !ok {
		return []string{}, fmt.Errorf("obj is supposed to be an WorkloadCluster, but is %T", obj)
	}

	lcluster := logicalcluster.From(cluster)
	return []string{lcluster.String()}, nil
}

func indexNamespacByWorkspace(obj interface{}) ([]string, error) {
	ns, ok := obj.(*corev1.Namespace)
	if !ok {
		return []string{}, fmt.Errorf("obj is supposed to be an Namespace, but is %T", obj)
	}

	return []string{logicalcluster.From(ns).String()}, nil
}

const indexUnscheduledNamedspacesKey = "unscheduled"

func indexUnscheduledNamespaces(obj interface{}) ([]string, error) {
	ns, ok := obj.(*corev1.Namespace)
	if !ok {
		return []string{}, fmt.Errorf("obj is supposed to be an Namespace, but is %T", obj)
	}

	if _, found := ns.Annotations[schedulingv1alpha1.PlacementAnnotationKey]; !found {
		return []string{indexUnscheduledNamedspacesKey}, nil
	}
	return []string{}, nil
}
