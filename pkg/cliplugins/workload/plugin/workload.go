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
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	"github.com/kcp-dev/kcp/pkg/cliplugins/base"
)

// CordonOptions contains options for cordoning or uncordoning a SyncTarget.
type CordonOptions struct {
	*base.Options

	// SyncTarget is the name of the SyncTarget to cordon or uncordon.
	SyncTarget string
	// Cordon indicates if the SyncTarget should be cordoned (true) or uncordoned (false).
	Cordon bool
}

// NewCordonOptions returns a new CordonOptions.
func NewCordonOptions(streams genericclioptions.IOStreams) *CordonOptions {
	return &CordonOptions{
		Options: base.NewOptions(streams),
	}
}

// Complete ensures all dynamically populated fields are initialized.
func (o *CordonOptions) Complete(args []string) error {
	if err := o.Options.Complete(); err != nil {
		return err
	}

	if len(args) > 0 {
		o.SyncTarget = args[0]
	}

	return nil
}

// Validate validates the CordonOptions are complete and usable.
func (o *CordonOptions) Validate() error {
	if o.SyncTarget == "" {
		return errors.New("sync target name is required")
	}

	return nil
}

// Run cordons the sync target and marks it as unschedulable.
func (o *CordonOptions) Run(ctx context.Context) error {
	config, err := o.ClientConfig.ClientConfig()
	if err != nil {
		return err
	}

	kcpClient, err := kcpclient.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("failed to create kcp client: %w", err)
	}

	syncTarget, err := kcpClient.WorkloadV1alpha1().SyncTargets().Get(ctx, o.SyncTarget, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get SyncTarget %s: %w", o.SyncTarget, err)
	}

	// See if there is nothing to do
	if o.Cordon && syncTarget.Spec.Unschedulable {
		fmt.Fprintln(o.Out, o.SyncTarget, "already cordoned")
		return nil
	} else if !o.Cordon && !syncTarget.Spec.Unschedulable {
		fmt.Fprintln(o.Out, o.SyncTarget, "already uncordoned")
		return nil
	}

	var patchBytes []byte
	if o.Cordon {
		patchBytes = []byte(`[{"op":"replace","path":"/spec/unschedulable","value":true}]`)

	} else {
		evict := ``
		if syncTarget.Spec.EvictAfter != nil {
			evict = `,{"op":"remove","path":"/spec/evictAfter"}`
		}

		patchBytes = []byte(`[{"op":"replace","path":"/spec/unschedulable","value":false}` + evict + `]`)
	}

	_, err = kcpClient.WorkloadV1alpha1().SyncTargets().Patch(ctx, o.SyncTarget, types.JSONPatchType, patchBytes, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("failed to update SyncTarget %s: %w", o.SyncTarget, err)
	}

	if o.Cordon {
		fmt.Fprintln(o.Out, o.SyncTarget, "cordoned")
	} else {
		fmt.Fprintln(o.Out, o.SyncTarget, "uncordoned")
	}

	return nil
}

// DrainOptions contains options for draining a SyncTarget.
type DrainOptions struct {
	*base.Options

	// SyncTarget is the name of the SyncTarget to drain.
	SyncTarget string
}

// NewDrainOptions returns a new DrainOptions.
func NewDrainOptions(streams genericclioptions.IOStreams) *DrainOptions {
	return &DrainOptions{
		Options: base.NewOptions(streams),
	}
}

// Complete ensures all dynamically populated fields are initialized.
func (o *DrainOptions) Complete(args []string) error {
	if err := o.Options.Complete(); err != nil {
		return err
	}

	if len(args) > 0 {
		o.SyncTarget = args[0]
	}

	return nil
}

// Validate validates the DrainOptions are complete and usable.
func (o *DrainOptions) Validate() error {
	if o.SyncTarget == "" {
		return errors.New("sync target name is required")
	}

	return nil
}

// Run drains the sync target and marks it as unschedulable.
func (o *DrainOptions) Run(ctx context.Context) error {
	config, err := o.ClientConfig.ClientConfig()
	if err != nil {
		return err
	}

	kcpClient, err := kcpclient.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("failed to create kcp client: %w", err)
	}

	syncTarget, err := kcpClient.WorkloadV1alpha1().SyncTargets().Get(ctx, o.SyncTarget, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get synctarget %s: %w", o.SyncTarget, err)
	}

	// See if there is nothing to do
	if syncTarget.Spec.EvictAfter != nil && syncTarget.Spec.Unschedulable {
		fmt.Fprintln(o.Out, o.SyncTarget, "already draining")
		return nil
	}

	nowTime := time.Now().UTC()
	var patchBytes = []byte(`[{"op":"replace","path":"/spec/unschedulable","value":true},{"op":"replace","path":"/spec/evictAfter","value":"` + nowTime.Format(time.RFC3339) + `"}]`)

	_, err = kcpClient.WorkloadV1alpha1().SyncTargets().Patch(ctx, o.SyncTarget, types.JSONPatchType, patchBytes, metav1.PatchOptions{})

	if err != nil {
		return fmt.Errorf("failed to update SyncTarget %s: %w", o.SyncTarget, err)
	}

	fmt.Fprintln(o.Out, o.SyncTarget, "draining")

	return nil

}
