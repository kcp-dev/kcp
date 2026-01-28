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
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/kcp-dev/cli/pkg/base"
	pluginhelpers "github.com/kcp-dev/cli/pkg/helpers"
	apishelpers "github.com/kcp-dev/cli/pkg/helpers/apis/apis"
	"github.com/kcp-dev/sdk/apis/apis"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
)

// RejectClaimOptions contains the options for rejecting a permission claim.
type RejectClaimOptions struct {
	*base.Options

	// APIBindingName is the name of the APIBinding.
	APIBindingName string

	// GroupResource is the group.resource of the claim to reject.
	GroupResource string

	// IdentityHash is the optional identity hash for the claim.
	IdentityHash string

	// All rejects all pending claims for the APIBinding.
	All bool
}

func NewRejectClaimOptions(streams genericclioptions.IOStreams) *RejectClaimOptions {
	return &RejectClaimOptions{
		Options: base.NewOptions(streams),
	}
}

func (r *RejectClaimOptions) Complete(args []string) error {
	if err := r.Options.Complete(); err != nil {
		return err
	}

	if len(args) > 0 {
		r.APIBindingName = args[0]
	}
	if len(args) > 1 {
		r.GroupResource = args[1]
	}
	return nil
}

func (r *RejectClaimOptions) Validate() error {
	if r.APIBindingName == "" {
		return fmt.Errorf("apibinding name is required")
	}
	if !r.All && r.GroupResource == "" {
		return fmt.Errorf("group.resource is required (or use --all to reject all claims)")
	}
	return r.Options.Validate()
}

func (r *RejectClaimOptions) BindFlags(cmd *cobra.Command) {
	r.Options.BindFlags(cmd)
	cmd.Flags().StringVar(&r.IdentityHash, "identity-hash", "", "Identity hash of the claim (optional, used to disambiguate claims with the same group.resource)")
	cmd.Flags().BoolVar(&r.All, "all", false, "Reject all pending permission claims for the APIBinding")
}

func (r *RejectClaimOptions) Run(ctx context.Context) error {
	cfg, err := r.ClientConfig.ClientConfig()
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

	kcpClusterClient, err := newRejectKCPClusterClient(r.ClientConfig)
	if err != nil {
		return fmt.Errorf("error while creating kcp client: %w", err)
	}

	client := kcpClusterClient.Cluster(currentClusterName)

	binding, err := apishelpers.GetAPIBinding(ctx, client, preferredAPIBindingVersion, r.APIBindingName)
	if err != nil {
		return fmt.Errorf("error finding APIBinding %q: %w", r.APIBindingName, err)
	}

	exportClaims := binding.GetExportPermissionClaims()
	currentClaims := binding.GetPermissionClaims()

	if r.All {
		// Reject all claims from the export
		newClaims := rejectAllClaims(exportClaims, currentClaims)
		if err := binding.SetPermissionClaims(newClaims); err != nil {
			return fmt.Errorf("error setting permission claims: %w", err)
		}

		if err := binding.Update(ctx, client); err != nil {
			return fmt.Errorf("error updating APIBinding: %w", err)
		}

		fmt.Fprintf(r.Out, "Rejected all permission claims for APIBinding %q\n", r.APIBindingName)
		return nil
	}

	// Parse the group.resource
	gr := parseGroupResource(r.GroupResource)

	// Find and reject the specific claim
	newClaims, found := rejectClaim(exportClaims, currentClaims, gr, r.IdentityHash)
	if !found {
		return fmt.Errorf("claim for %q not found in APIExport's permission claims", r.GroupResource)
	}

	if err := binding.SetPermissionClaims(newClaims); err != nil {
		return fmt.Errorf("error setting permission claims: %w", err)
	}

	if err := binding.Update(ctx, client); err != nil {
		return fmt.Errorf("error updating APIBinding: %w", err)
	}

	fmt.Fprintf(r.Out, "Rejected permission claim %q for APIBinding %q\n", r.GroupResource, r.APIBindingName)
	return nil
}

func newRejectKCPClusterClient(clientConfig clientcmd.ClientConfig) (kcpclientset.ClusterInterface, error) {
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

func rejectAllClaims(exportClaims []apisv1alpha2.PermissionClaim, currentClaims []apisv1alpha2.AcceptablePermissionClaim) []apisv1alpha2.AcceptablePermissionClaim {
	// Build a map of current claims by key
	currentMap := make(map[string]apisv1alpha2.AcceptablePermissionClaim)
	for _, claim := range currentClaims {
		key := claimKey(claim.Group, claim.Resource, claim.IdentityHash)
		currentMap[key] = claim
	}

	// Reject all export claims
	newClaims := make([]apisv1alpha2.AcceptablePermissionClaim, 0, len(exportClaims))
	for _, exportClaim := range exportClaims {
		key := claimKey(exportClaim.Group, exportClaim.Resource, exportClaim.IdentityHash)
		if existing, ok := currentMap[key]; ok {
			// Update state to Rejected if not already
			existing.State = apisv1alpha2.ClaimRejected
			newClaims = append(newClaims, existing)
		} else {
			// Create new rejected claim
			newClaims = append(newClaims, apisv1alpha2.AcceptablePermissionClaim{
				ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
					PermissionClaim: exportClaim,
					Selector: apisv1alpha2.PermissionClaimSelector{
						MatchAll: true,
					},
				},
				State: apisv1alpha2.ClaimRejected,
			})
		}
	}

	return newClaims
}

func rejectClaim(exportClaims []apisv1alpha2.PermissionClaim, currentClaims []apisv1alpha2.AcceptablePermissionClaim, gr schema.GroupResource, identityHash string) ([]apisv1alpha2.AcceptablePermissionClaim, bool) {
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
			// Update existing claim to Rejected
			claim.State = apisv1alpha2.ClaimRejected
			found = true
		}
		newClaims = append(newClaims, claim)
	}

	if !found {
		// Add new rejected claim
		newClaims = append(newClaims, apisv1alpha2.AcceptablePermissionClaim{
			ScopedPermissionClaim: apisv1alpha2.ScopedPermissionClaim{
				PermissionClaim: *targetClaim,
				Selector: apisv1alpha2.PermissionClaimSelector{
					MatchAll: true,
				},
			},
			State: apisv1alpha2.ClaimRejected,
		})
	}

	return newClaims, true
}
