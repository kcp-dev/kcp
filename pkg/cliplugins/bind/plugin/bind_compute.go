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
	"path"
	"strings"

	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/martinlindhe/base36"
	"github.com/spf13/cobra"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/rest"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	"github.com/kcp-dev/kcp/pkg/cliplugins/base"
	"github.com/kcp-dev/kcp/pkg/cliplugins/helpers"
)

type BindWorkloadOptions struct {
	*base.Options

	// APIExports is a list of APIExport to use in the workspace.
	APIExports []string

	// Namespace selector is a label selector to select namespace for the workload.
	NamespaceSelector string

	// LocationSelectors is a list of label selectors to select locations in the compute workspace.
	LocationSelectors []string

	// ComputeWorkspace is the workspace for synctarget
	ComputeWorkspace logicalcluster.Name
}

func NewBindWorkloadOptions(streams genericclioptions.IOStreams) *BindWorkloadOptions {
	return &BindWorkloadOptions{
		Options: base.NewOptions(streams),
	}
}

// BindFlags binds fields SyncOptions as command line flags to cmd's flagset.
func (o *BindWorkloadOptions) BindFlags(cmd *cobra.Command) {
	o.Options.BindFlags(cmd)

	cmd.Flags().StringSliceVar(&o.APIExports, "apiexports", o.APIExports,
		"APIExport to bind to this workspace for workload, each APIExoport should be in the format of <absolute_ref_to_workspace>:<apiexport>")
	cmd.Flags().StringVar(&o.NamespaceSelector, "namespace-selector", o.NamespaceSelector, "Label select to select namespaces to create workload.")
	cmd.Flags().StringSliceVar(&o.LocationSelectors, "location-selectors", o.LocationSelectors,
		"A list of label selectors to select locations in the compute workspace to sync workload.")
}

// Complete ensures all dynamically populated fields are initialized.
func (o *BindWorkloadOptions) Complete(args []string) error {
	if err := o.Options.Complete(); err != nil {
		return err
	}

	if len(args[0]) == 0 {
		// if workspace is not set, use the current workspace
		config, err := o.ClientConfig.ClientConfig()
		if err != nil {
			return err
		}
		_, clusterName, err := helpers.ParseClusterURL(config.Host)
		if err != nil {
			return err
		}
		o.ComputeWorkspace = clusterName
	} else if len(args[0]) == 1 {
		o.ComputeWorkspace = logicalcluster.New(args[0])
	} else {
		return fmt.Errorf("a compute workspace should be specified")
	}

	// if APIExport is not set use global kubernetes APIExpor and kubernetes APIExport in compute workspace
	if len(o.APIExports) == 0 {
		o.APIExports = []string{
			"root:compute:kubernetes",
			fmt.Sprintf("%s:kubernetes", o.ComputeWorkspace.String()),
		}
	}

	// select all ns if namespace selector is not set
	if len(o.NamespaceSelector) == 0 {
		o.NamespaceSelector = labels.Everything().String()
	}

	// select all locations is location selectos is not set
	if len(o.LocationSelectors) == 0 {
		o.LocationSelectors = []string{labels.Everything().String()}
	}

	return nil
}

// Validate validates the BindOptions are complete and usable.
func (o *BindWorkloadOptions) Validate() error {
	return nil
}

// Run create a placement in the workspace linkind to the compute workspace
func (o *BindWorkloadOptions) Run(ctx context.Context) error {
	config, err := o.ClientConfig.ClientConfig()
	if err != nil {
		return err
	}
	userWorkspaceKcpClient, err := kcpclient.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("failed to create kcp client: %w", err)
	}

	// build config to connect to compute workspace
	computeWorkspaceConfig := rest.CopyConfig(config)
	url, _, err := helpers.ParseClusterURL(config.Host)
	if err != nil {
		return err
	}

	url.Path = path.Join(url.Path, o.ComputeWorkspace.Path())
	computeWorkspaceConfig.Host = url.String()
	computeWorkspaceKcpClient, err := kcpclient.NewForConfig(computeWorkspaceConfig)
	if err != nil {
		return fmt.Errorf("failed to create kcp client: %w", err)
	}

	err = o.hasSupportedSyncTargets(ctx, computeWorkspaceKcpClient)
	if err != nil {
		return err
	}

	err = o.applyPlacement(ctx, userWorkspaceKcpClient)
	if err != nil {
		return err
	}

	err = o.applyAPIBinding(ctx, userWorkspaceKcpClient)
	if err != nil {
		return err
	}

	return nil
}

func apiBindingName(clusterName logicalcluster.Name, apiExportName string) string {
	hash := sha256.Sum224([]byte(clusterName.Path()))
	base36hash := strings.ToLower(base36.EncodeBytes(hash[:]))
	return fmt.Sprintf("%s-%s", apiExportName, base36hash[:8])
}

