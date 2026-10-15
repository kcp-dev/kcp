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
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/storage/value"
	"k8s.io/apiserver/pkg/storage/value/encrypt/identity"
)

type recordingTransformers struct{ resource schema.GroupResource }

func (r *recordingTransformers) TransformerForResource(gr schema.GroupResource) value.Transformer {
	r.resource = gr
	return identity.NewEncryptCheckTransformer()
}
func TestTransformerForKey(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ key, group, resource string }{
		{"core/secrets/cluster/default/s1", "", "secrets"},
		{"apps/deployments/cluster/default/d1", "apps", "deployments"},
		{"example.io/widgets/customresources/cluster/default/w1", "example.io", "widgets"},
		{"example.io/widgets/identity/cluster/w1", "example.io", "widgets"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			t.Parallel()
			transformers := &recordingTransformers{}
			_, err := TransformerForKey(transformers, tc.key)
			require.NoError(t, err)
			require.Equal(t, schema.GroupResource{Group: tc.group, Resource: tc.resource}, transformers.resource)
		})
	}
}
func TestTransformerWithoutEncryptionRejectsEncryptedSource(t *testing.T) {
	t.Parallel()
	transformer, err := TransformerForKey(nil, "core/secrets/cluster/default/s1")
	require.NoError(t, err)
	_, _, err = transformer.TransformFromStorage(context.Background(), []byte("k8s:enc:aesgcm:v1:key1:data"), value.DefaultContext("key"))
	require.Error(t, err)
}
