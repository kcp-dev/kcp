/*
Copyright 2024 The Kubernetes Authors.

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

package gentype

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// objectWithMeta matches objects implementing both runtime.Object and metav1.Object.
type objectWithMeta interface {
	runtime.Object
	metav1.Object
}

// namedObject matches comparable objects implementing GetName(); it is intended for use with apply declarative configurations.
type namedObject interface {
	comparable
	GetName() *string
}
