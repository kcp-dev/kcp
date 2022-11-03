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

	apiv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	"github.com/kcp-dev/kcp/pkg/cliplugins/base"
	pluginhelpers "github.com/kcp-dev/kcp/pkg/cliplugins/helpers"
	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/cli-runtime/pkg/genericclioptions"
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

	_, currentClusterName, err := pluginhelpers.ParseClusterURL(cfg.Host)
	if err != nil {
		return fmt.Errorf("current URL %q does not point to cluster workspace", cfg.Host)
	}

	kcpClusterClient, err := newKCPClusterClient(e.ClientConfig)
	if err != nil {
		return fmt.Errorf("error while creating kcp client %w", err)
	}

	_, err = fmt.Fprintf(e.Out, "Running interactive promt. Enter (%s/%s/%s)", "Yes", "No", "Skip")
	if err != nil {
		return err
	}

	//     The status of an apibinding has a set of `exportClaims`. The subset which is present in exportClaims but not in spec.AcceptableClaims, are the open claims.
	// The command will list those open claims and update `spec.AcceptableClaims` with the status based on the input of the user.

	apibindings := []apiv1alpha1.APIBinding{}
	// List permission claims for all bindings in current workspace.
	if e.allBindings {
		bindings, err := kcpClusterClient.Cluster(currentClusterName).ApisV1alpha1().APIBindings().List(ctx, metav1.ListOptions{})
		if err != nil {
			return fmt.Errorf("error listing apibindings in %q workspace: %w", currentClusterName, err)
		}
		apibindings = append(apibindings, bindings.Items...)
	} else {
		binding, err := kcpClusterClient.Cluster(currentClusterName).ApisV1alpha1().APIBindings().Get(ctx, e.APIBindingName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("error finding apibinding: %w", err)
		}
		apibindings = append(apibindings, *binding)
	}

	allErrors := []error{}
	for _, apibinding := range apibindings {
		openClaims := getOpenClaims(apibinding.Status.ExportPermissionClaims, apibinding.Spec.PermissionClaims)
		// The status of an apibinding has a set of `exportClaims`. The subset which is present in exportClaims but not in spec.AcceptableClaims, are the open claims.
		// The command will list those open claims and update `spec.AcceptableClaims` with the status based on the input of the user.
		for _, openClaim := range openClaims {
			// print the prompt and infer the user input.
			action := getRequiredInput(e.In, apibinding.Name, openClaim.Group, openClaim.Resource)
			err := updateAPIBinding(ctx, action, openClaim, &apibinding, currentClusterName, kcpClusterClient)
			if err != nil {
				allErrors = append(allErrors, err)
			}
		}
	}
	return utilerrors.NewAggregate(allErrors)
}

// getOpenClaims gets the claims which are in a set of `exportClaims` but not in `spec.AcceptableClaims` of the apibinding.
func getOpenClaims(exportClaims []apiv1alpha1.PermissionClaim, aceptableClaims []apiv1alpha1.AcceptablePermissionClaim) []apiv1alpha1.PermissionClaim {
	openPermissionClaims := []apiv1alpha1.PermissionClaim{}

	// Convert both the lists into a map, with IdentityHash being the key so that
	// the complexity of finding a resource which in in both export and acceptable claim is not n^2.
	exportClaimsMap := map[string]apiv1alpha1.PermissionClaim{}
	for _, e := range exportClaims {
		exportClaimsMap[e.IdentityHash] = e
	}

	acceptableClaimsMap := map[string]apiv1alpha1.PermissionClaim{}
	for _, a := range aceptableClaims {
		acceptableClaimsMap[a.IdentityHash] = a.PermissionClaim
	}

	for identity, _ := range exportClaimsMap {
		if val, ok := acceptableClaimsMap[identity]; !ok {
			openPermissionClaims = append(openPermissionClaims, val)
		}
	}
	return openPermissionClaims
}

func updateAPIBinding(ctx context.Context, action ClaimAction, permissionClaim apiv1alpha1.PermissionClaim, apibinding *apiv1alpha1.APIBinding, clusterName logicalcluster.Name, kcpclusterclient kcpclient.ClusterInterface) error {
	if action == SkipClaim {
		return nil
	}

	for _, pc := range apibinding.Spec.PermissionClaims {
		if pc.IdentityHash == permissionClaim.IdentityHash {
			if action == AcceptClaim {
				pc.State = apiv1alpha1.ClaimAccepted
			} else if action == RejectClaim {
				pc.State = apiv1alpha1.ClaimRejected
			}
		}
	}

	_, err := kcpclusterclient.Cluster(clusterName).ApisV1alpha1().APIBindings().Update(ctx, apibinding, metav1.UpdateOptions{})
	return err
}
