/*
Copyright 2022 The Kube Bind Authors.

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

package kubernetes

import (
	corev1 "k8s.io/api/core/v1"

	"github.com/kcp-dev/kcp/contrib/kube-bind/kubernetes/resources"
)

const (
	NamespacesByIdentity = "namespacesByIdentity"
)

func IndexNamespacesByIdentity(obj interface{}) ([]string, error) {
	ns, ok := obj.(*corev1.Namespace)
	if !ok {
		return nil, nil
	}

	if id, found := ns.Annotations[resources.IdentityAnnotationKey]; found {
		return []string{id}, nil
	}

	return nil, nil
}
