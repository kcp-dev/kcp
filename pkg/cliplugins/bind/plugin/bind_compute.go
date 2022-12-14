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
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/martinlindhe/base36"
	"github.com/spf13/cobra"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	"github.com/kcp-dev/kcp/pkg/cliplugins/base"
)

type BindComputeOptions struct {
	*base.Options

	// PlacementName is the name of the placement
	PlacementName string

	// APIExports is a list of APIExport to use in the workspace.
	APIExports []string

	// Namespace selector is a label selector to select namespace for the workload.
	namespaceSelector       *metav1.LabelSelector
	NamespaceSelectorString string

	// LocationSelectors is a list of label selectors to select locations in the location workspace.
	locationSelectors        []metav1.LabelSelector
	LocationSelectorsStrings []string

	// LocationWorkspace is the workspace for synctarget
	LocationWorkspace logicalcluster.Path

	// BindWaitTimeout is how long to wait for the placement to be created and successful.
	BindWaitTimeout time.Duration
}

func NewBindComputeOptions(streams genericclioptions.IOStreams) *BindComputeOptions {
	return &BindComputeOptions{
		Options:                 base.NewOptions(streams),
		NamespaceSelectorString: labels.Everything().String(),
		LocationSelectorsStrings: []string{
			labels.Everything().String(),
		},
		APIExports: []string{
			"root:compute:kubernetes",
		},
	}
}

// BindFlags binds fields SyncOptions as command line flags to cmd's flagset.
func (o *BindComputeOptions) BindFlags(cmd *cobra.Command) {
	o.Options.BindFlags(cmd)

	cmd.Flags().StringSliceVar(&o.APIExports, "apiexports", o.APIExports,
		"APIExport to bind to this workspace for workload, each APIExport should be in the format of <absolute_ref_to_workspace>:<apiexport>")
	cmd.Flags().StringVar(&o.NamespaceSelectorString, "namespace-selector", o.NamespaceSelectorString, "Label select to select namespaces to create workload.")
	cmd.Flags().StringSliceVar(&o.LocationSelectorsStrings, "location-selectors", o.LocationSelectorsStrings,
		"A list of label selectors to select locations in the location workspace to sync workload.")
	cmd.Flags().StringVar(&o.PlacementName, "name", o.PlacementName, "Path of the placement to be created.")
	cmd.Flags().DurationVar(&o.BindWaitTimeout, "timeout", time.Second*30, "Duration to wait for Placement to be created and bound successfully.")
}

// Complete ensures all dynamically populated fields are initialized.
func (o *BindComputeOptions) Complete(args []string) error {
	if err := o.Options.Complete(); err != nil {
		return err
	}

	if len(args) != 1 {
		return fmt.Errorf("a location workspace should be specified")
	}
	clusterName, validated := logicalcluster.NewValidatedPath(args[0])
	if !validated {
		return fmt.Errorf("location workspace type is incorrect")
	}
	o.LocationWorkspace = clusterName

	var err error
	if o.namespaceSelector, err = metav1.ParseToLabelSelector(o.NamespaceSelectorString); err != nil {
		return fmt.Errorf("namespace selector format not correct: %w", err)
	}

	for _, locSelector := range o.LocationSelectorsStrings {
		selector, err := metav1.ParseToLabelSelector(locSelector)
		if err != nil {
			return fmt.Errorf("location selector %s format not correct: %w", locSelector, err)
		}
		o.locationSelectors = append(o.locationSelectors, *selector)
	}

	if len(o.PlacementName) == 0 {
		// placement name is a hash of location selectors and ns selector, with location workspace name as the prefix
		hash := sha256.Sum224([]byte(o.NamespaceSelectorString + strings.Join(o.LocationSelectorsStrings, ",") + o.LocationWorkspace.String()))
		base36hash := strings.ToLower(base36.EncodeBytes(hash[:]))
		o.PlacementName = fmt.Sprintf("placement-%s", base36hash[:8])
	}

	return nil
}

// Validate validates the BindOptions are complete and usable.
func (o *BindComputeOptions) Validate() error {
	return nil
}

// Run creates a placement in the workspace, linking to the location workspace
func (o *BindComputeOptions) Run(ctx context.Context) error {
	config, err := o.ClientConfig.ClientConfig()
	if err != nil {
		return err
	}
	userWorkspaceKcpClient, err := kcpclient.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("failed to create kcp client: %w", err)
	}

	bindings, err := o.applyAPIBinding(ctx, userWorkspaceKcpClient, sets.NewString(o.APIExports...))
	if err != nil {
		return err
	}

	placement, err := o.applyPlacement(ctx, userWorkspaceKcpClient)
	if err != nil {
		return err
	}

	// wait for bind to be ready
	if ready, message := bindReady(bindings, placement); !ready {
		if err := wait.PollImmediate(time.Millisecond*500, o.BindWaitTimeout, func() (done bool, err error) {
			currentPlacement, err := userWorkspaceKcpClient.SchedulingV1alpha1().Placements().Get(ctx, placement.Name, metav1.GetOptions{})
			if err != nil {
				return false, err
			}
			var currentBindings []*apisv1alpha1.APIBinding
			for _, binding := range bindings {
				currentBinding, err := userWorkspaceKcpClient.ApisV1alpha1().APIBindings().Get(ctx, binding.Name, metav1.GetOptions{})
				if err != nil {
					return false, err
				}
				currentBindings = append(currentBindings, currentBinding)
			}

			ready, message = bindReady(currentBindings, currentPlacement)
			return ready, nil
		}); err != nil {
			if err.Error() == wait.ErrWaitTimeout.Error() {
				return fmt.Errorf("bind compute is not ready %s: %s", placement.Name, message)
			}
			return fmt.Errorf("bind compute is not ready %s: %w", placement.Name, err)
		}
	}

	return nil
}