func (o *BindWorkloadOptions) applyAPIBinding(ctx context.Context, client kcpclient.Interface) error {
	apiBindings, err := client.ApisV1alpha1().APIBindings().List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}

	desiredAPIExports := sets.NewString(o.APIExports...)
	existingAPIExports := sets.NewString()
	for _, binding := range apiBindings.Items {
		if binding.Spec.Reference.Workspace == nil {
			continue
		}
		existingAPIExports.Insert(fmt.Sprintf("%s:%s", binding.Spec.Reference.Workspace.Path, binding.Spec.Reference.Workspace.ExportName))
	}

	diff := desiredAPIExports.Difference(existingAPIExports)
	var errs []error
	for export := range diff {
		lclusterName, name := logicalcluster.New(export).Split()
		apiBinding := &apisv1alpha1.APIBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: apiBindingName(lclusterName, name),
			},
			Spec: apisv1alpha1.APIBindingSpec{
				Reference: apisv1alpha1.ExportReference{
					Workspace: &apisv1alpha1.WorkspaceExportReference{
						Path:       lclusterName.String(),
						ExportName: name,
					},
				},
			},
		}
		_, err := client.ApisV1alpha1().APIBindings().Create(ctx, apiBinding, metav1.CreateOptions{})
		if err != nil && !errors.IsAlreadyExists(err) {
			errs = append(errs, err)
		}

		_, err = fmt.Fprintf(o.Out, "apibinding %s for apiexport %s created.\n", apiBinding.Name, export)
		if err != nil {
			errs = append(errs, err)
		}
	}

	return utilerrors.NewAggregate(errs)
}

// placement name is a hash of location selectors and ns selector, with location workspace name as the prefix
func (o *BindWorkloadOptions) placementName() string {
	clusterName, name := o.ComputeWorkspace.Split()
	hash := sha256.Sum224([]byte(o.NamespaceSelector + strings.Join(o.LocationSelectors, ",") + clusterName.Path()))
	base36hash := strings.ToLower(base36.EncodeBytes(hash[:]))
	return fmt.Sprintf("%s-%s", name, base36hash[:8])
}

func (o *BindWorkloadOptions) applyPlacement(ctx context.Context, client kcpclient.Interface) error {
	nsSelector, err := metav1.ParseToLabelSelector(o.NamespaceSelector)
	if err != nil {
		return fmt.Errorf("namespace selector format not correct: %w", err)
	}

	var locationSelectors []metav1.LabelSelector
	for _, locSelector := range o.LocationSelectors {
		selector, err := metav1.ParseToLabelSelector(locSelector)
		if err != nil {
			return fmt.Errorf("location selector %s format not correct: %w", locSelector, err)
		}
		locationSelectors = append(locationSelectors, *selector)
	}

	placement := &schedulingv1alpha1.Placement{
		ObjectMeta: metav1.ObjectMeta{
			Name: o.placementName(),
		},
		Spec: schedulingv1alpha1.PlacementSpec{
			NamespaceSelector: nsSelector,
			LocationSelectors: locationSelectors,
			LocationWorkspace: o.ComputeWorkspace.String(),
			LocationResource: schedulingv1alpha1.GroupVersionResource{
				Group:    "workload.kcp.dev",
				Version:  "v1alpha1",
				Resource: "synctargets",
			},
		},
	}

	_, err = client.SchedulingV1alpha1().Placements().Get(ctx, placement.Name, metav1.GetOptions{})
	switch {
	case errors.IsNotFound(err):
		_, err := client.SchedulingV1alpha1().Placements().Create(ctx, placement, metav1.CreateOptions{})
		if err != nil && !errors.IsAlreadyExists(err) {
			return err
		}
	case err != nil:
		return err
	}

	_, err = fmt.Fprintf(o.Out, "placement %s created.\n", placement.Name)
	return err
}

func (o *BindWorkloadOptions) hasSupportedSyncTargets(ctx context.Context, client kcpclient.Interface) error {
	syncTargets, err := client.WorkloadV1alpha1().SyncTargets().List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}

	currentExports := sets.NewString(o.APIExports...)

	supportedExports := sets.NewString()
	for _, syncTarget := range syncTargets.Items {
		for _, apiExport := range syncTarget.Spec.SupportedAPIExports {
			if apiExport.Workspace == nil {
				continue
			}

			path := apiExport.Workspace.Path
			// if path is not set, the apiexport is in the compute workspace
			if len(path) == 0 {
				path = o.ComputeWorkspace.String()
			}
			supportedExports.Insert(fmt.Sprintf("%s:%s", path, apiExport.Workspace.ExportName))
		}
	}

	diff := currentExports.Difference(supportedExports)
	if diff.Len() > 0 {
		return fmt.Errorf("not all apiexports is supported by the synctargets in workspace %s: %s", o.ComputeWorkspace, strings.Join(diff.List(), ","))
	}

	return nil
}
