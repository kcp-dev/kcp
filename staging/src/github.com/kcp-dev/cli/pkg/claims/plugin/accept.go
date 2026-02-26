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
	"strings"

	"github.com/spf13/cobra"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/kcp-dev/cli/pkg/base"
	pluginhelpers "github.com/kcp-dev/cli/pkg/helpers"
	apishelpers "github.com/kcp-dev/cli/pkg/helpers/apis/apis"
	"github.com/kcp-dev/sdk/apis/apis"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
)

// AcceptClaimOptions contains the options for accepting a permission claim.
type AcceptClaimOptions struct {
	*base.Options

	// APIBindingName is the name of the APIBinding.
	APIBindingName string

	// GroupResource is the group.resource of the claim to accept.
	GroupResource string

	// IdentityHash is the optional identity hash for the claim.
	IdentityHash string

	// All accepts all pending claims for the APIBinding.
	All bool
}

func NewAcceptClaimOptions(streams genericclioptions.IOStreams) *AcceptClaimOptions {
	return &AcceptClaimOptions{
		Options: base.NewOptions(streams),
	}
}

func (a *AcceptClaimOptions) Complete(args []string) error {
	if err := a.Options.Complete(); err != nil {
		return err
	}

	if len(args) > 0 {
		a.APIBindingName = args[0]
	}
	if len(args) > 1 {
		a.GroupResource = args[1]
	}
	return nil
}

func (a *AcceptClaimOptions) Validate() error {
	if a.APIBindingName == "" {
		return fmt.Errorf("apibinding name is required")
	}
	if !a.All && a.GroupResource == "" {
		return fmt.Errorf("group.resource is required (or use --all to accept all claims)")
	}
	return a.Options.Validate()
}

func (a *AcceptClaimOptions) BindFlags(cmd *cobra.Command) {
	a.Options.BindFlags(cmd)
	cmd.Flags().StringVar(&a.IdentityHash, "identity-hash", "", "Identity hash of the claim (optional, used to disambiguate claims with the same group.resource)")
	cmd.Flags().BoolVar(&a.All, "all", false, "Accept all pending permission claims for the APIBinding")
}

func (a *AcceptClaimOptions) Run(ctx context.Context) error {
	cfg, err := a.ClientConfig.ClientConfig()
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

	kcpClusterClient, err := newAcceptKCPClusterClient(a.ClientConfig)
	if err != nil {
		return fmt.Errorf("error while creating kcp client: %w", err)
	}

	client := kcpClusterClient.Cluster(currentClusterName)

	binding, err := apishelpers.GetAPIBinding(ctx, client, preferredAPIBindingVersion, a.APIBindingName)
	if err != nil {
		return fmt.Errorf("error finding APIBinding %q: %w", a.APIBindingName, err)
	}

	exportClaims := binding.GetExportPermissionClaims()
	currentClaims := binding.GetPermissionClaims()

	if a.All {
		// Accept all claims from the export
		newClaims := acceptAllClaims(exportClaims, currentClaims)
		if err := binding.SetPermissionClaims(newClaims); err != nil {
			return fmt.Errorf("error setting permission claims: %w", err)
		}

		if err := binding.Update(ctx, client); err != nil {
			return fmt.Errorf("error updating APIBinding: %w", err)
		}

		fmt.Fprintf(a.Out, "Accepted all permission claims for APIBinding %q\n", a.APIBindingName)
		return nil
	}

	// Parse the group.resource
	gr := parseGroupResource(a.GroupResource)

	// Find and accept the specific claim
	newClaims, found := acceptClaim(exportClaims, currentClaims, gr, a.IdentityHash)
	if !found {
		return fmt.Errorf("claim for %q not found in APIExport's permission claims", a.GroupResource)
	}

	if err := binding.SetPermissionClaims(newClaims); err != nil {
		return fmt.Errorf("error setting permission claims: %w", err)
	}

	if err := binding.Update(ctx, client); err != nil {
		return fmt.Errorf("error updating APIBinding: %w", err)
	}

	fmt.Fprintf(a.Out, "Accepted permission claim %q for APIBinding %q\n", a.GroupResource, a.APIBindingName)
	return nil
}

