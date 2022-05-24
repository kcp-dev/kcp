/*
Copyright 2014 The Kubernetes Authors.

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

package keyfunctions

import (
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/tools/cache"
)

// The original implementation of DeletionHandlingMetaNamespaceKeyFunc from
// https://github.com/kubernetes/kubernetes/blob/release-1.23/staging/src/k8s.io/client-go/tools/cache/controller.go#L294-L299
// DeletionHandlingMetaNamespaceKeyFunc checks for
// DeletedFinalStateUnknown objects before calling
// MetaNamespaceKeyFunc.
func DeletionHandlingMetaNamespaceKeyFunc(obj interface{}) (string, error) {
	if d, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		return d.Key, nil
	}
	return MetaNamespaceKeyFunc(obj)
}

// The original implementation of MetaNamespaceKeyFunc from
// https://github.com/kubernetes/kubernetes/blob/f043e3cdd6c8b8d7e52e2d0848c7bea1ec043c87/staging/src/k8s.io/client-go/tools/cache/store.go#L104-L116
// MetaNamespaceKeyFunc is a convenient default KeyFunc which knows how to make
// keys for API objects which implement meta.Interface.
// The key uses the format <namespace>/<name> unless <namespace> is empty, then
// it's just <name>.
func MetaNamespaceKeyFunc(obj interface{}) (string, error) {

	if key, ok := obj.(cache.ExplicitKey); ok {
		return string(key), nil
	}
	metadata, err := meta.Accessor(obj)
	if err != nil {
		return "", fmt.Errorf("object has no meta: %v", err)
	}
	if len(metadata.GetNamespace()) > 0 {
		return metadata.GetNamespace() + "/" + metadata.GetName(), nil
	}
	return metadata.GetName(), nil
}
