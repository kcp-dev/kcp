/*
Copyright 2021 The KCP Authors.

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

package apibinding

import (
	"embed"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

//go:embed *.yaml
var testFiles embed.FS

func groupExists(list *metav1.APIGroupList, group string) bool {
	for _, g := range list.Groups {
		if g.Name == group {
			return true
		}
	}
	return false
}

func resourceExists(list *metav1.APIResourceList, resource string) bool {
	for _, r := range list.APIResources {
		if r.Name == resource {
			return true
		}
	}
	return false
}
