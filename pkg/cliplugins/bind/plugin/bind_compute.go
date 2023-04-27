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

	"github.com/kcp-dev/kcp/pkg/cliplugins/base"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	schedulingv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/scheduling/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/util/conditions"
	kcpclient "github.com/kcp-dev/kcp/sdk/client/clientset/versioned"
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
	cmd.Flags().StringVar(&o.PlacementName, "name", o.PlacementName, "Name of the placement to be created.")
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

// Run creates a placement in the workspace, linking to the location workspace.
func (o *BindComputeOptions) Run(ctx context.Context) error {
	config, err := o.ClientConfig.ClientConfig()
	if err != nil {
		return err
	}
	userWorkspaceKcpClient, err := kcpclient.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("failed to create kcp client: %w", err)
	}

	// apply APIBindings
	bindings, err := o.applyAPIBinding(ctx, userWorkspaceKcpClient, sets.New[string](o.APIExports...))
	if err != nil {
		return err
	}

	// and wait for them to be ready
	var message string
	if err := wait.PollImmediate(time.Millisecond*500, o.BindWaitTimeout, func() (done bool, err error) {
		var ready bool
		if ready, message = bindingsReady(bindings); ready {
			return true, nil
		}

		var updated []*apisv1alpha1.APIBinding
		for _, binding := range bindings {
			b, err := userWorkspaceKcpClient.ApisV1alpha1().APIBindings().Get(ctx, binding.Name, metav1.GetOptions{})
			if err != nil {
				return false, err
			}
			updated = append(updated, b)
		}
		bindings = updated
		return false, nil
	}); err != nil && err.Error() == wait.ErrWaitTimeout.Error() {
		return fmt.Errorf("APIBindings not ready: %s", message)
	} else if err != nil {
		return fmt.Errorf("APIBindings not ready: %w", err)
	}

	// apply placement
	if err := o.applyPlacement(ctx, userWorkspaceKcpClient); err != nil {
		return err
	}

	// and wait for it to be ready
	if err := wait.PollImmediate(time.Millisecond*500, o.BindWaitTimeout, func() (done bool, err error) {
		placement, err := userWorkspaceKcpClient.SchedulingV1alpha1().Placements().Get(ctx, o.PlacementName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		done, message = placementReadyAndScheduled(placement)
		return done, nil
	}); err != nil && err.Error() == wait.ErrWaitTimeout.Error() {
		return fmt.Errorf("placement %q not ready: %s", o.PlacementName, message)
	} else if err != nil {
		return fmt.Errorf("placement %q not ready: %w", o.PlacementName, err)
	}

	_, err = fmt.Fprintf(o.IOStreams.ErrOut, "Placement %q is ready.\n", o.PlacementName)
	return err
}

func placementReadyAndScheduled(placement *schedulingv1alpha1.Placement) (bool, string) {
	if !conditions.IsTrue(placement, schedulingv1alpha1.PlacementScheduled) {
		if msg := conditions.GetMessage(placement, schedulingv1alpha1.PlacementScheduled); len(msg) > 0 {
			return false, fmt.Sprintf("placement is not scheduled: %s", msg)
		}
		return false, "placement is not scheduled"
	}

	if !conditions.IsTrue(placement, schedulingv1alpha1.PlacementReady) {
		if msg := conditions.GetMessage(placement, schedulingv1alpha1.PlacementReady); msg != "" {
			return false, fmt.Sprintf("placement is not ready: %s", msg)
		}
		return false, "placement is not ready"
	}

	return true, ""
}

