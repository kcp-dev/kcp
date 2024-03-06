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
	"io"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/cli-runtime/pkg/printers"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/kcp-dev/kcp/cli/pkg/base"
	pluginhelpers "github.com/kcp-dev/kcp/cli/pkg/helpers"
	apiv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
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

	kcpClusterClient, err := newKCPClusterClient(g.ClientConfig)
	if err != nil {
		return fmt.Errorf("error while creating kcp client %w", err)
	}

	out := printers.GetNewTabWriter(g.Out)
	defer out.Flush()

	err = printHeaders(out)
	if err != nil {
		return fmt.Errorf("error: %w", err)
	}

	allErrors := []error{}
	apibindings := []apiv1alpha1.APIBinding{}
	// List permission claims for all bindings in current workspace.
	if g.allBindings {
		bindings, err := kcpClusterClient.Cluster(currentClusterName).ApisV1alpha1().APIBindings().List(ctx, metav1.ListOptions{})
		if err != nil {
			return fmt.Errorf("error listing apibindings in %q workspace: %w", currentClusterName, err)
		}
		apibindings = append(apibindings, bindings.Items...)
	} else {
		binding, err := kcpClusterClient.Cluster(currentClusterName).ApisV1alpha1().APIBindings().Get(ctx, g.APIBindingName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("error finding apibinding: %w", err)
		}
		apibindings = append(apibindings, *binding)
	}

	for _, b := range apibindings {
		for _, claim := range b.Spec.PermissionClaims {
			err := printDetails(out, b.Name, claim.Group+"-"+claim.Resource, string(claim.State))
			if err != nil {
				allErrors = append(allErrors, err)
			}
		}
	}

	return utilerrors.NewAggregate(allErrors)
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

func printHeaders(out io.Writer) error {
	columnNames := []string{"APIBINDING", "RESOURCE GROUP-VERSION", "STATUS"}
	_, err := fmt.Fprintf(out, "%s\n", strings.Join(columnNames, "\t"))
	return err
}

func printDetails(w io.Writer, name, binding, status string) error {
	_, err := fmt.Fprintf(w, "%s\t%s\t%s\n", name, binding, status)
	return err
}
