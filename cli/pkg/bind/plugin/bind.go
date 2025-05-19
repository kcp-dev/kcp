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

	"github.com/spf13/cobra"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/rest"

	"github.com/kcp-dev/logicalcluster/v3"

	"github.com/kcp-dev/kcp/cli/pkg/base"
	pluginhelpers "github.com/kcp-dev/kcp/cli/pkg/helpers"
	apisv1alpha2 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha2"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
)

// BindOptions contains the options for creating an APIBinding.
type BindOptions struct {
	*base.Options
	// APIExportRef is the argument accepted by the command. It contains the
	// reference to where APIExport exists. For ex: <absolute_ref_to_workspace>:<apiexport>.
	APIExportRef string
	// Name of the APIBinding.
	APIBindingName string
	// BindWaitTimeout is how long to wait for the APIBinding to be created and successful.
	BindWaitTimeout time.Duration
	// AcceptedPermissionClaims is the list of accepted permission claims for the APIBinding.
	AcceptedPermissionClaims []string
	// RejectedPermissionClaims is the list of rejected permission claims for the APIBinding.
	RejectedPermissionClaims []string

	// acceptedPermissionClaims is the parsed list of accepted permission claims for the APIBinding parsed from AcceptedPermissionClaims.
	acceptedPermissionClaims []apisv1alpha2.AcceptablePermissionClaim
	// rejectedPermissionClaims is the parsed list of rejected permission claims for the APIBinding parsed from RejectedPermissionClaims.
	rejectedPermissionClaims []apisv1alpha2.AcceptablePermissionClaim
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

	cmd.Flags().StringVar(&b.APIBindingName, "name", b.APIBindingName, "Name of the APIBinding to create.")
	cmd.Flags().DurationVar(&b.BindWaitTimeout, "timeout", time.Second*30, "Duration to wait for APIBinding to be created successfully.")
	cmd.Flags().StringSliceVar(&b.AcceptedPermissionClaims, "accept-permission-claim", nil, "List of accepted permission claims for the APIBinding. Format:  --accept-permission-claim resource.group")
	cmd.Flags().StringSliceVarP(&b.RejectedPermissionClaims, "reject-permission-claim", "", nil, "List of rejected permission claims for the APIBinding. Format:  --reject-permission-claim resource.group")
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

	// We validate the path component of the APIExport. Its name component will be implicitly validated at API look-up time.
	path, _ := logicalcluster.NewPath(b.APIExportRef).Split()

	if !path.IsValid() {
		return fmt.Errorf("fully qualified reference to workspace where APIExport exists is required. The format is `<logical-cluster-name>:<apiexport>` or `<full>:<path>:<to>:<apiexport>`")
	}

	if b.AcceptedPermissionClaims != nil {
		var errs []error
		for _, claim := range b.AcceptedPermissionClaims {
			if err := b.parsePermissionClaim(claim, true); err != nil {
				errs = append(errs, err)
			}
		}
		if len(errs) > 0 {
			return fmt.Errorf("invalid accepted permission claims: %v", errs)
		}
	}
	if b.RejectedPermissionClaims != nil {
		var errs []error
		for _, claim := range b.RejectedPermissionClaims {
			if err := b.parsePermissionClaim(claim, false); err != nil {
				errs = append(errs, err)
			}
		}
		if len(errs) > 0 {
			return fmt.Errorf("invalid rejected permission claims: %v", errs)
		}
	}
	// once parsed we can validate if the dont conflict with each other
	for _, acceptedClaim := range b.acceptedPermissionClaims {
		for _, rejectedClaim := range b.rejectedPermissionClaims {
			if acceptedClaim.Group == rejectedClaim.Group && acceptedClaim.Resource == rejectedClaim.Resource {
				return fmt.Errorf("accepted permission claim %s conflicts with rejected permission claim %s", acceptedClaim, rejectedClaim)
			}
		}
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

	// if a custom name is not provided, default it to <apiExportname>.
	apiBindingName := b.APIBindingName
	if apiBindingName == "" {
		apiBindingName = apiExportName
	}

	_, currentClusterName, err := pluginhelpers.ParseClusterURL(config.Host)
	if err != nil {
		return fmt.Errorf("current URL %q does not point to workspace", config.Host)
	}

	binding := &apisv1alpha2.APIBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: apiBindingName,
		},
		Spec: apisv1alpha2.APIBindingSpec{
			Reference: apisv1alpha2.BindingReference{
				Export: &apisv1alpha2.ExportBindingReference{
					Path: path.String(),
					Name: apiExportName,
				},
			},
		},
	}

	if len(b.acceptedPermissionClaims) > 0 {
		binding.Spec.PermissionClaims = b.acceptedPermissionClaims
	}
	if len(b.rejectedPermissionClaims) > 0 {
		binding.Spec.PermissionClaims = append(binding.Spec.PermissionClaims, b.rejectedPermissionClaims...)
	}

	kcpclient, err := newKCPClusterClient(config)
	if err != nil {
		return err
	}

	createdBinding, err := kcpclient.Cluster(currentClusterName).ApisV1alpha2().APIBindings().Create(ctx, binding, metav1.CreateOptions{})
	if err != nil {
		return err
	}

	if _, err := fmt.Fprintf(b.Out, "apibinding %s created. Waiting to successfully bind ...\n", binding.Name); err != nil {
		return err
	}

	// wait for phase to be bound
	if createdBinding.Status.Phase != apisv1alpha2.APIBindingPhaseBound {
		if err := wait.PollUntilContextTimeout(ctx, time.Millisecond*500, b.BindWaitTimeout, true, func(ctx context.Context) (done bool, err error) {
			createdBinding, err := kcpclient.Cluster(currentClusterName).ApisV1alpha2().APIBindings().Get(ctx, binding.Name, metav1.GetOptions{})
			if err != nil {
				return false, err
			}
			if createdBinding.Status.Phase == apisv1alpha2.APIBindingPhaseBound {
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

func (b *BindOptions) parsePermissionClaim(claim string, accepted bool) error {
	claimParts := strings.SplitN(claim, ".", 2)
	if len(claimParts) != 2 {
		return fmt.Errorf("invalid permission claim %q", claim)
	}

	parsedClaim := apisv1alpha2.AcceptablePermissionClaim{}
	resource := claimParts[0]
	group := claimParts[1]
	if group == "core" {
		group = ""
	}

	parsedClaim.Group = group
	parsedClaim.Resource = resource
	if accepted {
		parsedClaim.State = apisv1alpha2.ClaimAccepted
	} else {
		parsedClaim.State = apisv1alpha2.ClaimRejected
	}
	// TODO(mjudeikis): Once we add support for selectors/
	parsedClaim.All = true

	if accepted {
		b.acceptedPermissionClaims = append(b.acceptedPermissionClaims, parsedClaim)
	} else {
		b.rejectedPermissionClaims = append(b.rejectedPermissionClaims, parsedClaim)
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
