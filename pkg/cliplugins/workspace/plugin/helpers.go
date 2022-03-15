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

package plugin

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	virtualcommandoptions "github.com/kcp-dev/kcp/cmd/virtual-workspaces/options"
	tenancyhelpers "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1/helper"
	tenancyv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
	tenancyclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
)

// getWorkspaceFromInternalName retrieves the workspace with this internal name in the
// user workspace directory, by requesting the `workspaces` virtual workspace.
func getWorkspaceFromInternalName(ctx context.Context, workspaceInternalName string, tenancyClient tenancyclient.Interface) (*tenancyv1beta1.Workspace, error) {
	if list, err := tenancyClient.TenancyV1beta1().Workspaces().List(ctx, metav1.ListOptions{
		FieldSelector: "metadata.name=" + workspaceInternalName,
	}); err != nil {
		return nil, err
	} else if list == nil || len(list.Items) == 0 {
		return nil, fmt.Errorf("workspace %q is not found", workspaceInternalName)
	} else if len(list.Items) > 1 {
		return nil, fmt.Errorf("several workspaces with the same internal name : %q", workspaceInternalName)
	} else {
		return &list.Items[0], nil
	}
}

// getWorkspaceAndBasePath gets the workspace name, org logical cluster name and the base URL for the current
// workspace.
func getWorkspaceAndBasePath(urlPath string) (orgClusterName, workspaceName, basePath string, err error) {
	// get workspace from current server URL and check it point to an org or the root workspace
	serverURL, err := url.Parse(urlPath)
	if err != nil {
		return "", "", "", err
	}

	possiblePrefixes := []string{
		"/clusters/",
		path.Join(virtualcommandoptions.DefaultRootPathPrefix, "workspaces") + "/",
	}

	var clusterName string
	for _, prefix := range possiblePrefixes {
		clusterIndex := strings.Index(serverURL.Path, prefix)
		if clusterIndex < 0 {
			continue
		}
		clusterName = strings.SplitN(serverURL.Path[clusterIndex+len(prefix):], "/", 2)[0]
		basePath = serverURL.Path[:clusterIndex]
	}

	if clusterName == "" {
		return "", "", basePath, fmt.Errorf("current cluster URL %s is not pointing to a workspace", serverURL)
	}

	var org string
	if clusterName == tenancyhelpers.RootCluster {
		orgClusterName = ""
		workspaceName = tenancyhelpers.RootCluster
	} else if org, workspaceName, err = tenancyhelpers.ParseLogicalClusterName(clusterName); err != nil {
		return "", "", "", fmt.Errorf("unable to parse cluster name %s", clusterName)
	} else if org == "system:" {
		return "", "", "", fmt.Errorf("no workspaces are accessible from %s", clusterName)
	} else if org == tenancyhelpers.RootCluster {
		orgClusterName = tenancyhelpers.RootCluster
	} else {
		orgClusterName, err = tenancyhelpers.ParentClusterName(clusterName)
		if err != nil {
			// should never happen
			return "", "", "", fmt.Errorf("unable to derive parent cluster name for %s", clusterName)
		}
	}

	return orgClusterName, workspaceName, basePath, nil
}

// upToOrg derives the org workspace cluster name to operate on,
// from a given workspace logical cluster name.
func upToOrg(orgClusterName, workspaceName string, always bool) string {

	if orgClusterName == "" && workspaceName == tenancyhelpers.RootCluster {
		return tenancyhelpers.RootCluster
	}

	if orgClusterName == tenancyhelpers.RootCluster && !always {
		return tenancyhelpers.EncodeOrganizationAndClusterWorkspace(tenancyhelpers.RootCluster, workspaceName)
	}

	return orgClusterName
}

func outputCurrentWorkspaceMessage(orgName, workspacePrettyName, workspaceName string, opts *Options) error {
	if workspaceName != "" {
		message := fmt.Sprintf("Current workspace is %q", workspacePrettyName)
		if workspaceName != workspacePrettyName {
			message = fmt.Sprintf("%s (an alias for %q)", message, workspaceName)
		}
		if orgName != "" {
			message = fmt.Sprintf("%s in organization %q", message, orgName)
		}
		err := write(opts, fmt.Sprintf("%s.\n", message))
		return err
	}
	return nil
}

func write(opts *Options, str string) error {
	_, err := opts.Out.Write([]byte(str))
	return err
}
