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

package admitters

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"
)

type ServiceAccountSecretAdmitter struct {
}

func (sm *ServiceAccountSecretAdmitter) GVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "secrets",
	}
}

var _ Admitter = NewServiceAccountSecretAdmitter()

func NewServiceAccountSecretAdmitter() *ServiceAccountSecretAdmitter {
	return &ServiceAccountSecretAdmitter{}
}

// Admit determines if we should sync the object or not.
func (sm *ServiceAccountSecretAdmitter) Admit(downstreamObj *unstructured.Unstructured) bool {
	var secret corev1.Secret
	err := runtime.DefaultUnstructuredConverter.FromUnstructured(
		downstreamObj.UnstructuredContent(),
		&secret)
	if err != nil {
		klog.V(3).Infof("failed to convert from unstructured: %v", err)
		return false
	}

	return secret.Type != corev1.SecretTypeServiceAccountToken
}
