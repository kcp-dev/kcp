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

// Package shardlookup provides primitives to let shards lookup objects
// regardless of if they are on logical clusters on the local shard or
// in logical clusters on remote shards.
//
// # When to use shardlookup vs the cache server
//
// In general using shardlookup should be the first approach and the
// cache server should only be used when necessary.
//
// The cache server replicates resources across all shards - meaning
// shards connect to it and run informers with watches on it. This is
// very expensive and will only get heavier as the kcp instance grows
// (since the bigger the instance is the more objects are replicated
// into the cache server).
//
// As a rule of thumb:
//  1. Use cache server if the objects are required so frequently that
//     a ttl cache would cause more load
//  2. Use cache server if event-driven behaviour is required
//  3. Use shardlookup in all other cases
package shardlookup
