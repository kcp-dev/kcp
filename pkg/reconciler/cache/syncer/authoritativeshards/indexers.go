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

package authoritativeshards

import (
	"k8s.io/apimachinery/pkg/api/meta"
)

const (
	// ByAuthoritativeShardName is the name for the index that indexes shards that are marked
	// "authoritative", i.e. the ones directly connected to the cache server.
	ByAuthoritativeShardName = "kcp-CacheAuthoritativeShardName"
)

func IndexByAuthoritativeShardName(cacheName string) func(obj interface{}) ([]string, error) {
	return func(obj interface{}) ([]string, error) {
		a, err := meta.Accessor(obj)
		if err != nil {
			return nil, err
		}
		annotations := a.GetAnnotations()
		if annotations == nil || annotations["kcp.io/cache"] != cacheName {
			return []string{}, nil
		}
		return []string{a.GetName()}, nil
	}
}
