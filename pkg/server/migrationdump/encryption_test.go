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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	clientv3 "go.etcd.io/etcd/client/v3"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/authentication/user"
	request "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/server/options/encryptionconfig"
	"k8s.io/apiserver/pkg/storage/value"
	encryptaes "k8s.io/apiserver/pkg/storage/value/encrypt/aes"

	"github.com/kcp-dev/logicalcluster/v3"
	migrationv1alpha1 "github.com/kcp-dev/sdk/apis/migration/v1alpha1"

	bootstrap "github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
	kcpetcd "github.com/kcp-dev/kcp/pkg/etcd"
	"github.com/kcp-dev/kcp/pkg/virtual/migratingworkspaces"
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
	entries, next, err := scanEtcdEntries(ctx, kv, nil, "/shard/source", logicalcluster.Name("cluster"), "", 0, 0, transformers)
	require.NoError(t, err)
	require.Empty(t, next)
	require.Len(t, entries, 1)
	require.Equal(t, "core/secrets/cluster/default/s1", entries[0].Key)
	require.Equal(t, plain, entries[0].Value)
	// A failed decrypt must not return a page that the destination can acknowledge.
	kv.kvs[key] = "corrupt ciphertext"
	entries, next, err = scanEtcdEntries(ctx, kv, nil, "/shard/source", logicalcluster.Name("cluster"), "", 0, 0, transformers)
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

func TestDigestMatchesAcrossDifferentEncryptionKeysAndPrefixes(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	makeTransformer := func(rawKey string) value.Transformer {
		block, err := aes.NewCipher([]byte(rawKey))
		require.NoError(t, err)
		tr, err := encryptaes.NewGCMTransformer(block)
		require.NoError(t, err)
		return tr
	}

	sourceTransformer := makeTransformer("0123456789abcdef0123456789abcdef")
	destTransformer := makeTransformer("abcdef0123456789abcdef0123456789")

	plain := []byte("serialized secret payload")
	sourceKey := "/shard/source/core/secrets/cluster/default/s1"
	destKey := "/shard/dest/core/secrets/cluster/default/s1"

	sourceCipher, err := sourceTransformer.TransformToStorage(ctx, plain, value.DefaultContext(sourceKey))
	require.NoError(t, err)
	destCipher, err := destTransformer.TransformToStorage(ctx, plain, value.DefaultContext(destKey))
	require.NoError(t, err)
	require.NotEqual(t, sourceCipher, destCipher)

	sourceTransformers := encryptionconfig.StaticTransformers{schema.GroupResource{Resource: "secrets"}: sourceTransformer}
	destTransformers := encryptionconfig.StaticTransformers{schema.GroupResource{Resource: "secrets"}: destTransformer}

	sourceKV := newFakeKV(map[string]string{sourceKey: string(sourceCipher)})
	destKV := newFakeKV(map[string]string{destKey: string(destCipher)})

	sourceDigests, next, err := kcpetcd.ScanEtcdDigests(ctx, sourceKV, nil, "/shard/source", logicalcluster.Name("cluster"), "", "", 0, sourceTransformers)
	require.NoError(t, err)
	require.Empty(t, next)
	require.Len(t, sourceDigests, 1)

	destDigests, next, err := kcpetcd.ScanEtcdDigests(ctx, destKV, nil, "/shard/dest", logicalcluster.Name("cluster"), "", "", 0, destTransformers)
	require.NoError(t, err)
	require.Empty(t, next)
	require.Equal(t, sourceDigests, destDigests)
}

func TestHandlerServesDigestEndpoint(t *testing.T) {
	t.Parallel()
	kv := newFakeKV(map[string]string{
		"/source/core/configmaps/cluster/default/a": "v1",
		"/source/core/configmaps/cluster/default/b": "v2",
	})
	handler := &Handler{
		etcdClient:               &clientv3.Client{KV: kv},
		etcdStoragePrefix:        "/source",
		migratingLogicalClusters: alwaysMigrating{},
	}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, migratingworkspaces.DigestHandlerPath, strings.NewReader(`{"spec":{"limit":1}}`))
	ctx := request.WithCluster(req.Context(), request.Cluster{Name: logicalcluster.Name("cluster")})
	ctx = request.WithUser(ctx, &user.DefaultInfo{Groups: []string{bootstrap.SystemExternalLogicalClusterAdmin}})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req.WithContext(ctx))
	require.Equal(t, http.StatusCreated, response.Code)

	var got migrationv1alpha1.LogicalClusterDigest
	require.NoError(t, json.NewDecoder(response.Body).Decode(&got))
	require.Len(t, got.Status.Entries, 1)
	require.Equal(t, "core/configmaps/cluster/default/a", got.Status.Entries[0].Key)
	require.NotEmpty(t, got.Status.Entries[0].Hash)
	require.Equal(t, "core/configmaps/cluster/default/b", got.Status.Continue)
}
