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

package logicalclustermigration

import (
	"context"
	"crypto/aes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	clientv3 "go.etcd.io/etcd/client/v3"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/server/options/encryptionconfig"
	"k8s.io/apiserver/pkg/storage/value"
	encryptaes "k8s.io/apiserver/pkg/storage/value/encrypt/aes"
	"k8s.io/apiserver/pkg/storage/value/encrypt/identity"

	"github.com/kcp-dev/logicalcluster/v3"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	migrationv1alpha1 "github.com/kcp-dev/sdk/apis/migration/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
)

type recordingPutKV struct {
	clientv3.KV
	values map[string][]byte
}

func (kv *recordingPutKV) Put(_ context.Context, key, data string, _ ...clientv3.OpOption) (*clientv3.PutResponse, error) {
	kv.values[key] = []byte(data)
	return &clientv3.PutResponse{}, nil
}

type failedWriteTransformer struct{ value.Transformer }

func (failedWriteTransformer) TransformToStorage(context.Context, []byte, value.Context) ([]byte, error) {
	return nil, fmt.Errorf("encryption unavailable")
}
func TestWriteDumpEntriesEncryption(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"aesgcm", "aescbc", "identity"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			makeTransformer := func(key string) value.Transformer {
				block, err := aes.NewCipher([]byte(key))
				require.NoError(t, err)
				switch mode {
				case "aesgcm":
					tr, err := encryptaes.NewGCMTransformer(block)
					require.NoError(t, err)
					return tr
				case "aescbc":
					return encryptaes.NewCBCTransformer(block)
				default:
					return identity.NewEncryptCheckTransformer()
				}
			}
			source := makeTransformer("0123456789abcdef0123456789abcdef")
			destination := makeTransformer("abcdef0123456789abcdef0123456789")
			plain := []byte("serialized secret")
			sourceKey := "/shard/source/core/secrets/cluster/default/s1"
			destinationKey := "/shard/dest/core/secrets/cluster/default/s1"
			encrypted, err := source.TransformToStorage(ctx, plain, value.DefaultContext(sourceKey))
			require.NoError(t, err)
			dumped, _, err := source.TransformFromStorage(ctx, encrypted, value.DefaultContext(sourceKey))
			require.NoError(t, err)
			var transformers value.ResourceTransformers
			if mode != "identity" {
				transformers = encryptionconfig.StaticTransformers{schema.GroupResource{Resource: "secrets"}: destination}
			}
			kv := &recordingPutKV{values: map[string][]byte{}}
			err = writeDumpEntries(ctx, kv, nil, "/shard/dest", transformers, []migrationv1alpha1.EtcdEntry{{Key: "core/secrets/cluster/default/s1", Value: dumped}})
			require.NoError(t, err)
			stored := kv.values[destinationKey]
			require.NotEmpty(t, stored)
			if mode != "identity" {
				require.NotEqual(t, plain, stored)
			}
			read, _, err := destination.TransformFromStorage(ctx, stored, value.DefaultContext(destinationKey))
			require.NoError(t, err)
			require.Equal(t, plain, read)
		})
	}
}
func TestWriteDumpEntriesEncryptionFailure(t *testing.T) {
	t.Parallel()
	kv := &recordingPutKV{values: map[string][]byte{}}
	transformers := encryptionconfig.StaticTransformers{schema.GroupResource{Resource: "secrets"}: failedWriteTransformer{}}
	err := writeDumpEntries(t.Context(), kv, nil, "/shard/dest", transformers, []migrationv1alpha1.EtcdEntry{{Key: "core/secrets/cluster/default/s1", Value: []byte("secret")}})
	require.ErrorContains(t, err, "encryption unavailable")
	require.Empty(t, kv.values)
}

func TestCopyRejectsLegacyDump(t *testing.T) {
	t.Parallel()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var dump migrationv1alpha1.LogicalClusterDump
		if err := json.NewDecoder(r.Body).Decode(&dump); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if dump.Spec.Encoding != migrationv1alpha1.DumpEncodingPlaintext {
			t.Errorf("unexpected encoding %q", dump.Spec.Encoding)
		}
		// An older origin ignores the new request field and returns raw entries.
		dump.Status.Entries = []migrationv1alpha1.EtcdEntry{{Key: "core/secrets/cluster/default/s1", Value: []byte("old stored bytes")}}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(dump); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(origin.Close)
	c := newTestController(t, &corev1alpha1.Shard{
		ObjectMeta: metav1.ObjectMeta{Name: "origin", Annotations: map[string]string{logicalcluster.AnnotationKey: "root"}},
		Spec:       corev1alpha1.ShardSpec{VirtualWorkspaceURL: origin.URL},
	})
	count, next, err := c.copyPageFromOriginViaHTTP(t.Context(), "cluster", "origin", "")
	require.ErrorContains(t, err, "unsupported dump encoding")
	require.Zero(t, count)
	require.Empty(t, next)
}

func TestEncryptionFailureDoesNotAdvanceMigration(t *testing.T) {
	t.Parallel()
	transformers := encryptionconfig.StaticTransformers{schema.GroupResource{Resource: "secrets"}: failedWriteTransformer{}}
	kv := &recordingPutKV{values: map[string][]byte{}}
	migration := &migrationv1alpha1.LogicalClusterMigration{
		ObjectMeta: metav1.ObjectMeta{Name: "move", Annotations: map[string]string{logicalcluster.AnnotationKey: "org"}},
		Spec:       migrationv1alpha1.LogicalClusterMigrationSpec{LogicalCluster: "cluster"},
		Status:     migrationv1alpha1.LogicalClusterMigrationStatus{Phase: migrationv1alpha1.LogicalClusterMigrationPhaseMigrating},
	}
	c := &Controller{
		ddsif:                       newEmptyDDSIF(),
		migratingLogicalClusters:    NewMigratingLogicalClusters(),
		deleteLogicalClusterContext: func(logicalcluster.Path, error) {},
		copyPageFromOrigin: func(ctx context.Context, _ logicalcluster.Name, _, _ string) (int64, string, error) {
			return 0, "", writeDumpEntries(ctx, kv, nil, "/dest", transformers, []migrationv1alpha1.EtcdEntry{{Key: "core/secrets/cluster/default/s1", Value: []byte("secret")}})
		},
	}
	_, err := c.reconcileMigrating(t.Context(), migration)
	require.ErrorContains(t, err, "encryption unavailable")
	require.Equal(t, migrationv1alpha1.LogicalClusterMigrationPhaseMigrating, migration.Status.Phase)
	require.Zero(t, migration.Status.EntriesCopied)
	require.Equal(t, "CopyFailed", conditions.Get(migration, migrationv1alpha1.LCMigrationDataCopied).Reason)
	require.Empty(t, kv.values)
}
