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
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
	"github.com/xlab/treeprint"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	"github.com/kcp-dev/cli/pkg/base"
	pluginhelpers "github.com/kcp-dev/cli/pkg/helpers"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
)

// workspaceInfo contains workspace path and type information.

type workspaceInfo struct {
	Path                    logicalcluster.Path
	Type                    *tenancyv1alpha1.WorkspaceTypeReference
	Cluster                 string
	APIExports              []apisv1alpha2.APIExport
	APIExportEndpointSlices []apisv1alpha1.APIExportEndpointSlice
	APIBindings             []apisv1alpha2.APIBinding
}

// TreeOptions contains options for displaying the workspace tree.
type TreeOptions struct {
	*base.Options

	Full        bool
	Interactive bool

	kcpClusterClient kcpclientset.ClusterInterface
}

// NewTreeOptions returns a new TreeOptions.
func NewTreeOptions(streams genericclioptions.IOStreams) *TreeOptions {
	return &TreeOptions{
		Options: base.NewOptions(streams),
	}
}

// BindFlags binds fields to cmd's flagset.
func (o *TreeOptions) BindFlags(cmd *cobra.Command) {
	o.Options.BindFlags(cmd)
	cmd.Flags().BoolVarP(&o.Full, "full", "f", o.Full, "Show full workspace names")
	cmd.Flags().BoolVarP(&o.Interactive, "interactive", "i", o.Interactive, "Interactive workspace tree browser")
}

// Complete ensures all dynamically populated fields are initialized.
func (o *TreeOptions) Complete() error {
	if err := o.Options.Complete(); err != nil {
		return err
	}

	kcpClusterClient, err := newKCPClusterClient(o.ClientConfig)
	if err != nil {
		return err
	}
	o.kcpClusterClient = kcpClusterClient

	return nil
}

// Run outputs the current workspace.
func (o *TreeOptions) Run(ctx context.Context) error {
	config, err := o.ClientConfig.ClientConfig()
	if err != nil {
		return err
	}
	_, current, err := pluginhelpers.ParseClusterURL(config.Host)
	if err != nil {
		return fmt.Errorf("current config context URL %q does not point to workspace", config.Host)
	}

	if o.Interactive {
		return o.runInteractive(ctx, current)
	}

	tree := treeprint.New()
	// NOTE(hasheddan): the cluster URL can be used for only the tree root as
	// the friendly name is used in kubeconfig.
	name := current.String()
	if !o.Full {
		name = name[strings.LastIndex(name, ":")+1:]
	}
	branch := tree.AddBranch(name)
	if err := o.populateBranch(ctx, branch, current, name); err != nil {
		return err
	}

	fmt.Println(tree.String())
	return nil
}

// runInteractive starts the interactive workspace tree browser.
func (o *TreeOptions) runInteractive(ctx context.Context, currentWorkspace logicalcluster.Path) error {
	rootWorkspace := currentWorkspace
	for {
		parent, hasParent := rootWorkspace.Parent()
		if !hasParent {
			break
		}
		rootWorkspace = parent
	}

	rootName := rootWorkspace.String()
	if !o.Full {
		rootName = rootName[strings.LastIndex(rootName, ":")+1:]
	}

	root := &treeNode{
		name:           rootName,
		path:           rootWorkspace,
		expanded:       true,
		selectable:     true,
		childrenLoaded: false,
		apiInfoLoaded:  false,
	}

	var currentNode *treeNode
	if err := o.populateInteractiveNodeBubble(ctx, root, rootWorkspace, rootName, currentWorkspace, &currentNode); err != nil {
		return err
	}

	if currentNode == nil {
		currentNode = root
		if currentWorkspace == rootWorkspace {
			root.selected = true
		}
	} else {
		o.expandParents(currentNode)
	}

	m := model{
		tree:              root,
		currentNode:       currentNode,
		selectedWorkspace: nil,
		treeOptions:       o,
	}

	p := tea.NewProgram(m, tea.WithAltScreen())
	finalModel, err := p.Run()
	if err != nil {
		return err
	}

	if m, ok := finalModel.(model); ok && m.selectedWorkspace != nil {
		return o.switchToWorkspace(ctx, *m.selectedWorkspace)
	}

	return nil
}

