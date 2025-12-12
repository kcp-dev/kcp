/*
Copyright 2025 The KCP Authors.

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

func (p *crdStorageVerbsProvider) statusSubresource() []string {
	return metav1.Verbs([]string{"get", "patch", "update"})
}

func (p *crdStorageVerbsProvider) scaleSubresource() []string {
	return metav1.Verbs([]string{"get", "patch", "update"})
}
