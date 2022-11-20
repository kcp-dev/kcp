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

func TestIsValidCluster(t *testing.T) {
	tests := []struct {
		workspace string
		valid     bool
	}{
		{"", false},

		{"root", true},
		{"root:a", true},
		{"root:a:b", true},
		{"root:foo", true},
		{"root:foo:bar", true},

		{"system", true},
		{"system:foo", true},
		{"system:foo:bar", true},

		// the plugin does not decide about segment length, the server does
		{"root:b1234567890123456789012345678912", true},
		{"root:test-8827a131-f796-4473-8904-a0fa527696eb:b1234567890123456789012345678912", true},
		{"root:test-too-long-org-0020-4473-0030-a0fa-0040-5276-0050-sdg2-0060:b1234567890123456789012345678912", true},

		{"foo", false},
		{"foo:bar", false},
		{"root:", false},
		{":root", false},
		{"root::foo", false},
		{"root:föö:bär", false},
		{"root:bar_bar", false},
		{"root:0a", false},
		{"root:0bar", false},
		{"root/bar", false},
		{"root:bar-", false},
		{"root:-bar", false},
	}
	for _, tt := range tests {
		t.Run(tt.workspace, func(t *testing.T) {
			if got := IsValidCluster(logicalcluster.New(tt.workspace)); got != tt.valid {
				t.Errorf("IsValidCluster(%q) = %v, want %v", tt.workspace, got, tt.valid)
			}
		})
	}
}

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
