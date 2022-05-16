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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type ConfigMapMutator struct {
}

func (sm *ConfigMapMutator) GVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "configmaps",
	}
}

var _ Mutator = NewConfigMapMutator()

func NewConfigMapMutator() *ConfigMapMutator {
	return &ConfigMapMutator{}
}

// Mutate applies the mutator changes to the object.
func (sm *ConfigMapMutator) Mutate(downstreamObj *unstructured.Unstructured) error {
	var configMap corev1.ConfigMap
	err := runtime.DefaultUnstructuredConverter.FromUnstructured(
		downstreamObj.UnstructuredContent(),
		&configMap)
	if err != nil {
		return err
	}

	if configMap.GetName() == "kube-root-ca.crt" {
		configMap.SetName("kcp-root-ca.crt")
	}

	unstructured, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&configMap)
	if err != nil {
		return err
	}

	// Set the changes back into the obj.
	downstreamObj.SetUnstructuredContent(unstructured)

	return nil
}
