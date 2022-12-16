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

package shard

import (
	"context"
	"fmt"
	"io"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/admission"

	corev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/core/v1alpha1"
)

// Validate ClusterWorkspace creation and updates for
// - immutability of fields like type
// - valid phase transitions fulfilling pre-conditions
// - status.location.current and status.baseURL cannot be unset.

const (
	PluginName = "tenancy.kcp.dev/Shard"
)

func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName,
		func(_ io.Reader) (admission.Interface, error) {
			return &shard{
				Handler: admission.NewHandler(admission.Create, admission.Update),
			}, nil
		})
}

type shard struct {
	*admission.Handler
}

// Ensure that the required admission interfaces are implemented.
var _ = admission.MutationInterface(&shard{})

// Admit sets.
func (o *shard) Admit(_ context.Context, a admission.Attributes, _ admission.ObjectInterfaces) (err error) {
	if a.GetResource().GroupResource() != corev1alpha1.Resource("shards") {
		return nil
	}

	u, ok := a.GetObject().(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("unexpected type %T", a.GetObject())
	}
	cws := &corev1alpha1.Shard{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, cws); err != nil {
		return fmt.Errorf("failed to convert unstructured to Shard: %w", err)
	}

	if cws.Spec.ExternalURL == "" {
		cws.Spec.ExternalURL = cws.Spec.BaseURL
	}

	if cws.Spec.VirtualWorkspaceURL == "" {
		cws.Spec.VirtualWorkspaceURL = cws.Spec.BaseURL
	}

	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(cws)
	if err != nil {
		return err
	}
	u.Object = raw

	return nil
}
