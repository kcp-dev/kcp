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

package helpers

import (
	"fmt"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
)

// Fingerprint generates a string that uniquely identifies an exported virtual resource.
func Fingerprint(owner *apisv1alpha2.APIExport, storage *apisv1alpha2.ResourceSchemaStorageVirtual) string {
	gk := schema.GroupKind{Group: ptr.Deref(storage.Reference.APIGroup, ""), Kind: storage.Reference.Kind}
	return fmt.Sprintf("%s|%s|%s/%s", logicalcluster.From(owner), owner.Name, storage.Reference.Name, gk)
}
