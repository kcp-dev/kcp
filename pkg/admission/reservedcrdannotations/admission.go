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

package reservedcrdannotations

import (
	"context"
	"fmt"
	"io"
	"strings"

	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/endpoints/request"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/reconciler/apis/apibinding"
	"github.com/kcp-dev/logicalcluster/v3"
)

const (
	PluginName = "apis.kcp.dev/ReservedCRDAnnotations"
)

func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName,
		func(_ io.Reader) (admission.Interface, error) {
			return &reservedCRDAnnotations{
				Handler: admission.NewHandler(admission.Create, admission.Update),
			}, nil
		})
}

type reservedCRDAnnotations struct {
	*admission.Handler
}

// Ensure that the required admission interfaces are implemented.
var _ = admission.ValidationInterface(&reservedCRDAnnotations{})

// Validate ensures that
// - if the bound annotation exists that it is in the in the Shadow Workspace.
func (o *reservedCRDAnnotations) Validate(ctx context.Context, a admission.Attributes, _ admission.ObjectInterfaces) (err error) {
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

	cluster, err := request.ClusterNameFrom(ctx)
	if err != nil {
		return fmt.Errorf("failed to retrieve cluster from context: %w", err)
	}
	clusterName := logicalcluster.Name(cluster.String()) // TODO(sttts): remove when ClusterFromfrom returns a tenancy.Name
	if clusterName == apibinding.ShadowWorkspaceName {
		return nil
	}

	var errs []error
	for key := range crd.GetAnnotations() {
		comps := strings.SplitN(key, "/", 2)
		if comps[0] == apisv1alpha1.SchemeGroupVersion.Group || strings.HasSuffix(comps[0], "."+apisv1alpha1.SchemeGroupVersion.Group) {
			errs = append(errs, fmt.Errorf("%s is a reserved annotation", key))
		}
	}

	if len(errs) > 0 {
		return admission.NewForbidden(a, utilerrors.NewAggregate(errs))
	}

	return nil
}
