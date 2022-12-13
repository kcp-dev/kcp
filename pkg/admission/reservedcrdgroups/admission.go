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

package reservedcrdgroups

import (
	"context"
	"fmt"
	"io"
	"strings"

	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/endpoints/request"
)

const (
	PluginName                  = "kcp.io/ReservedCRDGroups"
	SystemCRDLogicalClusterName = "system:system-crds"
)

func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName,
		func(_ io.Reader) (admission.Interface, error) {
			return &reservedCRDGroups{
				Handler: admission.NewHandler(admission.Create, admission.Update),
			}, nil
		})
}

type reservedCRDGroups struct {
	*admission.Handler
}

// Ensure that the required admission interfaces are implemented.
var _ = admission.ValidationInterface(&reservedCRDGroups{})

// Ensure that CRDs in *.kcp.io group are only created inside system:system-crds workspace.
func (o *reservedCRDGroups) Validate(ctx context.Context, a admission.Attributes, _ admission.ObjectInterfaces) (err error) {
	if a.GetResource().GroupResource() != apiextensions.Resource("customresourcedefinitions") {
		return nil
	}

	if a.GetKind().GroupKind() != apiextensions.Kind("CustomResourceDefinition") {
		return nil
	}
	crd, ok := a.GetObject().(*apiextensions.CustomResourceDefinition)
	if !ok {
		return fmt.Errorf("unexpected type %T", a.GetObject())
	}

	clusterName, err := request.ClusterNameFrom(ctx)
	if err != nil {
		return fmt.Errorf("failed to retrieve cluster from context: %w", err)
	}
	if clusterName.String() == SystemCRDLogicalClusterName {
		return nil
	}

	if strings.HasSuffix(crd.Spec.Group, "apis.kcp.io") {
		return admission.NewForbidden(a, fmt.Errorf("%s is a reserved group", crd.Spec.Group))
	}
	return nil
}