func (o *TreeOptions) populateInteractiveNodeBubble(ctx context.Context, node *treeNode, workspace logicalcluster.Path, workspaceName string, currentWorkspace logicalcluster.Path, currentNode **treeNode) error {
	var workspaceType *tenancyv1alpha1.WorkspaceTypeReference
	var workspaceCluster string
	if parent, hasParent := workspace.Parent(); hasParent {
		workspaceBaseName := workspace.Base()
		ws, err := o.kcpClusterClient.Cluster(parent).TenancyV1alpha1().Workspaces().Get(ctx, workspaceBaseName, metav1.GetOptions{})
		if err == nil {
			if ws.Spec.Type != nil {
				workspaceType = ws.Spec.Type
			}
			workspaceCluster = ws.Spec.Cluster
		}
	}
	if workspaceCluster == "" {
		workspaceCluster = workspace.Base()
	}

	wsInfo := &workspaceInfo{
		Path:    workspace,
		Type:    workspaceType,
		Cluster: workspaceCluster,
	}

	node.info = wsInfo
	node.apiInfoLoaded = false

	if workspace == currentWorkspace {
		node.selected = true
		*currentNode = node
	}

	results, err := o.kcpClusterClient.Cluster(workspace).TenancyV1alpha1().Workspaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			node.hasChildren = false
			node.childrenLoaded = true
			return nil
		}
		return err
	}

	node.hasChildren = len(results.Items) > 0
	node.childrenLoaded = false

	shouldLoadChildren := node.expanded
	if !shouldLoadChildren {
		if workspace == currentWorkspace {
			shouldLoadChildren = true
		} else if currentWorkspace.HasPrefix(workspace) {
			shouldLoadChildren = true
		}
	}

	if shouldLoadChildren {
		loadAllChildren := node.expanded

		for _, ws := range results.Items {
			_, childPath, err := pluginhelpers.ParseClusterURL(ws.Spec.URL)
			if err != nil {
				return fmt.Errorf("workspace URL %q does not point to valid workspace", ws.Spec.URL)
			}

			if !loadAllChildren {
				if !currentWorkspace.HasPrefix(childPath) && childPath != currentWorkspace {
					continue
				}
			}

			childName := ws.Name
			if o.Full {
				childName = workspaceName + ":" + childName
			}

			childWorkspaceInfo := &workspaceInfo{
				Path:    childPath,
				Type:    ws.Spec.Type,
				Cluster: ws.Spec.Cluster,
			}

			childResults, err := o.kcpClusterClient.Cluster(childPath).TenancyV1alpha1().Workspaces().List(ctx, metav1.ListOptions{Limit: childrenCheckLimit})
			hasChildren := err == nil && len(childResults.Items) > 0

			childNode := &treeNode{
				name:           childName,
				path:           childPath,
				info:           childWorkspaceInfo,
				selectable:     true,
				parent:         node,
				childrenLoaded: false,
				apiInfoLoaded:  false,
				hasChildren:    hasChildren,
			}

			node.children = append(node.children, childNode)

			if !loadAllChildren || currentWorkspace.HasPrefix(childPath) || childPath == currentWorkspace {
				if err := o.populateInteractiveNodeBubble(ctx, childNode, childPath, childName, currentWorkspace, currentNode); err != nil {
					return err
				}
			}
		}
		node.childrenLoaded = true
	}

	return nil
}

// switchToWorkspace switches to a selected workspace.
func (o *TreeOptions) switchToWorkspace(ctx context.Context, workspacePath logicalcluster.Path) error {
	useOpts := NewUseWorkspaceOptions(o.IOStreams)
	useOpts.ClientConfig = o.ClientConfig

	workspaceStr := workspacePath.String()
	if workspaceStr != "" && !strings.HasPrefix(workspaceStr, ":") {
		workspaceStr = ":" + workspaceStr
	}
	useOpts.Name = workspaceStr

	if err := useOpts.Complete([]string{workspaceStr}); err != nil {
		return fmt.Errorf("failed to complete workspace switch: %w", err)
	}

	if err := useOpts.Validate(); err != nil {
		return fmt.Errorf("failed to validate workspace switch: %w", err)
	}

	return useOpts.Run(ctx)
}

func (o *TreeOptions) populateBranch(ctx context.Context, tree treeprint.Tree, parent logicalcluster.Path, parentName string) error {
	results, err := o.kcpClusterClient.Cluster(parent).TenancyV1alpha1().Workspaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	for _, workspace := range results.Items {
		_, current, err := pluginhelpers.ParseClusterURL(workspace.Spec.URL)
		if err != nil {
			return fmt.Errorf("current config context URL %q does not point to workspace", workspace.Spec.URL)
		}
		// NOTE(hasheddan): the cluster URL from the Workspace does not use the
		// friendly name, so we use the Workspace name instead.
		name := workspace.Name
		if o.Full {
			name = parentName + ":" + name
		}
		branch := tree.AddBranch(name)
		if err := o.populateBranch(ctx, branch, current, name); err != nil {
			return err
		}
	}
	return nil
}

// expandParents expands all parent nodes from the given node up to the root.
func (o *TreeOptions) expandParents(node *treeNode) {
	current := node
	for current != nil {
		current.expanded = true
		current = current.parent
	}
}
