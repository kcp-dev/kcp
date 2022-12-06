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

package indexers

import (
	"encoding/json"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kcp-dev/kcp/pkg/syncer/shared"
)

const (
	ByNamespaceLocatorIndexName = "syncer-spec-ByNamespaceLocator"
	ByTenantIDIndexName         = "syncer-workspace-cleaner-ByTenantID"
)

// indexByNamespaceLocator is a cache.IndexFunc that indexes namespaces by the namespaceLocator annotation.
func IndexByNamespaceLocator(obj interface{}) ([]string, error) {
	metaObj, ok := obj.(metav1.Object)
	if !ok {
		return []string{}, fmt.Errorf("obj is supposed to be a metav1.Object, but is %T", obj)
	}
	if loc, found, err := shared.LocatorFromAnnotations(metaObj.GetAnnotations()); err != nil {
		return []string{}, fmt.Errorf("failed to get locator from annotations: %w", err)
	} else if !found {
		return []string{}, nil
	} else {
		bs, err := json.Marshal(loc)
		if err != nil {
			return []string{}, fmt.Errorf("failed to marshal locator %#v: %w", loc, err)
		}
		return []string{string(bs)}, nil
	}
}

// IndexByTenantID is a cache.IndexFunc that indexes namespaces by the tenant ID
// ( == originating KCP workspace ID).
func IndexByTenantID(obj interface{}) ([]string, error) {
	metaObj, ok := obj.(metav1.Object)
	if !ok {
		return []string{}, fmt.Errorf("obj is supposed to be a metav1.Object, but is %T", obj)
	}
	if loc, found, err := shared.LocatorFromAnnotations(metaObj.GetAnnotations()); err != nil {
		return []string{}, fmt.Errorf("failed to get locator from annotations: %w", err)
	} else if !found {
		return []string{}, nil
	} else {
		tenantID, err := shared.GetTenantID(*loc)
		if err != nil {
			return []string{}, fmt.Errorf("failed to get tenant ID from locator %#v: %w", loc, err)
		}
		return []string{tenantID}, nil
	}
}
