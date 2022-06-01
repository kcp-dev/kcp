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

package mutators

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type SecretMutator struct {
}

func (sm *SecretMutator) GVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "secrets",
	}
}

func NewSecretMutator() *SecretMutator {
	return &SecretMutator{}
}

// Mutate applies the mutator changes to the object.
func (sm *SecretMutator) Mutate(obj *unstructured.Unstructured) error {
	// We need transform the serviceaccount tokens into an Opaque secret, in order to avoid the pcluster to rewrite them,
	// and we remove the annotations that point to the kcp serviceaccount name/uid.
	if _, ok := obj.GetAnnotations()[corev1.ServiceAccountNameKey]; ok {
		obj.Object["type"] = string(corev1.SecretTypeOpaque)
		annotations := obj.GetAnnotations()
		delete(annotations, corev1.ServiceAccountNameKey)
		delete(annotations, corev1.ServiceAccountUIDKey)
		if len(annotations) == 0 {
			obj.SetAnnotations(nil)
		} else {
			obj.SetAnnotations(annotations)
		}
	}

	return nil
}
