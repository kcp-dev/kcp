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

	"github.com/spf13/cobra"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/printers"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/kcp-dev/cli/pkg/base"
	pluginhelpers "github.com/kcp-dev/cli/pkg/helpers"
	apishelpers "github.com/kcp-dev/cli/pkg/helpers/apis/apis"
	"github.com/kcp-dev/sdk/apis/apis"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
)

// GetAPIBindingOptions contains the options for fetching claims
// and their status corresponding to a specific API Binding.
type GetAPIBindingOptions struct {
	*base.Options

	// Name of the APIbinding whose claims we need to list.
	APIBindingName string

	// If allBindings is true, then get permission claims for all
	// APIBindings in a workspace.
	allBindings bool
}

func NewGetAPIBindingOptions(streams genericclioptions.IOStreams) *GetAPIBindingOptions {
	return &GetAPIBindingOptions{
		Options: base.NewOptions(streams),
	}
}

func (g *GetAPIBindingOptions) Complete(args []string) error {
	if err := g.Options.Complete(); err != nil {
		return err
	}

	if len(args) > 0 {
		g.APIBindingName = args[0]
	}
	return nil
}

func (g *GetAPIBindingOptions) Validate() error {
	if g.APIBindingName == "" {
		g.allBindings = true
	}
	return g.Options.Validate()
}

func (g *GetAPIBindingOptions) BindFlags(cmd *cobra.Command) {
	g.Options.BindFlags(cmd)
}

func (g *GetAPIBindingOptions) Run(ctx context.Context) error {
	cfg, err := g.ClientConfig.ClientConfig()
	if err != nil {
		return err
	}

	_, currentClusterName, err := pluginhelpers.ParseClusterURL(cfg.Host)
	if err != nil {
		return fmt.Errorf("current URL %q does not point to workspace", cfg.Host)
	}

	preferredAPIBindingVersion, err := pluginhelpers.PreferredVersion(cfg, schema.GroupResource{
		Group:    apis.GroupName,
		Resource: "apibindings",
	})
	if err != nil {
		return fmt.Errorf("service discovery failed: %w", err)
	}

	kcpClusterClient, err := newKCPClusterClient(g.ClientConfig)
	if err != nil {
		return fmt.Errorf("error while creating kcp client %w", err)
	}

	out := printers.GetNewTabWriter(g.Out)
	defer out.Flush()

	// List permission claims for all bindings in current workspace.

	var bindingsList apishelpers.APIBindingList
	if g.allBindings {
		bindings, err := apishelpers.ListAPIBindings(ctx, kcpClusterClient.Cluster(currentClusterName), preferredAPIBindingVersion)
		if err != nil {
			return fmt.Errorf("error listing APIBindings in %q workspace: %w", currentClusterName, err)
		}
		bindingsList = bindings
	} else {
		binding, err := apishelpers.GetAPIBinding(ctx, kcpClusterClient.Cluster(currentClusterName), preferredAPIBindingVersion, g.APIBindingName)
		if err != nil {
			return fmt.Errorf("error finding APIBinding: %w", err)
		}
		bindingsList = apishelpers.NewAPIBindingList(binding)
	}

	return bindingsList.PrintPermissionClaims(out)
}

func newKCPClusterClient(clientConfig clientcmd.ClientConfig) (kcpclientset.ClusterInterface, error) {
	config, err := clientConfig.ClientConfig()
	if err != nil {
		return nil, err
	}
	clusterConfig := rest.CopyConfig(config)
	u, err := url.Parse(config.Host)
	if err != nil {
		return nil, err
	}
	u.Path = ""
	clusterConfig.Host = u.String()
	clusterConfig.UserAgent = rest.DefaultKubernetesUserAgent()
	return kcpclientset.NewForConfig(clusterConfig)
}
