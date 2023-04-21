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

package finalizer

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/user"
)

// Validate creation and updates for
// - existence of the finalizer while deletion timestamp is not set

// Mutate creation and updates for
// - add finalizer on create, and when missing during update

type FinalizerPlugin struct {
	*admission.Handler

	Resource      schema.GroupResource
	FinalizerName string
}

// Ensure that the required admission interfaces are implemented.
var _ = admission.ValidationInterface(&FinalizerPlugin{})
var _ = admission.MutationInterface(&FinalizerPlugin{})

func (f *FinalizerPlugin) Admit(ctx context.Context, a admission.Attributes, _ admission.ObjectInterfaces) error {
	if a.GetResource().GroupResource() != f.Resource {
		return nil
	}

	u, ok := a.GetObject().(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("unexpected type %T", a.GetObject())
	}

	if !finalizerExists(u.GetFinalizers(), f.FinalizerName) && u.GetDeletionTimestamp().IsZero() {
		u.SetFinalizers(append(u.GetFinalizers(), f.FinalizerName))
	}

	return nil
}

func (f *FinalizerPlugin) Validate(ctx context.Context, a admission.Attributes, _ admission.ObjectInterfaces) error {
	if a.GetResource().GroupResource() != f.Resource {
		return nil
	}

	u, ok := a.GetObject().(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("unexpected type %T", a.GetObject())
	}

	switch a.GetOperation() {
	case admission.Create:
		if !finalizerExists(u.GetFinalizers(), f.FinalizerName) {
			return admission.NewForbidden(a, fmt.Errorf("finalizer %s is required", f.FinalizerName))
		}
	case admission.Update:
		old, ok := a.GetOldObject().(*unstructured.Unstructured)
		if !ok {
			return fmt.Errorf("unexpected type %T", a.GetOldObject())
		}

		isSystem := sets.New[string](a.GetUserInfo().GetGroups()...).Has(user.SystemPrivilegedGroup)
		isDeleting := !u.GetDeletionTimestamp().IsZero()
		isRemoving := finalizerExists(old.GetFinalizers(), f.FinalizerName) && !finalizerExists(u.GetFinalizers(), f.FinalizerName)
		exists := finalizerExists(u.GetFinalizers(), f.FinalizerName)

		if !isDeleting && !exists {
			return admission.NewForbidden(a, fmt.Errorf("finalizer %s is required", f.FinalizerName))
		} else if isDeleting && isRemoving && !isSystem {
			return admission.NewForbidden(a, fmt.Errorf("removing the finalizer %s is forbidden", f.FinalizerName))
		}
	}

	return nil
}

func finalizerExists(finalizers []string, finalizer string) bool {
	for _, f := range finalizers {
		if f == finalizer {
			return true
		}
	}
	return false
}
