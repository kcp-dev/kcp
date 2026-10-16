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

package identitymigrator

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.etcd.io/etcd/api/v3/mvccpb"
	clientv3 "go.etcd.io/etcd/client/v3"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/storage/value"

	"github.com/kcp-dev/logicalcluster/v3"
)

// fakeKV implements clientv3.KV over a map, enough for the prefix copy:
// range reads with limit, sort and count-only, and plain puts.
type fakeKV struct {
	kvs map[string]string
}

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

	resp := &clientv3.GetResponse{Count: int64(len(keys))}
	if op.IsCountOnly() {
		return resp, nil
	}
	if limit > 0 && int64(len(keys)) > limit {
		keys = keys[:limit]
		resp.More = true
	}
	for _, k := range keys {
		resp.Kvs = append(resp.Kvs, &mvccpb.KeyValue{Key: []byte(k), Value: []byte(f.kvs[k])})
	}
	return resp, nil
}

func (f *fakeKV) Put(_ context.Context, key, val string, _ ...clientv3.OpOption) (*clientv3.PutResponse, error) {
	f.kvs[key] = val
	return &clientv3.PutResponse{}, nil
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

// keyBoundTransformer mimics an AEAD provider: the stored form embeds the
// authenticated data (the etcd key), and reading back under a different key
// fails. It records which resource it was selected for.
type keyBoundTransformer struct {
	resource schema.GroupResource
}

func (t *keyBoundTransformer) TransformFromStorage(_ context.Context, data []byte, dataCtx value.Context) ([]byte, bool, error) {
	prefix := string(dataCtx.AuthenticatedData()) + "|"
	if !strings.HasPrefix(string(data), prefix) {
		return nil, false, fmt.Errorf("authentication failed for key %q", dataCtx.AuthenticatedData())
	}
	return []byte(strings.TrimPrefix(string(data), prefix)), false, nil
}

func (t *keyBoundTransformer) TransformToStorage(_ context.Context, data []byte, dataCtx value.Context) ([]byte, error) {
	return []byte(string(dataCtx.AuthenticatedData()) + "|" + string(data)), nil
}

type keyBoundTransformers struct {
	selected []schema.GroupResource
}

func (r *keyBoundTransformers) TransformerForResource(resource schema.GroupResource) value.Transformer {
	r.selected = append(r.selected, resource)
	return &keyBoundTransformer{resource: resource}
}

func TestCopyPrefixRetransformsUnderTargetKey(t *testing.T) {
	t.Parallel()

	const (
		prefix  = "/registry"
		cluster = "abcd1234"
	)
	sourceKey := func(name string) string {
		return prefix + "/wildwest.dev/cowboys/oldhash/" + cluster + "/default/" + name
	}
	targetKey := func(name string) string {
		return prefix + "/wildwest.dev/cowboys/newhash/" + cluster + "/default/" + name
	}

	kv := &fakeKV{kvs: map[string]string{}}
	enc := &keyBoundTransformer{}
	for _, name := range []string{"woody", "jessie", "bullseye"} {
		stored, err := enc.TransformToStorage(context.Background(), []byte("cowboy-"+name), value.DefaultContext(sourceKey(name)))
		require.NoError(t, err)
		kv.kvs[sourceKey(name)] = string(stored)
	}
	// a sibling cluster and resource under the same identity must not be touched.
	kv.kvs[prefix+"/wildwest.dev/cowboys/oldhash/other/default/woody"] = "other-cluster"
	kv.kvs[prefix+"/wildwest.dev/sheriffs/oldhash/"+cluster+"/earp"] = "other-resource"

	before := map[string]string{}
	for k, v := range kv.kvs {
		before[k] = v
	}

	transformers := &keyBoundTransformers{}
	c := &Controller{
		etcdClient:           kv,
		etcdStoragePrefix:    prefix,
		resourceTransformers: transformers,
	}

	require.NoError(t, c.copyPrefix(context.Background(), "wildwest.dev", "cowboys", "oldhash", "newhash", logicalcluster.Name(cluster)))

	for _, name := range []string{"woody", "jessie", "bullseye"} {
		stored, ok := kv.kvs[targetKey(name)]
		require.True(t, ok, "expected %s to be copied", targetKey(name))
		// a verbatim copy would still carry the source key as authenticated
		// data and fail to decrypt under the target key.
		plain, _, err := enc.TransformFromStorage(context.Background(), []byte(stored), value.DefaultContext(targetKey(name)))
		require.NoError(t, err, "copied value must be re-encrypted under the target key")
		require.Equal(t, "cowboy-"+name, string(plain))
	}
	for k, v := range before {
		require.Equal(t, v, kv.kvs[k], "pre-existing key %s must be left untouched", k)
	}
	require.NotContains(t, kv.kvs, prefix+"/wildwest.dev/cowboys/newhash/other/default/woody")
	require.NotContains(t, kv.kvs, prefix+"/wildwest.dev/sheriffs/newhash/"+cluster+"/earp")

	require.NotEmpty(t, transformers.selected)
	for _, gr := range transformers.selected {
		require.Equal(t, schema.GroupResource{Group: "wildwest.dev", Resource: "cowboys"}, gr, "transformer must be selected for the copied resource")
	}
}

func TestCopyPrefixWithoutTransformersCopiesVerbatim(t *testing.T) {
	t.Parallel()

	kv := &fakeKV{kvs: map[string]string{
		"/registry/wildwest.dev/cowboys/oldhash/abcd1234/default/woody": "raw-bytes",
	}}
	c := &Controller{etcdClient: kv, etcdStoragePrefix: "/registry"}

	require.NoError(t, c.copyPrefix(context.Background(), "wildwest.dev", "cowboys", "oldhash", "newhash", logicalcluster.Name("abcd1234")))
	require.Equal(t, "raw-bytes", kv.kvs["/registry/wildwest.dev/cowboys/newhash/abcd1234/default/woody"])
}
