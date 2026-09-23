/*
Copyright 2025 The kcp Authors.

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

package aggregatingcrdversiondiscovery

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type crdStorageVerbsProvider struct {
	terminating bool
}

func (p *crdStorageVerbsProvider) resource() []string {
	verbs := metav1.Verbs([]string{"delete", "deletecollection", "get", "list", "patch", "create", "update", "watch"})

	// if we're terminating we don't allow some verbs
	if p.terminating {
		verbs = metav1.Verbs([]string{"delete", "deletecollection", "get", "list", "watch"})
	}

	return verbs
}

// subresources reports the verbs for the sub-resources CRD storage can serve.
// Which of these the resource actually has is decided by the caller from the
// bound CRD's own spec; this only says what the verbs would be.
func (p *crdStorageVerbsProvider) subresources() map[string][]string {
	return map[string][]string{
		"status": metav1.Verbs([]string{"get", "patch", "update"}),
		"scale":  metav1.Verbs([]string{"get", "patch", "update"}),
	}
}
