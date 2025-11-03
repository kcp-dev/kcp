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

package apis

import (
	"context"
	"fmt"
	"io"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned"
)

type apiBindingV1alpha2 struct {
	binding *apisv1alpha2.APIBinding
}

func getAPIBindingV1alpha2(ctx context.Context, client kcpclientset.Interface, name string) (*apiBindingV1alpha2, error) {
	binding, err := client.ApisV1alpha2().APIBindings().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	return &apiBindingV1alpha2{binding: binding}, nil
}

func (b *apiBindingV1alpha2) Refresh(ctx context.Context, client kcpclientset.Interface) error {
	current, err := client.ApisV1alpha2().APIBindings().Get(ctx, b.binding.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	b.binding = current

	return nil
}

func (b *apiBindingV1alpha2) Create(ctx context.Context, client kcpclientset.Interface) error {
	created, err := client.ApisV1alpha2().APIBindings().Create(ctx, b.binding, metav1.CreateOptions{})
	if err != nil {
		return err
	}
	b.binding = created

	return nil
}

func (b *apiBindingV1alpha2) IsBound() bool {
	return b.binding.Status.Phase == apisv1alpha2.APIBindingPhaseBound
}

func (b *apiBindingV1alpha2) Name() string {
	return b.binding.Name
}

func (b *apiBindingV1alpha2) SetPermissionClaims(claims []apisv1alpha2.AcceptablePermissionClaim) error {
	b.binding.Spec.PermissionClaims = claims
	return nil
}

type apiBindingListV1alpha2 struct {
	bindings []*apisv1alpha2.APIBinding
}

func listAPIBindingsV1alpha2(ctx context.Context, client kcpclientset.Interface) (APIBindingList, error) {
	nativeBindings, err := client.ApisV1alpha2().APIBindings().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	list := &apiBindingListV1alpha2{
		bindings: make([]*apisv1alpha2.APIBinding, len(nativeBindings.Items)),
	}

	for idx := range nativeBindings.Items {
		list.bindings[idx] = &nativeBindings.Items[idx]
	}

	return list, nil
}

func (l *apiBindingListV1alpha2) PrintPermissionClaims(out io.Writer) error {
	columnNames := []string{"APIBINDING", "GROUP-RESOURCE", "VERBS", "STATUS"}
	if _, err := fmt.Fprintf(out, "%s\n", strings.Join(columnNames, "\t")); err != nil {
		return err
	}

	for _, binding := range l.bindings {
		for _, claim := range binding.Spec.PermissionClaims {
			claimed := schema.GroupResource{
				Group:    claim.Group,
				Resource: claim.Resource,
			}

			verbs := strings.Join(claim.Verbs, ",")

			if _, err := fmt.Fprintf(out, "%s\t%s\t%s\t%s\n", binding.Name, claimed.String(), verbs, string(claim.State)); err != nil {
				return err
			}
		}
	}

	return nil
}
