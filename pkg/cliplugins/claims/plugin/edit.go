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

	"github.com/spf13/cobra"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	apiv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/cliplugins/base"
	pluginhelpers "github.com/kcp-dev/kcp/pkg/cliplugins/helpers"
)

// EditAPIBindingOptions contains the options for fetching claims
// and their status corresponding to a specific API Binding.
type EditAPIBindingOptions struct {
	*base.Options

	// Name of the APIbinding whose claims we need to list.
	APIBindingName string

	// If allBindings is true, then get permission claims for all
	// APIBindings in a workspace.
	allBindings bool
}

func NewEditAPIBindingOptions(streams genericclioptions.IOStreams) *EditAPIBindingOptions {
	return &EditAPIBindingOptions{
		Options: base.NewOptions(streams),
	}
}

func (e *EditAPIBindingOptions) Complete(args []string) error {
	if err := e.Options.Complete(); err != nil {
		return err
	}

	if len(args) > 0 {
		e.APIBindingName = args[0]
	}
	return nil
}

func (e *EditAPIBindingOptions) Validate() error {
	if e.APIBindingName == "" {
		e.allBindings = true
	}
	return e.Options.Validate()
}

func (e *EditAPIBindingOptions) BindFlags(cmd *cobra.Command) {
	e.Options.BindFlags(cmd)
}

func (e *EditAPIBindingOptions) Run(ctx context.Context) error {
	cfg, err := e.ClientConfig.ClientConfig()
	if err != nil {
		return err
	}

	currentClusterName, err := pluginhelpers.GetCurrentClusterName(cfg.Host)
	if err != nil {
		return err
	}

	kcpClusterClient, err := newKCPClusterClient(e.ClientConfig)
	if err != nil {
		return fmt.Errorf("error while creating kcp client %w", err)
	}

	// The status of an APIBinding has a set of PermissionClaims declared by the bound APIExport. The spec of an APIBinding has the subset of those PermissionClaims that the user has already taken action on.
	// The remaining PermissionClaims are open to be accepted or rejected.
	// The command will list those open claims and update `spec.PermissionClaims` with the status based on the input of the user.

	apibindings := []apiv1alpha1.APIBinding{}
	// List permission claims for all bindings in current workspace.
	if e.allBindings {
		bindings, err := kcpClusterClient.Cluster(currentClusterName).ApisV1alpha1().APIBindings().List(ctx, metav1.ListOptions{})
		if err != nil {
			return fmt.Errorf("error listing apibindings in %q workspace: %w", currentClusterName, err)
		}
		apibindings = bindings.Items
	} else {
		binding, err := kcpClusterClient.Cluster(currentClusterName).ApisV1alpha1().APIBindings().Get(ctx, e.APIBindingName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("error finding apibinding: %w", err)
		}
		apibindings = append(apibindings, *binding)
	}

	if len(apibindings) == 0 {
		_, err = fmt.Fprintf(e.Out, "No apibindings available in %s.", currentClusterName)
		if err != nil {
			return err
		}
		return nil
	}

	_, err = fmt.Fprintf(e.Out, "Running interactive prompt. Enter (%s/%s/%s)\n", "Yes", "No", "Skip")
	if err != nil {
		return err
	}

	allErrors := []error{}
	for _, apibinding := range apibindings {
		openClaims := getOpenClaims(apibinding.Status.ExportPermissionClaims, apibinding.Spec.PermissionClaims)
		if len(openClaims) == 0 {
			_, err = fmt.Fprintf(e.Out, "No open claims found for apibinding: %q\n", apibinding.Name)
			if err != nil {
				allErrors = append(allErrors, err)
			}
		}

		// List open claims and update `spec.AcceptableClaims` with the status based on the input of the user.
		for _, openClaim := range openClaims {
			// print the prompt and infer the user input.
			action, err := getRequiredInput(e.In, e.Out, apibinding.Name, openClaim)
			if err != nil {
				allErrors = append(allErrors, err)
			}

			// Update apibinding based on the user input.
			updateAPIBinding(action, openClaim, &apibinding)
		}
		_, err := kcpClusterClient.Cluster(currentClusterName).ApisV1alpha1().APIBindings().Update(ctx, &apibinding, metav1.UpdateOptions{})
		if err != nil {
			allErrors = append(allErrors, err)
		}

	}
	return utilerrors.NewAggregate(allErrors)
}

// getOpenClaims gets the claims which are in a set of `exportClaims` but not in `spec.AcceptableClaims` of the apibinding.
func getOpenClaims(exportClaims []apiv1alpha1.PermissionClaim, acceptableClaims []apiv1alpha1.AcceptablePermissionClaim) []apiv1alpha1.PermissionClaim {
	openPermissionClaims := []apiv1alpha1.PermissionClaim{}

	// Find the subset which is in exportClaims but not in AcceptableClaims.
	exportClaimsMap := map[apiv1alpha1.GroupResource]apiv1alpha1.PermissionClaim{}
	for _, e := range exportClaims {
		exportClaimsMap[e.GroupResource] = e
	}

	acceptableClaimsMap := map[apiv1alpha1.GroupResource]apiv1alpha1.PermissionClaim{}
	for _, a := range acceptableClaims {
		acceptableClaimsMap[a.GroupResource] = a.PermissionClaim
	}

	for gr, cl := range exportClaimsMap {
		if _, ok := acceptableClaimsMap[gr]; !ok {
			openPermissionClaims = append(openPermissionClaims, cl)
		}
	}
	return openPermissionClaims
}

// updateAPIBinding send a request to API server to update the APIBinding with the updated status of the claim.
func updateAPIBinding(action ClaimAction, permissionClaim apiv1alpha1.PermissionClaim, apibinding *apiv1alpha1.APIBinding) {
	if action == SkipClaim {
		return
	}

	var state apiv1alpha1.AcceptablePermissionClaimState
	if action == AcceptClaim {
		state = apiv1alpha1.ClaimAccepted
	} else if action == RejectClaim {
		state = apiv1alpha1.ClaimRejected
	}

	apibinding.Spec.PermissionClaims = append(apibinding.Spec.PermissionClaims, apiv1alpha1.AcceptablePermissionClaim{PermissionClaim: permissionClaim, State: state})
}
