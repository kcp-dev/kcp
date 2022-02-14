/*
Copyright 2021 The KCP Authors.

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

package helper

import (
	"fmt"
	"strings"

	"k8s.io/client-go/tools/clusters"

	tenancyapi "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
)

const (
	separator = "_"

	// OrganizationCluster is the name of the logical cluster we expect to find
	// organization inside.
	OrganizationCluster = "admin"
)

// EncodeLogicalClusterName determines the logical cluster name for a workspace.
// We assume that the organization that this workspace resides in is the cluster
// it lives in.
func EncodeLogicalClusterName(workspace *tenancyapi.ClusterWorkspace) (string, error) {
	orgName := workspace.ClusterName
	if workspace.ClusterName != OrganizationCluster {
		_, name, err := ParseLogicalClusterName(workspace.ClusterName)
		if err != nil {
			return "", err
		}
		orgName = name
	}
	return EncodeOrganizationAndWorkspace(orgName, workspace.Name), nil
}

// EncodeOrganizationAndWorkspace determines the logical cluster name for
// an organization and workspace.
func EncodeOrganizationAndWorkspace(organization, workspace string) string {
	return organization + separator + workspace
}

// WorkspaceKey returns a key to use when looking up a ClusterWorkspace in a lister or indexer.
// If org is the value of OrganizationCluster, the key will be of the format
// <OrganizationCluster>#$#<ws>. Otherwise, the key will be of the format
// <OrganizationClsuter>_<org>#$#<ws>.
func WorkspaceKey(org, ws string) string {
	if org == OrganizationCluster {
		return clusters.ToClusterAwareKey(org, ws)
	}

	return clusters.ToClusterAwareKey(EncodeOrganizationAndWorkspace(OrganizationCluster, org), ws)
}

// ParseLogicalClusterName determines the organization and workspace name from a
// logical cluster name.
func ParseLogicalClusterName(name string) (string, string, error) {
	parts := strings.Split(name, separator)
	switch len(parts) {
	case 1:
		if name != OrganizationCluster {
			return "", "", fmt.Errorf("expected logical cluster name to be %s or in org_name format, got %s", OrganizationCluster, name)
		}
		return OrganizationCluster, name, nil
	case 2:
		return parts[0], parts[1], nil
	default:
		return "", "", fmt.Errorf("expected logical cluster name to be %s or in org_name format, got %s", OrganizationCluster, name)
	}
}
