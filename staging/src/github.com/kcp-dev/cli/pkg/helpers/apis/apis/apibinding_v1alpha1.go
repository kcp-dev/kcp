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
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned"
)

type apiBindingV1alpha1 struct {
	binding *apisv1alpha1.APIBinding
}

func getAPIBindingV1alpha1(ctx context.Context, client kcpclientset.Interface, name string) (*apiBindingV1alpha1, error) {
	binding, err := client.ApisV1alpha1().APIBindings().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}

	return &apiBindingV1alpha1{binding: binding}, nil
}

func (b *apiBindingV1alpha1) Refresh(ctx context.Context, client kcpclientset.Interface) error {
	current, err := client.ApisV1alpha1().APIBindings().Get(ctx, b.binding.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	b.binding = current

	return nil
}

func (b *apiBindingV1alpha1) Create(ctx context.Context, client kcpclientset.Interface) error {
	created, err := client.ApisV1alpha1().APIBindings().Create(ctx, b.binding, metav1.CreateOptions{})
	if err != nil {
		return err
	}
	b.binding = created

	return nil
}

func (b *apiBindingV1alpha1) IsBound() bool {
	return b.binding.Status.Phase == apisv1alpha1.APIBindingPhaseBound
}

func (b *apiBindingV1alpha1) Name() string {
	return b.binding.Name
}

func (b *apiBindingV1alpha1) SetPermissionClaims(claims []apisv1alpha2.AcceptablePermissionClaim) error {
	alpha1Claims := []apisv1alpha1.AcceptablePermissionClaim{}

	for _, claim := range claims {
		if len(claim.Verbs) != 0 && !slices.Equal(claim.Verbs, []string{"*"}) {
			return errors.New("apis v1alpha1 does not support verbs in permission claims")
		}

		if !claim.Selector.MatchAll {
			return errors.New("apis v1alpha1 only supports the `all` selector for permission claims")
		}

		alpha1Claims = append(alpha1Claims, apisv1alpha1.AcceptablePermissionClaim{
			PermissionClaim: apisv1alpha1.PermissionClaim{
				GroupResource: apisv1alpha1.GroupResource{
					Group:    claim.Group,
					Resource: claim.Resource,
				},
				All:          true,
				IdentityHash: claim.IdentityHash,
			},
			State: apisv1alpha1.AcceptablePermissionClaimState(claim.State),
		})
	}

	b.binding.Spec.PermissionClaims = alpha1Claims
	return nil
}

type apiBindingListV1alpha1 struct {
	bindings []*apisv1alpha1.APIBinding
}

func listAPIBindingsV1alpha1(ctx context.Context, client kcpclientset.Interface) (APIBindingList, error) {
	nativeBindings, err := client.ApisV1alpha1().APIBindings().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	list := &apiBindingListV1alpha1{
		bindings: make([]*apisv1alpha1.APIBinding, len(nativeBindings.Items)),
	}

	for idx := range nativeBindings.Items {
		list.bindings[idx] = &nativeBindings.Items[idx]
	}

	return list, nil
}

func (l *apiBindingListV1alpha1) PrintPermissionClaims(out io.Writer) error {
	columnNames := []string{"APIBINDING", "GROUP-RESOURCE", "STATUS"}
	if _, err := fmt.Fprintf(out, "%s\n", strings.Join(columnNames, "\t")); err != nil {
		return err
	}

	for _, binding := range l.bindings {
		for _, claim := range binding.Spec.PermissionClaims {
			claimed := schema.GroupResource{
				Group:    claim.Group,
				Resource: claim.Resource,
			}

			if _, err := fmt.Fprintf(out, "%s\t%s\t%s\n", binding.Name, claimed.String(), string(claim.State)); err != nil {
				return err
			}
		}
	}

	return nil
}
