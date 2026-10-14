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

package etcd

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/storage/value"
	"k8s.io/apiserver/pkg/storage/value/encrypt/identity"
)

// TransformerForKey selects the same resource transformer used by API storage.
// key is relative to the shard's storage prefix. Kcp stores the core API group
// as "core", whereas encryption configuration uses the empty group name.
// Keep ResourceTransformers itself, rather than a snapshot of its transformers,
// so automatic encryption configuration reloads apply to migration as well.
func TransformerForKey(transformers value.ResourceTransformers, key string) (value.Transformer, error) {
	parts := strings.SplitN(key, "/", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return nil, fmt.Errorf("invalid relative storage key %q", key)
	}
	group := parts[0]
	if group == "core" {
		group = ""
	}
	if transformers == nil {
		return identity.NewEncryptCheckTransformer(), nil
	}
	return transformers.TransformerForResource(schema.GroupResource{Group: group, Resource: parts[1]}), nil
}
