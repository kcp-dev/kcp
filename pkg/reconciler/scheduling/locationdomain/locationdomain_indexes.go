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

package locationdomain

import (
	"fmt"

	"github.com/kcp-dev/apimachinery/pkg/logicalcluster"

	"k8s.io/client-go/tools/clusters"

	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
)

// indexLocationsByLocationDomain is an index function that maps an APIBinding to the key for its
// spec.reference.workspace.
func indexLocationsByLocationDomain(obj interface{}) ([]string, error) {
	l, ok := obj.(*schedulingv1alpha1.Location)
	if !ok {
		return []string{}, fmt.Errorf("obj is supposed to be an Location, but is %T", obj)
	}

	key := clusters.ToClusterAwareKey(logicalcluster.New(string(l.Spec.Domain.Workspace)), l.Spec.Domain.Name)
	return []string{key}, nil
}

// indexLocationDomainByNegotiationWorkspace is an index function that maps an negotiation workspace to
// LocationDomains that reference it.
func indexLocationDomainByNegotiationWorkspace(obj interface{}) ([]string, error) {
	domain, ok := obj.(*schedulingv1alpha1.LocationDomain)
	if !ok {
		return []string{}, fmt.Errorf("obj is supposed to be an LocationDomain, but is %T", obj)
	}

	if domain.Spec.Instances.Workspace == nil {
		return []string{}, nil
	}
	return []string{logicalcluster.From(domain).Join(string(domain.Spec.Instances.Workspace.WorkspaceName)).String()}, nil
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
