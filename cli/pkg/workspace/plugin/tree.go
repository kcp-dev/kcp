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

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/spf13/cobra"
	"github.com/xlab/treeprint"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	"github.com/kcp-dev/kcp/cli/pkg/base"
	pluginhelpers "github.com/kcp-dev/kcp/cli/pkg/helpers"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
)

// TreeOptions contains options for displaying the workspace tree.
type TreeOptions struct {
	*base.Options

	Full bool

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
