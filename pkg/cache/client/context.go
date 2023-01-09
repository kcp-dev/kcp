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

package client

import (
	"context"

	"github.com/kcp-dev/kcp/pkg/cache/client/shard"
)

type shardKey int

const (
	// shardContextKey is the context key for a request.
	shardContextKey shardKey = iota
)

// WithShardInContext returns a context with the given shard set.
func WithShardInContext(parent context.Context, shard shard.Name) context.Context {
	return context.WithValue(parent, shardContextKey, shard)
}

// ShardFromContext returns the value of the shard key on the ctx,
// or an empty Name if there is no shard key.
func ShardFromContext(ctx context.Context) shard.Name {
	shard, ok := ctx.Value(shardContextKey).(shard.Name)
	if !ok {
		return ""
	}
	return shard
}
