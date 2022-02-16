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
	"k8s.io/apimachinery/pkg/util/json"

	kcpclientscheme "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/scheme"
)

// DecodeUnstructured decodes an unstructured KCP object into the Golang type.
func DecodeUnstructured(u *unstructured.Unstructured) (runtime.Object, error) {
	bs, err := json.Marshal(u)
	if err != nil {
		return nil, err
	}
	newObj, err := runtime.Decode(kcpclientscheme.Codecs.UniversalDecoder(u.GroupVersionKind().GroupVersion()), bs)
	if err != nil {
		return nil, err
	}
	return newObj, nil
}

func EncodeIntoUnstructured(u *unstructured.Unstructured, obj runtime.Object) error {
	if u == nil {
		return fmt.Errorf("unstructured object is nil") // programming error
	}

	bs, err := runtime.Encode(kcpclientscheme.Codecs.LegacyCodec(u.GroupVersionKind().GroupVersion()), obj)
	if err != nil {
		return err
	}
	err = json.Unmarshal(bs, &u.Object)
	if err != nil {
		return err
	}
	return nil
}

// NativeObject returns the native Golang object from the unstructured object.
func NativeObject(obj runtime.Object) (runtime.Object, error) {
	if obj == nil {
		return nil, nil
	}
	if unstructuredObj, ok := obj.(*unstructured.Unstructured); ok {
		return DecodeUnstructured(unstructuredObj)
	}
	return obj, nil
}
