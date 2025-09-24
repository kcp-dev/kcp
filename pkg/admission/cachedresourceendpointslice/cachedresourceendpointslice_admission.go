/*
Copyright 2025 The KCP Authors.

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

package cachedresourceendpointslice

import (
	"context"
	"fmt"
	"io"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/admission"

	kcpinitializers "github.com/kcp-dev/kcp/pkg/admission/initializers"
	cachev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/cache/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/core"
	kcpinformers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions"
)

const (
	PluginName = "apis.kcp.io/CachedResourceEndpointSlice"
)

func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName,
		func(_ io.Reader) (admission.Interface, error) {
			p := &cachedResourceEndpointSliceAdmission{
				Handler: admission.NewHandler(admission.Create),
			}
			return p, nil
		})
}

type cachedResourceEndpointSliceAdmission struct {
	*admission.Handler
}

// Ensure that the required admission interfaces are implemented.
var (
	_ = admission.ValidationInterface(&cachedResourceEndpointSliceAdmission{})
	_ = admission.InitializationValidator(&cachedResourceEndpointSliceAdmission{})
	_ = admission.MutationInterface(&cachedResourceEndpointSliceAdmission{})
	_ = kcpinitializers.WantsKcpInformers(&cachedResourceEndpointSliceAdmission{})
)

// Validate validates the creation of APIExportEndpointSlice resources. It also performs a SubjectAccessReview
// making sure the user is allowed to use the 'bind' verb with the referenced APIExport.
func (o *cachedResourceEndpointSliceAdmission) Validate(ctx context.Context, a admission.Attributes, _ admission.ObjectInterfaces) error {
	if a.GetResource().GroupResource() != cachev1alpha1.Resource("cachedresourceendpointslices") {
		return nil
	}

	u, ok := a.GetObject().(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("unexpected type %T", a.GetObject())
	}

	slice := &cachev1alpha1.CachedResourceEndpointSlice{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, slice); err != nil {
		return fmt.Errorf("failed to convert unstructured to CachedResourceEndpointSlice: %w", err)
	}

	return nil
}

// ValidateInitialization ensures the required injected fields are set.
func (o *cachedResourceEndpointSliceAdmission) ValidateInitialization() error {
	return nil
}

func (o *cachedResourceEndpointSliceAdmission) SetKcpInformers(local, global kcpinformers.SharedInformerFactory) {
}

func (o *cachedResourceEndpointSliceAdmission) Admit(ctx context.Context, a admission.Attributes, _ admission.ObjectInterfaces) (err error) {
	if a.GetResource().GroupResource() != cachev1alpha1.Resource("cachedresourceendpointslices") {
		return nil
	}

	u, ok := a.GetObject().(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("unexpected type %T", a.GetObject())
	}

	slice := &cachev1alpha1.CachedResourceEndpointSlice{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, slice); err != nil {
		return fmt.Errorf("failed to convert unstructured to CachedResourceEndpointSlice: %w", err)
	}

	switch {
	case a.GetOperation() == admission.Create:
		if slice.Annotations == nil {
			slice.Annotations = make(map[string]string)
		}
		slice.Annotations[core.LogicalClusterPathAnnotationKey] = ""
	}

	// write back
	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(slice)
	if err != nil {
		return err
	}
	u.Object = raw

	return nil
}
