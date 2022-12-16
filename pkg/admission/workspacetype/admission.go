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

package workspacetype

import (
	"context"
	"fmt"
	"io"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/admission"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
)

// Validate WorkspaceTypes creation and updates for
//  - "organization" type is only created in root workspace.

const (
	PluginName = "tenancy.kcp.dev/WorkspaceType"
)

func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName,
		func(_ io.Reader) (admission.Interface, error) {
			return &workspacetype{
				Handler: admission.NewHandler(admission.Create, admission.Update),
			}, nil
		})
}

type workspacetype struct {
	*admission.Handler
}

// Ensure that the required admission interfaces are implemented.
var _ = admission.ValidationInterface(&workspacetype{})

func (o *workspacetype) Validate(ctx context.Context, a admission.Attributes, _ admission.ObjectInterfaces) (err error) {
	clusterName, err := genericapirequest.ClusterNameFrom(ctx)
	if err != nil {
		return apierrors.NewInternalError(err)
	}

	if a.GetResource().GroupResource() != tenancyv1alpha1.Resource("workspacetypes") {
		return nil
	}

	u, ok := a.GetObject().(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("unexpected type %T", a.GetObject())
	}
	cwt := &tenancyv1alpha1.WorkspaceType{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, cwt); err != nil {
		return fmt.Errorf("failed to convert unstructured to WorkspaceType: %w", err)
	}

	if cwt.Name == "root" && clusterName != tenancyv1alpha1.RootCluster {
		return admission.NewForbidden(a, fmt.Errorf("root workspace type can only be created in root cluster"))
	}

	if cwt.Spec.DefaultChildWorkspaceType != nil && cwt.Spec.DefaultChildWorkspaceType.Path == "" {
		return admission.NewForbidden(a, fmt.Errorf(".spec.defaultChildWorkspaceType.path must be set"))
	}

	if cwt.Spec.LimitAllowedChildren != nil {
		for i, t := range cwt.Spec.LimitAllowedChildren.Types {
			if t.Path == "" {
				return admission.NewForbidden(a, fmt.Errorf(".spec.limitAllowedChildren.types[%d].path must be set", i))
			}
		}
	}

	if cwt.Spec.LimitAllowedParents != nil {
		for i, t := range cwt.Spec.LimitAllowedParents.Types {
			if t.Path == "" {
				return admission.NewForbidden(a, fmt.Errorf(".spec.limitAllowedParents.types[%d].path must be set", i))
			}
		}
	}

	return nil
}
