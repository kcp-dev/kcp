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

package helper

import (
	"testing"

	"github.com/kcp-dev/logicalcluster/v2"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestQualifiedObjectName(t *testing.T) {
	tests := []struct {
		obj  metav1.Object
		name string
	}{
		{&metav1.ObjectMeta{
			Name: "cool-name",
			Annotations: map[string]string{
				logicalcluster.AnnotationKey: "cool-cluster",
			},
		}, "cool-cluster|cool-name"},
		{&metav1.ObjectMeta{
			Name:      "cool-name",
			Namespace: "cool-namespace",
			Annotations: map[string]string{
				logicalcluster.AnnotationKey: "cool-cluster",
			},
		}, "cool-cluster|cool-namespace/cool-name"},
	}
	for _, tt := range tests {
		t.Run(tt.obj.GetName(), func(t *testing.T) {
			if got := QualifiedObjectName(tt.obj); got != tt.name {
				t.Errorf("QualifiedObjectName(%v) = %s, want %s", tt.obj, got, tt.name)
			}
		})
	}
}
