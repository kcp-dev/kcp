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
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/spf13/cobra"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/rest"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/pkg/cliplugins/base"
	pluginhelpers "github.com/kcp-dev/kcp/pkg/cliplugins/helpers"
)

// BindOptions contains the options for creating an APIBinding.
type BindOptions struct {
	*base.Options
	// APIExportRef is the argument accepted by the command. It contains the
	// reference to where APIExport exists. For ex: <absolute_ref_to_workspace>:<apiexport>.
	APIExportRef string
	// Path of the APIBinding.
	APIBindingName string
	// BindWaitTimeout is how long to wait for the APIBinding to be created and successful.
	BindWaitTimeout time.Duration
}

// NewBindOptions returns new BindOptions.
func NewBindOptions(streams genericclioptions.IOStreams) *BindOptions {
	return &BindOptions{
		Options: base.NewOptions(streams),
	}
}

// BindFlags binds fields to cmd's flagset.
func (b *BindOptions) BindFlags(cmd *cobra.Command) {
	b.Options.BindFlags(cmd)

	cmd.Flags().StringVar(&b.APIBindingName, "name", b.APIBindingName, "Path of the APIBinding to create.")
	cmd.Flags().DurationVar(&b.BindWaitTimeout, "timeout", time.Second*30, "Duration to wait for APIBinding to be created successfully.")
}

// Complete ensures all fields are initialized.
func (b *BindOptions) Complete(args []string) error {
	if err := b.Options.Complete(); err != nil {
		return err
	}

	if len(args) > 0 {
		b.APIExportRef = args[0]
	}
	return nil
}

// Validate validates the BindOptions are complete and usable.
func (b *BindOptions) Validate() error {
	if b.APIExportRef == "" {
		return errors.New("`root:ws:apiexport_object` reference to bind is required as an argument")
	}

	if !strings.HasPrefix(b.APIExportRef, "root") || !logicalcluster.NewPath(b.APIExportRef).IsValid() {
		return fmt.Errorf("fully qualified reference to workspace where APIExport exists is required. The format is `root:<ws>:<apiexport>`")
	}

	return b.Options.Validate()
}

// Run creates an apibinding for the user.
func (b *BindOptions) Run(ctx context.Context) error {
	config, err := b.ClientConfig.ClientConfig()
	if err != nil {
		return err
	}

	path, apiExportName := logicalcluster.NewPath(b.APIExportRef).Split()

	// if apibindingName is not provided, default it to <apiExportname>.
	apiBindingName := b.APIBindingName
	if apiBindingName == "" {
		apiBindingName = apiExportName
	}

	_, currentClusterName, err := pluginhelpers.ParseClusterURL(config.Host)
	if err != nil {
		return fmt.Errorf("current URL %q does not point to cluster workspace", config.Host)
	}

	binding := &apisv1alpha1.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: apiBindingName,
		},
		Spec: apisv1alpha1.APIBindingSpec{
			Reference: apisv1alpha1.BindingReference{
				Export: &apisv1alpha1.ExportBindingReference{
					Path: path.String(),
					Name: apiExportName,
				},
			},
		},
	}

	kcpclient, err := newKCPClusterClient(config)
	if err != nil {
		return err
	}

	createdBinding, err := kcpclient.Cluster(currentClusterName).ApisV1alpha1().APIBindings().Create(ctx, binding, metav1.CreateOptions{})
	if err != nil {
		return err
	}

	if _, err := fmt.Fprintf(b.Out, "apibinding %s created. Waiting to successfully bind ...\n", binding.Name); err != nil {
		return err
	}

	// wait for phase to be bound
	if createdBinding.Status.Phase != apisv1alpha1.APIBindingPhaseBound {
		if err := wait.PollImmediate(time.Millisecond*500, b.BindWaitTimeout, func() (done bool, err error) {
			createdBinding, err := kcpclient.Cluster(currentClusterName).ApisV1alpha1().APIBindings().Get(ctx, binding.Name, metav1.GetOptions{})
			if err != nil {
				return false, err
			}
			if createdBinding.Status.Phase == apisv1alpha1.APIBindingPhaseBound {
				return true, nil
			}
			return false, nil
		}); err != nil {
			return fmt.Errorf("could not bind %s: %w", binding.Name, err)
		}
	}

	if _, err := fmt.Fprintf(b.Out, "%s created and bound.\n", binding.Name); err != nil {
		return err
	}

	return nil
}

func newKCPClusterClient(config *rest.Config) (kcpclientset.ClusterInterface, error) {
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