func bindingsReady(bindings []*apisv1alpha1.APIBinding) (bool, string) {
	for _, binding := range bindings {
		if binding.Status.Phase == apisv1alpha1.APIBindingPhaseBound {
			continue
		}

		conditionMessage := "unknown reason"
		if conditions.IsFalse(binding, apisv1alpha1.InitialBindingCompleted) {
			conditionMessage = conditions.GetMessage(binding, apisv1alpha1.InitialBindingCompleted)
		} else if conditions.IsFalse(binding, apisv1alpha1.APIExportValid) {
			conditionMessage = conditions.GetMessage(binding, apisv1alpha1.APIExportValid)
		}
		path := logicalcluster.NewPath(binding.Spec.Reference.Export.Path)
		var bindTo string
		if path.Empty() {
			bindTo = fmt.Sprintf("local APIExport %q", binding.Spec.Reference.Export.Name)
		} else {
			bindTo = fmt.Sprintf("APIExport %s", path.Join(binding.Spec.Reference.Export.Name))
		}
		return false, fmt.Sprintf("APIBinding %s is not bound to APIExport %q yet: %s", binding.Name, bindTo, conditionMessage)
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

func (o *BindComputeOptions) applyAPIBinding(ctx context.Context, client kcpclient.Interface, desiredAPIExports sets.Set[string]) ([]*apisv1alpha1.APIBinding, error) {
	apiBindings, err := client.ApisV1alpha1().APIBindings().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	// the cluster name we use for local bindings. If there is a binding already,
	// we use it to get the local cluster name. Otherwise, we use the empty string.
	// This is important to not get confused about local bindings with empty
	// path and those with the cluster name as path.
	var localClusterName logicalcluster.Name
	var localPath logicalcluster.Path

	existingAPIExports := sets.New[string]()
	for i := range apiBindings.Items {
		binding := apiBindings.Items[i]
		if binding.Spec.Reference.Export == nil {
			continue
		}
		// TODO(sttts): binding.Spec.Reference.Export.Path is not unique for one export. This whole method does not work reliably.
		path := logicalcluster.NewPath(binding.Spec.Reference.Export.Path)
		if path.Empty() {
			path = logicalcluster.From(&binding).Path()
		}
		existingAPIExports.Insert(path.Join(binding.Spec.Reference.Export.Name).String())
		localClusterName = logicalcluster.From(&binding)

		// try to get the local path too, to be able to identify empty path, local cluster name and local path.
		if localPath.Empty() {
			cluster, err := client.CoreV1alpha1().LogicalClusters().Get(ctx, corev1alpha1.LogicalClusterName, metav1.GetOptions{})
			if err != nil {
				return nil, err
			}
			localPath = logicalcluster.NewPath(cluster.Annotations[core.LogicalClusterPathAnnotationKey])
		}
		if !localPath.Empty() {
			existingAPIExports.Insert(localPath.Join(binding.Spec.Reference.Export.Name).String())
		}
	}

	if localClusterName != "" {
		// add clusterName when missing such that our set logic works
		old := desiredAPIExports
		desiredAPIExports = sets.New[string]()
		for _, export := range sets.List[string](old) {
			path, name := logicalcluster.NewPath(export).Split()
			if path.Empty() {
				path = localClusterName.Path()
			}
			desiredAPIExports.Insert(path.Join(name).String())
		}
	}

	var errs []error
	diff := desiredAPIExports.Difference(existingAPIExports)
	bindings := make([]*apisv1alpha1.APIBinding, 0, len(diff))
	for export := range diff {
		path, name := logicalcluster.NewPath(export).Split()
		if path == localClusterName.Path() {
			// empty path for local bindings
			path = logicalcluster.Path{}
		}
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
			errs = append(errs, fmt.Errorf("failed binding APIExport %q: %w", path.Join(name), err))
			continue
		}

		bindings = append(bindings, binding)

		if _, err = fmt.Fprintf(o.IOStreams.ErrOut, "Binding APIExport %q.\n", export); err != nil {
			errs = append(errs, err)
		}
	}

	return bindings, utilerrors.NewAggregate(errs)
}

func (o *BindComputeOptions) applyPlacement(ctx context.Context, client kcpclient.Interface) error {
	placement := &schedulingv1alpha1.Placement{
		ObjectMeta: metav1.ObjectMeta{
			Name: o.PlacementName,
		},
		Spec: schedulingv1alpha1.PlacementSpec{
			NamespaceSelector: o.namespaceSelector,
			LocationSelectors: o.locationSelectors,
			LocationWorkspace: o.LocationWorkspace.String(),
			LocationResource: schedulingv1alpha1.GroupVersionResource{
				Group:    "workload.kcp.io",
				Version:  "v1alpha1",
				Resource: "synctargets",
			},
		},
	}

	_, err := client.SchedulingV1alpha1().Placements().Create(ctx, placement, metav1.CreateOptions{})
	if err != nil {
		if errors.IsAlreadyExists(err) {
			_, err = fmt.Fprintf(o.Out, "placement %s already exists.\n", o.PlacementName)
			return err
		}

		return err
	}

	_, err = fmt.Fprintf(o.Out, "placement %s created.\n", o.PlacementName)
	return err
}
