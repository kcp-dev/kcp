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

package conversion

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// schemaBasedConverter converts apis.kcp.io objects between API versions using the typed
// Go scheme (including permission-claim field round-trips via conversion annotations).
type schemaBasedConverter struct {
	scheme *runtime.Scheme
}

func (s *schemaBasedConverter) Convert(in *unstructured.UnstructuredList, targetGV schema.GroupVersion) (*unstructured.UnstructuredList, error) {
	out := &unstructured.UnstructuredList{}

	for _, item := range in.Items {
		obj, err := s.scheme.New(item.GroupVersionKind())
		if err != nil {
			return nil, err
		}

		err = runtime.DefaultUnstructuredConverter.FromUnstructured(item.Object, obj)
		if err != nil {
			return nil, err
		}

		converted, err := s.scheme.ConvertToVersion(obj, targetGV)
		if err != nil {
			return nil, err
		}

		uns, err := runtime.DefaultUnstructuredConverter.ToUnstructured(converted)
		if err != nil {
			return nil, err
		}

		newObj := unstructured.Unstructured{}
		newObj.Object = uns

		out.Items = append(out.Items, newObj)
	}

	return out, nil
}
