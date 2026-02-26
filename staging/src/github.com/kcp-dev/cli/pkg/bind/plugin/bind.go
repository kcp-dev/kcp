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
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/rest"

	"github.com/kcp-dev/cli/pkg/base"
	pluginhelpers "github.com/kcp-dev/cli/pkg/helpers"
	apishelpers "github.com/kcp-dev/cli/pkg/helpers/apis/apis"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/kcp-dev/sdk/apis/apis"
	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
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

	_, currentClusterName, err := pluginhelpers.ParseClusterURL(config.Host)
	if err != nil {
		return fmt.Errorf("current URL %q does not point to workspace", config.Host)
	}

	preferredAPIBindingVersion, err := pluginhelpers.PreferredVersion(config, schema.GroupResource{
		Group:    apis.GroupName,
		Resource: "apibindings",
	})
	if err != nil {
		return fmt.Errorf("service discovery failed: %w", err)
	}

	apiBinding, err := b.newAPIBinding(preferredAPIBindingVersion)
	if err != nil {
		return fmt.Errorf("failed to create APIBinding: %w", err)
	}

	kcpClusterClient, err := newKCPClusterClient(config)
	if err != nil {
		return err
	}

	if err := apiBinding.Create(ctx, kcpClusterClient.Cluster(currentClusterName)); err != nil {
		return fmt.Errorf("failed to create APIBinding: %w", err)
	}

	if _, err := fmt.Fprintf(b.Out, "apibinding %s created. Waiting to successfully bind ...\n", apiBinding.Name()); err != nil {
		return err
	}

	// wait for phase to be bound
	if !apiBinding.IsBound() {
		if err := wait.PollUntilContextTimeout(ctx, time.Millisecond*500, b.BindWaitTimeout, true, func(ctx context.Context) (bool, error) {
			if err := apiBinding.Refresh(ctx, kcpClusterClient.Cluster(currentClusterName)); err != nil {
				return false, err
			}

			return apiBinding.IsBound(), nil
		}); err != nil {
			return fmt.Errorf("could not bind %s: %w", apiBinding.Name(), err)
		}
	}

	if _, err := fmt.Fprintf(b.Out, "%s created and bound.\n", apiBinding.Name()); err != nil {
		return err
	}

	return nil
}

func (b *BindOptions) newAPIBinding(preferredAPIBindingVersion string) (apishelpers.APIBinding, error) {
	path, apiExportName := logicalcluster.NewPath(b.APIExportRef).Split()

	// if a custom name is not provided, default it to <apiExportname>.
	apiBindingName := b.APIBindingName
	if apiBindingName == "" {
		apiBindingName = apiExportName
	}

	var binding apishelpers.APIBinding

	switch preferredAPIBindingVersion {
	case "v1alpha2":
		binding = apishelpers.NewAPIBinding(&apisv1alpha2.APIBinding{
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
		})

	case "v1alpha1":
		binding = apishelpers.NewAPIBinding(&apisv1alpha1.APIBinding{
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
		})

	default:
		return nil, fmt.Errorf("%s is not supported by this plugin", preferredAPIBindingVersion)
	}

	claims := []apisv1alpha2.AcceptablePermissionClaim{}
	claims = append(claims, b.acceptedPermissionClaims...)
	claims = append(claims, b.rejectedPermissionClaims...)

	if err := binding.SetPermissionClaims(claims); err != nil {
		return nil, fmt.Errorf("invalid permission claims: %w", err)
	}

	return binding, nil
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
	parsedClaim.Selector = apisv1alpha2.PermissionClaimSelector{MatchAll: true}
	parsedClaim.Verbs = []string{"*"}

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
