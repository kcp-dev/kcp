/*
Copyright 2026 The kcp Authors.

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

package crdpuller

import (
	"k8s.io/apimachinery/pkg/runtime"
)

// MergeSchemes merges multiple source schemes into a target scheme by copying
// all registered group versions and known types.
func MergeSchemes(target *runtime.Scheme, sources ...*runtime.Scheme) *runtime.Scheme {
	for _, source := range sources {
		for gvk := range source.AllKnownTypes() {
			if obj, err := source.New(gvk); err == nil {
				target.AddKnownTypeWithName(gvk, obj)
			}
		}
	}

	return target
}
