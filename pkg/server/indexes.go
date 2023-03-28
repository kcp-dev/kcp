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

package server

import (
	"fmt"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
)

const (
	byGroupResourceName     = "byGroupResourceName" // <plural>.<group>, core group uses "core"
	byIdentityGroupResource = "byIdentityGroupResource"
)

func indexCRDByGroupResourceName(obj interface{}) ([]string, error) {
	crd, ok := obj.(*apiextensionsv1.CustomResourceDefinition)
	if !ok {
		return []string{}, fmt.Errorf("obj is supposed to be a apiextensionsv1.CustomResourceDefinition, but is %T", obj)
	}

	group := crd.Spec.Group
	if group == "" {
		group = "core"
	}
	return []string{fmt.Sprintf("%s.%s", crd.Spec.Names.Plural, group)}, nil
}

func indexAPIBindingByIdentityGroupResource(obj interface{}) ([]string, error) {
	apiBinding, ok := obj.(*apisv1alpha1.APIBinding)
	if !ok {
		return []string{}, fmt.Errorf("obj is supposed to be an APIBinding, but is %T", obj)
	}

	ret := make([]string, 0, len(apiBinding.Status.BoundResources))

	for _, r := range apiBinding.Status.BoundResources {
		ret = append(ret, identityGroupResourceKeyFunc(r.Schema.IdentityHash, r.Group, r.Resource))
	}

	return ret, nil
}

func identityGroupResourceKeyFunc(identity, group, resource string) string {
	return fmt.Sprintf("%s/%s/%s", identity, group, resource)
}
