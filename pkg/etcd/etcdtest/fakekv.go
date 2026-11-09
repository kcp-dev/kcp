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

// Package etcdtest provides in-memory fakes of etcd client interfaces for
// unit tests.
package etcdtest

import (
	"context"
	"sort"

	"go.etcd.io/etcd/api/v3/mvccpb"
	clientv3 "go.etcd.io/etcd/client/v3"
)

var _ clientv3.KV = &FakeKV{}

// FakeKV is a minimal in-memory fake of clientv3.KV for unit-testing range
// scans. It supports only Get, with an optional range end, limit and
// keys-only flag. All other methods panic.
//
// Every Get is recorded so tests can assert on how a scanner walked the
// keyspace.
type FakeKV struct {
	// KVs holds the key/value pairs served by Get.
	KVs map[string]string
	// Leases optionally maps keys to the lease ID reported on them.
	Leases map[string]int64

	// Requests records every Get op issued, in order.
	Requests []clientv3.Op
	// ValueKeys records every key whose value was returned, i.e. that was
	// served by a Get without WithKeysOnly.
	ValueKeys []string
	// ReturnedKeys counts the keys returned across all Gets.
	ReturnedKeys int
}

// NewFakeKV returns a FakeKV serving the given key/value pairs.
func NewFakeKV(kvs map[string]string) *FakeKV { return &FakeKV{KVs: kvs} }

func (f *FakeKV) Get(_ context.Context, key string, opts ...clientv3.OpOption) (*clientv3.GetResponse, error) {
	op := clientv3.OpGet(key, opts...)
	f.Requests = append(f.Requests, op)
	rangeEnd := string(op.RangeBytes())
	limit := op.Limit()

	keys := make([]string, 0, len(f.KVs))
	for k := range f.KVs {
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
		entry := &mvccpb.KeyValue{Key: []byte(k), Lease: f.Leases[k]}
		if !op.IsKeysOnly() {
			entry.Value = []byte(f.KVs[k])
			f.ValueKeys = append(f.ValueKeys, k)
		}
		f.ReturnedKeys++
		resp.Kvs = append(resp.Kvs, entry)
	}
	return resp, nil
}

func (f *FakeKV) Put(context.Context, string, string, ...clientv3.OpOption) (*clientv3.PutResponse, error) {
	panic("not implemented")
}

func (f *FakeKV) Delete(context.Context, string, ...clientv3.OpOption) (*clientv3.DeleteResponse, error) {
	panic("not implemented")
}

func (f *FakeKV) Compact(context.Context, int64, ...clientv3.CompactOption) (*clientv3.CompactResponse, error) {
	panic("not implemented")
}

func (f *FakeKV) Do(context.Context, clientv3.Op) (clientv3.OpResponse, error) {
	panic("not implemented")
}

func (f *FakeKV) Txn(context.Context) clientv3.Txn { panic("not implemented") }

func (f *FakeKV) GetStream(context.Context, string, ...clientv3.OpOption) (clientv3.GetStreamChan, error) {
	panic("not implemented")
}
