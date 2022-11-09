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

package thisworkspace

import (
	"context"
	"fmt"
	"io"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/admission"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
)

// Protects deletion of ThisWorkspace if spec.directlyDeletable is false.

const (
	PluginName = "tenancy.kcp.dev/ThisWorkspace"
)

func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName,
		func(_ io.Reader) (admission.Interface, error) {
			return &thisWorkspace{
				Handler: admission.NewHandler(admission.Create, admission.Update),
			}, nil
		})
}

type thisWorkspace struct {
	*admission.Handler
}

// Ensure that the required admission interfaces are implemented.
var _ = admission.ValidationInterface(&thisWorkspace{})

func (o *thisWorkspace) Validate(_ context.Context, a admission.Attributes, _ admission.ObjectInterfaces) (err error) {
	if a.GetResource().GroupResource() != tenancyv1alpha1.Resource("thisworkspaces") {
		return nil
	}

	groups := sets.NewString(a.GetUserInfo().GetGroups()...)
	if groups.Has("system:masters") || groups.Has(bootstrap.SystemLogicalClusterAdmin) || groups.Has(bootstrap.SystemKcpWorkspaceBootstrapper) {
		return nil
	}

	switch a.GetOperation() {
	case admission.Update:
		return admission.NewForbidden(a, fmt.Errorf("ThisWorkspace cannot be updated"))
	case admission.Delete:
		u, ok := a.GetObject().(*unstructured.Unstructured)
		if !ok {
			return fmt.Errorf("unexpected type %T", a.GetObject())
		}

		directlyDeletable, found, err := unstructured.NestedBool(u.Object, "spec", "directlyDeletable")
		if err != nil {
			return err
		}
		if !found || !directlyDeletable {
			return admission.NewForbidden(a, fmt.Errorf("ThisWorkspace cannot be deleted"))
		}
	case admission.Create:
		return admission.NewForbidden(a, fmt.Errorf("ThisWorkspace cannot be created"))
	}

	return nil
}