func newAcceptKCPClusterClient(clientConfig clientcmd.ClientConfig) (kcpclientset.ClusterInterface, error) {
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

func parseGroupResource(gr string) schema.GroupResource {
	parts := strings.SplitN(gr, ".", 2)
	if len(parts) == 1 {
		// No group specified, assume core group
		return schema.GroupResource{Resource: parts[0]}
	}
	return schema.GroupResource{Resource: parts[0], Group: parts[1]}
}

func acceptAllClaims(exportClaims []apisv1alpha2.PermissionClaim, currentClaims []apisv1alpha2.AcceptablePermissionClaim) []apisv1alpha2.AcceptablePermissionClaim {
	// Build a map of current claims by key
	currentMap := make(map[string]apisv1alpha2.AcceptablePermissionClaim)
	for _, claim := range currentClaims {
		key := claimKey(claim.Group, claim.Resource, claim.IdentityHash)
		currentMap[key] = claim
	}

	// Accept all export claims
	newClaims := make([]apisv1alpha2.AcceptablePermissionClaim, 0, len(exportClaims))
	for _, exportClaim := range exportClaims {
		key := claimKey(exportClaim.Group, exportClaim.Resource, exportClaim.IdentityHash)
		if existing, ok := currentMap[key]; ok {
			// Update state to Accepted if not already
			existing.State = apisv1alpha2.ClaimAccepted
			newClaims = append(newClaims, existing)
		} else {
			// Create new accepted claim
			newClaims = append(newClaims, apisv1alpha2.AcceptablePermissionClaim{
				ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
					PermissionClaim: exportClaim,
					Selector: apisv1alpha2.PermissionClaimSelector{
						MatchAll: true,
					},
				},
				State: apisv1alpha2.ClaimAccepted,
			})
		}
	}

	return newClaims
}

func acceptClaim(exportClaims []apisv1alpha2.PermissionClaim, currentClaims []apisv1alpha2.AcceptablePermissionClaim, gr schema.GroupResource, identityHash string) ([]apisv1alpha2.AcceptablePermissionClaim, bool) {
	// Find the export claim
	var targetClaim *apisv1alpha2.PermissionClaim
	for i := range exportClaims {
		claim := &exportClaims[i]
		if claim.Resource == gr.Resource && claim.Group == gr.Group {
			if identityHash == "" || claim.IdentityHash == identityHash {
				targetClaim = claim
				break
			}
		}
	}

	if targetClaim == nil {
		return nil, false
	}

	// Build new claims list
	key := claimKey(targetClaim.Group, targetClaim.Resource, targetClaim.IdentityHash)
	found := false
	newClaims := make([]apisv1alpha2.AcceptablePermissionClaim, 0, len(currentClaims)+1)

	for _, claim := range currentClaims {
		claimK := claimKey(claim.Group, claim.Resource, claim.IdentityHash)
		if claimK == key {
			// Update existing claim to Accepted
			claim.State = apisv1alpha2.ClaimAccepted
			found = true
		}
		newClaims = append(newClaims, claim)
	}

	if !found {
		// Add new accepted claim
		newClaims = append(newClaims, apisv1alpha2.AcceptablePermissionClaim{
			ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
				PermissionClaim: *targetClaim,
				Selector: apisv1alpha2.PermissionClaimSelector{
					MatchAll: true,
				},
			},
			State: apisv1alpha2.ClaimAccepted,
		})
	}

	return newClaims, true
}

func claimKey(group, resource, identityHash string) string {
	return fmt.Sprintf("%s.%s.%s", resource, group, identityHash)
}
