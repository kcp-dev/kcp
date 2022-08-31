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
	"testing"

	"github.com/kcp-dev/kcp/pkg/cache/client/shard"
)

func TestShardRoundTripper(t *testing.T) {
	tests := map[string]struct {
		originalPath string
		desired      string
	}{
		"empty path": {
			desired: "/shards/amber",
		},
		"single segment prefix with slashes on both ends": {
			originalPath: "/prefix/",
			desired:      "/shards/amber/prefix/",
		},
		"multiple segments prefix": {
			originalPath: "/several/divisions/of/prefix",
			desired:      "/shards/amber/several/divisions/of/prefix",
		},
		"single segment prefix with slash at beginning only": {
			originalPath: "/prefix",
			desired:      "/shards/amber/prefix",
		},
		"single segment prefix with no slashes": {
			originalPath: "prefix",
			desired:      "/shards/amber/prefix",
		},
		"overwrite wildcard": {
			originalPath: "/shards/*",
			desired:      "/shards/amber",
		},
		"overwrite wildcard ending with /": {
			originalPath: "/shards/*/",
			desired:      "/shards/amber/",
		},
		"overwrite wildcard with single segment": {
			originalPath: "/shards/*/reminder",
			desired:      "/shards/amber/reminder",
		},
		"overwrite wildcard with single segment ending with /": {
			originalPath: "/shards/*/reminder/",
			desired:      "/shards/amber/reminder/",
		},
		"overwrite multiple segments": {
			originalPath: "/shards/sapphire/several/divisions/of/reminder",
			desired:      "/shards/amber/several/divisions/of/reminder",
		},
	}
	for testName, tt := range tests {
		t.Run(testName, func(t *testing.T) {
			result, err := generatePath(tt.originalPath, shard.New("amber"))
			if err != nil {
				t.Error(err)
			}
			if result != tt.desired {
				t.Errorf("got %v, want %v", result, tt.desired)
			}
		})
	}
}
