/*
Copyright 2023 The KCP Authors.

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

package v1alpha1

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	v1 "k8s.io/api/core/v1"
)

func TestParseTenancyMountAnnotation(t *testing.T) {
	input := "{\"spec\":{\"ref\":{\"apiVersion\":\"proxy.faros.sh/v1alpha1\",\"kind\":\"KubeCluster\",\"name\":\"dev-cluster\"}},\"status\":{}}"
	expected := &Mount{
		MountSpec: MountSpec{
			Reference: &v1.ObjectReference{
				APIVersion: "proxy.faros.sh/v1alpha1",
				Kind:       "KubeCluster",
				Name:       "dev-cluster",
			},
		},
		MountStatus: MountStatus{},
	}

	v, err := ParseTenancyMountAnnotation(input)
	if err != nil {
		t.Fatal(err)
	}
	if cmp.Diff(v, expected) != "" {
		t.Fatalf("unexpected diff: %s", cmp.Diff(v, &expected))
	}
}