func bindReady(bindings []*apisv1alpha1.APIBinding, placement *schedulingv1alpha1.Placement) (bool, string) {
	if !conditions.IsTrue(placement, schedulingv1alpha1.PlacementReady) {
		return false, fmt.Sprintf("placement is not ready: %s", conditions.GetMessage(placement, schedulingv1alpha1.PlacementReady))
	}
	if !conditions.IsTrue(placement, schedulingv1alpha1.PlacementScheduled) {
		return false, fmt.Sprintf("placement is not scheduled: %s", conditions.GetMessage(placement, schedulingv1alpha1.PlacementScheduled))
	}

	for _, binding := range bindings {
		if binding.Status.Phase != apisv1alpha1.APIBindingPhaseBound {
			conditionMessage := "unknown reason"
			if conditions.IsFalse(binding, apisv1alpha1.InitialBindingCompleted) {
				conditionMessage = conditions.GetMessage(binding, apisv1alpha1.InitialBindingCompleted)
			} else if conditions.IsFalse(binding, apisv1alpha1.APIExportValid) {
				conditionMessage = conditions.GetMessage(binding, apisv1alpha1.APIExportValid)
			}
			return false, fmt.Sprintf("not bound to apiexport '%s': %s", logicalcluster.NewPath(binding.Spec.Reference.Export.Path).Join(binding.Spec.Reference.Export.Name), conditionMessage)
		}
	}

	return true, ""
}

const maxBindingNamePrefixLength = validation.DNS1123SubdomainMaxLength - 1 - 8

func apiBindingName(clusterName logicalcluster.Path, apiExportName string) string {
	maxLen := len(apiExportName)
	if maxLen > maxBindingNamePrefixLength {
		maxLen = maxBindingNamePrefixLength
	}
	bindingNamePrefix := apiExportName[:maxLen]

	hash := sha256.Sum224([]byte(clusterName.RequestPath()))
	base36hash := strings.ToLower(base36.EncodeBytes(hash[:]))
	return fmt.Sprintf("%s-%s", bindingNamePrefix, base36hash[:8])
}

func (o *BindComputeOptions) applyAPIBinding(ctx context.Context, client kcpclient.Interface, desiredAPIExports sets.String) ([]*apisv1alpha1.APIBinding, error) {
	apiBindings, err := client.ApisV1alpha1().APIBindings().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	existingAPIExports := sets.NewString()
	for _, binding := range apiBindings.Items {
		if binding.Spec.Reference.Export == nil {
			continue
		}
		existingAPIExports.Insert(logicalcluster.NewPath(binding.Spec.Reference.Export.Path).Join(binding.Spec.Reference.Export.Name).String())
	}

	var errs []error
	diff := desiredAPIExports.Difference(existingAPIExports)
	var bindings []*apisv1alpha1.APIBinding
	for export := range diff {
		path, name := logicalcluster.NewPath(export).Split()
		apiBinding := &apisv1alpha1.APIBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: apiBindingName(path, name),
			},
			Spec: apisv1alpha1.APIBindingSpec{
				Reference: apisv1alpha1.BindingReference{
					Export: &apisv1alpha1.ExportBindingReference{
						Path: path.String(),
						Name: name,
					},
				},
			},
		}
		binding, err := client.ApisV1alpha1().APIBindings().Create(ctx, apiBinding, metav1.CreateOptions{})
		if err != nil && !errors.IsAlreadyExists(err) {
			errs = append(errs, err)
		}

		bindings = append(bindings, binding)

		_, err = fmt.Fprintf(o.Out, "apibinding %s for apiexport %s created.\n", apiBinding.Name, export)
		if err != nil {
			errs = append(errs, err)
		}
	}

	return bindings, utilerrors.NewAggregate(errs)
}

func (o *BindComputeOptions) applyPlacement(ctx context.Context, client kcpclient.Interface) (*schedulingv1alpha1.Placement, error) {
	placement := &schedulingv1alpha1.Placement{
		ObjectMeta: metav1.ObjectMeta{
			Name: o.PlacementName,
		},
		Spec: schedulingv1alpha1.PlacementSpec{
			NamespaceSelector: o.namespaceSelector,
			LocationSelectors: o.locationSelectors,
			LocationWorkspace: o.LocationWorkspace.String(),
			LocationResource: schedulingv1alpha1.GroupVersionResource{
				Group:    "workload.kcp.dev",
				Version:  "v1alpha1",
				Resource: "synctargets",
			},
		},
	}

	placement, err := client.SchedulingV1alpha1().Placements().Create(ctx, placement, metav1.CreateOptions{})
	if err != nil && !errors.IsAlreadyExists(err) {
		return nil, err
	}

	_, err = fmt.Fprintf(o.Out, "placement %s created.\n", placement.Name)
	return placement, err
}
