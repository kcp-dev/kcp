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

package forwardingregistry

import "k8s.io/apimachinery/pkg/api/validate/content"

// ValidatePathSegmentName validates the name can be safely encoded as a path segment.
func ValidatePathSegmentName(name string, prefix bool) []string {
	if prefix {
		return content.IsPathSegmentPrefix(name)
	}
	return content.IsPathSegmentName(name)
}
