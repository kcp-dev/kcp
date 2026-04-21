/*
Copyright 2022 The kcp Authors.

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
	"text/tabwriter"

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
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
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
	Wide        bool

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
	cmd.Flags().BoolVar(&o.Wide, "wide", o.Wide, "Show workspace and logical cluster status for each node")
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
		// The current context URL does not follow the /clusters/ pattern — the
		// user may be pointing at a mount, a rootless cluster, or a workspace
		// they do not have access to. Do not assume root exists or is accessible.
		// Intentionally returning nil: a non-kcp URL is not an error for the tree
		// command; we surface an informational message and exit cleanly.
		fmt.Fprintf(o.Out, "current context URL %q does not point directly to a kcp workspace\n", config.Host)
		return nil //nolint:nilerr
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
	label := name
	if o.Wide {
		label = o.formatWideName(ctx, name, current, "")
	}
	branch := tree.AddBranch(label)
	if err := o.populateBranch(ctx, branch, current, name); err != nil {
		return err
	}

	if o.Wide {
		w := tabwriter.NewWriter(o.Out, 0, 0, 2, ' ', 0)
		fmt.Fprint(w, tree.String())
		return w.Flush()
	}
	fmt.Println(tree.String())
	return nil
}

// formatWideName appends workspace and logical cluster status to the name.
// workspacePhase is the Phase read from the parent's Workspace list ("" when unknown, e.g. for the root).
func (o *TreeOptions) formatWideName(ctx context.Context, name string, path logicalcluster.Path, workspacePhase corev1alpha1.LogicalClusterPhaseType) string {
	lcPhase := corev1alpha1.LogicalClusterPhaseType("Unknown")
	lc, err := o.kcpClusterClient.Cluster(path).CoreV1alpha1().LogicalClusters().Get(ctx, corev1alpha1.LogicalClusterName, metav1.GetOptions{})
	if err == nil && lc.Status.Phase != "" {
		lcPhase = lc.Status.Phase
	}

	wsPart := "-"
	if workspacePhase != "" {
		wsPart = string(workspacePhase)
	}
	return fmt.Sprintf("%s\tws=%s\tlc=%s", name, wsPart, lcPhase)
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

	shouldLoadChildren := node.expanded || currentWorkspace.HasPrefix(workspace)

	if shouldLoadChildren {
		for _, ws := range results.Items {
			// Skip workspaces that are being deleted, as they may no longer be
			// accessible and listing their children could result in a 403.
			if ws.DeletionTimestamp != nil {
				continue
			}
			_, childPath, err := pluginhelpers.ParseClusterURL(ws.Spec.URL)
			if err != nil {
				if ws.Spec.Mount != nil {
					// Mounted workspaces use a provider-specific URL that does not
					// follow the /clusters/ pattern. Represent them as non-selectable
					// leaf nodes; their content is served by the mount provider.
					childName := ws.Name
					if o.Full {
						childName = workspaceName + ":" + childName
					}
					// Use the logical parent path + workspace name as an approximation.
					mountPath := workspace.Join(ws.Name)
					childWorkspaceInfo := &workspaceInfo{
						Path:    mountPath,
						Type:    ws.Spec.Type,
						Cluster: ws.Spec.Cluster,
					}
					childNode := &treeNode{
						name:           childName + " [m]",
						path:           mountPath,
						info:           childWorkspaceInfo,
						selectable:     false,
						parent:         node,
						childrenLoaded: true,
						apiInfoLoaded:  false,
						hasChildren:    false,
					}
					node.children = append(node.children, childNode)
					node.hasChildren = true
					continue
				}
				return fmt.Errorf("workspace URL %q does not point to valid workspace", ws.Spec.URL)
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

			if currentWorkspace.HasPrefix(childPath) {
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
		if workspace.DeletionTimestamp != nil {
			continue
		}
		_, current, err := pluginhelpers.ParseClusterURL(workspace.Spec.URL)
		if err != nil {
			if workspace.Spec.Mount != nil {
				// Mounted workspaces use a provider-specific URL that does not follow
				// the /clusters/ pattern. Add them as leaf nodes since their children
				// (if any) are served by the mount provider, not by the kcp API.
				name := workspace.Name
				if o.Full {
					name = parentName + ":" + name
				}
				tree.AddBranch(name + " [m]")
				continue
			}
			return fmt.Errorf("current config context URL %q does not point to workspace", workspace.Spec.URL)
		}
		// NOTE(hasheddan): the cluster URL from the Workspace does not use the
		// friendly name, so we use the Workspace name instead.
		name := workspace.Name
		if o.Full {
			name = parentName + ":" + name
		}
		label := name
		if o.Wide {
			label = o.formatWideName(ctx, name, current, workspace.Status.Phase)
		}
		branch := tree.AddBranch(label)
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
