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

package pathannotation

import (
	"context"
	"fmt"
	"io"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/admission"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
)

const (
	PluginName = "kcp.dev/PathAnnotation"
)

func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName,
		func(_ io.Reader) (admission.Interface, error) {
			return &pathAnnotationPlugin{
				Handler: admission.NewHandler(admission.Create, admission.Update),
			}, nil
		})
}

// Validate checks the value of the logical cluster path annotation to match the
// canonical path in the context.

// Admit sets the value of the logical cluster path annotation for some resources
// to match the canonical path in the context.

type pathAnnotationPlugin struct {
	*admission.Handler
}

var pathAnnotationResources = sets.NewString(
	apisv1alpha1.Resource("apiexports").String(),
	schedulingv1alpha1.Resource("locations").String(),
	tenancyv1alpha1.Resource("clusterworkspacetypes").String(),
)

// Ensure that the required admission interfaces are implemented.
var _ = admission.ValidationInterface(&pathAnnotationPlugin{})
var _ = admission.MutationInterface(&pathAnnotationPlugin{})

func (p *pathAnnotationPlugin) Admit(ctx context.Context, a admission.Attributes, _ admission.ObjectInterfaces) error {
	if a.GetOperation() != admission.Create && a.GetOperation() != admission.Update {
		return nil
	}

	u, ok := a.GetObject().(metav1.Object)
	if !ok {
		return fmt.Errorf("unexpected type %T", a.GetObject())
	}

	annotations := u.GetAnnotations()
	value, found := annotations[tenancy.LogicalClusterPathAnnotationKey]
	if !found && !pathAnnotationResources.Has(a.GetResource().GroupResource().String()) {
		return nil
	}
	path := tenancy.CanonicalPathFrom(ctx)
	if !path.Empty() && value != path.String() {
		if annotations == nil {
			annotations = map[string]string{}
		}
		annotations[tenancy.LogicalClusterPathAnnotationKey] = path.String()
		u.SetAnnotations(annotations)
	}

	return nil
}

func (p *pathAnnotationPlugin) Validate(ctx context.Context, a admission.Attributes, _ admission.ObjectInterfaces) error {
	if a.GetOperation() != admission.Create && a.GetOperation() != admission.Update {
		return nil
	}

	u, ok := a.GetObject().(metav1.Object)
	if !ok {
		return fmt.Errorf("unexpected type %T", a.GetObject())
	}

	path := tenancy.CanonicalPathFrom(ctx)
	value, found := u.GetAnnotations()[tenancy.LogicalClusterPathAnnotationKey]

	if pathAnnotationResources.Has(a.GetResource().GroupResource().String()) || found {
		if path.Empty() {
			return admission.NewForbidden(a, fmt.Errorf("cannot create without knowing the logical cluster, try again later"))
		}
		if !found {
			return admission.NewForbidden(a, fmt.Errorf("annotation %q must match canonical path %q", tenancy.LogicalClusterPathAnnotationKey, path.String()))
		}
		if value != path.String() {
			return admission.NewForbidden(a, fmt.Errorf("annotation %q must match canonical path %q", tenancy.LogicalClusterPathAnnotationKey, path.String()))
		}
	}

	return nil
}
