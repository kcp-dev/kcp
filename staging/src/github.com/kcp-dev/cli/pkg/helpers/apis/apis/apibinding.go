/*
Copyright 2025 The kcp Authors.

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

	apisv1alpha1 "github.com/kcp-dev/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned"
)

type APIBinding interface {
	Name() string
	Refresh(ctx context.Context, client kcpclientset.Interface) error
	Create(ctx context.Context, client kcpclientset.Interface) error
	SetPermissionClaims(claims []apisv1alpha2.AcceptablePermissionClaim) error
	IsBound() bool
}

type APIBindingList interface {
	PrintPermissionClaims(out io.Writer) error
}

func GetAPIBinding(ctx context.Context, client kcpclientset.Interface, preferredVersion string, name string) (APIBinding, error) {
	switch preferredVersion {
	case "v1alpha1":
		return getAPIBindingV1alpha1(ctx, client, name)

	case "v1alpha2":
		return getAPIBindingV1alpha2(ctx, client, name)

	default:
		return nil, fmt.Errorf("version %q is not supported by this plugin", preferredVersion)
	}
}

func ListAPIBindings(ctx context.Context, client kcpclientset.Interface, preferredVersion string) (APIBindingList, error) {
	switch preferredVersion {
	case "v1alpha1":
		return listAPIBindingsV1alpha1(ctx, client)

	case "v1alpha2":
		return listAPIBindingsV1alpha2(ctx, client)

	default:
		return nil, fmt.Errorf("version %q is not supported by this plugin", preferredVersion)
	}
}

func NewAPIBinding(nativeBinding any) APIBinding {
	switch asserted := nativeBinding.(type) {
	case *apisv1alpha1.APIBinding:
		return &apiBindingV1alpha1{binding: asserted}
	case *apisv1alpha2.APIBinding:
		return &apiBindingV1alpha2{binding: asserted}
	}

	panic("Unsupported APIBinding version provided.")
}

func NewAPIBindingList(nativeBinding any) APIBindingList {
	switch asserted := nativeBinding.(type) {
	case *apisv1alpha1.APIBinding:
		return &apiBindingListV1alpha1{bindings: []*apisv1alpha1.APIBinding{asserted}}
	case *apisv1alpha2.APIBinding:
		return &apiBindingListV1alpha2{bindings: []*apisv1alpha2.APIBinding{asserted}}
	}

	panic("Unsupported APIBinding version provided.")
}
