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

package clusterworkspace

import (
	"fmt"

	"github.com/kcp-dev/apimachinery/pkg/logicalcluster"

	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
)

// indexLocationDomainByWorkspace maps LocationDomain to their workspace for easy lookup.
func indexLocationDomainByWorkspace(obj interface{}) ([]string, error) {
	domain, ok := obj.(*schedulingv1alpha1.LocationDomain)
	if !ok {
		return []string{}, fmt.Errorf("obj is supposed to be an LocationDomain, but is %T", obj)
	}

	lcluster := logicalcluster.From(domain)
	return []string{lcluster.String()}, nil
}

// indexWorkspacesByWorkspacePrefix maps ClusterWorkspaces to their logical cluster prefixes.
func indexWorkspacesByWorkspacePrefix(obj interface{}) ([]string, error) {
	cluster, ok := obj.(*tenancyv1alpha1.ClusterWorkspace)
	if !ok {
		return []string{}, fmt.Errorf("obj is supposed to be an ClusterWorkspace, but is %T", obj)
	}

	keys := []string{}
	for clusterName := logicalcluster.From(cluster); !clusterName.Empty(); clusterName, _ = clusterName.Split() {
		keys = append(keys, clusterName.String())
	}
	return keys, nil
}
