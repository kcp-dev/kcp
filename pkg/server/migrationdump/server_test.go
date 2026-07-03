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
	"fmt"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
	"go.etcd.io/etcd/api/v3/mvccpb"
	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/kcp-dev/logicalcluster/v3"
	migrationv1alpha1 "github.com/kcp-dev/sdk/apis/migration/v1alpha1"
)

func TestScanEtcdEntries_singlePageReturnsEverythingWhenItFits(t *testing.T) {
	t.Parallel()

	prefix := "/registry/"
	target := logicalcluster.Name("root:ws")

	kv := newFakeKV(map[string]string{
		"/registry/apps/deployments/root:ws/default/foo": "v1",
		"/registry/core/configmaps/root:ws/default/cm1":  "v2",
		// other cluster, must not be returned
		"/registry/apps/deployments/root:other/default/x": "v3",
	})

	entries, next, err := scanEtcdEntries(context.Background(), kv, prefix, target, "", 0, 0)
	require.NoError(t, err)
	require.Empty(t, next)

	got := entryKeys(entries)
	sort.Strings(got)
	require.Equal(t, []string{
		"apps/deployments/root:ws/default/foo",
		"core/configmaps/root:ws/default/cm1",
	}, got)
}

func TestScanEtcdEntries_limitPaginatesAndCoversAllEntriesAcrossPages(t *testing.T) {
	t.Parallel()

	prefix := "/registry/"
	target := logicalcluster.Name("root:ws")

	kvs := map[string]string{
		"/registry/apps/deployments/root:ws/default/a": "v",
		"/registry/apps/deployments/root:ws/default/b": "v",
		"/registry/apps/deployments/root:ws/default/c": "v",
		"/registry/apps/deployments/root:ws/default/d": "v",
		"/registry/apps/deployments/root:ws/default/e": "v",
	}
	kv := newFakeKV(kvs)

	var got []string
	continueToken := ""
	pages := 0
	for {
		pages++
		require.LessOrEqual(t, pages, len(kvs)+1, "too many pages, pagination is likely stuck")

		entries, next, err := scanEtcdEntries(context.Background(), kv, prefix, target, continueToken, 2, 0)
		require.NoError(t, err)
		require.LessOrEqual(t, len(entries), 2)

		got = append(got, entryKeys(entries)...)

		if next == "" {
			break
		}
		continueToken = next
	}

	require.Equal(t, 3, pages, "5 entries with a page limit of 2 should take 3 pages")
	sort.Strings(got)
	require.Equal(t, []string{
		"apps/deployments/root:ws/default/a",
		"apps/deployments/root:ws/default/b",
		"apps/deployments/root:ws/default/c",
		"apps/deployments/root:ws/default/d",
		"apps/deployments/root:ws/default/e",
	}, got)
}

func TestScanEtcdEntries_maxBytesStopsPageBeforeLimit(t *testing.T) {
	t.Parallel()

	prefix := "/registry/"
	target := logicalcluster.Name("root:ws")

	// Each value is 10 bytes. A 25 byte budget should only fit 2 of them
	// even though limit allows far more.
	kv := newFakeKV(map[string]string{
		"/registry/apps/deployments/root:ws/default/a": "0123456789",
		"/registry/apps/deployments/root:ws/default/b": "0123456789",
		"/registry/apps/deployments/root:ws/default/c": "0123456789",
	})

	entries, next, err := scanEtcdEntries(context.Background(), kv, prefix, target, "", 1000, 25)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	require.NotEmpty(t, next)
}

func TestScanEtcdEntries_singleEntryLargerThanMaxBytesIsStillReturnedAlone(t *testing.T) {
	t.Parallel()

	prefix := "/registry/"
	target := logicalcluster.Name("root:ws")

	kv := newFakeKV(map[string]string{
		"/registry/apps/deployments/root:ws/default/big":    fmt.Sprintf("%0100d", 0),
		"/registry/apps/deployments/root:ws/default/small1": "v",
		"/registry/apps/deployments/root:ws/default/small2": "v",
	})

	entries, next, err := scanEtcdEntries(context.Background(), kv, prefix, target, "", 1000, 10)
	require.NoError(t, err)
	require.Len(t, entries, 1, "the oversized entry must be returned alone, not dropped")
	require.NotEmpty(t, next, "pagination must continue after the oversized entry")
}

