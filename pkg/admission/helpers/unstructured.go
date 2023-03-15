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

package helpers

import (
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	kcpscheme "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/scheme"
)

func ToUnstructuredOrDie(obj runtime.Object) *unstructured.Unstructured {
	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		panic(err)
	}
	u := &unstructured.Unstructured{Object: raw}

	kinds, _, err := kcpscheme.Scheme.ObjectKinds(obj)
	if err != nil {
		panic(err)
	}
	if len(kinds) == 0 {
		panic(fmt.Errorf("unable to determine kind for %T", obj))
	}
	u.SetKind(kinds[0].Kind)
	u.SetAPIVersion(kinds[0].GroupVersion().String())

	return u
}
