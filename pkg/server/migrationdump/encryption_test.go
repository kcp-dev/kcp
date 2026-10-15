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

package migrationdump

import (
	"context"
	"crypto/aes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/authentication/user"
	request "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/server/options/encryptionconfig"
	"k8s.io/apiserver/pkg/storage/value"
	encryptaes "k8s.io/apiserver/pkg/storage/value/encrypt/aes"

	"github.com/kcp-dev/logicalcluster/v3"

	bootstrap "github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
)

func TestScanDecryptsUsingOriginalKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	key := "/shard/source/core/secrets/cluster/default/s1"
	block, err := aes.NewCipher([]byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	transformer, err := encryptaes.NewGCMTransformer(block)
	require.NoError(t, err)
	plain := []byte("serialized secret")
	encrypted, err := transformer.TransformToStorage(ctx, plain, value.DefaultContext(key))
	require.NoError(t, err)
	// Prove that moving the original ciphertext breaks authentication.
	_, _, err = transformer.TransformFromStorage(ctx, encrypted, value.DefaultContext("/shard/dest/core/secrets/cluster/default/s1"))
	require.Error(t, err)
	transformers := encryptionconfig.StaticTransformers{schema.GroupResource{Resource: "secrets"}: transformer}
	kv := newFakeKV(map[string]string{key: string(encrypted)})
	entries, next, err := scanEtcdEntries(ctx, kv, "/shard/source", logicalcluster.Name("cluster"), "", 0, 0, transformers)
	require.NoError(t, err)
	require.Empty(t, next)
	require.Len(t, entries, 1)
	require.Equal(t, "core/secrets/cluster/default/s1", entries[0].Key)
	require.Equal(t, plain, entries[0].Value)
	// A failed decrypt must not return a page that the destination can acknowledge.
	kv.kvs[key] = "corrupt ciphertext"
	entries, next, err = scanEtcdEntries(ctx, kv, "/shard/source", logicalcluster.Name("cluster"), "", 0, 0, transformers)
	require.Error(t, err)
	require.Empty(t, entries)
	require.Empty(t, next)
}

type alwaysMigrating struct{}

func (alwaysMigrating) IsMigrating(logicalcluster.Name) bool { return true }
func TestDumpRejectsLegacyEncoding(t *testing.T) {
	t.Parallel()
	for _, body := range []string{`{}`, `{"spec":{"encoding":"Raw"}}`} {
		t.Run(body, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader(body))
			ctx := request.WithCluster(req.Context(), request.Cluster{Name: logicalcluster.Name("cluster")})
			ctx = request.WithUser(ctx, &user.DefaultInfo{Groups: []string{bootstrap.SystemExternalLogicalClusterAdmin}})
			response := httptest.NewRecorder()
			handler := NewHandler(nil, "/source", nil, alwaysMigrating{})
			handler.ServeHTTP(response, req.WithContext(ctx))
			require.Equal(t, http.StatusBadRequest, response.Code)
			require.Contains(t, response.Body.String(), "require Plaintext encoding")
		})
	}
}