func TestScanEtcdEntries_resumesFromContinueTokenWithoutDuplicatesOrGaps(t *testing.T) {
	t.Parallel()

	prefix := "/registry/"
	target := logicalcluster.Name("root:ws")

	kv := newFakeKV(map[string]string{
		"/registry/apps/deployments/root:ws/default/a": "v",
		"/registry/apps/deployments/root:ws/default/b": "v",
		"/registry/apps/deployments/root:ws/default/c": "v",
		"/registry/apps/deployments/root:ws/default/d": "v",
	})

	first, next, err := scanEtcdEntries(context.Background(), kv, prefix, target, "", 2, 0)
	require.NoError(t, err)
	require.Len(t, first, 2)
	require.NotEmpty(t, next)

	second, next2, err := scanEtcdEntries(context.Background(), kv, prefix, target, next, 1000, 0)
	require.NoError(t, err)
	require.Empty(t, next2)

	all := append(entryKeys(first), entryKeys(second)...)
	sort.Strings(all)
	require.Equal(t, []string{
		"apps/deployments/root:ws/default/a",
		"apps/deployments/root:ws/default/b",
		"apps/deployments/root:ws/default/c",
		"apps/deployments/root:ws/default/d",
	}, all)
}

func TestScanEtcdEntries_ignoresOtherClusters(t *testing.T) {
	t.Parallel()

	prefix := "/registry/"
	target := logicalcluster.Name("root:ws")

	kv := newFakeKV(map[string]string{
		"/registry/apps/deployments/root:other/default/a": "v",
		"/registry/core/configmaps/root:other/default/b":  "v",
	})

	entries, next, err := scanEtcdEntries(context.Background(), kv, prefix, target, "", 0, 0)
	require.NoError(t, err)
	require.Empty(t, next)
	require.Empty(t, entries)
}

func entryKeys(entries []migrationv1alpha1.EtcdEntry) []string {
	keys := make([]string, len(entries))
	for i, e := range entries {
		keys[i] = e.Key
	}
	return keys
}

// fakeKV is a minimal in-memory fake of clientv3.KV for unit-testing range
// scans, mirroring the one used in
// pkg/reconciler/migration/logicalclustermigration/datacleanup_test.go. It
// supports only the operations scanEtcdEntries uses: Get with a range end
// and an optional limit. Other methods are not implemented and panic if
// called.
type fakeKV struct {
	kvs map[string]string
}

func newFakeKV(kvs map[string]string) *fakeKV { return &fakeKV{kvs: kvs} }

func (f *fakeKV) Get(_ context.Context, key string, opts ...clientv3.OpOption) (*clientv3.GetResponse, error) {
	op := clientv3.OpGet(key, opts...)
	rangeEnd := string(op.RangeBytes())
	limit := op.Limit()

	keys := make([]string, 0, len(f.kvs))
	for k := range f.kvs {
		if k >= key && (rangeEnd == "" || k < rangeEnd) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)

	more := false
	if limit > 0 && int64(len(keys)) > limit {
		keys = keys[:limit]
		more = true
	}

	resp := &clientv3.GetResponse{More: more}
	for _, k := range keys {
		resp.Kvs = append(resp.Kvs, &mvccpb.KeyValue{Key: []byte(k), Value: []byte(f.kvs[k])})
	}
	return resp, nil
}

func (f *fakeKV) Put(context.Context, string, string, ...clientv3.OpOption) (*clientv3.PutResponse, error) {
	panic("not implemented")
}
func (f *fakeKV) Delete(context.Context, string, ...clientv3.OpOption) (*clientv3.DeleteResponse, error) {
	panic("not implemented")
}
func (f *fakeKV) Compact(context.Context, int64, ...clientv3.CompactOption) (*clientv3.CompactResponse, error) {
	panic("not implemented")
}
func (f *fakeKV) Do(context.Context, clientv3.Op) (clientv3.OpResponse, error) {
	panic("not implemented")
}
func (f *fakeKV) Txn(context.Context) clientv3.Txn { panic("not implemented") }
